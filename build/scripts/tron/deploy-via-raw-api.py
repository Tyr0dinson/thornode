#!/usr/bin/env python3
"""
TRON TRC20 Token Deployment - Raw API Version

Mirrors the EVM pattern from evm-tool.py but uses TRON's raw HTTP API
instead of tronpy, avoiding dependency conflicts.

This follows the exact same pattern as BASE chain deployment:
1. Load pre-compiled ABI and bytecode
2. Deploy using raw TRON API
3. Transfer tokens to simulation master

KEY DERIVATION (matches test/simulation/pkg/evm/evm.go):
---------------------------------------------------------
- Derives private key from master mnemonic using BIP39/BIP32
- Uses THORChain HD path: m/44'/931'/0'/0/0
- Matches Go code flow in test/simulation/pkg/types/account.go:NewUser()
"""

import json
import os
import sys
import time

import requests

try:
    import base58
    from ecdsa import SECP256k1, SigningKey
except ImportError:
    print("ERROR: Missing required libraries")
    print("Install: pip3 install ecdsa==0.18.0 base58==2.1.1")
    sys.exit(1)

########################################################################################
# Config
########################################################################################

DEFAULT_RPC = "http://localhost:8090"

# Master mnemonic (from test/simulation/cmd/mocknet.go)
MASTER_MNEMONIC = " ".join(["master"] * 23) + " notice"
THORCHAIN_HD_PATH = "m/44'/931'/0'/0/0"  # cmd/constants.go:THORChainHDPath

# Derive private key from mnemonic
# This matches the flow in test/simulation/pkg/types/account.go:NewUser()
try:
    from bip32 import BIP32
    from mnemonic import Mnemonic

    mnemo = Mnemonic("english")
    seed = mnemo.to_seed(MASTER_MNEMONIC, passphrase="")
    bip32 = BIP32.from_seed(seed)
    derived_key = bip32.get_privkey_from_path(THORCHAIN_HD_PATH)
    ADMIN_KEY = derived_key.hex()
    print(f"✅ Derived private key from mnemonic using path {THORCHAIN_HD_PATH}")
    print("   (Matches: test/simulation/pkg/evm/evm.go:NewClient)")
except ImportError:
    print("❌ ERROR: Missing required libraries for mnemonic derivation")
    print("   Install: pip3 install mnemonic==0.20 bip32==3.4")
    sys.exit(1)
except Exception as e:
    print(f"❌ ERROR: Could not derive key from mnemonic: {e}")
    sys.exit(1)

########################################################################################
# TRON Address Utilities
########################################################################################


def private_key_to_address(private_key_hex: str) -> tuple:
    """Convert private key to TRON address (base58 and hex)"""
    from Crypto.Hash import keccak

    private_key = bytes.fromhex(private_key_hex)
    sk = SigningKey.from_string(private_key, curve=SECP256k1)

    # Get public key (uncompressed, 64 bytes)
    vk = sk.get_verifying_key()
    pub_key = vk.to_string()

    # TRON uses keccak256 of public key (same as Ethereum)
    k = keccak.new(digest_bits=256)
    k.update(pub_key)
    pub_key_hash = k.digest()

    # Take last 20 bytes and prepend 0x41 (TRON mainnet prefix)
    address_bytes = b"\x41" + pub_key_hash[-20:]

    # Base58check encoding
    address_base58 = base58.b58encode_check(address_bytes).decode()
    address_hex = address_bytes.hex()

    return address_base58, address_hex


########################################################################################
# TRONSetupTool
########################################################################################


class TRONSetupTool:
    """
    Deploy TRC20 tokens using TRON's raw HTTP API.
    Mirrors the pattern from evm-tool.py for BASE/ETH/AVAX.
    """

    def __init__(self, rpc_url: str):
        self.rpc_url = rpc_url
        self.admin_key = ADMIN_KEY

        # Derive admin address from private key (matches Go: pubKey.GetAddress(chain))
        self.admin_address_base58, self.admin_address_hex = private_key_to_address(
            ADMIN_KEY
        )

        print("📍 Derived admin address from private key:")
        print(f"   Base58: {self.admin_address_base58}")
        print(f"   Hex:    {self.admin_address_hex}")

        # Verify it matches expected master/witness account
        expected_base58 = "TXhG4Vhrxyu8htL8tBdCmndF66D1jyKgJW"
        if self.admin_address_base58 != expected_base58:
            print("⚠️  WARNING: Derived address doesn't match expected master address")
            print(f"   Expected: {expected_base58}")
            print(f"   Got:      {self.admin_address_base58}")
        else:
            print("✅ Address matches expected master/witness account")

        self.wait_for_network()

    def wait_for_network(self, retries: int = 30):
        """Wait for TRON network to be ready"""
        print("Waiting for TRON network...")
        for i in range(retries):
            try:
                response = requests.post(
                    f"{self.rpc_url}/wallet/getnowblock", json={}, timeout=1
                )
                if response.status_code == 200:
                    data = response.json()
                    block_num = (
                        data.get("block_header", {})
                        .get("raw_data", {})
                        .get("number", 0)
                    )
                    print(f"TRON network ready at block {block_num}")
                    return
            except Exception:
                pass
            time.sleep(1)

        print(f"ERROR: TRON network not reachable at {self.rpc_url}")
        sys.exit(1)

    def deploy_init_contracts(self):
        """Deploy token contract (router not needed for TRON)"""
        self.deploy_token()

    def deploy_token(self):
        """
        Deploy TRC20 token contract using raw TRON API

        NOTE: This is a REFERENCE implementation showing the pattern.
        Actual deployment requires:
        1. Converting Solidity bytecode to TRON format
        2. Signing transactions with the admin private key
        3. Proper parameter encoding

        See: https://developers.tron.network/reference/wallet-deploycontract
        """
        print("deploying TRC20 token contract...")

        # Load pre-compiled artifacts (same pattern as EVM)
        script_dir = os.path.dirname(__file__)
        with open(os.path.join(script_dir, "trc20-abi.json")) as f:
            abi = json.load(f)
        with open(os.path.join(script_dir, "trc20-bytecode.txt")) as f:
            bytecode = f.read().strip()

        print(f"Loaded ABI ({len(abi)} entries)")
        print(f"Loaded bytecode ({len(bytecode)} bytes)")

        # TRON deployment uses /wallet/deploycontract endpoint
        # This is similar to web3.eth.contract().constructor().transact()

        print("\n" + "=" * 70)
        print("DEPLOYMENT BLOCKED - REQUIRES IMPLEMENTATION")
        print("=" * 70)
        print("\nTo complete deployment, you need to:")
        print("1. Sign the deployment transaction with admin private key")
        print("2. Convert EVM bytecode to TRON format (if needed)")
        print("3. Call /wallet/deploycontract with signed transaction")
        print("\nAlternatively, deploy manually with tronbox:")
        print("  npm install -g tronbox")
        print("  tronbox migrate --network mocknet")
        print("\nThen update token address in:")
        print("  common/tokenlist/trontokens/tron_nile_latest.json")
        print("=" * 70 + "\n")

        # Reference API call structure (not executed):
        deployment_payload = {
            "owner_address": self.admin_address_hex,  # Use hex format for API calls
            "abi": json.dumps(abi),
            "bytecode": bytecode,
            "name": "TestToken",
            "consume_user_resource_percent": 100,
            "fee_limit": 1000000000,  # 1000 TRX
        }

        print("Reference deployment payload structure:")
        print(json.dumps(deployment_payload, indent=2))

        return None  # Would return contract address


########################################################################################
# Main
########################################################################################


def main():
    import argparse

    parser = argparse.ArgumentParser(
        description="Deploy TRC20 tokens to TRON mocknet (reference implementation)"
    )
    parser.add_argument(
        "--rpc", default=DEFAULT_RPC, help=f"TRON RPC URL (default: {DEFAULT_RPC})"
    )
    parser.add_argument(
        "--action", default="deploy", choices=["deploy"], help="Action to perform"
    )

    args = parser.parse_args()

    # Run deployment
    tool = TRONSetupTool(args.rpc)
    if args.action == "deploy":
        tool.deploy_init_contracts()


if __name__ == "__main__":
    main()
