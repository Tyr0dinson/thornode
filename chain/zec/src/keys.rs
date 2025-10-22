use crate::error::ZecError;
use crate::network::Network;

// Common imports (both build modes)
use zcash_keys::encoding::AddressCodec;
use zcash_keys::keys::UnifiedSpendingKey;
use zcash_transparent::keys::{AccountPrivKey, NonHardenedChildIndex};
use zcash_transparent::address::TransparentAddress;
use bip32::ChildNumber;
use sha2::{Sha256, Digest as ShaDigest};
use ripemd::{Ripemd160, Digest as RipemdDigest};
use zcash_protocol::consensus::{Parameters, BranchId as ZcashBranchId};
use zcash_primitives::zip32::AccountId;
use bip0039::{English, Mnemonic};
use secp256k1::Secp256k1;
use std::convert::TryFrom;

// Shielded address helpers only needed when shielded is enabled
#[cfg(feature = "shielded")]
use zcash_keys::address::{Address as ZcashAddress, UnifiedAddress};
#[cfg(feature = "shielded")]
use zcash_keys::keys::{ReceiverRequirement::*, UnifiedAddressRequest};


#[derive(Clone, Copy, Debug)]
pub enum BranchId {
    Overwinter,
    Sapling,
    Blossom,
    Heartwood,
    Canopy,
    NU5,
}

// Use the UDL-generated enum `BranchId` (same module as your other UDL types)
impl From<BranchId> for ZcashBranchId {
    fn from(b: BranchId) -> Self {
        use ZcashBranchId as Z;
        match b {
            BranchId::Overwinter => Z::Overwinter,
            BranchId::Sapling    => Z::Sapling,
            BranchId::Blossom    => Z::Blossom,
            BranchId::Heartwood  => Z::Heartwood,
            BranchId::Canopy     => Z::Canopy,
            BranchId::NU5        => Z::Nu5,
        }
    }
}

pub struct ZecAccount {
    pub private_key: Vec<u8>,
    pub public_key: Vec<u8>,
    pub index: u32,
    pub transparent: String,
    pub sapling: String,
    pub orchard: String,
    pub unified: String,
}

#[cfg(feature = "shielded")]
pub fn derive_account_from_mnemonic(mnemonic: String, network: Network) -> Result<ZecAccount, ZecError> {
    // --- Mnemonic to Seed Conversion ---
    let mnemonic = Mnemonic::<English>::from_phrase(&mnemonic)
        .map_err(|e| ZecError::GenericError(e.to_string()))?;
    let seed_array = mnemonic.to_seed("");
    let seed_bytes = seed_array.to_vec();

    // --- Derive Unified Keys and Find Correct Diversifier Index ---
    let usk = UnifiedSpendingKey::from_seed(&network, &seed_bytes, AccountId::try_from(0).unwrap())
        .map_err(|e| ZecError::GenericError(format!("Failed to derive USK: {e}")))?;
    let uivk = usk.to_unified_full_viewing_key().to_unified_incoming_viewing_key();
    #[cfg(feature = "shielded")]
    let request = UnifiedAddressRequest::unsafe_custom(
        Require, // Orchard
        Require, // Sapling
        Require, // Transparent (P2PKH)
    );
    #[cfg(not(feature = "shielded"))]
    let request = UnifiedAddressRequest::unsafe_custom(
        Omit,    // Orchard - not available in production
        Omit,    // Sapling - not available in production
        Require, // Transparent (P2PKH)
    );

    let (unified_addr, diversifier_index) = uivk.default_address(request)
        .map_err(|e| ZecError::GenericError(format!("Failed to find default UA: {e}")))?;

    // --- Extract All Address Strings from the Unified Address ---
    let transparent = unified_addr.transparent().map(|t| t.encode(&network)).unwrap_or_default();
    
    #[cfg(feature = "shielded")]
    let unified = unified_addr.encode(&network);
    #[cfg(feature = "shielded")]
    let sapling = unified_addr.sapling().map(|s| s.encode(&network)).unwrap_or_default();
    #[cfg(feature = "shielded")]
    let orchard = unified_addr.orchard()
        .map(|o| UnifiedAddress::from_receivers(Some(*o), None, None)
        .ok_or_else(|| ZecError::GenericError("failed to construct Orchard-only UA".into()))
        .map(|ua| ZcashAddress::Unified(ua).encode(&network)))
        .transpose()? // Result<Option<String>, ZecError>
        .unwrap_or_default();

    #[cfg(not(feature = "shielded"))]
    let unified = String::new();
    #[cfg(not(feature = "shielded"))]
    let sapling = String::new();
    #[cfg(not(feature = "shielded"))]
    let orchard = String::new();


    // --- Derive the Specific Transparent Keys at the Correct Index ---
    let transparent_index = NonHardenedChildIndex::try_from(diversifier_index)
        .map_err(|_| ZecError::GenericError("Could not convert diversifier index".to_string()))?;
    let account_priv_key = usk.transparent();
    let external_sk = account_priv_key
        .derive_external_secret_key(transparent_index)
        .map_err(|e| ZecError::GenericError(format!("Failed to derive external secret key: {e}")))?;
    let secp = Secp256k1::new();
    let pubkey = external_sk.public_key(&secp);

    // --- Return the Final Struct ---
    Ok(ZecAccount {
        private_key: external_sk.secret_bytes().to_vec(),
        public_key: pubkey.serialize().to_vec(),
        index: transparent_index.index(),
        transparent,
        sapling,
        orchard,
        unified,
    })
}

// Production (non-shielded): derive transparent-only; leave shielded fields empty.
#[cfg(not(feature = "shielded"))]
pub fn derive_account_from_mnemonic(mnemonic: String, network: Network) -> Result<ZecAccount, ZecError> {
    // Mnemonic → seed
    let mnemonic = Mnemonic::<English>::from_phrase(&mnemonic)
        .map_err(|e| ZecError::GenericError(e.to_string()))?;
    let seed_bytes = mnemonic.to_seed("").to_vec();

    // Derive USK and transparent keys only
    let usk = UnifiedSpendingKey::from_seed(&network, &seed_bytes, AccountId::try_from(0).unwrap())
        .map_err(|e| ZecError::GenericError(format!("Failed to derive USK: {e}")))?;

    // Choose index 0 for production (no shielded diversifier search)
    let child = ChildNumber::new(0, false)
        .map_err(|_| ZecError::GenericError("Invalid child number".to_string()))?;
    let transparent_index = NonHardenedChildIndex::try_from(child)
        .map_err(|_| ZecError::GenericError("Could not convert child number to NonHardenedChildIndex".to_string()))?;

    let account_priv_key = usk.transparent();
    let external_sk = account_priv_key
        .derive_external_secret_key(transparent_index)
        .map_err(|e| ZecError::GenericError(format!("Failed to derive external secret key: {e}")))?;
    let secp = Secp256k1::new();
    let pubkey = external_sk.public_key(&secp);

    // Encode the transparent P2PKH address from the derived public key
    let pubkey_bytes = pubkey.serialize(); // compressed 33 bytes
    let sha = Sha256::digest(pubkey_bytes);
    let rip = Ripemd160::digest(sha);
    let mut pkh20 = [0u8; 20];
    pkh20.copy_from_slice(&rip);
    let taddr = TransparentAddress::PublicKeyHash(pkh20).encode(&network);

    Ok(ZecAccount {
        private_key: external_sk.secret_bytes().to_vec(),
        public_key: pubkey.serialize().to_vec(),
        index: transparent_index.index(),
        transparent: taddr.clone(),
        // Shielded components disabled in production build
        sapling: String::new(),
        orchard: String::new(),
        // Provide a usable identifier; reuse transparent address as a stand-in
        unified: taddr,
    })
}

/*
// This new function handles the mnemonic-to-seed conversion.
pub fn generate_seed_from_mnemonic(mnemonic: String) -> Result<Vec<u8>, ZecError> {
    // Parse the mnemonic phrase, assuming English word list.
    let mnemonic = Mnemonic::<English>::from_phrase(&mnemonic)
        .map_err(|e| ZecError::GenericError(e.to_string()))?;

    // Create the seed with an empty passphrase, which is the standard.
    let seed_array = mnemonic.to_seed("");

    // The .to_seed() method returns a [u8; 64] array. Convert it to a Vec<u8>.
    Ok(seed_array.to_vec())
}

pub fn derive_addresses_from_mnemonic(mnemonic: String, network: Network) -> Result<ZecAddresses, ZecError> {
    let seed = generate_seed_from_mnemonic(mnemonic)?;
    derive_addresses_from_seed(seed, network)
}

pub fn derive_transparent_keys_from_mnemonic(mnemonic: String, network: Network) -> Result<ZecTransparentKeys, ZecError> {
    let seed = generate_seed_from_mnemonic(mnemonic)?;
    derive_transparent_keys_from_seed(seed, network)
}

pub fn derive_addresses_from_seed(seed: Vec<u8>, network: Network) -> Result<ZecAddresses, ZecError> {
    if seed.len() < 64 {
        return Err(ZecError::GenericError(
            "Seed must be at least 64 bytes".to_string(),
        ));
    }


    // Derive the master keys and the complete Unified Address.
    let usk = UnifiedSpendingKey::from_seed(
        &network,
        &seed,
        //AccountId::try_from(0).unwrap(),
        AccountId::ZERO,
    ).map_err(|e| ZecError::GenericError(format!("Failed to derive USK: {}", e)))?;

    let uivk = usk.to_unified_full_viewing_key().to_unified_incoming_viewing_key();

    // Create a request for a "rich" UA that contains all possible components.
    let request = UnifiedAddressRequest::unsafe_custom(
        Require, // Orchard
        Require, // Sapling
        Require, // Transparent (P2PKH)
    );

    // Generate the complete UnifiedAddress object. This is our source of truth.
    let (unified_addr, _) = uivk.default_address(request)
        .map_err(|e| ZecError::GenericError(format!("Failed to generate default address: {}", e)))?;

    // Extract the parts from the complete UA
    let mut t_addr = String::new();
    let mut s_addr = String::new();
    let mut o_addr = String::new();

    // Extract the transparent component, if it exists, and encode it.
    if let Some(transparent_receiver) = unified_addr.transparent() {
        t_addr = transparent_receiver.encode(&network);
    }

    // Extract the Sapling component, if it exists, and encode it.
    if let Some(sapling_receiver) = unified_addr.sapling() {
        s_addr = sapling_receiver.encode(&network);
    }

    // Extract the Orchard component, if it exists, and encode it.
    if let Some(orchard_receiver) = unified_addr.orchard() {
        // An individual Orchard receiver is encoded as a UA containing only that receiver.
        let orchard_ua = UnifiedAddress::from_receivers(Some(*orchard_receiver), None, None)
            .ok_or_else(|| ZecError::GenericError("Failed to construct Orchard-only UA for encoding".to_string()))?;
        let orchard_zcash_address = ZcashAddress::Unified(orchard_ua);
        o_addr = orchard_zcash_address.encode(&network);
    }
    
    Ok(ZecAddresses {
        unified: unified_addr.encode(&network),
        transparent: t_addr,
        sapling: s_addr,
        orchard: o_addr,
    })
}

pub fn derive_transparent_keys_from_seed(
    seed: Vec<u8>,
    network: Network,
) -> Result<ZecTransparentKeys, ZecError> {
    if seed.len() < 64 {
        return Err(ZecError::GenericError(
            "Seed must be at least 64 bytes".to_string(),
        ));
    }

    // Derive the UnifiedSpendingKey. This is the single source of truth.
    let usk = UnifiedSpendingKey::from_seed(&network, &seed, AccountId::ZERO)
        .map_err(|e| ZecError::GenericError(format!("Failed to derive USK: {}", e)))?;

    // Get the Unified Incoming Viewing Key to perform the cross-pool search.
    let uivk = usk.to_unified_full_viewing_key().to_unified_incoming_viewing_key();
    let request = UnifiedAddressRequest::unsafe_custom(
        Require,
        Require,
        Require,
    );

    // Find the default Unified Address AND the diversifier index that was used.
    // This finds the correct index 'N'.
    let (unified_addr, diversifier_index) = uivk.default_address(request)
        .map_err(|e| ZecError::GenericError(format!("Failed to find default UA: {}", e)))?;

    // Convert the found diversifier index into a transparent-specific child index.
    let transparent_index = NonHardenedChildIndex::try_from(diversifier_index)
        .map_err(|_| ZecError::GenericError("Could not convert diversifier index".to_string()))?;

    // Get the transparent account private key from the USK.
    let account_priv_key = usk.transparent();

    // Derive the secret key for the address at the index 'N'.
    let external_sk = account_priv_key
        .derive_external_secret_key(transparent_index)
        .map_err(|e| ZecError::GenericError(format!("Failed to derive external secret key: {}", e)))?;

    // Derive the public key from the secret key.
    let secp = Secp256k1::signing_only();
    let pubkey = external_sk.public_key(&secp);
    
    // Extract the address string from the unified address bundle we already found.
    let transparent_addr_str = unified_addr
        .transparent()
        .map(|t| t.encode(&network))
        .unwrap_or_default();

    // Build and return the final struct. All data is now consistent.
    Ok(ZecTransparentKeys {
        private_key: external_sk.secret_bytes().to_vec(),
        public_key: pubkey.serialize().to_vec(),
        address: transparent_addr_str,
        index: transparent_index.index(),
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::network::Network;
    use bip32::{ChildNumber, ExtendedPrivateKey, PrivateKey};
    use zcash_primitives::zip32::AccountId;
    use hex;
    use zcash_protocol::consensus::NetworkConstants;
    use secp256k1::SecretKey; 

    #[test]
    fn debug_derivation_discrepancy() {
        // The exact same 64-byte seed from your Go test logs.
        let seed_hex = "7acd51896a26b7acee2c9af0f68dd3e92fe0f396282229da18af300485ae34412374ed645cffe143763152e66ec7866a11b71aff9e89115196f57d9d311aa6bf";
        let seed = hex::decode(seed_hex).expect("Failed to decode seed hex");
        let network = Network::Regtest;


        // --- METHOD 1: The "Official" zcash_keys UnifiedSpendingKey Derivation ---
        let zcash_keys_result = derive_transparent_keys_from_seed(seed.clone(), network)
            .expect("zcash_keys derivation failed");
        

        // --- METHOD 2: "Standard" BIP44 Manual Derivation
        // This mimics what the standard Go libraries are doing.
        // Path: m/44'/133'/0'/0/<index> - mainnet transparent
        // Path: m/44'/1'/0'/0/<index> - regtest/testnet
        
        let master_xprv = ExtendedPrivateKey::<SecretKey>::new(seed.clone())
            .expect("Failed to create master key from seed");
        
        let account_xprv = master_xprv
            .derive_child(ChildNumber::new(44, true).unwrap())
            .and_then(|k| k.derive_child(ChildNumber::new(network.coin_type(), true).unwrap())) // 1 (regtest) 133 (mainnet)'
            .and_then(|k| k.derive_child(ChildNumber::new(u32::from(AccountId::AccountId::ZERO), true).unwrap())) // 0'
            .expect("Failed to derive account path");
        
        let change_xprv = account_xprv.derive_child(ChildNumber::new(0, false).unwrap()) // 0
            .expect("Failed to derive change path");

        // Use the index found by the official method to ensure we are comparing the same leaf node.
        let address_xprv = change_xprv.derive_child(ChildNumber::new(zcash_keys_result.index, false).unwrap())
            .expect("Failed to derive address path");

        let standard_priv_key = address_xprv.private_key();
        let secp = Secp256k1::new();
        let standard_pub_key = standard_priv_key.public_key(&secp);
        

        // --- ASSERTION ---
        // This assertion will almost certainly fail, and that will be our proof.
        assert_eq!(
            zcash_keys_result.private_key,
            standard_priv_key.to_bytes().to_vec(),
            "The private keys derived by the two methods DO NOT MATCH!"
        );
    }
}
*/
