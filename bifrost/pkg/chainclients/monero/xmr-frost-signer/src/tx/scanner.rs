use std::collections::{HashMap, HashSet};

use anyhow::{Context, Result};
use monero_wallet::{
    address::{Network, SubaddressIndex},
    rpc::ScannableBlock,
    Scanner as StandardWalletScanner, Timelocked, ViewPair, WalletOutput,
};
use tracing::{debug, info};

use crate::config::ChainNetworkMode;
use crate::state::store::{
    CollisionRecord, CreditedOutput, DepositRoute, OutputRef, TrackedOutput,
};
use crate::xmr;

/// Subaddress account indices used by THORChain vaults.
pub mod subaddress {
    /// (0,0) External: inbound deposits from users.
    pub const EXTERNAL: (u32, u32) = (0, 0);
    /// (1,0) Branch: internal routing between vaults.
    pub const BRANCH: (u32, u32) = (1, 0);
    /// (2,0) Change: change outputs from outbound transactions.
    pub const CHANGE: (u32, u32) = (2, 0);
    /// (3,0) Forward: forwarded internal outputs.
    pub const FORWARD: (u32, u32) = (3, 0);
}

/// A transfer detected by the scanner.
#[derive(Debug, Clone, serde::Serialize, serde::Deserialize)]
pub struct Transfer {
    pub txid: String,
    pub address: String,
    pub from_address: Option<String>,
    pub vault_pub_key: Option<String>,
    pub amount: u64,
    pub fee: u64,
    pub height: u64,
    pub confirmations: u64,
    pub note: String,
    pub locked: bool,
    pub subaddress_major: u32,
    pub subaddress_minor: u32,
    pub direction: TransferDirection,
}

#[derive(Debug, Clone, serde::Serialize, serde::Deserialize, PartialEq)]
pub enum TransferDirection {
    Incoming,
    Outgoing,
}

struct VaultScanState {
    wallet_scanner: WalletScanner,
    outputs: Vec<TrackedOutput>,
    collision_records: Vec<CollisionRecord>,
    last_height: u64,
    addresses: VaultAddresses,
}

pub struct ScanBlockOutcome {
    pub added_outputs: Vec<TrackedOutput>,
    pub credited_outputs: Vec<CreditedOutput>,
}

enum WalletScanner {
    Standard(StandardWalletScanner),
}

impl WalletScanner {
    fn register_subaddress(&mut self, index: SubaddressIndex) {
        match self {
            WalletScanner::Standard(scanner) => scanner.register_subaddress(index),
        }
    }

    fn scan(&mut self, block: ScannableBlock) -> Result<Timelocked, monero_wallet::ScanError> {
        match self {
            WalletScanner::Standard(scanner) => scanner.scan(block),
        }
    }
}

#[derive(Clone)]
struct VaultAddresses {
    base: String,
    branch: String,
    change: String,
    forward: String,
    all: Vec<String>,
}

/// Scanner wrapping monero-wallet scanner state per vault.
pub struct Scanner {
    vault_states: HashMap<String, VaultScanState>,
    vault_health: HashMap<String, bool>,
    current_height: u64,
}

impl Scanner {
    pub fn new() -> Self {
        Scanner {
            vault_states: HashMap::new(),
            vault_health: HashMap::new(),
            current_height: 0,
        }
    }

    pub fn set_current_height(&mut self, height: u64) {
        self.current_height = height;
    }

    pub fn current_height(&self) -> u64 {
        self.current_height
    }

    pub fn has_vault(&self, vault_pub_key: &str) -> bool {
        self.vault_states.contains_key(vault_pub_key)
    }

    pub fn set_vault_health(&mut self, vault_pub_key: &str, healthy: bool) {
        self.vault_health.insert(vault_pub_key.to_string(), healthy);
    }

    pub fn is_vault_healthy(&self, vault_pub_key: &str) -> bool {
        self.vault_health
            .get(vault_pub_key)
            .copied()
            .unwrap_or(true)
    }

    pub fn register_vault(
        &mut self,
        vault_pub_key: &str,
        group_key: &str,
        chain_network: ChainNetworkMode,
        mut outputs: Vec<TrackedOutput>,
        mut collision_records: Vec<CollisionRecord>,
        last_height: Option<u64>,
    ) -> Result<()> {
        normalize_tracked_outputs(&mut outputs)?;
        if collision_records.is_empty() && !outputs.is_empty() {
            collision_records = rebuild_collision_records_from_outputs(&outputs);
        }

        let view_pair = xmr::view_pair_from_group_key_hex(group_key).with_context(|| {
            format!(
                "failed to derive standard scanner view pair for vault {}",
                vault_pub_key
            )
        })?;
        let base_address = xmr::derive_vault_address(group_key, chain_network)?;
        let branch_address = derive_role_subaddress(&view_pair, subaddress::BRANCH, &base_address);
        let change_address = derive_role_subaddress(&view_pair, subaddress::CHANGE, &base_address);
        let forward_address =
            derive_role_subaddress(&view_pair, subaddress::FORWARD, &base_address);
        let (mut wallet_scanner, addresses) = (
            WalletScanner::Standard(StandardWalletScanner::new(view_pair)),
            VaultAddresses::new(
                base_address,
                branch_address,
                change_address,
                forward_address,
            ),
        );

        for (major, minor) in [
            subaddress::EXTERNAL,
            subaddress::BRANCH,
            subaddress::CHANGE,
            subaddress::FORWARD,
        ] {
            if let Some(index) = SubaddressIndex::new(major, minor) {
                wallet_scanner.register_subaddress(index);
            }
        }

        self.vault_states.insert(
            vault_pub_key.to_string(),
            VaultScanState {
                wallet_scanner,
                outputs,
                collision_records,
                last_height: last_height.unwrap_or(0),
                addresses,
            },
        );
        self.vault_health.insert(vault_pub_key.to_string(), true);

        info!(
            vault = vault_pub_key,
            num_outputs = self.vault_states[vault_pub_key].outputs.len(),
            last_height = self.vault_states[vault_pub_key].last_height,
            "registered vault scanner state"
        );
        Ok(())
    }

    pub fn scan_block_for_vault(
        &mut self,
        vault_pub_key: &str,
        height: u64,
        block: ScannableBlock,
    ) -> Result<bool> {
        Ok(!self
            .scan_block_for_vault_with_added(vault_pub_key, height, block)?
            .added_outputs
            .is_empty())
    }

    pub fn scan_block_for_vault_with_added(
        &mut self,
        vault_pub_key: &str,
        height: u64,
        block: ScannableBlock,
    ) -> Result<ScanBlockOutcome> {
        let Some(state) = self.vault_states.get_mut(vault_pub_key) else {
            return Ok(ScanBlockOutcome {
                added_outputs: Vec::new(),
                credited_outputs: Vec::new(),
            });
        };

        let mut known = HashSet::new();
        for output in &state.outputs {
            known.insert((output.tx_hash.clone(), output.output_index));
        }

        let scanned = state
            .wallet_scanner
            .scan(block)
            .map_err(|e| anyhow::anyhow!("scan failure for vault {}: {}", vault_pub_key, e))?
            .not_additionally_locked();

        let mut added_outputs = Vec::new();
        for wallet_output in scanned {
            let tx_hash = hex::encode(wallet_output.transaction());
            let Ok(output_index) = u32::try_from(wallet_output.index_in_transaction()) else {
                continue;
            };
            if known.contains(&(tx_hash.clone(), output_index)) {
                continue;
            }

            let (sub_major, sub_minor) = wallet_output
                .subaddress()
                .map(|s| (s.account(), s.address()))
                .unwrap_or(subaddress::EXTERNAL);

            state.outputs.push(TrackedOutput {
                tx_hash: tx_hash.clone(),
                output_index,
                amount: wallet_output.commitment().amount,
                height,
                one_time_key: Some(one_time_key_from_wallet_output(&wallet_output)),
                key_image: None,
                state: crate::state::store::OutputState::Unspent,
                reservation: None,
                subaddress_major: sub_major,
                subaddress_minor: sub_minor,
                wallet_output_hex: Some(hex::encode(wallet_output.serialize())),
                address: Some(resolve_output_address(state, sub_major, sub_minor)),
                note: extract_note(&wallet_output),
            });
            if let Some(output) = state.outputs.last() {
                added_outputs.push(output.clone());
            }
            known.insert((tx_hash, output_index));
        }

        let mut added_sorted_refs = added_outputs
            .iter()
            .map(|output| output.ref_key())
            .collect::<Vec<_>>();
        added_sorted_refs.sort_by(|lhs, rhs| {
            lhs.tx_hash
                .cmp(&rhs.tx_hash)
                .then_with(|| lhs.output_index.cmp(&rhs.output_index))
        });
        for added_ref in added_sorted_refs {
            apply_new_output_to_collision_records(
                &mut state.collision_records,
                &state.outputs,
                &added_ref,
            );
        }

        let credited_outputs =
            credited_outputs_at_height(&state.outputs, &state.collision_records, height);

        state.last_height = height;
        debug!(
            vault = vault_pub_key,
            height,
            added_outputs = added_outputs.len(),
            credited_outputs = credited_outputs.len(),
            "scanned vault block"
        );
        Ok(ScanBlockOutcome {
            added_outputs,
            credited_outputs,
        })
    }

    pub fn get_balance(&self, vault_pub_key: &str) -> (u64, u64) {
        let Some(state) = self.vault_states.get(vault_pub_key) else {
            return (0, 0);
        };

        let mut total = 0u64;
        let mut unlocked = 0u64;
        for output in active_winner_outputs(&state.outputs, &state.collision_records) {
            total += output.amount;
            if output.is_reserved() {
                continue;
            }
            let confirmations = self.current_height.saturating_sub(output.height);
            if confirmations >= 10 {
                unlocked += output.amount;
            }
        }
        (total, unlocked)
    }

    pub fn get_transfers(&self, vault_pub_key: &str, from_height: u64) -> Vec<Transfer> {
        let Some(state) = self.vault_states.get(vault_pub_key) else {
            return Vec::new();
        };

        collision_credit_events(&state.outputs, &state.collision_records)
            .into_iter()
            .filter_map(|credited| {
                let output = output_by_ref(&state.outputs, &credited.as_output_ref())?;
                if output.height < from_height {
                    return None;
                }

                let confirmations = self.current_height.saturating_sub(output.height);
                Some(Transfer {
                    txid: output.tx_hash.clone(),
                    address: output.address.clone().unwrap_or_default(),
                    from_address: None,
                    vault_pub_key: Some(vault_pub_key.to_string()),
                    amount: credited.amount,
                    fee: 0,
                    height: output.height,
                    confirmations,
                    note: output.note.clone().unwrap_or_default(),
                    locked: confirmations < 10,
                    subaddress_major: output.subaddress_major,
                    subaddress_minor: output.subaddress_minor,
                    direction: TransferDirection::Incoming,
                })
            })
            .collect()
    }

    pub fn get_outputs(&self, vault_pub_key: &str) -> Vec<TrackedOutput> {
        self.vault_states
            .get(vault_pub_key)
            .map(|s| {
                active_winner_outputs(&s.outputs, &s.collision_records)
                    .into_iter()
                    .filter(|o| o.is_available_for_spend_at(self.current_height))
                    .cloned()
                    .collect()
            })
            .unwrap_or_default()
    }

    pub fn get_all_outputs(&self, vault_pub_key: &str) -> Vec<TrackedOutput> {
        self.vault_states
            .get(vault_pub_key)
            .map(|s| s.outputs.clone())
            .unwrap_or_default()
    }

    pub fn get_collision_records(&self, vault_pub_key: &str) -> Vec<CollisionRecord> {
        self.vault_states
            .get(vault_pub_key)
            .map(|s| s.collision_records.clone())
            .unwrap_or_default()
    }

    pub fn get_credited_outputs_at_height(
        &self,
        vault_pub_key: &str,
        height: u64,
    ) -> Vec<CreditedOutput> {
        self.vault_states
            .get(vault_pub_key)
            .map(|state| {
                credited_outputs_at_height(&state.outputs, &state.collision_records, height)
            })
            .unwrap_or_default()
    }

    pub fn get_addresses(&self, vault_pub_key: &str) -> Vec<String> {
        self.vault_states
            .get(vault_pub_key)
            .map(|s| s.addresses.all.clone())
            .unwrap_or_default()
    }

    pub fn mark_spent_by_refs(&mut self, vault_pub_key: &str, refs: &[(String, u32)]) {
        let Some(state) = self.vault_states.get_mut(vault_pub_key) else {
            return;
        };
        for (tx_hash, output_index) in refs {
            if let Some(output) = state
                .outputs
                .iter_mut()
                .find(|o| &o.tx_hash == tx_hash && &o.output_index == output_index)
            {
                output.state = crate::state::store::OutputState::Spent;
                output.reservation = None;
            }
        }
        sync_collision_records_after_spend(&mut state.collision_records, &state.outputs);
    }

    pub fn merge_output_states(&mut self, vault_pub_key: &str, persisted: &[TrackedOutput]) {
        let Some(state) = self.vault_states.get_mut(vault_pub_key) else {
            return;
        };
        let by_ref: HashMap<(String, u32), TrackedOutput> = persisted
            .iter()
            .map(|o| ((o.tx_hash.clone(), o.output_index), o.clone()))
            .collect();
        for output in &mut state.outputs {
            if let Some(saved) = by_ref.get(&(output.tx_hash.clone(), output.output_index)) {
                output.state = saved.state;
                output.reservation = saved.reservation.clone();
            }
        }
        sync_collision_records_after_spend(&mut state.collision_records, &state.outputs);
    }

    pub fn remove_outputs_by_refs(&mut self, vault_pub_key: &str, refs: &[(String, u32)]) {
        let Some(state) = self.vault_states.get_mut(vault_pub_key) else {
            return;
        };
        let to_remove: HashSet<(String, u32)> = refs.iter().cloned().collect();
        state
            .outputs
            .retain(|o| !to_remove.contains(&(o.tx_hash.clone(), o.output_index)));
        state.collision_records = rebuild_collision_records_from_outputs(&state.outputs);
    }

    pub fn set_last_scanned_height(&mut self, vault_pub_key: &str, height: u64) {
        if let Some(state) = self.vault_states.get_mut(vault_pub_key) {
            state.last_height = height;
        }
    }

    pub fn last_scanned_height(&self, vault_pub_key: &str) -> Option<u64> {
        self.vault_states.get(vault_pub_key).map(|s| s.last_height)
    }
}

fn resolve_output_address(state: &VaultScanState, major: u32, minor: u32) -> String {
    match (major, minor) {
        (1, 0) => state.addresses.branch.clone(),
        (2, 0) => state.addresses.change.clone(),
        (3, 0) => state.addresses.forward.clone(),
        _ => state.addresses.base.clone(),
    }
}

fn derive_role_subaddress(view_pair: &ViewPair, role: (u32, u32), fallback: &str) -> String {
    SubaddressIndex::new(role.0, role.1)
        .map(|index| view_pair.subaddress(Network::Mainnet, index).to_string())
        .unwrap_or_else(|| fallback.to_string())
}

fn one_time_key_from_wallet_output(wallet_output: &WalletOutput) -> String {
    hex::encode(wallet_output.key().compress().to_bytes())
}

fn derive_one_time_key_from_serialized_output(serialized_hex: &str) -> Result<String> {
    let bytes = hex::decode(serialized_hex)
        .with_context(|| format!("invalid tracked wallet_output_hex: {serialized_hex}"))?;
    let wallet_output = WalletOutput::read(&mut bytes.as_slice())
        .context("failed to deserialize tracked wallet output while deriving one-time key")?;
    Ok(one_time_key_from_wallet_output(&wallet_output))
}

fn normalize_tracked_outputs(outputs: &mut [TrackedOutput]) -> Result<()> {
    for output in outputs {
        if output
            .one_time_key
            .as_ref()
            .map(|key| !key.trim().is_empty())
            .unwrap_or(false)
        {
            continue;
        }
        let serialized_hex = output.wallet_output_hex.as_deref().ok_or_else(|| {
            anyhow::anyhow!(
                "tracked output {}:{} is missing wallet_output_hex for one-time key derivation",
                output.tx_hash,
                output.output_index
            )
        })?;
        output.one_time_key = Some(derive_one_time_key_from_serialized_output(serialized_hex)?);
    }
    Ok(())
}

trait CreditedOutputExt {
    fn as_output_ref(&self) -> OutputRef;
}

impl CreditedOutputExt for CreditedOutput {
    fn as_output_ref(&self) -> OutputRef {
        OutputRef {
            tx_hash: self.tx_hash.clone(),
            output_index: self.output_index,
        }
    }
}

fn output_route(output: &TrackedOutput) -> DepositRoute {
    DepositRoute::new(output.subaddress_major, output.subaddress_minor)
}

fn compare_collision_candidates(lhs: &TrackedOutput, rhs: &TrackedOutput) -> std::cmp::Ordering {
    rhs.amount
        .cmp(&lhs.amount)
        .then_with(|| lhs.height.cmp(&rhs.height))
        .then_with(|| lhs.tx_hash.cmp(&rhs.tx_hash))
        .then_with(|| lhs.output_index.cmp(&rhs.output_index))
}

fn output_by_ref<'a>(
    outputs: &'a [TrackedOutput],
    output_ref: &OutputRef,
) -> Option<&'a TrackedOutput> {
    outputs.iter().find(|output| {
        output.tx_hash == output_ref.tx_hash && output.output_index == output_ref.output_index
    })
}

fn push_unique_ref(refs: &mut Vec<OutputRef>, output_ref: OutputRef) {
    if refs.iter().any(|existing| existing == &output_ref) {
        return;
    }
    refs.push(output_ref);
}

fn all_record_member_refs(record: &CollisionRecord) -> Vec<OutputRef> {
    let mut refs = Vec::new();
    if let Some(canonical_ref) = record.canonical_ref.as_ref() {
        refs.push(canonical_ref.clone());
    }
    refs.extend(record.shadowed_refs.iter().cloned());
    refs.extend(record.quarantined_refs.iter().cloned());
    refs
}

fn apply_new_output_to_collision_records(
    records: &mut Vec<CollisionRecord>,
    outputs: &[TrackedOutput],
    output_ref: &OutputRef,
) {
    let Some(output) = output_by_ref(outputs, output_ref) else {
        return;
    };
    let Some(one_time_key) = output.one_time_key.clone() else {
        return;
    };
    let route = output_route(output);

    let Some(record) = records
        .iter_mut()
        .find(|record| record.one_time_key == one_time_key)
    else {
        records.push(CollisionRecord {
            one_time_key,
            route,
            canonical_ref: Some(output_ref.clone()),
            credited_amount: output.amount,
            shadowed_refs: Vec::new(),
            quarantined_refs: Vec::new(),
            resolved_spent_ref: None,
        });
        return;
    };

    if record.route != route {
        push_unique_ref(&mut record.quarantined_refs, output_ref.clone());
        return;
    }

    if record.is_resolved() {
        push_unique_ref(&mut record.shadowed_refs, output_ref.clone());
        return;
    }

    let current_ref = record.canonical_ref.clone();
    let current = current_ref
        .as_ref()
        .and_then(|current_ref| output_by_ref(outputs, current_ref));
    if current.is_none() {
        record.canonical_ref = Some(output_ref.clone());
        record.credited_amount = output.amount;
        return;
    }

    if compare_collision_candidates(output, current.expect("checked is_some"))
        == std::cmp::Ordering::Less
    {
        if let Some(previous_ref) = current_ref {
            if previous_ref != *output_ref {
                push_unique_ref(&mut record.shadowed_refs, previous_ref);
            }
        }
        record
            .shadowed_refs
            .retain(|shadowed| shadowed != output_ref);
        record.canonical_ref = Some(output_ref.clone());
        record.credited_amount = output.amount;
        return;
    }

    if current_ref.as_ref() != Some(output_ref) {
        push_unique_ref(&mut record.shadowed_refs, output_ref.clone());
    }
}

fn sync_collision_records_after_spend(records: &mut [CollisionRecord], outputs: &[TrackedOutput]) {
    for record in records.iter_mut() {
        let spent_ref = record
            .canonical_ref
            .iter()
            .chain(record.shadowed_refs.iter())
            .chain(record.quarantined_refs.iter())
            .find_map(|output_ref| {
                output_by_ref(outputs, output_ref)
                    .filter(|output| output.is_spent())
                    .map(|_| output_ref.clone())
            });
        record.resolved_spent_ref = spent_ref;
        if let Some(canonical_ref) = record.canonical_ref.as_ref() {
            if let Some(output) = output_by_ref(outputs, canonical_ref) {
                record.credited_amount = output.amount;
            }
        }
    }
}

fn rebuild_collision_records_from_outputs(outputs: &[TrackedOutput]) -> Vec<CollisionRecord> {
    let mut sorted_refs = outputs
        .iter()
        .map(|output| output.ref_key())
        .collect::<Vec<_>>();
    sorted_refs.sort_by(|lhs, rhs| {
        output_by_ref(outputs, lhs)
            .expect("ref from outputs must resolve")
            .height
            .cmp(
                &output_by_ref(outputs, rhs)
                    .expect("ref from outputs must resolve")
                    .height,
            )
            .then_with(|| lhs.tx_hash.cmp(&rhs.tx_hash))
            .then_with(|| lhs.output_index.cmp(&rhs.output_index))
    });

    let mut records = Vec::<CollisionRecord>::new();
    for output_ref in sorted_refs {
        apply_new_output_to_collision_records(&mut records, outputs, &output_ref);
    }
    sync_collision_records_after_spend(&mut records, outputs);
    records
}

fn active_winner_outputs<'a>(
    outputs: &'a [TrackedOutput],
    records: &'a [CollisionRecord],
) -> Vec<&'a TrackedOutput> {
    records
        .iter()
        .filter(|record| !record.is_resolved())
        .filter_map(|record| record.canonical_ref.as_ref())
        .filter_map(|output_ref| output_by_ref(outputs, output_ref))
        .collect()
}

fn collision_credit_events(
    outputs: &[TrackedOutput],
    records: &[CollisionRecord],
) -> Vec<CreditedOutput> {
    let mut credits = Vec::new();
    for record in records {
        let resolved_marker = record.resolved_spent_ref.as_ref().and_then(|output_ref| {
            output_by_ref(outputs, output_ref).map(|output| {
                (
                    output.height,
                    output_ref.tx_hash.clone(),
                    output_ref.output_index,
                )
            })
        });
        let mut members = all_record_member_refs(record)
            .iter()
            .filter_map(|output_ref| {
                output_by_ref(outputs, output_ref).map(|output| output.ref_key())
            })
            .collect::<Vec<_>>();
        members.sort_by(|lhs, rhs| {
            output_by_ref(outputs, lhs)
                .expect("member ref must resolve")
                .height
                .cmp(
                    &output_by_ref(outputs, rhs)
                        .expect("member ref must resolve")
                        .height,
                )
                .then_with(|| lhs.tx_hash.cmp(&rhs.tx_hash))
                .then_with(|| lhs.output_index.cmp(&rhs.output_index))
        });

        let mut winner_ref: Option<OutputRef> = None;
        let mut credited_amount = 0u64;
        for member_ref in members {
            let Some(output) = output_by_ref(outputs, &member_ref) else {
                continue;
            };
            if resolved_marker.as_ref().is_some_and(|marker| {
                (
                    output.height,
                    member_ref.tx_hash.clone(),
                    member_ref.output_index,
                ) > *marker
            }) {
                break;
            }
            if output_route(output) != record.route {
                continue;
            }
            let should_promote = winner_ref
                .as_ref()
                .and_then(|current_ref| output_by_ref(outputs, current_ref))
                .is_none_or(|current| {
                    compare_collision_candidates(output, current) == std::cmp::Ordering::Less
                });
            if !should_promote {
                continue;
            }
            let delta = output.amount.saturating_sub(credited_amount);
            winner_ref = Some(member_ref.clone());
            credited_amount = output.amount;
            if delta == 0 {
                continue;
            }
            credits.push(CreditedOutput {
                tx_hash: member_ref.tx_hash.clone(),
                output_index: member_ref.output_index,
                amount: delta,
            });
        }
    }
    credits
}

fn credited_outputs_at_height(
    outputs: &[TrackedOutput],
    records: &[CollisionRecord],
    height: u64,
) -> Vec<CreditedOutput> {
    collision_credit_events(outputs, records)
        .into_iter()
        .filter(|credited| {
            output_by_ref(outputs, &credited.as_output_ref())
                .is_some_and(|output| output.height == height && !output.is_spent())
        })
        .collect()
}

fn extract_note(output: &WalletOutput) -> Option<String> {
    for chunk in output.arbitrary_data() {
        if let Ok(text) = std::str::from_utf8(chunk) {
            let trimmed = text.trim();
            if !trimmed.is_empty() {
                return Some(trimmed.to_string());
            }
        }
    }
    None
}

impl VaultAddresses {
    fn new(base: String, branch: String, change: String, forward: String) -> Self {
        let mut all = Vec::new();
        let mut seen = HashSet::new();
        for addr in [
            base.clone(),
            branch.clone(),
            change.clone(),
            forward.clone(),
        ] {
            if seen.insert(addr.clone()) {
                all.push(addr);
            }
        }
        VaultAddresses {
            base,
            branch,
            change,
            forward,
            all,
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use curve25519_dalek::{constants::ED25519_BASEPOINT_TABLE, Scalar};
    use monero_wallet::address::MoneroAddress;

    #[test]
    fn role_mapping_is_stable_and_not_index_sorted() {
        let mut scanner = Scanner::new();
        let base = Scalar::from(7u64);
        let group_key_hex = hex::encode((&base * ED25519_BASEPOINT_TABLE).compress().to_bytes());

        scanner
            .register_vault(
                "thorpub1vault",
                &group_key_hex,
                ChainNetworkMode::Public,
                Vec::new(),
                Vec::new(),
                None,
            )
            .unwrap();

        let state = scanner.vault_states.get("thorpub1vault").unwrap();
        assert!(!state.addresses.base.is_empty());
        assert!(!state.addresses.branch.is_empty());
        assert!(!state.addresses.change.is_empty());
        assert!(!state.addresses.forward.is_empty());
        assert_eq!(
            state.addresses.all.first().cloned().unwrap(),
            state.addresses.base
        );
        assert_eq!(
            resolve_output_address(state, subaddress::EXTERNAL.0, subaddress::EXTERNAL.1),
            state.addresses.base
        );
        assert_eq!(
            resolve_output_address(state, subaddress::BRANCH.0, subaddress::BRANCH.1),
            state.addresses.branch
        );
        assert_eq!(
            resolve_output_address(state, subaddress::CHANGE.0, subaddress::CHANGE.1),
            state.addresses.change
        );
        assert_eq!(
            resolve_output_address(state, subaddress::FORWARD.0, subaddress::FORWARD.1),
            state.addresses.forward
        );

        let base = MoneroAddress::from_str_with_unchecked_network(&state.addresses.base).unwrap();
        assert!(!base.is_guaranteed());
        assert!(!base.is_subaddress());
        for address in &state.addresses.all {
            let parsed = MoneroAddress::from_str_with_unchecked_network(address).unwrap();
            assert!(!parsed.is_guaranteed());
        }
    }

    #[test]
    fn mocknet_role_mapping_uses_wallet_compatible_addresses() {
        let mut scanner = Scanner::new();
        let base = Scalar::from(7u64);
        let group_key_hex = hex::encode((&base * ED25519_BASEPOINT_TABLE).compress().to_bytes());

        scanner
            .register_vault(
                "thorpub1vault",
                &group_key_hex,
                ChainNetworkMode::Mocknet,
                Vec::new(),
                Vec::new(),
                None,
            )
            .unwrap();

        let state = scanner.vault_states.get("thorpub1vault").unwrap();
        let base = MoneroAddress::from_str_with_unchecked_network(&state.addresses.base).unwrap();
        assert!(!base.is_guaranteed());
        assert!(!base.is_subaddress());
        assert!(state.addresses.base.starts_with('4'));

        for address in [
            &state.addresses.branch,
            &state.addresses.change,
            &state.addresses.forward,
        ] {
            let parsed = MoneroAddress::from_str_with_unchecked_network(address).unwrap();
            assert!(!parsed.is_guaranteed());
            assert!(parsed.is_subaddress());
            assert!(address.starts_with('8'));
        }
    }

    fn sample_addresses() -> VaultAddresses {
        VaultAddresses::new(
            "48vault".to_string(),
            "89branch".to_string(),
            "89change".to_string(),
            "89forward".to_string(),
        )
    }

    fn sample_scanner() -> WalletScanner {
        let base = Scalar::from(7u64);
        let group_key_hex = hex::encode((&base * ED25519_BASEPOINT_TABLE).compress().to_bytes());
        let view_pair = xmr::view_pair_from_group_key_hex(&group_key_hex).unwrap();
        WalletScanner::Standard(StandardWalletScanner::new(view_pair))
    }

    fn tracked_output(
        tx_hash: &str,
        output_index: u32,
        amount: u64,
        height: u64,
        one_time_key: &str,
        route: (u32, u32),
    ) -> TrackedOutput {
        TrackedOutput {
            tx_hash: tx_hash.to_string(),
            output_index,
            amount,
            height,
            one_time_key: Some(one_time_key.to_string()),
            key_image: None,
            state: crate::state::store::OutputState::Unspent,
            reservation: None,
            subaddress_major: route.0,
            subaddress_minor: route.1,
            wallet_output_hex: Some(format!("wallet-output-{tx_hash}-{output_index}")),
            address: Some("48vault".to_string()),
            note: Some("memo".to_string()),
        }
    }

    #[test]
    fn transfers_credit_only_collision_delta_for_later_larger_output() {
        let mut scanner = Scanner::new();
        scanner.current_height = 115;
        scanner.vault_states.insert(
            "vault".to_string(),
            VaultScanState {
                wallet_scanner: sample_scanner(),
                outputs: vec![
                    tracked_output("tx-a", 0, 5_000, 100, "collision-key", subaddress::EXTERNAL),
                    tracked_output("tx-b", 1, 7_000, 105, "collision-key", subaddress::EXTERNAL),
                ],
                collision_records: rebuild_collision_records_from_outputs(&[
                    tracked_output("tx-a", 0, 5_000, 100, "collision-key", subaddress::EXTERNAL),
                    tracked_output("tx-b", 1, 7_000, 105, "collision-key", subaddress::EXTERNAL),
                ]),
                last_height: 105,
                addresses: sample_addresses(),
            },
        );

        let transfers = scanner.get_transfers("vault", 105);
        assert_eq!(transfers.len(), 1);
        assert_eq!(transfers[0].txid, "tx-b");
        assert_eq!(transfers[0].amount, 2_000);
    }

    #[test]
    fn collision_winner_is_only_spendable_output_and_balance() {
        let mut scanner = Scanner::new();
        scanner.current_height = 120;
        scanner.vault_states.insert(
            "vault".to_string(),
            VaultScanState {
                wallet_scanner: sample_scanner(),
                outputs: vec![
                    tracked_output("tx-a", 0, 5_000, 100, "collision-key", subaddress::EXTERNAL),
                    tracked_output("tx-b", 1, 7_000, 105, "collision-key", subaddress::EXTERNAL),
                    tracked_output("tx-c", 2, 11_000, 110, "unique-key", subaddress::EXTERNAL),
                ],
                collision_records: rebuild_collision_records_from_outputs(&[
                    tracked_output("tx-a", 0, 5_000, 100, "collision-key", subaddress::EXTERNAL),
                    tracked_output("tx-b", 1, 7_000, 105, "collision-key", subaddress::EXTERNAL),
                    tracked_output("tx-c", 2, 11_000, 110, "unique-key", subaddress::EXTERNAL),
                ]),
                last_height: 110,
                addresses: sample_addresses(),
            },
        );

        let outputs = scanner.get_outputs("vault");
        assert_eq!(outputs.len(), 2);
        assert!(outputs.iter().any(|output| output.tx_hash == "tx-b"));
        assert!(outputs.iter().any(|output| output.tx_hash == "tx-c"));

        let (total, unlocked) = scanner.get_balance("vault");
        assert_eq!(total, 18_000);
        assert_eq!(unlocked, 18_000);
    }

    #[test]
    fn cross_route_collision_keeps_original_binding_and_quarantines_sibling() {
        let outputs = vec![
            tracked_output("tx-a", 0, 5_000, 100, "collision-key", subaddress::EXTERNAL),
            tracked_output("tx-b", 1, 7_000, 105, "collision-key", subaddress::BRANCH),
        ];
        let mut scanner = Scanner::new();
        scanner.current_height = 120;
        scanner.vault_states.insert(
            "vault".to_string(),
            VaultScanState {
                wallet_scanner: sample_scanner(),
                outputs: outputs.clone(),
                collision_records: rebuild_collision_records_from_outputs(&outputs),
                last_height: 105,
                addresses: sample_addresses(),
            },
        );

        let transfers = scanner.get_transfers("vault", 100);
        assert_eq!(transfers.len(), 1);
        assert_eq!(transfers[0].txid, "tx-a");
        assert_eq!(transfers[0].amount, 5_000);

        let spendable = scanner.get_outputs("vault");
        assert_eq!(spendable.len(), 1);
        assert_eq!(spendable[0].tx_hash, "tx-a");
        assert_eq!(scanner.get_balance("vault"), (5_000, 5_000));

        let records = scanner.get_collision_records("vault");
        assert_eq!(records.len(), 1);
        assert_eq!(records[0].route, DepositRoute::new(0, 0));
        assert_eq!(records[0].quarantined_refs.len(), 1);
        assert_eq!(records[0].quarantined_refs[0].tx_hash, "tx-b");
    }

    #[test]
    fn resolved_collision_ignores_later_siblings_forever() {
        let mut outputs = vec![
            tracked_output("tx-a", 0, 5_000, 100, "collision-key", subaddress::EXTERNAL),
            tracked_output("tx-b", 1, 7_000, 105, "collision-key", subaddress::EXTERNAL),
            tracked_output("tx-c", 2, 9_000, 110, "collision-key", subaddress::EXTERNAL),
        ];
        outputs[1].state = crate::state::store::OutputState::Spent;
        let records = rebuild_collision_records_from_outputs(&outputs);

        let credited = credited_outputs_at_height(&outputs, &records, 110);
        assert!(credited.is_empty());

        let mut scanner = Scanner::new();
        scanner.current_height = 120;
        scanner.vault_states.insert(
            "vault".to_string(),
            VaultScanState {
                wallet_scanner: sample_scanner(),
                outputs,
                collision_records: records,
                last_height: 110,
                addresses: sample_addresses(),
            },
        );

        assert!(scanner.get_outputs("vault").is_empty());
        assert_eq!(scanner.get_balance("vault"), (0, 0));
        assert_eq!(scanner.get_transfers("vault", 110).len(), 0);
    }
}
