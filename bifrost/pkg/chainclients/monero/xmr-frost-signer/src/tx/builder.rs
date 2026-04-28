use anyhow::{Context, Result};
use monero_wallet::{
    address::MoneroAddress,
    ringct::RctType,
    rpc::FeeRate,
    send::{Change, SignableTransaction, TransactionKeys},
    WalletOutput,
};
use sha2::{Digest, Sha256};
use tracing::info;
use zeroize::Zeroizing;

use crate::config::ChainNetworkMode;
use crate::state::store::{OutputRef, TrackedOutput};
use std::collections::HashSet;

use super::daemon::OxideRpc;
use super::decoy;

#[derive(Debug, Clone)]
pub struct SelectedInputRef {
    pub tx_hash: String,
    pub output_index: u32,
}

#[derive(Clone)]
pub struct UnsignedTransaction {
    pub signable: SignableTransaction,
    pub selected_inputs: Vec<SelectedInputRef>,
    pub to_address: String,
    pub amount: u64,
    pub fee: u64,
    pub memo: String,
    pub tx_keys: Vec<String>,
    pub has_owned_change: bool,
}

pub async fn build_unsigned_transaction(
    rpc: &OxideRpc,
    to_address: &str,
    amount: u64,
    max_gas_atomic: u64,
    sweep_if_final: bool,
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
    let to = MoneroAddress::from_str_with_unchecked_network(to_address)
        .with_context(|| format!("invalid destination Monero address: {to_address}"))?;
    ensure_supported_address_type("destination", &to, chain_network)?;
    ensure_address_network_mode("destination", &to, chain_network)?;
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
    let requested_total = if sweep_if_final {
        amount
            .checked_add(max_gas_atomic)
            .ok_or_else(|| anyhow::anyhow!("amount + max_gas_atomic overflow"))?
    } else {
        amount
    };

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

    // Greedy accumulation: grab outputs in sort order until sum >= requested_total,
    // capped at max_inputs. Matches BTC UTXO selection strategy.
    let mut selected: Vec<&(TrackedOutput, WalletOutput)> = Vec::new();
    let mut selected_total: u128 = 0;
    for candidate in &candidates {
        if selected.len() >= max {
            break;
        }
        selected.push(candidate);
        selected_total += u128::from(candidate.0.amount);
        if !sweep_if_final && selected_total >= u128::from(requested_total) {
            break;
        }
    }

    if selected_total < u128::from(requested_total) && !sweep_if_final {
        anyhow::bail!(
            "insufficient funds: need {} but only {} available from {} outputs (max_inputs={})",
            requested_total,
            selected_total,
            candidates.len(),
            max
        );
    }
    let effective_total =
        effective_requested_total(requested_total, selected_total, sweep_if_final)?;

    let rct_type = rct_type_for_hf(hard_fork_version).with_context(|| {
        format!("cannot construct transaction for hard fork version {hard_fork_version}")
    })?;
    let outgoing_view_key =
        derive_outgoing_view_key(decoy_context, to_address, effective_total, memo);
    let data = if memo.trim().is_empty() {
        Vec::new()
    } else {
        vec![memo.as_bytes().to_vec()]
    };

    // Build the signable transaction. Each added input increases the on-wire
    // size and therefore `necessary_fee`, so we may need to grow the input set
    // multiple times: greedy gives us amount, then we top up for fee, then a
    // larger input set bumps the fee further, and so on. Loop until either
    // SignableTransaction::new succeeds or we hit max_inputs / run out.
    let (mut signable, used) = loop {
        match build_signable_for_selection(
            rpc,
            &selected,
            &to,
            effective_total,
            &change,
            fee_rate,
            rct_type,
            outgoing_view_key.clone(),
            data.clone(),
            decoy_context,
            hard_fork_version,
            tip_height,
        )
        .await
        {
            Ok(s) => break (s, selected.clone()),
            Err(e) => {
                // The crate refuses with "not enough funds" once it computes
                // the real fee for the chosen ring sizes. Add another input
                // and retry. Other errors are fatal.
                let msg = e.to_string();
                if !msg.contains("not enough funds") {
                    return Err(anyhow::anyhow!(
                        "failed to construct signable transaction: {}",
                        msg
                    ));
                }
                let mut grew = false;
                for candidate in &candidates {
                    if selected.len() >= max {
                        break;
                    }
                    if selected.iter().any(|s| std::ptr::eq(*s, candidate)) {
                        continue;
                    }
                    selected.push(candidate);
                    selected_total += u128::from(candidate.0.amount);
                    grew = true;
                    break;
                }
                if !grew {
                    if sweep_if_final {
                        // Final-round migrate sweeps intentionally target the
                        // whole selected balance and then back the real fee out
                        // afterward. When the selection is already exhausted,
                        // bootstrap the fee-convergence loop from the scheduled
                        // net amount instead of bailing on the first fee-aware
                        // construction attempt.
                        let bootstrap_amount = amount.min(
                            effective_total.checked_sub(max_gas_atomic).ok_or_else(|| {
                                anyhow::anyhow!(
                                    "selected sweep total is not enough to cover max gas"
                                )
                            })?,
                        );
                        let fallback = build_signable_for_selection(
                            rpc,
                            &selected,
                            &to,
                            bootstrap_amount,
                            &change,
                            fee_rate,
                            rct_type,
                            outgoing_view_key.clone(),
                            data.clone(),
                            decoy_context,
                            hard_fork_version,
                            tip_height,
                        )
                        .await;
                        match fallback {
                            Ok(s) => break (s, selected.clone()),
                            Err(fallback_err) => {
                                return Err(anyhow::anyhow!(
                                    "insufficient funds to cover amount ({}) + necessary fee: \
                                     exhausted candidates ({} selected, max {}, total {}): {}; \
                                     fallback sweep build with net amount {} also failed: {}",
                                    effective_total,
                                    selected.len(),
                                    max,
                                    selected_total,
                                    msg,
                                    bootstrap_amount,
                                    fallback_err
                                ));
                            }
                        }
                    }
                    anyhow::bail!(
                        "insufficient funds to cover amount ({}) + necessary fee: \
                         exhausted candidates ({} selected, max {}, total {}): {}",
                        requested_total,
                        selected.len(),
                        max,
                        selected_total,
                        msg
                    );
                }
            }
        }
    };

    let mut fee = signable.necessary_fee();
    let final_amount = if sweep_if_final {
        let mut send_amount = effective_total
            .checked_sub(fee)
            .ok_or_else(|| anyhow::anyhow!("actual fee exceeds requested migrate total"))?;
        for _ in 0..2 {
            signable = build_signable_for_selection(
                rpc,
                &used,
                &to,
                send_amount,
                &change,
                fee_rate,
                rct_type,
                outgoing_view_key.clone(),
                data.clone(),
                decoy_context,
                hard_fork_version,
                tip_height,
            )
            .await?;
            fee = signable.necessary_fee();
            let adjusted_amount = effective_total
                .checked_sub(fee)
                .ok_or_else(|| anyhow::anyhow!("actual fee exceeds requested migrate total"))?;
            if adjusted_amount == send_amount {
                break;
            }
            send_amount = adjusted_amount;
        }
        send_amount
    } else {
        amount
    };
    let selected_refs = used
        .iter()
        .map(|(tracked, _)| SelectedInputRef {
            tx_hash: tracked.tx_hash.clone(),
            output_index: tracked.output_index,
        })
        .collect::<Vec<_>>();

    info!(
        selected_inputs = selected_refs.len(),
        fee,
        selected_total,
        requested_total,
        effective_total,
        final_amount,
        "constructed Monero signable transaction"
    );

    let tx_keys = derive_tx_keys_for_built_outbound(&outgoing_view_key, &used, &to);
    let has_owned_change = selected_total
        > u128::from(final_amount)
            .checked_add(u128::from(fee))
            .ok_or_else(|| anyhow::anyhow!("final amount + fee overflow"))?;

    Ok(UnsignedTransaction {
        signable,
        selected_inputs: selected_refs,
        to_address: to_address.to_string(),
        amount: final_amount,
        fee,
        memo: memo.to_string(),
        tx_keys,
        has_owned_change,
    })
}

pub fn select_outbound_input_refs(
    available_outputs: &[TrackedOutput],
    amount: u64,
    fee: u64,
    dust_threshold: Option<u64>,
) -> Result<Vec<OutputRef>> {
    let requested_total = amount
        .checked_add(fee)
        .ok_or_else(|| anyhow::anyhow!("amount + fee overflow"))?;
    let dust = dust_threshold.unwrap_or(0);

    let mut candidates = available_outputs
        .iter()
        .filter(|output| output.is_available_for_spend())
        .filter(|output| {
            output
                .wallet_output_hex
                .as_ref()
                .is_some_and(|hex| !hex.trim().is_empty())
        })
        .filter(|output| dust == 0 || output.amount >= dust)
        .cloned()
        .collect::<Vec<_>>();
    candidates.sort_by(|a, b| {
        a.height
            .cmp(&b.height)
            .then_with(|| a.tx_hash.cmp(&b.tx_hash))
            .then_with(|| a.output_index.cmp(&b.output_index))
    });

    if candidates.is_empty() {
        anyhow::bail!("no spendable outputs with serialized wallet payload were available");
    }

    let mut selected_total: u128 = 0;
    let mut selected_refs = Vec::new();
    for output in candidates {
        selected_total += u128::from(output.amount);
        selected_refs.push(OutputRef {
            tx_hash: output.tx_hash,
            output_index: output.output_index,
        });
        if selected_total >= u128::from(requested_total) {
            return Ok(selected_refs);
        }
    }

    anyhow::bail!(
        "insufficient locally tracked funds to attribute outbound: need {} but only {} available from {} outputs",
        requested_total,
        selected_total,
        selected_refs.len()
    )
}

fn derive_tx_keys_for_built_outbound(
    outgoing_view_key: &Zeroizing<[u8; 32]>,
    selected: &[&(TrackedOutput, WalletOutput)],
    to: &MoneroAddress,
) -> Vec<String> {
    let input_keys = selected
        .iter()
        .map(|(_, wallet_output)| wallet_output.key())
        .collect();
    derive_tx_keys_from_components(outgoing_view_key, input_keys, to.is_subaddress())
}

fn effective_requested_total(
    requested_total: u64,
    selected_total: u128,
    sweep_if_final: bool,
) -> Result<u64> {
    if sweep_if_final && selected_total < u128::from(requested_total) {
        return u64::try_from(selected_total)
            .map_err(|_| anyhow::anyhow!("selected sweep total exceeds u64"));
    }
    Ok(requested_total)
}

fn derive_tx_keys_from_components(
    outgoing_view_key: &Zeroizing<[u8; 32]>,
    input_keys: Vec<curve25519_dalek::EdwardsPoint>,
    destination_is_subaddress: bool,
) -> Vec<String> {
    let mut tx_keys = TransactionKeys::new(outgoing_view_key, input_keys);

    let payment_count = 2usize; // one outbound payment plus one fingerprintable change output
    let mut derived = Vec::with_capacity(1 + payment_count);
    derived.push(hex::encode(
        tx_keys
            .next()
            .expect("TransactionKeys (never-ending) was exhausted")
            .to_bytes(),
    ));

    // fingerprintable change uses AddressOnly, so a subaddress destination still needs
    // additional keys even in the common two-output payment+change case.
    if destination_is_subaddress {
        for _ in 0..payment_count {
            derived.push(hex::encode(
                tx_keys
                    .next()
                    .expect("TransactionKeys (never-ending) was exhausted")
                    .to_bytes(),
            ));
        }
    }

    derived
}

async fn build_signable_for_selection(
    rpc: &OxideRpc,
    selected: &[&(TrackedOutput, WalletOutput)],
    to: &MoneroAddress,
    amount: u64,
    change: &MoneroAddress,
    fee_rate: FeeRate,
    rct_type: RctType,
    outgoing_view_key: Zeroizing<[u8; 32]>,
    data: Vec<Vec<u8>>,
    decoy_context: &str,
    hard_fork_version: u8,
    tip_height: usize,
) -> Result<SignableTransaction> {
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

    SignableTransaction::new(
        rct_type,
        outgoing_view_key,
        inputs,
        vec![(to.clone(), amount)],
        Change::fingerprintable(Some(change.clone())),
        data,
        fee_rate,
    )
    .map_err(|e| anyhow::anyhow!("failed to construct signable transaction: {}", e))
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

/// Verify the address matches the expected network mode.
/// The sidecar currently accepts all valid Monero address types in both modes.
/// THOR's network scoping is enforced by the THOR build and the connected
/// monerod rather than by Monero address prefixes alone.
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
    use crate::state::store::OutputState;
    use curve25519_dalek::{constants::ED25519_BASEPOINT_TABLE, Scalar};
    use monero_wallet::address::{AddressType, Network};

    fn tracked_output(
        tx_hash: &str,
        output_index: u32,
        amount: u64,
        height: u64,
        state: OutputState,
        wallet_output_hex: Option<&str>,
    ) -> TrackedOutput {
        TrackedOutput {
            tx_hash: tx_hash.to_string(),
            output_index,
            amount,
            height,
            key_image: None,
            spent_by_key_image_height: None,
            state,
            reservation: None,
            subaddress_major: 0,
            subaddress_minor: 0,
            wallet_output_hex: wallet_output_hex.map(str::to_string),
            address: None,
            note: None,
        }
    }

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
    fn select_outbound_input_refs_uses_deterministic_prefix() {
        let outputs = vec![
            tracked_output("b", 0, 90, 12, OutputState::Unspent, Some("aa")),
            tracked_output("a", 0, 70, 10, OutputState::Unspent, Some("bb")),
            tracked_output("c", 0, 50, 14, OutputState::Unspent, Some("cc")),
        ];

        let refs = select_outbound_input_refs(&outputs, 100, 20, Some(0))
            .expect("selection should succeed");
        assert_eq!(refs.len(), 2);
        assert_eq!(refs[0].tx_hash, "a");
        assert_eq!(refs[1].tx_hash, "b");
    }

    #[test]
    fn select_outbound_input_refs_skips_non_spendable_or_subdust_outputs() {
        let outputs = vec![
            tracked_output("a", 0, 40, 10, OutputState::Spent, Some("aa")),
            tracked_output("b", 0, 50, 11, OutputState::Reserved, Some("bb")),
            tracked_output("c", 0, 60, 12, OutputState::Unspent, None),
            tracked_output("d", 0, 70, 13, OutputState::Unspent, Some("dd")),
            tracked_output("e", 0, 140, 14, OutputState::Unspent, Some("ee")),
        ];

        let refs = select_outbound_input_refs(&outputs, 100, 30, Some(75))
            .expect("selection should succeed");
        assert_eq!(refs.len(), 1);
        assert_eq!(refs[0].tx_hash, "e");
    }

    #[test]
    fn final_sweep_uses_selected_total_when_short_of_requested_total() {
        assert_eq!(
            effective_requested_total(318_029_330_000, 316_929_650_000, true).unwrap(),
            316_929_650_000
        );
        assert_eq!(
            effective_requested_total(318_029_330_000, 316_929_650_000, false).unwrap(),
            318_029_330_000
        );
        assert_eq!(
            effective_requested_total(318_029_330_000, 400_000_000_000, true).unwrap(),
            318_029_330_000
        );
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
    fn derive_tx_keys_for_standard_destination_uses_primary_key_only() {
        let outgoing_view_key = Zeroizing::new([7u8; 32]);
        let input_keys = vec![
            &Scalar::from(3u64) * ED25519_BASEPOINT_TABLE,
            &Scalar::from(5u64) * ED25519_BASEPOINT_TABLE,
        ];

        let actual = derive_tx_keys_from_components(&outgoing_view_key, input_keys.clone(), false);

        let mut expected_keys = TransactionKeys::new(&outgoing_view_key, input_keys);
        let expected = vec![hex::encode(
            expected_keys
                .next()
                .expect("TransactionKeys (never-ending) was exhausted")
                .to_bytes(),
        )];

        assert_eq!(actual, expected);
    }

    #[test]
    fn derive_tx_keys_for_subaddress_destination_includes_additional_keys() {
        let outgoing_view_key = Zeroizing::new([11u8; 32]);
        let input_keys = vec![
            &Scalar::from(13u64) * ED25519_BASEPOINT_TABLE,
            &Scalar::from(17u64) * ED25519_BASEPOINT_TABLE,
        ];

        let actual = derive_tx_keys_from_components(&outgoing_view_key, input_keys.clone(), true);

        let mut expected_keys = TransactionKeys::new(&outgoing_view_key, input_keys);
        let expected = (0..3)
            .map(|_| {
                hex::encode(
                    expected_keys
                        .next()
                        .expect("TransactionKeys (never-ending) was exhausted")
                        .to_bytes(),
                )
            })
            .collect::<Vec<_>>();

        assert_eq!(actual, expected);
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

        assert!(ensure_supported_address_type(
            "test",
            &featured_guaranteed,
            ChainNetworkMode::Mocknet
        )
        .is_ok());
        assert!(ensure_supported_address_type("test", &legacy, ChainNetworkMode::Mocknet).is_ok());
        assert!(
            ensure_supported_address_type("test", &non_guaranteed, ChainNetworkMode::Mocknet)
                .is_ok()
        );
        assert!(
            ensure_supported_address_type("test", &non_guaranteed, ChainNetworkMode::Mocknet)
                .is_ok()
        );
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
        assert!(
            ensure_address_network_mode("test", &subaddress, ChainNetworkMode::Mocknet).is_ok()
        );
        assert!(ensure_address_network_mode("test", &featured, ChainNetworkMode::Mocknet).is_ok());
        assert!(ensure_address_network_mode("test", &legacy, ChainNetworkMode::Mocknet).is_ok());
        assert!(ensure_address_network_mode("test", &featured, ChainNetworkMode::Mocknet).is_ok());
    }
}
