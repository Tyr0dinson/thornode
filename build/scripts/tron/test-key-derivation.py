#!/usr/bin/env python3
"""Test which private key the mnemonic derives to"""
from mnemonic import Mnemonic
from bip32 import BIP32
from ecdsa import SigningKey, SECP256k1
import base58
from Crypto.Hash import keccak

MASTER_MNEMONIC = " ".join(["master"] * 23) + " notice"
THORCHAIN_HD_PATH = "m/44'/931'/0'/0/0"

def private_key_to_address(private_key_hex):
    private_key = bytes.fromhex(private_key_hex)
    sk = SigningKey.from_string(private_key, curve=SECP256k1)
    vk = sk.get_verifying_key()
    pub_key = vk.to_string()
    k = keccak.new(digest_bits=256)
    k.update(pub_key)
    pub_key_hash = k.digest()
    address_bytes = b'\x41' + pub_key_hash[-20:]
    address_base58 = base58.b58encode_check(address_bytes).decode()
    return address_base58

# Derive from mnemonic
mnemo = Mnemonic("english")
seed = mnemo.to_seed(MASTER_MNEMONIC, passphrase="")
bip32 = BIP32.from_seed(seed)
derived_key = bip32.get_privkey_from_path(THORCHAIN_HD_PATH)
derived_key_hex = derived_key.hex()

# Expected key from other scripts
expected_key = "TODO"

print("=" * 70)
print("TRON Private Key Derivation Test")
print("=" * 70)
print(f"HD Path:  {THORCHAIN_HD_PATH}")
print(f"\nDerived Private Key:")
print(f"  {derived_key_hex}")
print(f"\nExpected (from other scripts):")
print(f"  {expected_key}")
print(f"\n✅ Keys Match: {derived_key_hex == expected_key}")

print(f"\nAddress from derived key:")
print(f"  {private_key_to_address(derived_key_hex)}")

print(f"\nAddress from expected key:")
print(f"  {private_key_to_address(expected_key)}")

print(f"\nExpected TRON master address:")
print(f"  TXhG4Vhrxyu8htL8tBdCmndF66D1jyKgJW")

print("=" * 70)

