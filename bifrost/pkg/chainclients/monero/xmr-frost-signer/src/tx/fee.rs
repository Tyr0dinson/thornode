use anyhow::Result;
use monero_wallet::rpc::{FeePriority, FeeRate, Rpc};

use super::daemon::OxideRpc;

pub const MAX_REASONABLE_FEE_PER_WEIGHT: u64 = 10_000_000_000;

pub async fn estimate_fee_rate(rpc: &OxideRpc) -> Result<FeeRate> {
    let fee_rate = rpc
        .get_fee_rate(FeePriority::Normal)
        .await
        .map_err(|e| anyhow::anyhow!("failed to fetch fee rate from daemon: {}", e))?;

    // Sanity-check against absurd daemon responses.
    let per_weight = per_weight(&fee_rate)?;
    if per_weight == 0 || per_weight > MAX_REASONABLE_FEE_PER_WEIGHT {
        anyhow::bail!(
            "daemon fee rate per weight {} outside sane bounds (max {})",
            per_weight,
            MAX_REASONABLE_FEE_PER_WEIGHT
        );
    }
    Ok(fee_rate)
}

pub async fn resolve_fee_rate(rpc: &OxideRpc, fee_rate_override: Option<u64>) -> Result<FeeRate> {
    let Some(override_rate) = fee_rate_override else {
        return estimate_fee_rate(rpc).await;
    };

    // Override is interpreted as a per-weight fee with minimal quantization.
    FeeRate::new(override_rate.max(1), 1)
        .map_err(|e| anyhow::anyhow!("invalid fee rate override: {}", e))
}

pub fn per_weight(rate: &FeeRate) -> Result<u64> {
    let serialized = rate.serialize();
    let bytes: [u8; 8] = serialized
        .get(0..8)
        .ok_or_else(|| anyhow::anyhow!("invalid serialized fee rate: missing per_weight"))?
        .try_into()
        .map_err(|_| anyhow::anyhow!("invalid serialized fee rate bytes"))?;
    Ok(u64::from_le_bytes(bytes))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn fee_rate_override_requires_non_zero() {
        let constructed = FeeRate::new(0, 1);
        assert!(constructed.is_err());
    }

    #[test]
    fn per_weight_roundtrip() {
        let rate = FeeRate::new(1234, 7).unwrap();
        assert_eq!(per_weight(&rate).unwrap(), 1234);
    }
}
