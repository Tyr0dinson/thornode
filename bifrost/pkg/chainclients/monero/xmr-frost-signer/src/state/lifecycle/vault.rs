use anyhow::Result;
use sha2::{Digest, Sha256};
use tracing::info;

use crate::config::ChainNetworkMode;
use crate::state::store::{
    OutgoingTransferRecord, RegisteredKeyImageRef, Store, TrackedOutput, VaultRecord,
};
use crate::tx::scanner::Scanner;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum VaultSyncState {
    SyncingToTip,
    AtTip,
}

#[derive(Debug, Clone)]
pub struct VaultSyncSnapshot {
    pub vault_pub_key: String,
    pub address: String,
    pub birthday: u64,
    pub rescan: bool,
    pub state: VaultSyncState,
    pub scan_height: Option<u64>,
    pub inventory_fingerprint: String,
    pub registered_key_image_count: usize,
    pub registered_tx_key_count: usize,
}

pub struct VaultLifecycle<'a> {
    store: &'a Store,
}

impl<'a> VaultLifecycle<'a> {
    pub fn new(store: &'a Store) -> Self {
        Self { store }
    }

    pub fn ensure_registered(
        &self,
        scanner: &mut Scanner,
        chain_network: ChainNetworkMode,
        vault_pub_key: &str,
        group_key: &str,
        address: &str,
        source: &str,
        birthday: Option<u64>,
        current_height: u64,
    ) -> Result<bool> {
        let birthday_height = birthday.unwrap_or(0);
        let registered = self.store.register_vault(
            vault_pub_key,
            group_key,
            address,
            source,
            birthday_height,
        )?;

        if self
            .store
            .load_scan_height(vault_pub_key)?
            .filter(|height| *height > 0)
            .is_none()
            && birthday_height > 0
        {
            self.store
                .save_scan_height(vault_pub_key, birthday_height)?;
        }
        let (_, scan_height) = self.rehydrate_scanner(
            scanner,
            chain_network,
            vault_pub_key,
            group_key,
            birthday_height,
            current_height,
        )?;

        info!(
            vault = %vault_pub_key,
            group_key,
            address,
            birthday = birthday_height,
            registered,
            scan_height,
            "vault lifecycle: ensured vault registration"
        );
        Ok(registered)
    }

    pub fn rehydrate_scanner(
        &self,
        scanner: &mut Scanner,
        chain_network: ChainNetworkMode,
        vault_pub_key: &str,
        group_key: &str,
        birthday: u64,
        fallback_scan_height: u64,
    ) -> Result<(Vec<TrackedOutput>, u64)> {
        let outputs = self.store.load_outputs(vault_pub_key)?;
        let scan_height = self
            .store
            .load_scan_height(vault_pub_key)?
            .filter(|height| *height > 0)
            .or_else(|| (birthday > 0).then_some(birthday))
            .unwrap_or(fallback_scan_height);

        scanner.register_vault(
            vault_pub_key,
            group_key,
            chain_network,
            outputs.clone(),
            Some(scan_height),
        )?;
        scanner.merge_output_states(vault_pub_key, &outputs);

        info!(
            vault = %vault_pub_key,
            group_key,
            birthday,
            scan_height,
            tracked_outputs = outputs.len(),
            "vault lifecycle: rehydrated scanner state"
        );
        Ok((outputs, scan_height))
    }

    pub fn apply_inventory(
        &self,
        vault_pub_key: &str,
        key_images: &[RegisteredKeyImageRef],
    ) -> Result<()> {
        self.store
            .save_registered_key_images(vault_pub_key, key_images)?;
        info!(
            vault = %vault_pub_key,
            key_images = key_images.len(),
            "vault lifecycle: applied registered key-image inventory"
        );
        Ok(())
    }

    pub fn complete_rescan_at_tip(&self, vault_pub_key: &str, birthday: u64) -> Result<()> {
        self.store
            .set_vault_rescan(vault_pub_key, false, birthday)?;
        info!(
            vault = %vault_pub_key,
            birthday,
            "vault lifecycle: marked vault at_tip"
        );
        Ok(())
    }

    pub fn advance_scan_height(&self, vault_pub_key: &str, height: u64) -> Result<()> {
        self.store.save_scan_height(vault_pub_key, height)?;
        info!(
            vault = %vault_pub_key,
            height,
            "vault lifecycle: advanced scan height"
        );
        Ok(())
    }

    pub fn reset(&self, scanner: &mut Scanner, vault_pub_key: &str) -> Result<()> {
        self.store.save_outputs(vault_pub_key, &[])?;
        self.store
            .rollback_scan_state_from_height(vault_pub_key, 0)?;
        self.store.save_registered_key_images(vault_pub_key, &[])?;
        self.store.clear_outgoing_transfers(vault_pub_key)?;
        self.store.delete_scan_height(vault_pub_key)?;
        self.store.delete_vault(vault_pub_key)?;
        scanner.remove_vault(vault_pub_key);
        info!(
            vault = %vault_pub_key,
            "vault lifecycle: reset vault state and removed scanner registration"
        );
        Ok(())
    }

    pub fn snapshot(&self, vault: &VaultRecord) -> Result<VaultSyncSnapshot> {
        let scan_height = self.store.load_scan_height(&vault.vault_pub_key)?;
        let registered_key_images = self
            .store
            .load_registered_key_images(&vault.vault_pub_key)?;
        let outgoing = self.store.load_outgoing_transfers(&vault.vault_pub_key)?;
        let registered_tx_key_count = outgoing
            .iter()
            .filter(|record| {
                record.registered_inventory
                    && !record.tx_keys.is_empty()
                    && !record.tx_hash.trim().is_empty()
            })
            .count();
        Ok(VaultSyncSnapshot {
            vault_pub_key: vault.vault_pub_key.clone(),
            address: vault.address.clone(),
            birthday: vault.birthday,
            rescan: vault.rescan,
            state: if vault.rescan {
                VaultSyncState::SyncingToTip
            } else {
                VaultSyncState::AtTip
            },
            scan_height,
            inventory_fingerprint: Self::build_inventory_fingerprint(
                &vault.vault_pub_key,
                vault.birthday,
                &registered_key_images,
                &outgoing,
            ),
            registered_key_image_count: registered_key_images.len(),
            registered_tx_key_count,
        })
    }

    pub fn build_inventory_fingerprint(
        vault_pub_key: &str,
        birthday: u64,
        registered_key_images: &[RegisteredKeyImageRef],
        outgoing: &[OutgoingTransferRecord],
    ) -> String {
        let mut builder = String::new();
        builder.push_str(vault_pub_key);
        builder.push('|');
        builder.push_str(&birthday.to_string());

        let mut key_images = registered_key_images.to_vec();
        key_images.sort_by(|left, right| {
            left.tx_hash
                .cmp(&right.tx_hash)
                .then(left.output_index.cmp(&right.output_index))
                .then(left.key_image.cmp(&right.key_image))
        });
        for entry in key_images {
            builder.push_str("|ki:");
            builder.push_str(&normalize_tx_hash(&entry.tx_hash));
            builder.push(':');
            builder.push_str(&entry.output_index.to_string());
            builder.push(':');
            builder.push_str(&entry.key_image.trim().to_ascii_lowercase());
        }

        let mut tx_entries: Vec<(String, Vec<String>)> = outgoing
            .iter()
            .filter_map(|record| {
                let tx_hash = normalize_tx_hash(&record.tx_hash);
                if tx_hash.is_empty() || record.tx_keys.is_empty() || !record.registered_inventory {
                    return None;
                }
                Some((tx_hash, normalized_tx_keys(&record.tx_keys)))
            })
            .collect();
        tx_entries.sort_by(|left, right| {
            left.0
                .cmp(&right.0)
                .then(left.1.join(",").cmp(&right.1.join(",")))
        });
        for (tx_hash, tx_keys) in tx_entries {
            builder.push_str("|tx:");
            builder.push_str(&tx_hash);
            builder.push(':');
            builder.push_str(&tx_keys.join(","));
        }

        hex::encode(Sha256::digest(builder.as_bytes()))
    }
}

fn normalize_tx_hash(hash: &str) -> String {
    hash.trim().to_ascii_lowercase()
}

fn normalized_tx_keys(keys: &[String]) -> Vec<String> {
    let mut normalized: Vec<String> = keys
        .iter()
        .map(|key| key.trim().to_ascii_lowercase())
        .filter(|key| !key.is_empty())
        .collect();
    normalized.sort();
    normalized
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::state::lifecycle::outgoing::{
        OutgoingLifecycle, OutgoingObservation, RecordValidatedParams,
    };
    use crate::tx::scanner::Scanner;
    use curve25519_dalek::{constants::ED25519_BASEPOINT_TABLE, Scalar};
    use tempfile::{tempdir, TempDir};

    fn test_store() -> (TempDir, Store) {
        let dir = tempdir().unwrap();
        let store = Store::open(dir.path()).unwrap();
        (dir, store)
    }

    fn group_key_hex() -> String {
        hex::encode(
            (&Scalar::from(7u64) * ED25519_BASEPOINT_TABLE)
                .compress()
                .to_bytes(),
        )
    }

    #[test]
    fn ensure_registered_and_snapshot_reflect_inventory() {
        let (_dir, store) = test_store();
        let mut scanner = Scanner::new();
        scanner.set_current_height(400);
        let lifecycle = VaultLifecycle::new(&store);
        let group_key = group_key_hex();
        let current_height = scanner.current_height();

        let registered = lifecycle
            .ensure_registered(
                &mut scanner,
                ChainNetworkMode::Mocknet,
                "thorpub1vault",
                &group_key,
                "48address",
                "test",
                Some(120),
                current_height,
            )
            .unwrap();
        assert!(registered);

        lifecycle
            .apply_inventory(
                "thorpub1vault",
                &[RegisteredKeyImageRef {
                    tx_hash: "tx-a".to_string(),
                    output_index: 0,
                    key_image: "ki-a".to_string(),
                }],
            )
            .unwrap();
        OutgoingLifecycle::new(&store)
            .record_validated(RecordValidatedParams {
                tx_hash: "tx-b".to_string(),
                tx_hex: "deadbeef".to_string(),
                tx_keys: vec!["key-b".to_string()],
                registered_inventory: true,
                has_owned_change: false,
                vault_pub_key: "thorpub1vault".to_string(),
                from_address: "from".to_string(),
                to_address: "to".to_string(),
                amount: 10,
                fee: 1,
                memo: "OUT:123".to_string(),
                planned_height: 120,
                observation: OutgoingObservation::ObservedMined(121),
                observed_at: Some(200),
                created_at: 100,
            })
            .unwrap();

        let vault = store
            .list_vaults()
            .unwrap()
            .into_iter()
            .find(|vault| vault.vault_pub_key == "thorpub1vault")
            .unwrap();
        let snapshot = lifecycle.snapshot(&vault).unwrap();
        assert!(snapshot.rescan);
        assert_eq!(snapshot.state, VaultSyncState::SyncingToTip);
        assert_eq!(snapshot.scan_height, Some(120));
        assert_eq!(snapshot.registered_key_image_count, 1);
        assert_eq!(snapshot.registered_tx_key_count, 1);
        assert!(!snapshot.inventory_fingerprint.is_empty());
    }

    #[test]
    fn complete_rescan_and_reset_update_vault_state() {
        let (_dir, store) = test_store();
        let mut scanner = Scanner::new();
        scanner.set_current_height(250);
        let lifecycle = VaultLifecycle::new(&store);
        let group_key = group_key_hex();
        let current_height = scanner.current_height();

        lifecycle
            .ensure_registered(
                &mut scanner,
                ChainNetworkMode::Mocknet,
                "thorpub1vault",
                &group_key,
                "48address",
                "test",
                Some(100),
                current_height,
            )
            .unwrap();
        lifecycle
            .complete_rescan_at_tip("thorpub1vault", 100)
            .unwrap();

        let vault = store
            .list_vaults()
            .unwrap()
            .into_iter()
            .find(|vault| vault.vault_pub_key == "thorpub1vault")
            .unwrap();
        let snapshot = lifecycle.snapshot(&vault).unwrap();
        assert_eq!(snapshot.state, VaultSyncState::AtTip);
        assert!(!snapshot.rescan);

        lifecycle.reset(&mut scanner, "thorpub1vault").unwrap();
        assert!(!scanner.has_vault("thorpub1vault"));
        assert!(store.list_vaults().unwrap().is_empty());
    }
}
