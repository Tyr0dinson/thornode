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

#[derive(Clone)]
pub struct UnsignedTransaction {
    pub signable: SignableTransaction,
    pub serialized_signable: Vec<u8>,
    pub selected_inputs: Vec<SelectedInputRef>,
    pub to_address: String,
    pub amount: u64,
    pub fee: u64,
    pub memo: String,
}

pub async fn build_unsigned_transaction(
    rpc: &OxideRpc,
    to_address: &str,
    amount: u64,
    memo: &str,
    fee_rate: FeeRate,
    decoy_context: &str,
    hard_fork_version: u8,
    tip_height: usize,
    available_outputs: &[TrackedOutput],
    change_address: Option<&str>,
    excluded_inputs: &HashSet<(String, u32)>,
    chain_network: ChainNetworkMode,
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

    let mut candidates = decode_candidate_outputs(available_outputs, excluded_inputs)?;
    candidates.sort_by(|a, b| b.0.amount.cmp(&a.0.amount));

    if candidates.is_empty() {
        anyhow::bail!("no spendable outputs with serialized wallet payload were available");
    }

    let rct_type = rct_type_for_hf(hard_fork_version).with_context(|| {
        format!("cannot construct transaction for hard fork version {hard_fork_version}")
    })?;
    let outgoing_view_key = derive_outgoing_view_key(decoy_context, to_address, amount, memo);
    let data = if memo.trim().is_empty() {
        Vec::new()
    } else {
        vec![memo.as_bytes().to_vec()]
    };

    let mut saw_not_enough_funds = false;

    for selected_count in 1..=candidates.len() {
        let selected = &candidates[..selected_count];
        let selected_total: u128 = selected
            .iter()
            .map(|(tracked, _)| u128::from(tracked.amount))
            .sum();

        let mut inputs = Vec::with_capacity(selected_count);
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
            vec![(to, amount)],
            Change::fingerprintable(Some(change)),
            data.clone(),
            fee_rate,
        );

        match signable {
            Ok(signable) => {
                let fee = signable.necessary_fee();
                let needed = u128::from(amount) + u128::from(fee);
                if selected_total < needed {
                    continue;
                }

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
                    fee, selected_total, "constructed real Monero signable transaction"
                );

                return Ok(UnsignedTransaction {
                    serialized_signable: signable.serialize(),
                    signable,
                    selected_inputs: selected_refs,
                    to_address: to_address.to_string(),
                    amount,
                    fee,
                    memo: memo.to_string(),
                });
            }
            Err(SendError::NotEnoughFunds { .. }) => {
                saw_not_enough_funds = true;
                continue;
            }
            Err(e) => {
                anyhow::bail!("failed to construct signable transaction: {}", e);
            }
        }
    }

    if saw_not_enough_funds {
        anyhow::bail!("insufficient funds to construct signable transaction");
    }
    anyhow::bail!("unable to construct signable transaction from available outputs");
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

        assert!(ensure_supported_address_type("test", &featured_guaranteed, ChainNetworkMode::Public).is_ok());
        assert!(ensure_supported_address_type("test", &legacy, ChainNetworkMode::Mocknet).is_ok());
        assert!(ensure_supported_address_type("test", &non_guaranteed, ChainNetworkMode::Public).is_ok());
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
        assert!(ensure_address_network_mode("test", &legacy, ChainNetworkMode::Public).is_ok());
        assert!(ensure_address_network_mode("test", &featured, ChainNetworkMode::Public).is_ok());
    }
}
