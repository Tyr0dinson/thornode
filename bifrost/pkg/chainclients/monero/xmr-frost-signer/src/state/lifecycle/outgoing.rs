use std::collections::{HashMap, HashSet};

use anyhow::Result;
use tracing::info;

use crate::state::store::{OutgoingTransferRecord, OutgoingTransferStatus, Store, TrackedOutput};

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum OutgoingObservation {
    SignedPending,
    AttestedPending,
    ObservedInMempool,
    ObservedMined(u64),
}

impl OutgoingObservation {
    pub const fn status(self) -> OutgoingTransferStatus {
        match self {
            Self::SignedPending => OutgoingTransferStatus::SignedPendingObservation,
            Self::AttestedPending => OutgoingTransferStatus::AttestedPendingObservation,
            Self::ObservedInMempool => OutgoingTransferStatus::ObservedMempool,
            Self::ObservedMined(_) => OutgoingTransferStatus::ObservedMined,
        }
    }

    pub const fn observed_height(self) -> Option<u64> {
        match self {
            Self::ObservedMined(height) => Some(height),
            _ => None,
        }
    }
}

#[derive(Debug, Clone)]
pub struct OutgoingAuditSnapshot {
    pub status: OutgoingTransferStatus,
    pub effective_height: u64,
    pub observed_height: Option<u64>,
    pub reported_effective_height: Option<u64>,
    pub registered_inventory: bool,
    pub has_owned_change: Option<bool>,
    pub local_change_materialized: Option<bool>,
    pub key_images_ready: Option<bool>,
    pub pending_reason: Option<&'static str>,
    pub has_tx_keys: bool,
}

#[derive(Debug, Clone)]
pub struct RecordSignedParams {
    pub tx_hash: String,
    pub tx_hex: String,
    pub tx_keys: Vec<String>,
    pub has_owned_change: bool,
    pub msg_id: String,
    pub vault_pub_key: String,
    pub from_address: String,
    pub to_address: String,
    pub amount: u64,
    pub fee: u64,
    pub memo: String,
    pub planned_height: u64,
    pub created_at: i64,
}

#[derive(Debug, Clone)]
pub struct RecordValidatedParams {
    pub tx_hash: String,
    pub tx_hex: String,
    pub tx_keys: Vec<String>,
    pub registered_inventory: bool,
    pub has_owned_change: bool,
    pub vault_pub_key: String,
    pub from_address: String,
    pub to_address: String,
    pub amount: u64,
    pub fee: u64,
    pub memo: String,
    pub planned_height: u64,
    pub observation: OutgoingObservation,
    pub observed_at: Option<i64>,
    pub created_at: i64,
}

pub struct OutgoingLifecycle<'a> {
    store: &'a Store,
}

impl<'a> OutgoingLifecycle<'a> {
    pub fn new(store: &'a Store) -> Self {
        Self { store }
    }

    pub fn snapshot(record: &OutgoingTransferRecord) -> OutgoingAuditSnapshot {
        OutgoingAuditSnapshot {
            status: record.status,
            effective_height: record.effective_height(),
            observed_height: record.observed_height,
            reported_effective_height: record.reported_effective_height,
            registered_inventory: record.registered_inventory,
            has_owned_change: record.has_owned_change,
            local_change_materialized: record.local_change_materialized,
            key_images_ready: record.key_images_ready,
            pending_reason: Self::pending_reason_for(record),
            has_tx_keys: !record.tx_keys.is_empty(),
        }
    }

    pub fn record_signed(&self, params: RecordSignedParams) -> Result<OutgoingTransferRecord> {
        let record = OutgoingTransferRecord {
            tx_hash: params.tx_hash,
            tx_hex: params.tx_hex,
            tx_keys: params.tx_keys,
            registered_inventory: false,
            has_owned_change: Some(params.has_owned_change),
            local_change_materialized: None,
            key_images_ready: None,
            msg_id: params.msg_id,
            vault_pub_key: params.vault_pub_key,
            from_address: params.from_address,
            to_address: params.to_address,
            amount: params.amount,
            fee: params.fee,
            memo: params.memo,
            planned_height: params.planned_height,
            observed_height: None,
            status: OutgoingTransferStatus::SignedPendingObservation,
            broadcasted_at: None,
            observed_at: None,
            reported_effective_height: None,
            created_at: params.created_at,
        };
        self.store.save_outgoing_transfer(&record)?;
        info!(
            vault = %record.vault_pub_key,
            tx_hash = %record.tx_hash,
            status = %record.status,
            "outgoing lifecycle: recorded signer-produced outbound"
        );
        Ok(record)
    }

    pub fn record_validated(
        &self,
        params: RecordValidatedParams,
    ) -> Result<OutgoingTransferRecord> {
        let existing = self
            .store
            .load_outgoing_transfer(&params.vault_pub_key, &params.tx_hash)?;
        if let Some(record) = existing {
            if !record.tx_keys.is_empty() && record.tx_keys != params.tx_keys {
                anyhow::bail!(
                    "existing outgoing transfer for {} has different tx_keys",
                    params.tx_hash
                );
            }
            if !record.to_address.eq_ignore_ascii_case(&params.to_address)
                || record.amount != params.amount
                || record.fee != params.fee
            {
                anyhow::bail!(
                    "existing outgoing transfer for {} conflicts with outbound metadata",
                    params.tx_hash
                );
            }
            self.store.update_outgoing_transfer_audit_metadata(
                &params.vault_pub_key,
                &params.tx_hash,
                &params.tx_keys,
                record.registered_inventory || params.registered_inventory,
                Some(params.has_owned_change),
                &params.from_address,
                &params.memo,
            )?;
            self.reconcile_output_readiness(&params.vault_pub_key)?;
            self.reconcile_observation(
                &params.vault_pub_key,
                &params.tx_hash,
                params.observation,
                params.observed_at,
                None,
            )?;
            let updated = self
                .store
                .load_outgoing_transfer(&params.vault_pub_key, &params.tx_hash)?
                .ok_or_else(|| {
                    anyhow::anyhow!(
                        "validated outgoing {} disappeared after update",
                        params.tx_hash
                    )
                })?;
            info!(
                vault = %params.vault_pub_key,
                tx_hash = %params.tx_hash,
                status = %updated.status,
                "outgoing lifecycle: updated validated outbound"
            );
            return Ok(updated);
        }

        let tx_hash = params.tx_hash.clone();
        let record = OutgoingTransferRecord {
            tx_hash: params.tx_hash,
            tx_hex: params.tx_hex,
            tx_keys: params.tx_keys,
            registered_inventory: params.registered_inventory,
            has_owned_change: Some(params.has_owned_change),
            local_change_materialized: None,
            key_images_ready: None,
            msg_id: format!("attestation:{tx_hash}"),
            vault_pub_key: params.vault_pub_key,
            from_address: params.from_address,
            to_address: params.to_address,
            amount: params.amount,
            fee: params.fee,
            memo: params.memo,
            planned_height: params.planned_height,
            observed_height: params.observation.observed_height(),
            status: params.observation.status(),
            broadcasted_at: None,
            observed_at: params.observed_at,
            reported_effective_height: None,
            created_at: params.created_at,
        };
        self.store.save_outgoing_transfer(&record)?;
        self.reconcile_output_readiness(&record.vault_pub_key)?;
        info!(
            vault = %record.vault_pub_key,
            tx_hash = %record.tx_hash,
            status = %record.status,
            "outgoing lifecycle: recorded validated outbound"
        );
        Ok(record)
    }

    pub fn reconcile_observation(
        &self,
        vault_pub_key: &str,
        tx_hash: &str,
        observation: OutgoingObservation,
        observed_at: Option<i64>,
        broadcasted_at: Option<i64>,
    ) -> Result<bool> {
        let existing = self.store.load_outgoing_transfer(vault_pub_key, tx_hash)?;
        let Some(record) = existing else {
            return Ok(false);
        };

        let target_status = observation.status();
        let target_height = observation.observed_height();
        if record.status == target_status
            && (target_height.is_none() || record.observed_height == target_height)
        {
            return Ok(false);
        }

        self.store.update_outgoing_transfer_status(
            vault_pub_key,
            tx_hash,
            target_status,
            target_height,
            observed_at,
            broadcasted_at,
        )?;
        info!(
            vault = %vault_pub_key,
            tx_hash,
            status = %target_status,
            observed_height = ?target_height,
            "outgoing lifecycle: reconciled observation"
        );
        Ok(true)
    }

    pub fn mark_reported(
        &self,
        vault_pub_key: &str,
        tx_hash: &str,
        effective_height: u64,
    ) -> Result<()> {
        self.store
            .mark_outgoing_transfer_reported(vault_pub_key, tx_hash, effective_height)?;
        info!(
            vault = %vault_pub_key,
            tx_hash,
            effective_height,
            "outgoing lifecycle: marked outbound reported"
        );
        Ok(())
    }

    pub fn prune_stale(
        &self,
        vault_pub_key: &str,
        tx_hashes: &HashSet<String>,
        observed_mined_only: bool,
        tx_key_backed_only: bool,
    ) -> Result<Vec<String>> {
        let removed = self.store.prune_outgoing_transfers(
            vault_pub_key,
            tx_hashes,
            observed_mined_only,
            tx_key_backed_only,
        )?;
        if !removed.is_empty() {
            info!(
                vault = %vault_pub_key,
                removed = removed.len(),
                observed_mined_only,
                tx_key_backed_only,
                "outgoing lifecycle: pruned stale outbounds"
            );
        }
        Ok(removed)
    }

    pub fn pending_hashes(records: &[OutgoingTransferRecord]) -> HashSet<String> {
        records
            .iter()
            .filter(|record| !record.status.is_observed_mined())
            .map(|record| record.tx_hash.to_ascii_lowercase())
            .filter(|hash| !hash.trim().is_empty())
            .collect()
    }

    pub fn reconcile_records_from_chain_view(
        &self,
        vault_pub_key: &str,
        outgoing: &[OutgoingTransferRecord],
        observed_in_pool: &HashSet<String>,
        observed_mined: &HashMap<String, u64>,
        now_ts: i64,
    ) -> Result<(usize, usize, usize)> {
        let mut marked_mined = 0usize;
        let mut marked_mempool = 0usize;
        let mut marked_pending = 0usize;

        for record in outgoing {
            let normalized = record.tx_hash.to_ascii_lowercase();
            let observation = if let Some(height) = observed_mined.get(&normalized).copied() {
                OutgoingObservation::ObservedMined(height)
            } else if observed_in_pool.contains(&normalized) {
                OutgoingObservation::ObservedInMempool
            } else {
                match record.status {
                    OutgoingTransferStatus::AttestedPendingObservation => {
                        OutgoingObservation::AttestedPending
                    }
                    _ => OutgoingObservation::SignedPending,
                }
            };
            if !self.reconcile_observation(
                vault_pub_key,
                &record.tx_hash,
                observation,
                Some(now_ts),
                None,
            )? {
                continue;
            }
            match observation.status() {
                OutgoingTransferStatus::ObservedMined => marked_mined += 1,
                OutgoingTransferStatus::ObservedMempool => marked_mempool += 1,
                _ => marked_pending += 1,
            }
        }

        Ok((marked_mined, marked_mempool, marked_pending))
    }

    pub fn reconcile_output_readiness(&self, vault_pub_key: &str) -> Result<usize> {
        let outputs = self.store.load_outputs(vault_pub_key)?;
        self.reconcile_output_readiness_with_outputs(vault_pub_key, &outputs)
    }

    pub fn reconcile_output_readiness_with_outputs(
        &self,
        vault_pub_key: &str,
        outputs: &[TrackedOutput],
    ) -> Result<usize> {
        let outgoing = self.store.load_outgoing_transfers(vault_pub_key)?;
        if outgoing.is_empty() {
            return Ok(0);
        }

        let mut outputs_by_tx: HashMap<String, Vec<&TrackedOutput>> = HashMap::new();
        for output in outputs {
            let normalized = normalize_hash(&output.tx_hash);
            if normalized.is_empty() {
                continue;
            }
            outputs_by_tx.entry(normalized).or_default().push(output);
        }

        let mut updated = 0usize;
        for record in outgoing {
            let (local_change_materialized, key_images_ready) =
                Self::derive_output_readiness(&record, &outputs_by_tx);
            if record.local_change_materialized == local_change_materialized
                && record.key_images_ready == key_images_ready
            {
                continue;
            }
            self.store.update_outgoing_transfer_readiness(
                vault_pub_key,
                &record.tx_hash,
                local_change_materialized,
                key_images_ready,
            )?;
            updated += 1;
        }

        if updated > 0 {
            info!(
                vault = %vault_pub_key,
                updated,
                "outgoing lifecycle: reconciled outbound readiness from tracked outputs"
            );
        }
        Ok(updated)
    }

    pub fn pending_reason_for(record: &OutgoingTransferRecord) -> Option<&'static str> {
        if !record.status.is_observed_mined() {
            return Some("awaiting_chain_observation");
        }
        if record.reported_effective_height.is_some() {
            return None;
        }
        if record.has_owned_change == Some(true) {
            if record.local_change_materialized != Some(true) {
                return Some("awaiting_change_materialization");
            }
            if record.key_images_ready != Some(true) {
                return Some("awaiting_key_images");
            }
        }
        Some("awaiting_finalized_observation")
    }

    fn derive_output_readiness(
        record: &OutgoingTransferRecord,
        outputs_by_tx: &HashMap<String, Vec<&TrackedOutput>>,
    ) -> (Option<bool>, Option<bool>) {
        if record.has_owned_change != Some(true) {
            return (None, None);
        }

        let normalized = normalize_hash(&record.tx_hash);
        let Some(outputs) = outputs_by_tx.get(&normalized) else {
            return (Some(false), None);
        };
        if outputs.is_empty() {
            return (Some(false), None);
        }

        let key_images_ready = outputs.iter().all(|output| {
            output
                .key_image
                .as_ref()
                .map(|key_image| !key_image.trim().is_empty())
                .unwrap_or(false)
        });
        (Some(true), Some(key_images_ready))
    }
}

fn normalize_hash(hash: &str) -> String {
    hash.trim().to_ascii_lowercase()
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::state::store::OutputState;
    use tempfile::{tempdir, TempDir};

    fn test_store() -> (TempDir, Store) {
        let dir = tempdir().unwrap();
        let store = Store::open(dir.path()).unwrap();
        (dir, store)
    }

    fn tracked_output(tx_hash: &str, output_index: u32, key_image: Option<&str>) -> TrackedOutput {
        TrackedOutput {
            tx_hash: tx_hash.to_string(),
            output_index,
            amount: 42,
            height: 10,
            key_image: key_image.map(str::to_string),
            spent_by_key_image_height: None,
            state: OutputState::Unspent,
            reservation: None,
            subaddress_major: 0,
            subaddress_minor: 0,
            wallet_output_hex: Some("c0ffee".to_string()),
            address: Some("48address".to_string()),
            note: None,
        }
    }

    #[test]
    fn record_signed_then_mined_and_reported_roundtrips() {
        let (_dir, store) = test_store();
        let lifecycle = OutgoingLifecycle::new(&store);

        lifecycle
            .record_signed(RecordSignedParams {
                tx_hash: "tx-a".to_string(),
                tx_hex: "deadbeef".to_string(),
                tx_keys: vec!["key-a".to_string()],
                has_owned_change: false,
                msg_id: "msg-a".to_string(),
                vault_pub_key: "thorpub1vault".to_string(),
                from_address: "addr-from".to_string(),
                to_address: "addr-to".to_string(),
                amount: 100,
                fee: 5,
                memo: "OUT:123".to_string(),
                planned_height: 77,
                created_at: 1234,
            })
            .unwrap();

        lifecycle
            .reconcile_observation(
                "thorpub1vault",
                "tx-a",
                OutgoingObservation::ObservedMined(88),
                Some(2000),
                Some(1900),
            )
            .unwrap();
        lifecycle
            .mark_reported("thorpub1vault", "tx-a", 88)
            .unwrap();

        let saved = store
            .load_outgoing_transfer("thorpub1vault", "tx-a")
            .unwrap()
            .unwrap();
        let snapshot = OutgoingLifecycle::snapshot(&saved);
        assert_eq!(snapshot.status, OutgoingTransferStatus::ObservedMined);
        assert_eq!(snapshot.observed_height, Some(88));
        assert_eq!(snapshot.effective_height, 88);
        assert_eq!(snapshot.reported_effective_height, Some(88));
        assert_eq!(snapshot.has_owned_change, Some(false));
        assert!(snapshot.has_tx_keys);
        assert!(!snapshot.registered_inventory);
    }

    #[test]
    fn record_validated_updates_existing_signed_outbound() {
        let (_dir, store) = test_store();
        let lifecycle = OutgoingLifecycle::new(&store);

        lifecycle
            .record_signed(RecordSignedParams {
                tx_hash: "tx-b".to_string(),
                tx_hex: "feedface".to_string(),
                tx_keys: vec!["key-b".to_string()],
                has_owned_change: true,
                msg_id: "msg-b".to_string(),
                vault_pub_key: "thorpub1vault".to_string(),
                from_address: "addr-from".to_string(),
                to_address: "addr-to".to_string(),
                amount: 55,
                fee: 2,
                memo: "OUT:456".to_string(),
                planned_height: 90,
                created_at: 500,
            })
            .unwrap();

        let updated = lifecycle
            .record_validated(RecordValidatedParams {
                tx_hash: "tx-b".to_string(),
                tx_hex: "feedface".to_string(),
                tx_keys: vec!["key-b".to_string()],
                registered_inventory: true,
                has_owned_change: true,
                vault_pub_key: "thorpub1vault".to_string(),
                from_address: "addr-from".to_string(),
                to_address: "addr-to".to_string(),
                amount: 55,
                fee: 2,
                memo: "OUT:456".to_string(),
                planned_height: 90,
                observation: OutgoingObservation::ObservedInMempool,
                observed_at: Some(900),
                created_at: 500,
            })
            .unwrap();

        assert_eq!(updated.status, OutgoingTransferStatus::ObservedMempool);
        assert_eq!(updated.tx_keys, vec!["key-b".to_string()]);
        assert_eq!(updated.has_owned_change, Some(true));
        assert!(updated.registered_inventory);
    }

    #[test]
    fn record_validated_preserves_peer_attestation_as_non_inventory() {
        let (_dir, store) = test_store();
        let lifecycle = OutgoingLifecycle::new(&store);

        let record = lifecycle
            .record_validated(RecordValidatedParams {
                tx_hash: "tx-peer".to_string(),
                tx_hex: "feedface".to_string(),
                tx_keys: vec!["key-peer".to_string()],
                registered_inventory: false,
                has_owned_change: true,
                vault_pub_key: "thorpub1vault".to_string(),
                from_address: "addr-from".to_string(),
                to_address: "addr-to".to_string(),
                amount: 55,
                fee: 2,
                memo: "OUT:peer".to_string(),
                planned_height: 90,
                observation: OutgoingObservation::ObservedMined(91),
                observed_at: Some(900),
                created_at: 500,
            })
            .unwrap();

        assert!(!record.registered_inventory);

        let upgraded = lifecycle
            .record_validated(RecordValidatedParams {
                tx_hash: "tx-peer".to_string(),
                tx_hex: "feedface".to_string(),
                tx_keys: vec!["key-peer".to_string()],
                registered_inventory: true,
                has_owned_change: true,
                vault_pub_key: "thorpub1vault".to_string(),
                from_address: "addr-from".to_string(),
                to_address: "addr-to".to_string(),
                amount: 55,
                fee: 2,
                memo: "OUT:peer".to_string(),
                planned_height: 90,
                observation: OutgoingObservation::ObservedMined(91),
                observed_at: Some(900),
                created_at: 500,
            })
            .unwrap();

        assert!(upgraded.registered_inventory);
    }

    #[test]
    fn reconcile_output_readiness_tracks_materialized_change_and_key_images() {
        let (_dir, store) = test_store();
        let lifecycle = OutgoingLifecycle::new(&store);

        lifecycle
            .record_validated(RecordValidatedParams {
                tx_hash: "tx-c".to_string(),
                tx_hex: "feedfeed".to_string(),
                tx_keys: vec!["key-c".to_string()],
                registered_inventory: true,
                has_owned_change: true,
                vault_pub_key: "thorpub1vault".to_string(),
                from_address: "addr-from".to_string(),
                to_address: "addr-to".to_string(),
                amount: 11,
                fee: 1,
                memo: "OUT:789".to_string(),
                planned_height: 91,
                observation: OutgoingObservation::ObservedMined(91),
                observed_at: Some(901),
                created_at: 900,
            })
            .unwrap();

        let mut record = store
            .load_outgoing_transfer("thorpub1vault", "tx-c")
            .unwrap()
            .unwrap();
        let mut snapshot = OutgoingLifecycle::snapshot(&record);
        assert_eq!(snapshot.local_change_materialized, Some(false));
        assert_eq!(snapshot.key_images_ready, None);
        assert_eq!(
            snapshot.pending_reason,
            Some("awaiting_change_materialization")
        );

        store
            .save_outputs("thorpub1vault", &[tracked_output("tx-c", 0, None)])
            .unwrap();
        lifecycle
            .reconcile_output_readiness("thorpub1vault")
            .unwrap();

        record = store
            .load_outgoing_transfer("thorpub1vault", "tx-c")
            .unwrap()
            .unwrap();
        snapshot = OutgoingLifecycle::snapshot(&record);
        assert_eq!(snapshot.local_change_materialized, Some(true));
        assert_eq!(snapshot.key_images_ready, Some(false));
        assert_eq!(snapshot.pending_reason, Some("awaiting_key_images"));

        store
            .save_outputs(
                "thorpub1vault",
                &[tracked_output("tx-c", 0, Some("key-image-c"))],
            )
            .unwrap();
        lifecycle
            .reconcile_output_readiness("thorpub1vault")
            .unwrap();
        lifecycle
            .mark_reported("thorpub1vault", "tx-c", 91)
            .unwrap();

        record = store
            .load_outgoing_transfer("thorpub1vault", "tx-c")
            .unwrap()
            .unwrap();
        snapshot = OutgoingLifecycle::snapshot(&record);
        assert_eq!(snapshot.local_change_materialized, Some(true));
        assert_eq!(snapshot.key_images_ready, Some(true));
        assert_eq!(snapshot.pending_reason, None);
    }
}
