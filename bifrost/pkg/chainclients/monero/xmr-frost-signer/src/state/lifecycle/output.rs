use std::collections::HashSet;

use anyhow::Result;
use tracing::info;

use super::OutgoingLifecycle;
use crate::state::store::{OutputRef, ScanBlockMeta, Store, TrackedOutput};

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct ApplyKeyImagesOutcome {
    pub applied: usize,
    pub not_found: usize,
}

pub struct OutputLifecycle<'a> {
    store: &'a Store,
}

impl<'a> OutputLifecycle<'a> {
    pub fn new(store: &'a Store) -> Self {
        Self { store }
    }

    pub fn merge_scanned_outputs(
        &self,
        vault_pub_key: &str,
        scanned: &[TrackedOutput],
    ) -> Result<()> {
        self.store.merge_scanned_outputs(vault_pub_key, scanned)?;
        OutgoingLifecycle::new(self.store).reconcile_output_readiness(vault_pub_key)?;
        info!(
            vault = %vault_pub_key,
            scanned = scanned.len(),
            "output lifecycle: merged scanned outputs"
        );
        Ok(())
    }

    pub fn record_scan_block(&self, vault_pub_key: &str, meta: &ScanBlockMeta) -> Result<()> {
        self.store.save_scan_block_meta(vault_pub_key, meta)?;
        info!(
            vault = %vault_pub_key,
            height = meta.height,
            added_outputs = meta.added_outputs.len(),
            spent_key_images = meta.spent_key_images.len(),
            "output lifecycle: recorded scan block metadata"
        );
        Ok(())
    }

    pub fn rollback_from_height(&self, vault_pub_key: &str, from_height: u64) -> Result<()> {
        self.store
            .rollback_scan_state_from_height(vault_pub_key, from_height)?;
        info!(
            vault = %vault_pub_key,
            from_height,
            "output lifecycle: rolled back persisted scan state"
        );
        Ok(())
    }

    pub fn prune_scan_blocks_before(&self, vault_pub_key: &str, min_height: u64) -> Result<usize> {
        let removed = self
            .store
            .prune_scan_block_meta_before(vault_pub_key, min_height)?;
        if removed > 0 {
            info!(
                vault = %vault_pub_key,
                min_height,
                removed,
                "output lifecycle: pruned historical scan block metadata"
            );
        }
        Ok(removed)
    }

    pub fn reserve(
        &self,
        vault_pub_key: &str,
        selected: &[OutputRef],
        session_id: &str,
        msg_id: &str,
        expires_at: i64,
    ) -> Result<()> {
        self.store
            .reserve_outputs(vault_pub_key, selected, session_id, msg_id, expires_at)?;
        info!(
            vault = %vault_pub_key,
            session_id,
            msg_id,
            reserved = selected.len(),
            "output lifecycle: reserved outputs"
        );
        Ok(())
    }

    pub fn bind_reservation_tx_hash(
        &self,
        vault_pub_key: &str,
        session_id: &str,
        msg_id: &str,
        tx_hash: &str,
    ) -> Result<()> {
        self.store
            .bind_reservation_tx_hash(vault_pub_key, session_id, msg_id, tx_hash)?;
        info!(
            vault = %vault_pub_key,
            session_id,
            msg_id,
            tx_hash,
            "output lifecycle: bound reservation tx hash"
        );
        Ok(())
    }

    pub fn release_session(
        &self,
        vault_pub_key: &str,
        session_id: &str,
        msg_id: &str,
    ) -> Result<usize> {
        let released =
            self.store
                .release_reservations_for_session(vault_pub_key, session_id, msg_id)?;
        if released > 0 {
            info!(
                vault = %vault_pub_key,
                session_id,
                msg_id,
                released,
                "output lifecycle: released session reservations"
            );
        }
        Ok(released)
    }

    pub fn finalize_for_msg(
        &self,
        vault_pub_key: &str,
        msg_id: &str,
        tx_hash: &str,
    ) -> Result<crate::state::store::FinalizeReservedForMsgResult> {
        let result = self
            .store
            .finalize_reserved_for_msg(vault_pub_key, msg_id, tx_hash)?;
        info!(
            vault = %vault_pub_key,
            msg_id,
            tx_hash,
            finalized = result.finalized,
            candidate_mismatch = result.candidate_mismatch,
            missing_candidate = result.missing_candidate,
            "output lifecycle: finalized reserved outputs for message"
        );
        Ok(result)
    }

    pub fn reconcile_reservations(
        &self,
        vault_pub_key: &str,
        observed_hashes: &HashSet<String>,
        now_ts: i64,
    ) -> Result<(usize, usize)> {
        let result = self
            .store
            .reconcile_reservations(vault_pub_key, observed_hashes, now_ts)?;
        if result.0 > 0 || result.1 > 0 {
            info!(
                vault = %vault_pub_key,
                finalized = result.0,
                released = result.1,
                "output lifecycle: reconciled reservations"
            );
        }
        Ok(result)
    }

    pub fn apply_key_images(
        &self,
        vault_pub_key: &str,
        key_images: &[(String, u32, String)],
        reconcile: bool,
    ) -> Result<ApplyKeyImagesOutcome> {
        let (applied, not_found) =
            self.store
                .apply_key_images(vault_pub_key, key_images, reconcile)?;
        OutgoingLifecycle::new(self.store).reconcile_output_readiness(vault_pub_key)?;
        if applied > 0 || not_found > 0 {
            info!(
                vault = %vault_pub_key,
                refs = key_images.len(),
                applied,
                not_found,
                reconcile,
                "output lifecycle: applied key images"
            );
        }
        Ok(ApplyKeyImagesOutcome { applied, not_found })
    }

    pub fn missing_outputs_for_key_images(
        &self,
        vault_pub_key: &str,
        key_images: &[(String, u32, String)],
    ) -> Result<Vec<(String, u32, String)>> {
        if key_images.is_empty() {
            return Ok(Vec::new());
        }

        let existing: HashSet<(String, u32)> = self
            .store
            .load_outputs(vault_pub_key)?
            .into_iter()
            .map(|output| {
                (
                    output.tx_hash.trim().to_ascii_lowercase(),
                    output.output_index,
                )
            })
            .collect();

        let mut seen = HashSet::new();
        let missing = key_images
            .iter()
            .filter_map(|(tx_hash, output_index, key_image)| {
                let normalized_tx_hash = tx_hash.trim().to_ascii_lowercase();
                let key = (normalized_tx_hash.clone(), *output_index);
                if existing.contains(&key) || !seen.insert(key) {
                    return None;
                }
                Some((normalized_tx_hash, *output_index, key_image.clone()))
            })
            .collect();
        Ok(missing)
    }

    pub fn mark_spent_on_chain(
        &self,
        vault_pub_key: &str,
        key_images: &[String],
        height: u64,
    ) -> Result<ApplyKeyImagesOutcome> {
        let (applied, not_found) =
            self.store
                .mark_spent_key_images(vault_pub_key, key_images, height)?;
        if applied > 0 || not_found > 0 {
            info!(
                vault = %vault_pub_key,
                key_images = key_images.len(),
                height,
                applied,
                not_found,
                "output lifecycle: marked outputs spent on chain"
            );
        }
        Ok(ApplyKeyImagesOutcome { applied, not_found })
    }

    pub fn replay_reconciled_spends(
        &self,
        vault_pub_key: &str,
        key_images: &[(String, u32, String)],
    ) -> Result<()> {
        if key_images.is_empty() {
            return Ok(());
        }

        let requested_refs: HashSet<(String, u32)> = key_images
            .iter()
            .map(|(tx_hash, output_index, _)| (tx_hash.clone(), *output_index))
            .collect();
        let outputs = self.store.load_outputs(vault_pub_key)?;

        let mut candidate_key_images = HashSet::new();
        for output in outputs {
            if !requested_refs.contains(&(output.tx_hash.clone(), output.output_index)) {
                continue;
            }
            if output.spent_by_key_image_height.is_some() {
                continue;
            }
            let Some(key_image) = output
                .key_image
                .as_ref()
                .map(|key_image| key_image.trim().to_ascii_lowercase())
            else {
                continue;
            };
            if key_image.is_empty() {
                continue;
            }
            candidate_key_images.insert(key_image);
        }
        if candidate_key_images.is_empty() {
            return Ok(());
        }

        let metas = self.store.list_scan_block_meta(vault_pub_key)?;
        let mut replayed = 0usize;
        for meta in metas {
            let relevant_key_images: Vec<String> = meta
                .spent_key_images
                .iter()
                .map(|key_image| key_image.trim().to_ascii_lowercase())
                .filter(|key_image| candidate_key_images.contains(key_image))
                .collect();
            if relevant_key_images.is_empty() {
                continue;
            }
            self.mark_spent_on_chain(vault_pub_key, &relevant_key_images, meta.height)?;
            let mut updated_meta = meta.clone();
            let outputs_after = self.store.load_outputs(vault_pub_key)?;
            for output in outputs_after {
                let key = (output.tx_hash.clone(), output.output_index);
                if !requested_refs.contains(&key) {
                    continue;
                }
                if output.spent_by_key_image_height != Some(meta.height) {
                    continue;
                }
                if !updated_meta
                    .spent_by_key_images
                    .iter()
                    .any(|r| r.tx_hash == output.tx_hash && r.output_index == output.output_index)
                {
                    updated_meta.spent_by_key_images.push(output.ref_key());
                }
            }
            self.record_scan_block(vault_pub_key, &updated_meta)?;
            replayed += 1;
        }

        if replayed > 0 {
            info!(
                vault = %vault_pub_key,
                replayed,
                "output lifecycle: replayed historical reconciled spends"
            );
        }
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::state::lifecycle::{OutgoingLifecycle, OutgoingObservation, RecordValidatedParams};
    use crate::state::store::{OutputState, ScanBlockMeta};
    use tempfile::{tempdir, TempDir};

    fn test_store() -> (TempDir, Store) {
        let dir = tempdir().unwrap();
        let store = Store::open(dir.path()).unwrap();
        (dir, store)
    }

    fn tracked_output(tx_hash: &str, output_index: u32) -> TrackedOutput {
        TrackedOutput {
            tx_hash: tx_hash.to_string(),
            output_index,
            amount: 42,
            height: 10,
            key_image: None,
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
    fn reserve_bind_and_finalize_transition_output() {
        let (_dir, store) = test_store();
        store
            .save_outputs("thorpub1vault", &[tracked_output("tx-a", 0)])
            .unwrap();
        let lifecycle = OutputLifecycle::new(&store);

        lifecycle
            .reserve(
                "thorpub1vault",
                &[OutputRef {
                    tx_hash: "tx-a".to_string(),
                    output_index: 0,
                }],
                "session-a",
                "msg-a",
                1000,
            )
            .unwrap();
        lifecycle
            .bind_reservation_tx_hash("thorpub1vault", "session-a", "msg-a", "outbound-a")
            .unwrap();
        let finalized = lifecycle
            .finalize_for_msg("thorpub1vault", "msg-a", "outbound-a")
            .unwrap();

        assert_eq!(finalized.finalized, 1);
        let outputs = store.load_outputs("thorpub1vault").unwrap();
        assert_eq!(outputs.len(), 1);
        assert!(outputs[0].is_spent());
        assert!(outputs[0].reservation.is_none());
    }

    #[test]
    fn replay_reconciled_spends_marks_historical_key_image_spend() {
        let (_dir, store) = test_store();
        store
            .save_outputs("thorpub1vault", &[tracked_output("tx-b", 1)])
            .unwrap();
        let lifecycle = OutputLifecycle::new(&store);

        lifecycle
            .record_scan_block(
                "thorpub1vault",
                &ScanBlockMeta {
                    height: 25,
                    block_hash: "block-25".to_string(),
                    parent_hash: "block-24".to_string(),
                    added_outputs: vec![OutputRef {
                        tx_hash: "tx-b".to_string(),
                        output_index: 1,
                    }],
                    spent_key_images: vec!["ki-b".to_string()],
                    spent_by_key_images: Vec::new(),
                },
            )
            .unwrap();
        lifecycle
            .apply_key_images(
                "thorpub1vault",
                &[("tx-b".to_string(), 1, "ki-b".to_string())],
                true,
            )
            .unwrap();
        lifecycle
            .replay_reconciled_spends(
                "thorpub1vault",
                &[("tx-b".to_string(), 1, "ki-b".to_string())],
            )
            .unwrap();

        let outputs = store.load_outputs("thorpub1vault").unwrap();
        assert_eq!(outputs[0].spent_by_key_image_height, Some(25));
        assert!(outputs[0].is_spent());

        let metas = store.list_scan_block_meta("thorpub1vault").unwrap();
        assert_eq!(metas[0].spent_by_key_images.len(), 1);
    }

    #[test]
    fn merge_and_apply_key_images_refresh_outgoing_readiness() {
        let (_dir, store) = test_store();
        let output_lifecycle = OutputLifecycle::new(&store);
        let outgoing_lifecycle = OutgoingLifecycle::new(&store);

        outgoing_lifecycle
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

        output_lifecycle
            .merge_scanned_outputs("thorpub1vault", &[tracked_output("tx-c", 0)])
            .unwrap();
        let updated = store
            .load_outgoing_transfer("thorpub1vault", "tx-c")
            .unwrap()
            .unwrap();
        assert_eq!(updated.local_change_materialized, Some(true));
        assert_eq!(updated.key_images_ready, Some(false));

        output_lifecycle
            .apply_key_images(
                "thorpub1vault",
                &[("tx-c".to_string(), 0, "key-image-c".to_string())],
                false,
            )
            .unwrap();
        let updated = store
            .load_outgoing_transfer("thorpub1vault", "tx-c")
            .unwrap()
            .unwrap();
        assert_eq!(updated.local_change_materialized, Some(true));
        assert_eq!(updated.key_images_ready, Some(true));
    }

    #[test]
    fn missing_outputs_for_key_images_reports_only_absent_refs() {
        let (_dir, store) = test_store();
        store
            .save_outputs("thorpub1vault", &[tracked_output("tx-d", 0)])
            .unwrap();
        let lifecycle = OutputLifecycle::new(&store);

        let missing = lifecycle
            .missing_outputs_for_key_images(
                "thorpub1vault",
                &[
                    (" TX-D ".to_string(), 0, "ki-d".to_string()),
                    (" TX-E ".to_string(), 1, "ki-e".to_string()),
                    ("tx-e".to_string(), 1, "ki-e".to_string()),
                ],
            )
            .unwrap();

        assert_eq!(missing, vec![("tx-e".to_string(), 1, "ki-e".to_string())]);
    }
}
