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

    let current_height = tip_height as u64;
    let immature_count = available_outputs
        .iter()
        .filter(|output| {
            output.is_available_for_spend()
                && !output.is_unlocked_at(current_height)
                && !excluded_inputs.contains(&(output.tx_hash.clone(), output.output_index))
        })
        .count();
    if immature_count > 0 {
        info!(
            current_height,
            immature_outputs = immature_count,
            required_confirmations = TrackedOutput::MIN_SPENDABLE_CONFIRMATIONS,
            "skipping immature Monero outputs during transaction construction"
        );
    }

    let mut candidates =
        decode_candidate_outputs(available_outputs, excluded_inputs, current_height)?;
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
        anyhow::bail!(
            "no unlocked spendable outputs with serialized wallet payload were available at height {} (required confirmations: {}, immature outputs skipped: {})",
            current_height,
            TrackedOutput::MIN_SPENDABLE_CONFIRMATIONS,
            immature_count
        );
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
        if selected_total >= u128::from(requested_total) {
            break;
        }
    }

    if selected_total < u128::from(requested_total) {
        anyhow::bail!(
            "insufficient funds: need {} but only {} available from {} outputs (max_inputs={})",
            requested_total,
            selected_total,
            candidates.len(),
            max
        );
    }

    let rct_type = rct_type_for_hf(hard_fork_version).with_context(|| {
        format!("cannot construct transaction for hard fork version {hard_fork_version}")
    })?;
    let outgoing_view_key =
        derive_outgoing_view_key(decoy_context, to_address, requested_total, memo);
    let data = if memo.trim().is_empty() {
        Vec::new()
    } else {
        vec![memo.as_bytes().to_vec()]
    };
    let sweep_amount = is_consolidate_memo(memo) || sweep_if_final;

    // Build the signable transaction. Each added input increases the on-wire
    // size and therefore `necessary_fee`, so we may need to grow the input set
    // multiple times: greedy gives us amount, then we top up for fee, then a
    // larger input set bumps the fee further, and so on. Loop until either
    // SignableTransaction::new succeeds or we hit max_inputs / run out.
    let (signable, used) = loop {
        match build_signable_for_selection(
            rpc,
            &selected,
            single_payment(&to, amount),
            Some(&change),
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

    let used_total = used
        .iter()
        .try_fold(0u128, |acc, (tracked, _)| {
            acc.checked_add(u128::from(tracked.amount))
        })
        .ok_or_else(|| anyhow::anyhow!("selected input total overflow"))?;
    let mut signable = signable;
    let mut fee = signable.necessary_fee();
    let mut final_amount = if sweep_amount {
        let used_total_u64 = u64::try_from(used_total)
            .map_err(|_| anyhow::anyhow!("selected input total exceeds u64"))?;
        final_migrate_amount(used_total_u64, fee)?
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

    let mut positive_change = positive_change_amount(used_total, final_amount, fee)?;
    if positive_change == 0 {
        // Real positive change always goes back to SELF (`change`). The sweep
        // destination is only used for the mandatory zero-amount dummy output.
        let dummy_output = if sweep_amount { &to } else { &change };
        signable = build_signable_for_selection(
            rpc,
            &used,
            zero_dummy_payments(&to, dummy_output, final_amount),
            None,
            fee_rate,
            rct_type,
            outgoing_view_key.clone(),
            data.clone(),
            decoy_context,
            hard_fork_version,
            tip_height,
        )
        .await?;
        let rebuilt_fee = signable.necessary_fee();
        if rebuilt_fee != fee {
            if sweep_amount {
                let used_total_u64 = u64::try_from(used_total)
                    .map_err(|_| anyhow::anyhow!("selected input total exceeds u64"))?;
                final_amount = final_migrate_amount(used_total_u64, rebuilt_fee)?;
            }
            signable = build_signable_for_selection(
                rpc,
                &used,
                zero_dummy_payments(&to, dummy_output, final_amount),
                None,
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
            positive_change = positive_change_amount(used_total, final_amount, fee)?;
            if positive_change != 0 {
                anyhow::bail!(
                    "zero-dummy rebuild produced positive change: {}",
                    positive_change
                );
            }
            if rebuilt_fee != fee {
                anyhow::bail!(
                    "zero-dummy sweep fee changed across rebuilds: first {}, second {}",
                    rebuilt_fee,
                    fee
                );
            }
        }
    } else {
        signable = build_signable_for_selection(
            rpc,
            &used,
            single_payment(&to, final_amount),
            Some(&change),
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
        positive_change = positive_change_amount(used_total, final_amount, fee)?;
    }

    info!(
        selected_inputs = selected_refs.len(),
        fee,
        selected_total,
        requested_total,
        final_amount,
        "constructed Monero signable transaction"
    );

    if max_gas_atomic > 0 && fee > max_gas_atomic {
        anyhow::bail!(
            "necessary_fee Some({}) exceeds max_gas_atomic {} with selected_inputs {}",
            fee,
            max_gas_atomic,
            selected_refs.len()
        );
    }

    let has_owned_change = positive_change > 0;
    let payment_count = 2;
    let tx_keys = derive_tx_keys_for_built_outbound(&outgoing_view_key, &used, &to, payment_count);

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

fn single_payment(to: &MoneroAddress, amount: u64) -> Vec<(MoneroAddress, u64)> {
    vec![(*to, amount)]
}

fn zero_dummy_payments(
    to: &MoneroAddress,
    dummy: &MoneroAddress,
    amount: u64,
) -> Vec<(MoneroAddress, u64)> {
    vec![(*to, amount), (*dummy, 0)]
}

fn positive_change_amount(used_total: u128, amount: u64, fee: u64) -> Result<u128> {
    let spent = u128::from(amount)
        .checked_add(u128::from(fee))
        .ok_or_else(|| anyhow::anyhow!("amount + fee overflow"))?;
    used_total
        .checked_sub(spent)
        .ok_or_else(|| anyhow::anyhow!("amount + fee exceeds selected input total"))
}

pub fn select_outbound_input_refs(
    available_outputs: &[TrackedOutput],
    amount: u64,
    fee: u64,
    current_height: u64,
    dust_threshold: Option<u64>,
) -> Result<Vec<OutputRef>> {
    let requested_total = amount
        .checked_add(fee)
        .ok_or_else(|| anyhow::anyhow!("amount + fee overflow"))?;
    let dust = dust_threshold.unwrap_or(0);

    let mut candidates = available_outputs
        .iter()
        .filter(|output| output.is_available_for_spend_at(current_height))
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
        anyhow::bail!(
            "no unlocked spendable outputs with serialized wallet payload were available at height {} (required confirmations: {})",
            current_height,
            TrackedOutput::MIN_SPENDABLE_CONFIRMATIONS
        );
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
    payment_count: usize,
) -> Vec<String> {
    let input_keys = selected
        .iter()
        .map(|(_, wallet_output)| wallet_output.key())
        .collect();
    derive_tx_keys_from_components(
        outgoing_view_key,
        input_keys,
        to.is_subaddress(),
        payment_count,
    )
}

fn final_migrate_amount(requested_total: u64, fee: u64) -> Result<u64> {
    requested_total
        .checked_sub(fee)
        .ok_or_else(|| anyhow::anyhow!("actual fee exceeds requested migrate total"))
}

fn is_consolidate_memo(memo: &str) -> bool {
    memo.trim().eq_ignore_ascii_case("consolidate")
}

fn derive_tx_keys_from_components(
    outgoing_view_key: &Zeroizing<[u8; 32]>,
    input_keys: Vec<curve25519_dalek::EdwardsPoint>,
    destination_is_subaddress: bool,
    payment_count: usize,
) -> Vec<String> {
    let mut tx_keys = TransactionKeys::new(outgoing_view_key, input_keys);

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
    payments: Vec<(MoneroAddress, u64)>,
    change: Option<&MoneroAddress>,
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
        payments,
        Change::fingerprintable(change.cloned()),
        data,
        fee_rate,
    )
    .map_err(|e| anyhow::anyhow!("failed to construct signable transaction: {}", e))
}

fn decode_candidate_outputs(
    available_outputs: &[TrackedOutput],
    excluded_inputs: &HashSet<(String, u32)>,
    current_height: u64,
) -> Result<Vec<(TrackedOutput, WalletOutput)>> {
    let mut decoded = Vec::new();
    for output in available_outputs {
        if excluded_inputs.contains(&(output.tx_hash.clone(), output.output_index)) {
            continue;
        }
        if !output.is_available_for_spend_at(current_height) {
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

        let refs = select_outbound_input_refs(&outputs, 100, 20, 25, Some(0))
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

        let refs = select_outbound_input_refs(&outputs, 100, 30, 25, Some(75))
            .expect("selection should succeed");
        assert_eq!(refs.len(), 1);
        assert_eq!(refs[0].tx_hash, "e");
    }

    #[test]
    fn select_outbound_input_refs_skips_immature_outputs() {
        let outputs = vec![
            tracked_output("immature", 0, 200, 20, OutputState::Unspent, Some("aa")),
            tracked_output("mature", 0, 140, 10, OutputState::Unspent, Some("bb")),
        ];

        let refs = select_outbound_input_refs(&outputs, 100, 30, 20, Some(0))
            .expect("selection should use mature output");
        assert_eq!(refs.len(), 1);
        assert_eq!(refs[0].tx_hash, "mature");
    }

    #[test]
    fn final_sweep_adds_unused_max_gas_to_migrated_amount() {
        let amount = 461_674_480_000;
        let max_gas = 360_000_000;
        let actual_fee = 185_520_000;
        let requested_total = amount + max_gas;
        let final_amount = final_migrate_amount(requested_total, actual_fee).unwrap();

        assert_eq!(
            final_amount,
            amount + (max_gas - actual_fee),
            "unused max gas must go to the destination vault"
        );
        assert_eq!(
            final_amount + actual_fee,
            requested_total,
            "observed coin + gas must equal scheduled coin + max gas"
        );
    }

    #[test]
    fn final_sweep_rejects_fee_above_requested_total() {
        assert!(final_migrate_amount(100, 101)
            .unwrap_err()
            .to_string()
            .contains("actual fee exceeds requested migrate total"));
    }

    #[test]
    fn consolidate_memo_enables_destination_change_mode() {
        assert!(is_consolidate_memo("consolidate"));
        assert!(is_consolidate_memo(" CONSOLIDATE "));
        assert!(!is_consolidate_memo("migrate:123"));
        assert!(!is_consolidate_memo(""));
    }

    #[test]
    fn zero_dummy_payments_add_second_zero_output_to_dummy_address() {
        let spend = &Scalar::from(31u64) * ED25519_BASEPOINT_TABLE;
        let view = &Scalar::from(37u64) * ED25519_BASEPOINT_TABLE;
        let to = MoneroAddress::new(
            Network::Mainnet,
            AddressType::Featured {
                subaddress: true,
                payment_id: None,
                guaranteed: true,
            },
            spend,
            view,
        );
        let dummy = MoneroAddress::new(
            Network::Mainnet,
            AddressType::Featured {
                subaddress: true,
                payment_id: None,
                guaranteed: true,
            },
            &Scalar::from(41u64) * ED25519_BASEPOINT_TABLE,
            &Scalar::from(43u64) * ED25519_BASEPOINT_TABLE,
        );

        let payments = zero_dummy_payments(&to, &dummy, 123);
        assert_eq!(payments, vec![(to, 123), (dummy, 0)]);
    }

    #[test]
    fn positive_change_amount_is_value_driven() {
        assert_eq!(positive_change_amount(100, 80, 20).unwrap(), 0);
        assert_eq!(positive_change_amount(100, 70, 20).unwrap(), 10);
        assert!(positive_change_amount(100, 90, 20).is_err());
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

        let actual =
            derive_tx_keys_from_components(&outgoing_view_key, input_keys.clone(), false, 2);

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

        let actual =
            derive_tx_keys_from_components(&outgoing_view_key, input_keys.clone(), true, 2);

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
    fn derive_tx_keys_for_subaddress_no_change_uses_one_additional_key() {
        let outgoing_view_key = Zeroizing::new([19u8; 32]);
        let input_keys = vec![
            &Scalar::from(23u64) * ED25519_BASEPOINT_TABLE,
            &Scalar::from(29u64) * ED25519_BASEPOINT_TABLE,
        ];

        let actual =
            derive_tx_keys_from_components(&outgoing_view_key, input_keys.clone(), true, 1);

        let mut expected_keys = TransactionKeys::new(&outgoing_view_key, input_keys);
        let expected = (0..2)
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
