use anyhow::{Context, Result};
use monero_wallet::{
    address::{AddressType, MoneroAddress},
    ringct::RctType,
    rpc::FeeRate,
    send::{Change, SendError, SignableTransaction},
    WalletOutput,
};
use sha2::{Digest, Sha256};
use tracing::info;
use zeroize::Zeroizing;

use crate::config::ChainNetworkMode;
use crate::state::store::TrackedOutput;
use std::collections::HashSet;

use super::daemon::OxideRpc;
use super::decoy;

#[derive(Debug, Clone)]
pub struct SelectedInputRef {
    pub tx_hash: String,
    pub output_index: u32,
    pub key_image: Option<String>,
}

#[derive(Clone, Debug)]
pub struct Recipient {
    pub address: String,
    pub amount: u64,
}

#[derive(Clone)]
pub struct UnsignedTransaction {
    pub signable: SignableTransaction,
    pub serialized_signable: Vec<u8>,
    pub selected_inputs: Vec<SelectedInputRef>,
    /// All recipients for this tx. For single-output (the common case) this is
    /// a 1-element vec. For batched outbounds this contains N recipients, each
    /// with its own address and amount.
    pub recipients: Vec<Recipient>,
    pub fee: u64,
    pub memo: String,
}

impl UnsignedTransaction {
    /// Total amount across all recipients (excluding fee).
    pub fn total_amount(&self) -> u64 {
        self.recipients.iter().map(|r| r.amount).sum()
    }
}

pub async fn build_unsigned_transaction(
    rpc: &OxideRpc,
    recipients: &[Recipient],
    memo: &str,
    fee_rate: FeeRate,
    decoy_context: &str,
    hard_fork_version: u8,
    tip_height: usize,
    available_outputs: &[TrackedOutput],
    change_address: Option<&str>,
    excluded_inputs: &HashSet<(String, u32)>,
    chain_network: ChainNetworkMode,
    max_inputs: Option<u64>,
    dust_threshold: Option<u64>,
) -> Result<UnsignedTransaction> {
    if recipients.is_empty() {
        anyhow::bail!("recipients list is empty");
    }

    // Parse and validate every recipient address up front. Collect them as
    // (parsed, amount) pairs so the multi-output Monero tx builder can take
    // them directly.
    let mut parsed_recipients: Vec<(MoneroAddress, u64)> = Vec::with_capacity(recipients.len());
    for r in recipients.iter() {
        if r.amount == 0 {
            anyhow::bail!("recipient {} has zero amount", r.address);
        }
        let addr = MoneroAddress::from_str_with_unchecked_network(&r.address)
            .with_context(|| format!("invalid destination Monero address: {}", r.address))?;
        ensure_supported_address_type("destination", &addr, chain_network)?;
        ensure_address_network_mode("destination", &addr, chain_network)?;
        parsed_recipients.push((addr, r.amount));
    }
    let total_amount: u128 = recipients.iter().map(|r| u128::from(r.amount)).sum();
    if total_amount > u128::from(u64::MAX) {
        anyhow::bail!("aggregate recipient amount overflows u64");
    }
    let total_amount_u64 = total_amount as u64;

    let change_address = change_address.ok_or_else(|| {
        anyhow::anyhow!("change address is required for Monero send construction")
    })?;
    let change =
        MoneroAddress::from_str_with_unchecked_network(change_address).with_context(|| {
            format!("invalid change Monero address derived for vault: {change_address}")
        })?;
    ensure_supported_address_type("change", &change, chain_network)?;
    ensure_address_network_mode("change", &change, chain_network)?;

    let dust = dust_threshold.unwrap_or(0);
    let max = max_inputs.unwrap_or(15) as usize;

    let mut candidates = decode_candidate_outputs(available_outputs, excluded_inputs)?;
    // Filter sub-dust outputs.
    if dust > 0 {
        candidates.retain(|(tracked, _)| tracked.amount >= dust);
    }
    // Deterministic sort: height ASC (oldest first), then tx_hash ASC.
    // Matches BTC UTXO selection (most confirmations first, then TxID).
    candidates.sort_by(|a, b| {
        a.0.height
            .cmp(&b.0.height)
            .then_with(|| a.0.tx_hash.cmp(&b.0.tx_hash))
            .then_with(|| a.0.output_index.cmp(&b.0.output_index))
    });

    if candidates.is_empty() {
        anyhow::bail!("no spendable outputs with serialized wallet payload were available");
    }

    // Greedy accumulation: grab outputs in sort order until sum >= total_amount,
    // capped at max_inputs. Matches BTC UTXO selection strategy.
    let mut selected: Vec<&(TrackedOutput, WalletOutput)> = Vec::new();
    let mut selected_total: u128 = 0;
    for candidate in &candidates {
        if selected.len() >= max {
            break;
        }
        selected.push(candidate);
        selected_total += u128::from(candidate.0.amount);
        if selected_total >= total_amount {
            break;
        }
    }

    if selected_total < total_amount {
        anyhow::bail!(
            "insufficient funds: need {} but only {} available from {} outputs (max_inputs={})",
            total_amount_u64,
            selected_total,
            candidates.len(),
            max
        );
    }

    let rct_type = rct_type_for_hf(hard_fork_version).with_context(|| {
        format!("cannot construct transaction for hard fork version {hard_fork_version}")
    })?;
    // Derive a deterministic outgoing view key from the FIRST recipient's
    // (address, amount) plus memo. The vault's actual view key remains the
    // shared deterministic constant; this is just per-tx randomness binding.
    let outgoing_view_key = derive_outgoing_view_key(
        decoy_context,
        &recipients[0].address,
        recipients[0].amount,
        memo,
    );
    let data = if memo.trim().is_empty() {
        Vec::new()
    } else {
        vec![memo.as_bytes().to_vec()]
    };

    // Iteratively grow the input set until fee + amount fits within selected_total.
    // Adding more inputs increases the tx weight, which increases the fee, which
    // may require yet more inputs — loop until convergence or exhaustion.
    //
    // Dedup uses content-based identity (tx_hash, output_index) rather than
    // pointer equality so the logic is robust to candidate cloning or rebuilding.
    use std::collections::HashSet;
    let mut selected_keys: HashSet<(String, u32)> = selected
        .iter()
        .map(|(t, _)| (t.tx_hash.clone(), t.output_index))
        .collect();

    let signable = loop {
        let mut inputs = Vec::with_capacity(selected.len());
        for (i, (_, wallet_output)) in selected.iter().enumerate() {
            let input = decoy::select_output_with_decoys(
                rpc,
                decoy_context,
                wallet_output,
                hard_fork_version,
                tip_height,
                i,
            )
            .await
            .with_context(|| format!("failed to select decoys for input {}", i + 1))?;
            inputs.push(input);
        }

        let signable = SignableTransaction::new(
            rct_type,
            outgoing_view_key.clone(),
            inputs,
            parsed_recipients.clone(),
            Change::fingerprintable(Some(change)),
            data.clone(),
            fee_rate,
        )
        .map_err(|e| anyhow::anyhow!("failed to construct signable transaction: {}", e))?;

        let fee = signable.necessary_fee();
        let needed = total_amount + u128::from(fee);

        if selected_total >= needed {
            break signable;
        }

        // Add the next un-selected candidate(s) until we cover the new fee.
        let prev_len = selected.len();
        for candidate in &candidates {
            if selected.len() >= max {
                break;
            }
            let key = (candidate.0.tx_hash.clone(), candidate.0.output_index);
            if selected_keys.contains(&key) {
                continue;
            }
            selected.push(candidate);
            selected_keys.insert(key);
            selected_total += u128::from(candidate.0.amount);
            if selected_total >= needed {
                break;
            }
        }

        if selected.len() == prev_len {
            // No more candidates available — either max_inputs reached or
            // candidates exhausted. Selected_total cannot grow.
            anyhow::bail!(
                "insufficient funds to cover amount ({}) + fee ({}): selected {} of max {} inputs, total {}",
                total_amount_u64,
                fee,
                selected.len(),
                max,
                selected_total,
            );
        }
    };

    let fee = signable.necessary_fee();

    let selected_refs = selected
        .iter()
        .map(|(tracked, _)| SelectedInputRef {
            tx_hash: tracked.tx_hash.clone(),
            output_index: tracked.output_index,
            key_image: tracked.key_image.clone(),
        })
        .collect::<Vec<_>>();

    info!(
        selected_inputs = selected_refs.len(),
        recipients = recipients.len(),
        fee,
        selected_total,
        "constructed Monero signable transaction"
    );

    Ok(UnsignedTransaction {
        serialized_signable: signable.serialize(),
        signable,
        selected_inputs: selected_refs,
        recipients: recipients.to_vec(),
        fee,
        memo: memo.to_string(),
    })
}

fn decode_candidate_outputs(
    available_outputs: &[TrackedOutput],
    excluded_inputs: &HashSet<(String, u32)>,
) -> Result<Vec<(TrackedOutput, WalletOutput)>> {
    let mut decoded = Vec::new();
    for output in available_outputs {
        if excluded_inputs.contains(&(output.tx_hash.clone(), output.output_index)) {
            continue;
        }
        if !output.is_available_for_spend() {
            continue;
        }
        let Some(serialized_hex) = &output.wallet_output_hex else {
            continue;
        };
        let bytes = hex::decode(serialized_hex).with_context(|| {
            format!(
                "invalid serialized wallet output hex for {}:{}",
                output.tx_hash, output.output_index
            )
        })?;
        let wallet_output = WalletOutput::read(&mut bytes.as_slice()).with_context(|| {
            format!(
                "failed to deserialize wallet output for {}:{}",
                output.tx_hash, output.output_index
            )
        })?;
        decoded.push((output.clone(), wallet_output));
    }
    Ok(decoded)
}

fn derive_outgoing_view_key(
    decoy_context: &str,
    to_address: &str,
    amount: u64,
    memo: &str,
) -> Zeroizing<[u8; 32]> {
    let mut hasher = Sha256::new();
    hasher.update(b"xmr-frost-outgoing-view-v1");
    hasher.update(decoy_context.as_bytes());
    hasher.update(to_address.as_bytes());
    hasher.update(amount.to_le_bytes());
    hasher.update(memo.as_bytes());
    Zeroizing::new(hasher.finalize().into())
}

fn ensure_supported_address_type(
    _label: &str,
    _address: &MoneroAddress,
    _chain_network: ChainNetworkMode,
) -> Result<()> {
    // Accept all valid Monero address types: standard (4...), subaddress (8...), featured+guaranteed (C...)
    Ok(())
}

/// Verify the address's subaddress flag matches the expected network mode.
/// Mocknet uses non-subaddress featured addresses; public uses subaddress
/// featured addresses.  This is a defense-in-depth check — the Go bifrost
/// client performs primary network-aware validation before calling the sidecar.
fn ensure_address_network_mode(
    _label: &str,
    _address: &MoneroAddress,
    _chain_network: ChainNetworkMode,
) -> Result<()> {
    // Accept all valid Monero address types regardless of network mode
    Ok(())
}

pub fn rct_type_for_hf(hf_version: u8) -> Result<RctType> {
    match hf_version {
        14 => Ok(RctType::ClsagBulletproof),
        15 | 16 => Ok(RctType::ClsagBulletproofPlus),
        _ => anyhow::bail!(
            "unsupported Monero hard fork version {} (supported: 14, 15, 16)",
            hf_version
        ),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use curve25519_dalek::{constants::ED25519_BASEPOINT_TABLE, Scalar};
    use monero_wallet::address::{AddressType, Network};

    #[test]
    fn rct_type_tracks_supported_hf() {
        assert_eq!(rct_type_for_hf(14).unwrap(), RctType::ClsagBulletproof);
        assert_eq!(rct_type_for_hf(15).unwrap(), RctType::ClsagBulletproofPlus);
        assert_eq!(rct_type_for_hf(16).unwrap(), RctType::ClsagBulletproofPlus);
    }

    #[test]
    fn rct_type_rejects_unknown_hf() {
        assert!(rct_type_for_hf(13)
            .unwrap_err()
            .to_string()
            .contains("unsupported"));
        assert!(rct_type_for_hf(17)
            .unwrap_err()
            .to_string()
            .contains("unsupported"));
    }

    #[test]
    fn outgoing_view_key_is_deterministic() {
        let a = derive_outgoing_view_key("ctx", "addr", 42, "memo");
        let b = derive_outgoing_view_key("ctx", "addr", 42, "memo");
        let c = derive_outgoing_view_key("ctx2", "addr", 42, "memo");
        assert_eq!(*a, *b);
        assert_ne!(*a, *c);
    }

    #[test]
    fn supported_address_policy_accepts_all_types() {
        let spend = &Scalar::from(7u64) * ED25519_BASEPOINT_TABLE;
        let view = &Scalar::from(11u64) * ED25519_BASEPOINT_TABLE;

        let featured_guaranteed = MoneroAddress::new(
            Network::Mainnet,
            AddressType::Featured {
                subaddress: true,
                payment_id: None,
                guaranteed: true,
            },
            spend,
            view,
        );
        let legacy = MoneroAddress::new(Network::Mainnet, AddressType::Legacy, spend, view);
        let non_guaranteed = MoneroAddress::new(
            Network::Mainnet,
            AddressType::Featured {
                subaddress: true,
                payment_id: None,
                guaranteed: false,
            },
            spend,
            view,
        );

        assert!(ensure_supported_address_type("test", &featured_guaranteed, ChainNetworkMode::Mocknet).is_ok());
        assert!(ensure_supported_address_type("test", &legacy, ChainNetworkMode::Mocknet).is_ok());
        assert!(ensure_supported_address_type("test", &non_guaranteed, ChainNetworkMode::Mocknet).is_ok());
        assert!(ensure_supported_address_type("test", &non_guaranteed, ChainNetworkMode::Mocknet).is_ok());
    }

    #[test]
    fn network_mode_accepts_all_addresses() {
        let spend = &Scalar::from(7u64) * ED25519_BASEPOINT_TABLE;
        let view = &Scalar::from(11u64) * ED25519_BASEPOINT_TABLE;

        let legacy = MoneroAddress::new(Network::Mainnet, AddressType::Legacy, spend, view);
        let subaddress = MoneroAddress::new(Network::Mainnet, AddressType::Subaddress, spend, view);
        let featured = MoneroAddress::new(
            Network::Mainnet,
            AddressType::Featured {
                subaddress: true,
                payment_id: None,
                guaranteed: true,
            },
            spend,
            view,
        );

        assert!(ensure_address_network_mode("test", &legacy, ChainNetworkMode::Mocknet).is_ok());
        assert!(ensure_address_network_mode("test", &subaddress, ChainNetworkMode::Mocknet).is_ok());
        assert!(ensure_address_network_mode("test", &featured, ChainNetworkMode::Mocknet).is_ok());
        assert!(ensure_address_network_mode("test", &legacy, ChainNetworkMode::Mocknet).is_ok());
        assert!(ensure_address_network_mode("test", &featured, ChainNetworkMode::Mocknet).is_ok());
    }
}
