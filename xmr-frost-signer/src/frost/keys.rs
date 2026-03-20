use anyhow::Result;
use zeroize::Zeroize;

use ciphersuite::group::GroupEncoding;
use dalek_ff_group::Ed25519;
use dkg_pedpop::ThresholdKeys;

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

    /// Get the raw serialized data.
    pub fn key_data(&self) -> &[u8] {
        &self.data
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
}
