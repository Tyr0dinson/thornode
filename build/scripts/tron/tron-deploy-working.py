#!/usr/bin/env python3
"""
TRON TRC20 Token Deployment - Working Implementation

Uses raw TRON HTTP API with minimal dependencies:
- requests (already installed)
- ecdsa (for transaction signing)
- base58 (for address encoding)
- mnemonic + bip32 (for key derivation)

KEY DERIVATION (matches test/simulation/pkg/evm/evm.go):
---------------------------------------------------------
1. Master mnemonic: "master " (24 words)
2. Derivation path: m/44'/931'/0'/0/0 (cmd/constants.go:THORChainHDPath)
3. Algorithm: BIP39 mnemonic -> BIP32 seed -> secp256k1 private key
4. This matches the Go code flow:
   - test/simulation/pkg/types/account.go:NewUser() uses hd.Secp256k1.Derive()
   - bifrost/thorclient/keys.go:GetPrivateKey() returns secp256k1.PrivKey
   - test/simulation/pkg/evm/evm.go:NewClient() calls evm.GetPrivateKey()
   - bifrost/pkg/chainclients/shared/evm/key_sign_wrapper.go:GetPrivateKey()
     converts to ECDSA via ecrypto.ToECDSA(key.Bytes())
5. TRON uses secp256k1 (same as Ethereum), so same key derivation works
"""

import json
import os
import sys
import time
import hashlib
import requests
from typing import Optional, Dict, Any

try:
    from ecdsa import SigningKey, SECP256k1
    from ecdsa.util import sigencode_string
    import base58
except ImportError:
    print("ERROR: Missing required libraries")
    print("Install: pip3 install ecdsa==0.18.0 base58==2.1.1")
    sys.exit(1)

########################################################################################
# Config
########################################################################################

DEFAULT_RPC = "http://tron-fullnode:8090"

# Master mnemonic (from test/simulation/cmd/mocknet.go)
MASTER_MNEMONIC = " ".join(["master"] * 23) + " notice"
THORCHAIN_HD_PATH = "m/44'/931'/0'/0/0"  # cmd/constants.go:THORChainHDPath

# Derive private key from mnemonic
# This matches the flow in test/simulation/pkg/types/account.go:NewUser()
# - Uses hd.Secp256k1.Derive() with THORChainHDPath
# - Keys are stored in keyring and extracted via keys.GetPrivateKey()
# - In EVM client: evm.GetPrivateKey(privateKey) calls ecrypto.ToECDSA(key.Bytes())
# - TRON uses the same secp256k1 curve, so we use the same derivation
try:
    from mnemonic import Mnemonic
    from bip32 import BIP32
    
    mnemo = Mnemonic("english")
    seed = mnemo.to_seed(MASTER_MNEMONIC, passphrase="")
    bip32 = BIP32.from_seed(seed)
    # This gives us the raw 32-byte secp256k1 private key
    # Same as what privateKey.Bytes() returns in Go
    derived_key = bip32.get_privkey_from_path(THORCHAIN_HD_PATH)
    ADMIN_KEY = derived_key.hex()
    print(f"✅ Derived private key from mnemonic using path {THORCHAIN_HD_PATH}")
    print(f"   (Matches: test/simulation/pkg/evm/evm.go:NewClient)")
except ImportError as e:
    print(f"❌ ERROR: Missing required libraries for mnemonic derivation")
    print(f"   Install: pip3 install mnemonic==0.20 bip32==3.4")
    sys.exit(1)
except Exception as e:
    print(f"❌ ERROR: Could not derive key from mnemonic: {e}")
    sys.exit(1)

SIMULATION_MASTER_HEX = "TODO"  # TXhG4Vhrxyu8htL8tBdCmndF66D1jyKgJW

########################################################################################
# TRON Address Utilities
########################################################################################

def private_key_to_address(private_key_hex: str) -> tuple:
    """Convert private key to TRON address (base58 and hex)"""
    from Crypto.Hash import keccak
    import hashlib
    
    private_key = bytes.fromhex(private_key_hex)
    sk = SigningKey.from_string(private_key, curve=SECP256k1)
    
    # Get public key (uncompressed, 64 bytes)
    vk = sk.get_verifying_key()
    pub_key = vk.to_string()  # Returns 64 bytes (x || y coordinates)
    
    # TRON uses keccak256 of public key (same as Ethereum)
    # Hash the public key
    k = keccak.new(digest_bits=256)
    k.update(pub_key)
    pub_key_hash = k.digest()
    
    # Take last 20 bytes and prepend 0x41 (TRON mainnet prefix)
    address_bytes = b'\x41' + pub_key_hash[-20:]
    
    # Base58check encoding
    address_base58 = base58.b58encode_check(address_bytes).decode()
    address_hex = address_bytes.hex()
    
    return address_base58, address_hex

def base58_to_hex(address_base58: str) -> str:
    """Convert base58 address to hex"""
    decoded = base58.b58decode_check(address_base58)
    return decoded.hex()

########################################################################################
# TRON Transaction Signing
########################################################################################

def sign_transaction(transaction: Dict[str, Any], private_key_hex: str) -> str:
    """
    Sign a TRON transaction with recovery ID (65 bytes total).
    
    TRON uses ECDSA signatures with a recovery ID to allow recovering the public
    key from the signature. The recovery ID is a single byte appended to the
    64-byte (r,s) signature.
    
    This matches Go's crypto.Sign() behavior from ethereum/go-ethereum/crypto.
    """
    from Crypto.Hash import keccak
    from ecdsa.util import sigencode_string_canonize
    from ecdsa.keys import VerifyingKey
    import ecdsa
    
    # Get transaction hash (txID)
    tx_hash = bytes.fromhex(transaction['txID'])
    
    # Sign with private key
    private_key = bytes.fromhex(private_key_hex)
    sk = SigningKey.from_string(private_key, curve=SECP256k1)
    vk = sk.get_verifying_key()
    pub_key = vk.to_string()  # 64 bytes uncompressed (x || y)
    
    # Sign the hash (produces 64 bytes: r || s)
    signature = sk.sign_digest(tx_hash, sigencode=sigencode_string_canonize)
    
    # Extract r and s from signature (32 bytes each)
    r = int.from_bytes(signature[:32], 'big')
    s = int.from_bytes(signature[32:], 'big')
    
    # TRON requires 65-byte signature (64 bytes + 1 recovery ID byte)
    # We must find the correct recovery ID by testing which one recovers our pubkey
    # The recovery ID is 0-3 and indicates which of 4 possible points was used
    
    for recovery_id in range(4):
        try:
            # Try to recover public key with this recovery_id
            recovered_pub = recover_pubkey(tx_hash, r, s, recovery_id)
            
            # Check if recovered pubkey matches our actual pubkey
            if recovered_pub == pub_key:
                # Found the correct recovery ID!
                return (signature + bytes([recovery_id])).hex()
        except:
            # This recovery ID doesn't work, try next one
            continue
    
    # Fallback: use y-parity heuristic (should not reach here with valid signature)
    y_is_odd = pub_key[-1] & 1
    recovery_id = y_is_odd
    return (signature + bytes([recovery_id])).hex()


def recover_pubkey(msg_hash: bytes, r: int, s: int, recovery_id: int) -> bytes:
    """
    Recover public key from ECDSA signature (mimics Ethereum's ecrecover).
    
    Args:
        msg_hash: The hash that was signed (32 bytes)
        r, s: Signature components
        recovery_id: Recovery ID (0-3)
    
    Returns:
        Recovered public key (64 bytes uncompressed)
    """
    from ecdsa.curves import SECP256k1
    from ecdsa import numbertheory
    
    curve = SECP256k1.curve
    G = SECP256k1.generator
    order = G.order()
    
    # Extract flags from recovery_id
    is_y_odd = recovery_id & 1
    is_second_key = recovery_id & 2
    
    # Calculate x coordinate of R point
    x = r
    if is_second_key:
        x += order
    
    # Ensure x is valid for the curve
    if x >= curve.p():
        raise ValueError("x >= curve.p()")
    
    # Calculate y from x
    # y^2 = x^3 + ax + b (for secp256k1: y^2 = x^3 + 7)
    y_squared = (pow(x, 3, curve.p()) + curve.b()) % curve.p()
    y = pow(y_squared, (curve.p() + 1) // 4, curve.p())
    
    # Ensure y has correct parity
    if (y & 1) != is_y_odd:
        y = curve.p() - y
    
    # Construct point R = (x, y)
    from ecdsa.ellipticcurve import Point
    R = Point(curve, x, y, order)
    
    # Verify R is on curve
    if not curve.contains_point(x, y):
        raise ValueError("Point not on curve")
    
    # Calculate public key: Q = r^-1 * (s*R - e*G)
    e = int.from_bytes(msg_hash, 'big')
    r_inv = numbertheory.inverse_mod(r, order)
    
    # Q = r^-1 * (s*R - e*G)
    Q = r_inv * (s * R + (-e % order) * G)
    
    # Return uncompressed public key (64 bytes: x || y)
    pub_x = Q.x().to_bytes(32, 'big')
    pub_y = Q.y().to_bytes(32, 'big')
    
    return pub_x + pub_y

########################################################################################
# TRON Client
########################################################################################

class TronClient:
    """Minimal TRON client using raw HTTP API"""
    
    def __init__(self, rpc_url: str, private_key_hex: str):
        self.rpc_url = rpc_url.rstrip('/')
        self.private_key = private_key_hex
        
        # Derive address from private key (matches Go: pubKey.GetAddress(chain))
        # This ensures we're using the same key that was derived from the mnemonic
        self.address_base58, self.address_hex = private_key_to_address(private_key_hex)
        
        print(f"📍 Derived address from private key:")
        print(f"   Base58: {self.address_base58}")
        print(f"   Hex:    {self.address_hex}")
        
        # Verify it matches the expected master address (optional check)
        expected_base58 = "TXhG4Vhrxyu8htL8tBdCmndF66D1jyKgJW"
        if self.address_base58 != expected_base58:
            print(f"⚠️  WARNING: Derived address doesn't match expected master address")
            print(f"   Expected: {expected_base58}")
            print(f"   Got:      {self.address_base58}")
        else:
            print(f"✅ Address matches expected master/witness account")
    
    def wait_for_network(self, retries: int = 30) -> bool:
        """Wait for TRON network to be ready"""
        print("Waiting for TRON network...")
        for i in range(retries):
            try:
                response = requests.post(
                    f"{self.rpc_url}/wallet/getnowblock",
                    json={},
                    timeout=2
                )
                if response.status_code == 200:
                    data = response.json()
                    block_num = data.get("block_header", {}).get("raw_data", {}).get("number", 0)
                    if block_num > 0:
                        print(f"✅ TRON network ready at block {block_num}")
                        return True
            except Exception as e:
                pass
            
            if i < retries - 1:
                time.sleep(1)
        
        print("❌ TRON network not reachable")
        return False
    
    def get_account(self, address: str = None) -> Dict[str, Any]:
        """Get account information"""
        if address is None:
            address = self.address_base58
        
        response = requests.post(
            f"{self.rpc_url}/wallet/getaccount",
            json={"address": address}
        )
        return response.json()
    
    def trigger_constant_contract(self, owner_address: str, contract_address: str, 
                                 function_selector: str, parameter: str) -> Dict[str, Any]:
        """Call a read-only contract method"""
        response = requests.post(
            f"{self.rpc_url}/wallet/triggerconstantcontract",
            json={
                "owner_address": owner_address,
                "contract_address": contract_address,
                "function_selector": function_selector,
                "parameter": parameter,
                "visible": True
            }
        )
        return response.json()
    
    def create_transaction(self, to_address: str, amount: int) -> Dict[str, Any]:
        """Create a TRX transfer transaction"""
        response = requests.post(
            f"{self.rpc_url}/wallet/createtransaction",
            json={
                "owner_address": self.address_hex,
                "to_address": to_address,
                "amount": amount
            }
        )
        return response.json()
    
    def deploy_contract(self, abi: str, bytecode: str, name: str = "TRC20Token",
                       fee_limit: int = 1000000000) -> Optional[str]:
        """
        Deploy a smart contract
        
        Returns: Contract address if successful
        """
        print(f"\n📝 Preparing to deploy contract: {name}")
        print(f"   Bytecode size: {len(bytecode)} bytes")
        print(f"   Fee limit: {fee_limit / 1e6} TRX")
        
        # Create the deployment transaction
        payload = {
            "owner_address": self.address_hex,
            "abi": abi,
            "bytecode": bytecode,
            "name": name,
            "consume_user_resource_percent": 100,
            "fee_limit": fee_limit,
            "call_value": 0,
            "origin_energy_limit": 10000000
        }
        
        try:
            # Request contract creation
            print("\n🔨 Creating deployment transaction...")
            response = requests.post(
                f"{self.rpc_url}/wallet/deploycontract",
                json=payload,
                timeout=10
            )
            
            if response.status_code != 200:
                print(f"❌ Failed to create deployment transaction: {response.status_code}")
                print(f"   Response: {response.text}")
                return None
            
            tx_data = response.json()
            
            # Check for errors
            if 'Error' in tx_data:
                print(f"❌ Error from TRON: {tx_data['Error']}")
                return None
            
            if 'txID' not in tx_data:
                print(f"❌ No txID in response")
                print(f"   Response: {json.dumps(tx_data, indent=2)}")
                return None
            
            # Sign the transaction
            print(f"✍️  Signing transaction {tx_data['txID'][:16]}...")
            signature = sign_transaction(tx_data, self.private_key)
            tx_data['signature'] = [signature]
            
            # Broadcast the transaction
            print("📡 Broadcasting signed transaction...")
            broadcast_response = requests.post(
                f"{self.rpc_url}/wallet/broadcasttransaction",
                json=tx_data,
                timeout=10
            )
            
            if broadcast_response.status_code != 200:
                print(f"❌ Broadcast failed: {broadcast_response.status_code}")
                return None
            
            result = broadcast_response.json()
            
            if not result.get('result', False):
                print(f"❌ Broadcast rejected")
                print(f"   Response: {json.dumps(result, indent=2)}")
                return None
            
            tx_id = result.get('txid', tx_data['txID'])
            print(f"✅ Transaction broadcast successfully!")
            print(f"   TX ID: {tx_id}")
            
            # Wait for transaction to be confirmed
            print("\n⏳ Waiting for transaction confirmation...")
            contract_address = self.wait_for_transaction(tx_id)
            
            if contract_address:
                print(f"\n🎉 Contract deployed successfully!")
                print(f"   Address: {contract_address}")
                return contract_address
            else:
                print("⚠️  Could not confirm contract address")
                return None
                
        except Exception as e:
            print(f"❌ Exception during deployment: {e}")
            import traceback
            traceback.print_exc()
            return None
    
    def wait_for_transaction(self, tx_id: str, timeout: int = 30) -> Optional[str]:
        """Wait for transaction confirmation and extract contract address"""
        for i in range(timeout):
            try:
                response = requests.post(
                    f"{self.rpc_url}/wallet/gettransactioninfobyid",
                    json={"value": tx_id},
                    timeout=2
                )
                
                if response.status_code == 200:
                    info = response.json()
                    
                    # Check if transaction is confirmed
                    if 'id' in info:
                        # Contract address is in the info
                        if 'contract_address' in info:
                            contract_hex = info['contract_address']
                            # Convert to base58 and uppercase (matches THORChain convention)
                            contract_bytes = bytes.fromhex(contract_hex)
                            contract_base58 = base58.b58encode_check(contract_bytes).decode().upper()
                            return contract_base58
                        
                        # Check receipt
                        if 'receipt' in info:
                            receipt = info['receipt']
                            if receipt.get('result') == 'SUCCESS':
                                print("   Transaction confirmed (checking for address...)")
                            else:
                                print(f"   Transaction failed: {receipt.get('result')}")
                                return None
            except:
                pass
            
            time.sleep(1)
            if (i + 1) % 5 == 0:
                print(f"   Still waiting... ({i + 1}s)")
        
        print("   Timeout waiting for confirmation")
        return None
    
    def trigger_smart_contract(self, contract_address: str, function_selector: str,
                              parameter: str, call_value: int = 0,
                              fee_limit: int = 100000000) -> Optional[str]:
        """Trigger a smart contract function"""
        payload = {
            "owner_address": self.address_hex,
            "contract_address": base58_to_hex(contract_address),
            "function_selector": function_selector,
            "parameter": parameter,
            "call_value": call_value,
            "fee_limit": fee_limit
        }
        
        try:
            response = requests.post(
                f"{self.rpc_url}/wallet/triggersmartcontract",
                json=payload,
                timeout=10
            )
            
            if response.status_code != 200:
                return None
            
            tx_data = response.json()
            
            if 'Error' in tx_data or 'txID' not in tx_data:
                return None
            
            # Sign and broadcast
            signature = sign_transaction(tx_data, self.private_key)
            tx_data['signature'] = [signature]
            
            broadcast_response = requests.post(
                f"{self.rpc_url}/wallet/broadcasttransaction",
                json=tx_data
            )
            
            if broadcast_response.status_code == 200:
                result = broadcast_response.json()
                if result.get('result'):
                    return result.get('txid', tx_data['txID'])
            
            return None
            
        except Exception as e:
            print(f"Error triggering contract: {e}")
            return None

########################################################################################
# Deployment Tool
########################################################################################

class TronDeployTool:
    """Deploy TRC20 tokens - mirrors BASE evm-tool.py pattern"""
    
    def __init__(self, rpc_url: str, private_key: str):
        self.client = TronClient(rpc_url, private_key)
        
        if not self.client.wait_for_network():
            sys.exit(1)
    
    def deploy_token(self) -> Optional[str]:
        """Deploy TRC20 token contract"""
        print("\n" + "="*70)
        print("TRON TRC20 TOKEN DEPLOYMENT")
        print("="*70)
        
        # Load pre-compiled artifacts (same as BASE)
        script_dir = os.path.dirname(__file__)
        with open(os.path.join(script_dir, "trc20-abi.json")) as f:
            abi_json = json.load(f)
        with open(os.path.join(script_dir, "trc20-bytecode.txt")) as f:
            bytecode = f.read().strip()
        
        # Convert ABI to string
        abi_str = json.dumps(abi_json)
        
        # Deploy contract
        contract_address = self.client.deploy_contract(
            abi=abi_str,
            bytecode=bytecode,
            name="TKN",
            fee_limit=1000000000  # 1000 TRX (maximum allowed by TRON)
        )
        
        if contract_address:
            print("\n" + "="*70)
            print("✅ DEPLOYMENT SUCCESSFUL")
            print("="*70)
            print(f"\nToken Contract Address: {contract_address}")
            print(f"\nNext steps:")
            print(f"1. Update token address in:")
            print(f"   common/tokenlist/trontokens/tron_nile_latest.json")
            print(f"2. Rebuild mocknet: make build-mocknet")
            print(f"3. Run tests: make test-simulation")
            print("="*70 + "\n")
            return contract_address
        else:
            print("\n" + "="*70)
            print("❌ DEPLOYMENT FAILED")
            print("="*70)
            print("\nCheck the error messages above.")
            print("Common issues:")
            print("- Admin account not funded with TRX")
            print("- TRON network not fully synced")
            print("- Fee limit too low")
            print("="*70 + "\n")
            return None

########################################################################################
# Main
########################################################################################

def main():
    import argparse
    
    parser = argparse.ArgumentParser(
        description="Deploy TRC20 tokens to TRON mocknet"
    )
    parser.add_argument("--rpc", default=DEFAULT_RPC, help="TRON RPC URL")
    parser.add_argument("--action", default="deploy", choices=["deploy"])
    parser.add_argument("--key", default=ADMIN_KEY, help="Private key (hex)")
    
    args = parser.parse_args()
    
    tool = TronDeployTool(args.rpc, args.key)
    
    if args.action == "deploy":
        result = tool.deploy_token()
        sys.exit(0 if result else 1)

if __name__ == "__main__":
    main()

