use thiserror::Error;
use anyhow;

// Define an error enum that UniFFI can understand
#[derive(Debug, Error)]
pub enum ZecError {
    #[error("something went wrong: {0}")]
    GenericError(String),
    #[error("invalid Vault public key {0}")]
    InvalidVaultPubkey(anyhow::Error),
    #[error("invalid address: {0}")]
    InvalidAddress(String),
    #[error("initialization Failed: {0}")]
    InitError(String),
    #[error("invalid memo: {0}, error: {1}")]
    InvalidMemo(String, anyhow::Error),
    #[error("invalid amount: {0}, error: {1}")]
    InvalidAmount(u64, anyhow::Error),
    #[error("invalid output viewing key: {0}")]
    InvalidOVK(String),
    #[error("failed to scan shielded transaction: {0}")]
    ScanError(String),
    #[error("refund address must be transparent: {0}")]
    NonTransparentRefundAddress(String),
}

impl From<bip0039::Error> for ZecError {
    fn from(e: bip0039::Error) -> Self {
        ZecError::GenericError(e.to_string())
    }
}

impl From<zcash_keys::keys::DerivationError> for ZecError {
    fn from(e: zcash_keys::keys::DerivationError) -> Self {
        ZecError::GenericError(e.to_string())
    }
}

impl From<std::io::Error> for ZecError {
    fn from(e: std::io::Error) -> Self {
        ZecError::GenericError(e.to_string())
    }
}

impl<E: std::fmt::Display> From<zcash_primitives::transaction::builder::Error<E>> for ZecError {
    fn from(e: zcash_primitives::transaction::builder::Error<E>) -> Self {
        ZecError::GenericError(e.to_string())
    }
}

impl From<zcash_primitives::zip32::TryFromIntError> for ZecError {
    fn from(e: zcash_primitives::zip32::TryFromIntError) -> Self {
        ZecError::GenericError(e.to_string())
    }
}

impl From<zcash_transparent::builder::Error> for ZecError {
    fn from(e: zcash_transparent::builder::Error) -> Self {
        ZecError::GenericError(e.to_string())
    }
}

impl<E: std::fmt::Display> From<zcash_primitives::transaction::builder::FeeError<E>> for ZecError {
    fn from(e: zcash_primitives::transaction::builder::FeeError<E>) -> Self {
        ZecError::GenericError(e.to_string())
    }
}

pub fn to_zecerror<E>(anyerror: anyhow::Error) -> impl FnOnce(E) -> ZecError {
    move |_| ZecError::GenericError(anyerror.to_string())
}
