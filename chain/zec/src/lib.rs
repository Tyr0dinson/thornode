#![allow(unused_imports)]

use once_cell::sync::OnceCell;

use tracing::{debug, error, info, warn}; // Import tracing macros
use tracing_subscriber::layer::SubscriberExt as _;
use tracing_subscriber::util::SubscriberInitExt as _;
use tracing_subscriber::{fmt, EnvFilter};

// Declare modules
pub mod error;
pub mod addr;
pub mod network;
pub mod tx;
pub mod config;
pub mod keys;
#[cfg(feature = "shielded")]
pub mod sapling_sender;
#[cfg(feature = "shielded")]
pub mod orchard_sender;

#[cfg(feature = "e2e")]
#[allow(clippy::uninlined_format_args, clippy::single_match)]
pub mod regtest_integration;

// NAPI bindings module
#[cfg(feature = "napi")]
pub mod napi_bindings;

#[cfg(feature = "napi")]
pub mod test_napi;

// Re-export NAPI bindings for Node.js when the napi feature is enabled
#[cfg(feature = "napi")]
pub use napi_bindings::*;

// Import the functions needed by the UniFFI scaffolding
use crate::addr::{validate_address_v124, validate_address_v1, is_transparent_address as is_transparent_address_impl};
use crate::network::Network;
use crate::error::ZecError;
use crate::tx::{UTXO, Output, Sighash, PartialTx, Sighashes, build_ptx, apply_signatures, compute_txid, get_ovk};
use crate::config::{init_logger, init_zec};
use crate::keys::{ZecAccount, derive_account_from_mnemonic, BranchId};
#[cfg(feature = "shielded")]
use crate::sapling_sender::{send_from_sapling_to_transparent, build_witness_from_blocks, decrypt_notes_in_tx, SpendableNote, DecryptedNote, WitnessParts};
#[cfg(feature = "shielded")]
use crate::orchard_sender::{send_from_orchard_to_transparent, decrypt_orchard_notes_in_tx, build_orchard_witness_from_blocks, OrchardDecryptedNote, OrchardSpendableNote, OrchardWitnessParts};

// Stub functions for when shielded feature is not enabled
#[cfg(not(feature = "shielded"))]
pub fn send_from_sapling_to_transparent(
    _mnemonic: String,
    _network: Network,
    _notes_to_spend: Vec<SpendableNote>,
    _to_transparent_address: String,
    _anchor_bytes: Vec<u8>,
    _target_height: u32,
    _memo: Option<String>,
) -> Result<Vec<u8>, ZecError> {
    Err(ZecError::GenericError("Sapling transactions not supported in production mode".into()))
}

#[cfg(not(feature = "shielded"))]
#[allow(clippy::too_many_arguments)]
pub fn build_witness_from_blocks(
    _mnemonic: String,
    _network: Network,
    _diversifier: Vec<u8>,
    _value: u64,
    _rseed: Vec<u8>,
    _zip212: bool,
    _blocks_txs: Vec<Vec<Vec<u8>>>,
    _branch_id_at_anchor: BranchId,
    _expected_root: Option<Vec<u8>>,
) -> Result<WitnessParts, ZecError> {
    Err(ZecError::GenericError("Sapling witness building not supported in production mode".into()))
}

#[cfg(not(feature = "shielded"))]
pub fn decrypt_notes_in_tx(
    _mnemonic: String,
    _network: Network,
    _tx_bytes: Vec<u8>,
    _mined_height: u32,
) -> Result<Vec<DecryptedNote>, ZecError> {
    Err(ZecError::GenericError("Sapling note decryption not supported in production mode".into()))
}

#[cfg(not(feature = "shielded"))]
pub fn send_from_orchard_to_transparent(
    _mnemonic: String,
    _network: Network,
    _notes_to_spend: Vec<OrchardSpendableNote>,
    _to_transparent_address: String,
    _anchor_bytes: Vec<u8>,
    _target_height: u32,
    _memo: Option<String>,
) -> Result<Vec<u8>, ZecError> {
    Err(ZecError::GenericError("Orchard transactions not supported in production mode".into()))
}

#[cfg(not(feature = "shielded"))]
pub fn decrypt_orchard_notes_in_tx(
    _mnemonic: String,
    _network: Network,
    _tx_bytes: Vec<u8>,
    _mined_height: u32,
) -> Result<Vec<OrchardDecryptedNote>, ZecError> {
    Err(ZecError::GenericError("Orchard note decryption not supported in production mode".into()))
}

#[cfg(not(feature = "shielded"))]
#[allow(clippy::too_many_arguments)]
pub fn build_orchard_witness_from_blocks(
    _mnemonic: String,
    _network: Network,
    _diversifier: Vec<u8>,
    _value: u64,
    _rseed: Vec<u8>,
    _rho: Vec<u8>,
    _cmx: Vec<u8>,
    _blocks_txs: Vec<Vec<Vec<u8>>>,
    _branch_id_at_anchor: BranchId,
    _expected_root: Option<Vec<u8>>,
) -> Result<OrchardWitnessParts, ZecError> {
    Err(ZecError::GenericError("Orchard witness building not supported in production mode".into()))
}

// Stub types for production mode
#[cfg(not(feature = "shielded"))]
pub struct SpendableNote {
    pub diversifier: Vec<u8>,
    pub value: u64,
    pub rseed: Vec<u8>,
    pub zip212: bool,
    pub witness: Vec<u8>,
}

#[cfg(not(feature = "shielded"))]
pub struct DecryptedNote {
    pub diversifier: Vec<u8>,
    pub value: u64,
    pub rseed: Vec<u8>,
    pub zip212: bool,
}

#[cfg(not(feature = "shielded"))]
pub struct WitnessParts {
    pub witness_bytes: Vec<u8>,
    pub anchor_bytes: Vec<u8>,
}

#[cfg(not(feature = "shielded"))]
pub struct OrchardSpendableNote {
    pub diversifier: Vec<u8>,
    pub value: u64,
    pub rseed: Vec<u8>,
    pub rho: Vec<u8>,
    pub witness: Vec<u8>,
    pub cmx: Vec<u8>,
    pub addr_bytes: Vec<u8>,
}

#[cfg(not(feature = "shielded"))]
pub struct OrchardDecryptedNote {
    pub diversifier: Vec<u8>,
    pub value: u64,
    pub rseed: Vec<u8>,
    pub rho: Vec<u8>,
    pub cmx: Vec<u8>,
    pub addr_bytes: Vec<u8>,
}

#[cfg(not(feature = "shielded"))]
pub struct OrchardWitnessParts {
    pub witness_bytes: Vec<u8>,
    pub anchor_bytes: Vec<u8>,
}

// Wrapper function for UniFFI that returns bool
// Perform the address-type check in all builds (pure validation only).
#[cfg(feature = "uniffi")]
pub fn validate_shielded_source(address: String, network: Network) -> Result<bool, ZecError> {
    match crate::addr::validate_shielded_source(&address, &network) {
        Ok(()) => Ok(true),
        Err(_) => Ok(false),
    }
}

// Wrapper function for UniFFI
#[cfg(feature = "uniffi")]
pub fn is_transparent_address(address: String, network: Network) -> bool {
    is_transparent_address_impl(&address, &network)
}

// Include the code generated by UniFFI based on interface.udl (only when uniffi feature is enabled)
#[cfg(feature = "uniffi")]
uniffi::include_scaffolding!("interface");
