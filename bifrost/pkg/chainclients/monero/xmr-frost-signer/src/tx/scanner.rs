use std::collections::{HashMap, HashSet};

use anyhow::{Context, Result};
use monero_wallet::{
    address::{Network, SubaddressIndex},
    rpc::ScannableBlock,
    GuaranteedScanner as GuaranteedWalletScanner, Scanner as StandardWalletScanner, Timelocked,
    WalletOutput,
};
use tracing::{debug, info};

use crate::config::ChainNetworkMode;
use crate::state::store::TrackedOutput;
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
    last_height: u64,
    addresses: VaultAddresses,
}

enum WalletScanner {
    Standard(StandardWalletScanner),
    Guaranteed(GuaranteedWalletScanner),
}

impl WalletScanner {
    fn register_subaddress(&mut self, index: SubaddressIndex) {
        match self {
            WalletScanner::Standard(scanner) => scanner.register_subaddress(index),
            WalletScanner::Guaranteed(scanner) => scanner.register_subaddress(index),
        }
    }

    fn scan(&mut self, block: ScannableBlock) -> Result<Timelocked, monero_wallet::ScanError> {
        match self {
            WalletScanner::Standard(scanner) => scanner.scan(block),
            WalletScanner::Guaranteed(scanner) => scanner.scan(block),
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
        outputs: Vec<TrackedOutput>,
        last_height: Option<u64>,
    ) -> Result<()> {
        let (mut wallet_scanner, addresses) = match chain_network {
            ChainNetworkMode::Mocknet => {
                let view_pair =
                    xmr::view_pair_from_group_key_hex(group_key).with_context(|| {
                        format!(
                            "failed to derive standard scanner view pair for vault {}",
                            vault_pub_key
                        )
                    })?;
                let base_address = xmr::derive_vault_address(group_key, chain_network)?;
                let branch_address =
                    SubaddressIndex::new(subaddress::BRANCH.0, subaddress::BRANCH.1)
                        .map(|index| view_pair.subaddress(Network::Mainnet, index).to_string())
                        .unwrap_or_else(|| base_address.clone());
                let change_address =
                    SubaddressIndex::new(subaddress::CHANGE.0, subaddress::CHANGE.1)
                        .map(|index| view_pair.subaddress(Network::Mainnet, index).to_string())
                        .unwrap_or_else(|| base_address.clone());
                let forward_address =
                    SubaddressIndex::new(subaddress::FORWARD.0, subaddress::FORWARD.1)
                        .map(|index| view_pair.subaddress(Network::Mainnet, index).to_string())
                        .unwrap_or_else(|| base_address.clone());
                (
                    WalletScanner::Standard(StandardWalletScanner::new(view_pair)),
                    VaultAddresses::new(
                        base_address,
                        branch_address,
                        change_address,
                        forward_address,
                    ),
                )
            }
            ChainNetworkMode::Public => {
                let guaranteed_view_pair = xmr::guaranteed_view_pair_from_group_key_hex(group_key)
                    .with_context(|| {
                        format!(
                            "failed to derive guaranteed scanner view pair for vault {}",
                            vault_pub_key
                        )
                    })?;
                let base_address = xmr::derive_vault_address(group_key, chain_network)?;
                let branch_address =
                    SubaddressIndex::new(subaddress::BRANCH.0, subaddress::BRANCH.1)
                        .map(|index| {
                            guaranteed_view_pair
                                .address(Network::Mainnet, Some(index), None)
                                .to_string()
                        })
                        .unwrap_or_else(|| base_address.clone());
                let change_address =
                    SubaddressIndex::new(subaddress::CHANGE.0, subaddress::CHANGE.1)
                        .map(|index| {
                            guaranteed_view_pair
                                .address(Network::Mainnet, Some(index), None)
                                .to_string()
                        })
                        .unwrap_or_else(|| base_address.clone());
                let forward_address =
                    SubaddressIndex::new(subaddress::FORWARD.0, subaddress::FORWARD.1)
                        .map(|index| {
                            guaranteed_view_pair
                                .address(Network::Mainnet, Some(index), None)
                                .to_string()
                        })
                        .unwrap_or_else(|| base_address.clone());
                (
                    WalletScanner::Guaranteed(GuaranteedWalletScanner::new(guaranteed_view_pair)),
                    VaultAddresses::new(
                        base_address,
                        branch_address,
                        change_address,
                        forward_address,
                    ),
                )
            }
        };

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
            .is_empty())
    }

    pub fn scan_block_for_vault_with_added(
        &mut self,
        vault_pub_key: &str,
        height: u64,
        block: ScannableBlock,
    ) -> Result<Vec<TrackedOutput>> {
        let Some(state) = self.vault_states.get_mut(vault_pub_key) else {
            return Ok(Vec::new());
        };

        let mut known = HashSet::new();
        for output in &state.outputs {
            known.insert((output.tx_hash.clone(), output.output_index));
        }

        let scanned = state
            .wallet_scanner
            .scan(block)
            .map_err(|e| anyhow::anyhow!("scan failure for vault {}: {}", vault_pub_key, e))?
            .ignore_additional_timelock();

        let mut added = 0usize;
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
            added += 1;
        }

        state.last_height = height;
        debug!(
            vault = vault_pub_key,
            height,
            added_outputs = added,
            "scanned vault block"
        );
        Ok(added_outputs)
    }

    pub fn get_balance(&self, vault_pub_key: &str) -> (u64, u64) {
        let Some(state) = self.vault_states.get(vault_pub_key) else {
            return (0, 0);
        };

        let mut total = 0u64;
        let mut unlocked = 0u64;
        for output in &state.outputs {
            if output.is_spent() {
                continue;
            }
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

        state
            .outputs
            .iter()
            .filter(|o| o.height >= from_height && !o.is_spent())
            .map(|o| {
                let confirmations = self.current_height.saturating_sub(o.height);
                Transfer {
                    txid: o.tx_hash.clone(),
                    address: o.address.clone().unwrap_or_default(),
                    from_address: None,
                    vault_pub_key: Some(vault_pub_key.to_string()),
                    amount: o.amount,
                    fee: 0,
                    height: o.height,
                    confirmations,
                    note: o.note.clone().unwrap_or_default(),
                    locked: confirmations < 10,
                    subaddress_major: o.subaddress_major,
                    subaddress_minor: o.subaddress_minor,
                    direction: TransferDirection::Incoming,
                }
            })
            .collect()
    }

    pub fn get_outputs(&self, vault_pub_key: &str) -> Vec<TrackedOutput> {
        self.vault_states
            .get(vault_pub_key)
            .map(|s| {
                s.outputs
                    .iter()
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
    }

    pub fn remove_outputs_by_refs(&mut self, vault_pub_key: &str, refs: &[(String, u32)]) {
        let Some(state) = self.vault_states.get_mut(vault_pub_key) else {
            return;
        };
        let to_remove: HashSet<(String, u32)> = refs.iter().cloned().collect();
        state
            .outputs
            .retain(|o| !to_remove.contains(&(o.tx_hash.clone(), o.output_index)));
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
        assert!(base.is_guaranteed());
        assert!(base.is_subaddress());
        for address in &state.addresses.all {
            let parsed = MoneroAddress::from_str_with_unchecked_network(address).unwrap();
            assert!(parsed.is_guaranteed());
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
}
