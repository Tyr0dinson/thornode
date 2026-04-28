use std::collections::HashMap;

use anyhow::Result;
use zeroize::Zeroize;

use ciphersuite::group::GroupEncoding;
use ciphersuite::Ciphersuite;
use dalek_ff_group::{Ed25519, Scalar};
use dkg_pedpop::{Interpolation, Participant, ThresholdKeys, ThresholdParams};

/// Serialized FROST threshold keys for a participant.
///
/// Wraps Serai's `ThresholdKeys<Ed25519>` serialization, which contains:
/// - The participant's secret share
/// - The group's public key (verification key)
/// - All participants' verification shares
/// - The threshold, participant count, and ciphersuite ID
///
/// Stored encrypted at rest via the Store.
#[derive(Clone)]
pub struct ThresholdKeyShare {
    /// Serai ThresholdKeys<Ed25519> serialized data.
    data: Vec<u8>,
}

impl ThresholdKeyShare {
    /// Create from Serai ThresholdKeys produced by PedPoP DKG.
    pub fn from_threshold_keys(keys: &ThresholdKeys<Ed25519>) -> Self {
        let serialized = keys.serialize();
        ThresholdKeyShare {
            data: serialized.to_vec(),
        }
    }

    /// Create a deterministic 1-of-1 threshold share from a raw spend seed.
    pub fn from_spend_seed_hex(spend_key_hex: &str) -> Result<Self> {
        let spend_scalar = crate::xmr::spend_scalar_from_seed_hex(spend_key_hex)?;
        let participant =
            Participant::new(1).ok_or_else(|| anyhow::anyhow!("invalid bootstrap participant"))?;
        let params = ThresholdParams::new(1, 1, participant).map_err(|e| anyhow::anyhow!("{e}"))?;
        let secret_share = zeroize::Zeroizing::new(Scalar(*spend_scalar));

        let mut verification_shares = HashMap::new();
        verification_shares.insert(participant, Ed25519::generator() * *secret_share);

        let threshold_keys = ThresholdKeys::<Ed25519>::new(
            params,
            Interpolation::Lagrange,
            secret_share,
            verification_shares,
        )
        .map_err(|e| anyhow::anyhow!("failed to build bootstrap threshold keys: {e}"))?;

        Ok(Self::from_threshold_keys(&threshold_keys))
    }

    /// Deserialize back to Serai ThresholdKeys (e.g. for signing).
    pub fn to_threshold_keys(&self) -> Result<ThresholdKeys<Ed25519>> {
        ThresholdKeys::<Ed25519>::read(&mut self.data.as_slice())
            .map_err(|e| anyhow::anyhow!("failed to deserialize threshold keys: {}", e))
    }

    /// Get the hex-encoded group public key.
    pub fn group_key_hex(&self) -> Result<String> {
        let keys = self.to_threshold_keys()?;
        Ok(hex::encode(keys.group_key().to_bytes().as_ref()))
    }

    /// Serialize for storage.
    pub fn to_bytes(&self) -> Vec<u8> {
        self.data.clone()
    }

    /// Deserialize from storage.
    pub fn from_stored_bytes(bytes: &[u8]) -> Result<Self> {
        // Validate by attempting deserialization
        let _ = ThresholdKeys::<Ed25519>::read(&mut &bytes[..])
            .map_err(|e| anyhow::anyhow!("invalid threshold key data: {}", e))?;
        Ok(ThresholdKeyShare {
            data: bytes.to_vec(),
        })
    }
}

impl Drop for ThresholdKeyShare {
    fn drop(&mut self) {
        self.data.zeroize();
    }
}

impl Zeroize for ThresholdKeyShare {
    fn zeroize(&mut self) {
        self.data.zeroize();
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_key_share_too_short() {
        let result = ThresholdKeyShare::from_stored_bytes(&[0, 1, 2]);
        assert!(result.is_err());
    }

    #[test]
    fn test_key_share_invalid_data() {
        // Random 100 bytes should not parse as valid ThresholdKeys
        let data = vec![0u8; 100];
        let result = ThresholdKeyShare::from_stored_bytes(&data);
        assert!(result.is_err());
    }

    #[test]
    fn spend_seed_bootstrap_matches_expected_group_key() {
        let spend_key_hex = "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20";
        let key_share = ThresholdKeyShare::from_spend_seed_hex(spend_key_hex)
            .expect("bootstrap share should be created");
        let threshold_keys = key_share
            .to_threshold_keys()
            .expect("bootstrap share should deserialize");

        assert_eq!(threshold_keys.params().t(), 1);
        assert_eq!(threshold_keys.params().n(), 1);
        assert_eq!(
            key_share
                .group_key_hex()
                .expect("group key should be readable"),
            crate::xmr::group_key_hex_from_spend_seed_hex(spend_key_hex)
                .expect("expected group key should derive"),
        );
    }
}
