use anyhow::{Context, Result};
use monero_wallet::{OutputWithDecoys, WalletOutput};
use rand::SeedableRng;
use rand_chacha::ChaCha20Rng;
use sha2::{Digest, Sha256};
use tracing::warn;

use super::daemon::OxideRpc;

pub const RING_SIZE_HF14: u8 = 11;
pub const RING_SIZE_HF15: u8 = 16;

/// Maximum number of decoy-seed attempts before giving up. Each attempt
/// derives a different deterministic ring; if the underlying monero-wallet
/// crate rejects the ring (e.g. because one of the picked decoys references
/// a tx the local pruned monerod can't fully validate), we bump the attempt
/// counter and try again. All threshold signers iterate the same seed
/// sequence in lock-step, so determinism is preserved.
const MAX_DECOY_SEED_ATTEMPTS: u16 = 128;

pub fn ring_size_for_hf(hf_version: u8) -> Result<u8> {
    match hf_version {
        14 => Ok(RING_SIZE_HF14),
        15 | 16 => Ok(RING_SIZE_HF15),
        _ => anyhow::bail!(
            "unsupported Monero hard fork version {} (supported: 14, 15, 16)",
            hf_version
        ),
    }
}

pub async fn select_output_with_decoys(
    rpc: &OxideRpc,
    decoy_context: &str,
    output: &WalletOutput,
    hf_version: u8,
    tip_height: usize,
    input_position: usize,
) -> Result<OutputWithDecoys> {
    let ring_size = ring_size_for_hf(hf_version)
        .with_context(|| format!("cannot select decoys for hard fork version {hf_version}"))?;

    let mut last_err: Option<anyhow::Error> = None;
    let output_tx_hex = hex::encode(output.transaction());
    for attempt in 0..MAX_DECOY_SEED_ATTEMPTS {
        let mut rng = ChaCha20Rng::from_seed(derive_decoy_seed(
            decoy_context,
            output,
            input_position,
            attempt,
        ));
        match OutputWithDecoys::fingerprintable_deterministic_new(
            &mut rng,
            rpc,
            ring_size,
            tip_height,
            output.clone(),
        )
        .await
        {
            Ok(out) => return Ok(out),
            Err(e) => {
                warn!(
                    attempt = attempt,
                    output_tx = %output_tx_hex,
                    output_index_in_tx = output.index_in_transaction(),
                    output_index_on_chain = output.index_on_blockchain(),
                    input_position = input_position,
                    error = %e,
                    "decoy ring selection failed, retrying with next deterministic seed"
                );
                last_err = Some(anyhow::Error::new(e));
            }
        }
    }

    Err(last_err.unwrap_or_else(|| anyhow::anyhow!("decoy retries exhausted")))
        .context(format!(
            "failed to derive deterministic decoys for output tx {output_tx_hex} after {MAX_DECOY_SEED_ATTEMPTS} attempts"
        ))
}

fn derive_decoy_seed(
    decoy_context: &str,
    output: &WalletOutput,
    input_position: usize,
    attempt: u16,
) -> [u8; 32] {
    let mut hasher = Sha256::new();
    hasher.update(b"xmr-frost-decoy-seed-v4");
    hasher.update(decoy_context.as_bytes());
    hasher.update(output.transaction());
    hasher.update(output.index_in_transaction().to_le_bytes());
    hasher.update(output.index_on_blockchain().to_le_bytes());
    hasher.update((input_position as u64).to_le_bytes());
    hasher.update(attempt.to_le_bytes());
    hasher.finalize().into()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn ring_size_tracks_supported_hf() {
        assert_eq!(ring_size_for_hf(14).unwrap(), RING_SIZE_HF14);
        assert_eq!(ring_size_for_hf(15).unwrap(), RING_SIZE_HF15);
        assert_eq!(ring_size_for_hf(16).unwrap(), RING_SIZE_HF15);
    }

    #[test]
    fn ring_size_rejects_unknown_hf() {
        assert!(ring_size_for_hf(13)
            .unwrap_err()
            .to_string()
            .contains("unsupported"));
        assert!(ring_size_for_hf(17)
            .unwrap_err()
            .to_string()
            .contains("unsupported"));
    }
}
