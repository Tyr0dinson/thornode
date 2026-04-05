use std::collections::{HashMap, HashSet};
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::Arc;
use std::time::{Duration, Instant};

use anyhow::{Context, Result};
use modular_frost::{
    curve::Ed25519 as FrostEd25519,
    sign::{PreprocessMachine, SignMachine, SignatureMachine, Writable},
    Participant, ThresholdKeys,
};
use parking_lot::Mutex;
use tracing::{debug, info, warn};

use ciphersuite::group::GroupEncoding;
use monero_wallet::send::{TransactionSignMachine, TransactionSignatureMachine};

use crate::config::ChainNetworkMode;
use crate::state::{
    store::{FinalizeReservedForMsgResult, OutgoingTransferRecord, OutputRef},
    Store,
};
use crate::tx::builder::{self, UnsignedTransaction};
use crate::tx::daemon::DaemonClient;
use crate::tx::fee;
use crate::xmr;

/// TTL for signing sessions before cleanup.
const SIGN_SESSION_TTL: Duration = Duration::from_secs(300); // 5 minutes
const RESERVATION_TTL: Duration = Duration::from_secs(1800); // 30 minutes
const RESERVATION_MAX_RETRIES: usize = 3;
const PREPARE_SPIN_RETRIES: usize = 64;
const PREPARE_YIELD_RETRIES: usize = 64;
const PREPARE_SLEEP_BASE_MICROS: u64 = 50;
const PREPARE_SLEEP_MAX_MICROS: u64 = 5_000;

#[derive(Debug, Clone, PartialEq)]
enum SignPhase {
    Preprocessed,
    PartialSigned,
    Complete,
}

struct SignSession {
    phase: SignPhase,
    created_at: Instant,
    vault_pub_key: String,
    msg_id: String,
    from_address: String,
    participant_count: u16,
    local_participant_index: u16,
    peer_participants: Vec<u16>,
    unsigned_tx: Option<UnsignedTransaction>,
    sign_machine: Option<TransactionSignMachine>,
    completion_machine: Option<TransactionSignatureMachine>,
    tx_hash: Option<String>,
    tx_hex: Option<String>,
    fee: Option<u64>,
}

/// FROST signing coordinator managing concurrent signing sessions.
pub struct SignCoordinator {
    sessions: Mutex<HashMap<String, SignSession>>,
    preparing_msg_ids: Mutex<HashSet<String>>,
    inflight_prepares: AtomicUsize,
    max_sessions: usize,
    store: Arc<Store>,
    daemon: DaemonClient,
    chain_network: ChainNetworkMode,
}

#[derive(Debug)]
struct PrepareSlotGuard<'a> {
    counter: &'a AtomicUsize,
}

impl Drop for PrepareSlotGuard<'_> {
    fn drop(&mut self) {
        self.counter.fetch_sub(1, Ordering::AcqRel);
    }
}

#[derive(Debug)]
struct PrepareMsgIdGuard<'a> {
    claims: &'a Mutex<HashSet<String>>,
    msg_id: Option<String>,
}

impl Drop for PrepareMsgIdGuard<'_> {
    fn drop(&mut self) {
        if let Some(msg_id) = self.msg_id.take() {
            self.claims.lock().remove(&msg_id);
        }
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum PrepareBackoffAction {
    Spin,
    Yield,
    Sleep(Duration),
}

fn prepare_backoff_action(retry: usize) -> PrepareBackoffAction {
    if retry < PREPARE_SPIN_RETRIES {
        return PrepareBackoffAction::Spin;
    }
    if retry < PREPARE_SPIN_RETRIES + PREPARE_YIELD_RETRIES {
        return PrepareBackoffAction::Yield;
    }

    let sleep_retry = retry - PREPARE_SPIN_RETRIES - PREPARE_YIELD_RETRIES;
    let shift = sleep_retry.min(31) as u32;
    let multiplier = 1u64 << shift;
    let micros = PREPARE_SLEEP_BASE_MICROS
        .saturating_mul(multiplier)
        .min(PREPARE_SLEEP_MAX_MICROS);
    PrepareBackoffAction::Sleep(Duration::from_micros(micros))
}

fn apply_prepare_backoff(retry: usize) {
    match prepare_backoff_action(retry) {
        PrepareBackoffAction::Spin => std::hint::spin_loop(),
        PrepareBackoffAction::Yield => std::thread::yield_now(),
        PrepareBackoffAction::Sleep(duration) => std::thread::sleep(duration),
    }
}

fn normalize_participant_payloads(
    payload_kind: &str,
    participant_count: u16,
    local_participant_index: u16,
    payloads: HashMap<u16, Vec<u8>>,
) -> Result<HashMap<u16, Vec<u8>>> {
    if participant_count == 0 {
        anyhow::bail!("signing session has invalid participant count 0");
    }
    if local_participant_index == 0 || local_participant_index > participant_count {
        anyhow::bail!(
            "signing session has invalid local participant index {} for participant count {}",
            local_participant_index,
            participant_count
        );
    }

    let mut normalized = HashMap::with_capacity(payloads.len().min(participant_count as usize));
    let mut dropped = Vec::new();
    for (idx, payload) in payloads {
        if idx == 0 || idx > participant_count || idx == local_participant_index {
            dropped.push(idx);
            continue;
        }
        normalized.insert(idx, payload);
    }

    if !dropped.is_empty() {
        dropped.sort_unstable();
        dropped.dedup();
        warn!(
            payload_kind,
            participant_count,
            dropped_participants = ?dropped,
            "ignoring participant payloads outside signing key set"
        );
    }

    Ok(normalized)
}

fn normalize_expected_peer_payloads(
    payload_kind: &str,
    expected_peers: &[u16],
    payloads: HashMap<u16, Vec<u8>>,
) -> HashMap<u16, Vec<u8>> {
    let expected: HashSet<u16> = expected_peers.iter().copied().collect();
    let mut normalized = HashMap::with_capacity(payloads.len().min(expected.len()));
    let mut dropped = Vec::new();
    for (idx, payload) in payloads {
        if !expected.contains(&idx) {
            dropped.push(idx);
            continue;
        }
        normalized.insert(idx, payload);
    }

    if !dropped.is_empty() {
        dropped.sort_unstable();
        dropped.dedup();
        warn!(
            payload_kind,
            expected_peers = ?expected_peers,
            dropped_participants = ?dropped,
            "ignoring participant payloads not present in signing session"
        );
    }

    normalized
}

impl SignCoordinator {
    pub fn new(
        store: Arc<Store>,
        daemon: DaemonClient,
        chain_network: ChainNetworkMode,
        max_sessions: usize,
    ) -> Self {
        SignCoordinator {
            sessions: Mutex::new(HashMap::new()),
            preparing_msg_ids: Mutex::new(HashSet::new()),
            inflight_prepares: AtomicUsize::new(0),
            max_sessions,
            store,
            daemon,
            chain_network,
        }
    }

    pub async fn prepare(
        &self,
        vault_pub_key: &str,
        to_address: &str,
        amount: u64,
        memo: &str,
        fee_rate_override: Option<u64>,
        decoy_context: &str,
        msg_id: &str,
    ) -> Result<(String, Vec<u8>)> {
        info!(
            vault = vault_pub_key,
            to_address, amount, memo, "preparing real FROST signing session"
        );
        let _prepare_slot = self.reserve_prepare_slot()?;
        let _msg_id_claim = self.claim_prepare_msg_id(msg_id)?;

        let key_id = self.resolve_group_key_for_vault(vault_pub_key)?;
        let key_data = self
            .store
            .load_frost_keys(&key_id)
            .context("failed to load FROST keys")?
            .ok_or_else(|| {
                anyhow::anyhow!(
                    "no FROST keys found for vault {vault_pub_key} (resolved key id: {key_id})"
                )
            })?;

        let threshold_keys = ThresholdKeys::<FrostEd25519>::read(&mut key_data.as_slice())
            .context("failed to deserialize threshold key material for signing")?;
        let participant_count = threshold_keys.params().n();
        let local_participant_index = u16::from(threshold_keys.params().i());
        let group_key_hex = hex::encode(threshold_keys.group_key().to_bytes().as_ref());
        let from_address = xmr::derive_vault_address(&group_key_hex, self.chain_network)
            .context("failed to derive vault address from signing key")?;

        let outputs = self
            .store
            .load_outputs(vault_pub_key)
            .context("failed to load tracked outputs for signing")?;

        let oxide_rpc = self
            .daemon
            .oxide_rpc()
            .await
            .context("failed to initialize monero-oxide RPC client")?;
        let fee_rate = fee::resolve_fee_rate(&oxide_rpc, fee_rate_override).await?;
        let last_header = self
            .daemon
            .get_last_block_header()
            .await
            .context("failed to fetch chain header for transaction construction")?;

        let session_id = uuid::Uuid::new_v4().to_string();
        let mut excluded_inputs: HashSet<(String, u32)> = HashSet::new();
        let mut unsigned_tx = None;
        for attempt in 0..RESERVATION_MAX_RETRIES {
            let built = builder::build_unsigned_transaction(
                &oxide_rpc,
                to_address,
                amount,
                memo,
                fee_rate,
                decoy_context,
                last_header.block_header.major_version,
                last_header.block_header.height as usize,
                &outputs,
                Some(&from_address),
                &excluded_inputs,
                self.chain_network,
            )
            .await
            .context("failed to build real signable transaction")?;

            let selected_refs = built
                .selected_inputs
                .iter()
                .map(|input| OutputRef {
                    tx_hash: input.tx_hash.clone(),
                    output_index: input.output_index,
                })
                .collect::<Vec<_>>();
            let expires_at = chrono::Utc::now()
                .checked_add_signed(chrono::Duration::from_std(RESERVATION_TTL)?)
                .ok_or_else(|| anyhow::anyhow!("failed to compute reservation expiry"))?
                .timestamp();

            match self.store.reserve_outputs(
                vault_pub_key,
                &selected_refs,
                &session_id,
                msg_id,
                expires_at,
            ) {
                Ok(()) => {
                    unsigned_tx = Some(built);
                    break;
                }
                Err(e) => {
                    let err_text = e.to_string();
                    if !err_text.contains("reservation conflict")
                        || attempt + 1 >= RESERVATION_MAX_RETRIES
                    {
                        return Err(e).context("failed to reserve outputs for signing");
                    }
                    for selected in selected_refs {
                        excluded_inputs.insert((selected.tx_hash, selected.output_index));
                    }
                }
            }
        }
        let unsigned_tx = unsigned_tx.ok_or_else(|| {
            anyhow::anyhow!("failed to reserve conflict-free outputs after retries")
        })?;

        let machine = unsigned_tx
            .signable
            .clone()
            .multisig(threshold_keys)
            .context("failed to initialize multisig transaction machine")?;
        let (sign_machine, preprocess) = machine.preprocess(&mut rand::rngs::OsRng);

        let mut preprocess_bytes = Vec::new();
        preprocess
            .write(&mut preprocess_bytes)
            .context("failed to serialize preprocess payload")?;

        let session = SignSession {
            phase: SignPhase::Preprocessed,
            created_at: Instant::now(),
            vault_pub_key: vault_pub_key.to_string(),
            msg_id: msg_id.to_string(),
            from_address,
            participant_count,
            local_participant_index,
            peer_participants: Vec::new(),
            unsigned_tx: Some(unsigned_tx),
            sign_machine: Some(sign_machine),
            completion_machine: None,
            tx_hash: None,
            tx_hex: None,
            fee: None,
        };
        self.sessions.lock().insert(session_id.clone(), session);

        Ok((session_id, preprocess_bytes))
    }

    fn reserve_prepare_slot(&self) -> Result<PrepareSlotGuard<'_>> {
        let mut retry = 0usize;
        loop {
            let active_sessions = self.sessions.lock().len();
            let inflight = self.inflight_prepares.load(Ordering::Acquire);
            if active_sessions.saturating_add(inflight) >= self.max_sessions {
                anyhow::bail!(
                    "signing session capacity exceeded (max active: {})",
                    self.max_sessions
                );
            }
            let next = inflight
                .checked_add(1)
                .ok_or_else(|| anyhow::anyhow!("signing session capacity counter overflow"))?;
            if self
                .inflight_prepares
                .compare_exchange(inflight, next, Ordering::AcqRel, Ordering::Acquire)
                .is_ok()
            {
                return Ok(PrepareSlotGuard {
                    counter: &self.inflight_prepares,
                });
            }
            apply_prepare_backoff(retry);
            retry = retry.saturating_add(1);
        }
    }

    fn claim_prepare_msg_id(&self, msg_id: &str) -> Result<PrepareMsgIdGuard<'_>> {
        let msg_id = msg_id.trim();
        if msg_id.is_empty() {
            anyhow::bail!("signing msg_id must not be empty");
        }

        {
            let sessions = self.sessions.lock();
            if sessions.values().any(|s| s.msg_id == msg_id) {
                anyhow::bail!("signing session already exists for msg_id {msg_id}");
            }
        }

        let mut claims = self.preparing_msg_ids.lock();
        if !claims.insert(msg_id.to_string()) {
            anyhow::bail!("signing session already exists for msg_id {msg_id}");
        }
        Ok(PrepareMsgIdGuard {
            claims: &self.preparing_msg_ids,
            msg_id: Some(msg_id.to_string()),
        })
    }

    fn abandon_session(&self, session_id: &str, vault_pub_key: &str, msg_id: &str, reason: &str) {
        let removed = self.sessions.lock().remove(session_id).is_some();
        match self
            .store
            .release_reservations_for_session(vault_pub_key, session_id, msg_id)
        {
            Ok(released) => {
                debug!(
                    session_id,
                    vault = vault_pub_key,
                    msg_id,
                    removed,
                    released,
                    reason,
                    "abandoned signing session and released reservations"
                );
            }
            Err(e) => {
                warn!(
                    error = %e,
                    session_id,
                    vault = vault_pub_key,
                    msg_id,
                    removed,
                    reason,
                    "failed to release reservations for abandoned signing session"
                );
            }
        }
    }

    #[cfg(test)]
    fn inflight_prepare_count(&self) -> usize {
        self.inflight_prepares.load(Ordering::Acquire)
    }

    pub fn partial_sign(
        &self,
        session_id: &str,
        all_preprocesses: HashMap<u16, Vec<u8>>,
    ) -> Result<Vec<u8>> {
        let (sign_machine, parsed, peer_participants, vault_pub_key, msg_id) = {
            let mut sessions = self.sessions.lock();
            let session = sessions
                .get_mut(session_id)
                .ok_or_else(|| anyhow::anyhow!("signing session {session_id} not found"))?;

            if session.phase != SignPhase::Preprocessed {
                anyhow::bail!(
                    "signing session {} not in correct phase for partial sign (current: {:?})",
                    session_id,
                    session.phase
                );
            }

            // Validate all preprocesses BEFORE consuming the sign machine so
            // that a malformed payload does not permanently poison the session.
            let sign_machine_ref = session.sign_machine.as_ref().ok_or_else(|| {
                anyhow::anyhow!("signing session {} missing sign machine", session_id)
            })?;
            let all_preprocesses = normalize_participant_payloads(
                "preprocesses",
                session.participant_count,
                session.local_participant_index,
                all_preprocesses,
            )?;

            let mut parsed = HashMap::with_capacity(all_preprocesses.len());
            for (idx, bytes) in all_preprocesses {
                let participant = Participant::new(idx)
                    .ok_or_else(|| anyhow::anyhow!("invalid participant index {idx}"))?;
                let preprocess = sign_machine_ref
                    .read_preprocess(&mut bytes.as_slice())
                    .map_err(|e| {
                        anyhow::anyhow!(
                            "failed to parse preprocess from participant {}: {}",
                            idx,
                            e
                        )
                    })?;
                parsed.insert(participant, preprocess);
            }
            let mut peer_participants = parsed
                .keys()
                .map(|participant| u16::from(*participant))
                .collect::<Vec<_>>();
            peer_participants.sort_unstable();
            peer_participants.dedup();

            (
                session.sign_machine.take().unwrap(),
                parsed,
                peer_participants,
                session.vault_pub_key.clone(),
                session.msg_id.clone(),
            )
        };

        // All inputs validated; now consume the machine for signing.
        let (completion_machine, share) = match sign_machine.sign(parsed, &[]) {
            Ok(result) => result,
            Err(e) => {
                self.abandon_session(
                    session_id,
                    &vault_pub_key,
                    &msg_id,
                    "partial_sign_failed_after_machine_consumption",
                );
                return Err(anyhow::anyhow!(
                    "failed to produce partial signature: {}",
                    e
                ));
            }
        };

        let mut sessions = self.sessions.lock();
        let session = sessions.get_mut(session_id).ok_or_else(|| {
            anyhow::anyhow!(
                "signing session {} disappeared before partial sign completion",
                session_id
            )
        })?;

        let mut share_bytes = Vec::new();
        share
            .write(&mut share_bytes)
            .context("failed to serialize signature share")?;

        session.completion_machine = Some(completion_machine);
        session.phase = SignPhase::PartialSigned;
        session.peer_participants = peer_participants;

        Ok(share_bytes)
    }

    pub async fn combine(
        &self,
        session_id: &str,
        all_shares: HashMap<u16, Vec<u8>>,
        msg_id: &str,
    ) -> Result<(String, String, u64, Vec<OutputRef>)> {
        let chain_height = self
            .daemon
            .get_height()
            .await
            .context("failed to fetch chain height for outgoing attribution")?;

        let (completion_machine, parsed, unsigned_tx, vault_pub_key, session_msg_id, from_address) = {
            let mut sessions = self.sessions.lock();
            let session = sessions
                .get_mut(session_id)
                .ok_or_else(|| anyhow::anyhow!("signing session {session_id} not found"))?;
            if session.msg_id != msg_id {
                anyhow::bail!(
                    "signing session {} msg_id mismatch (expected {}, got {})",
                    session_id,
                    session.msg_id,
                    msg_id
                );
            }

            if session.phase != SignPhase::PartialSigned {
                anyhow::bail!(
                    "signing session {} not in correct phase for combine (current: {:?})",
                    session_id,
                    session.phase
                );
            }

            // Validate all shares BEFORE consuming the completion machine so
            // that a malformed payload does not permanently poison the session.
            let completion_machine_ref = session.completion_machine.as_ref().ok_or_else(|| {
                anyhow::anyhow!("signing session {} missing completion machine", session_id)
            })?;
            let unsigned_tx = session.unsigned_tx.as_ref().ok_or_else(|| {
                anyhow::anyhow!(
                    "signing session {} missing unsigned transaction",
                    session_id
                )
            })?;
            let all_shares =
                normalize_expected_peer_payloads("shares", &session.peer_participants, all_shares);

            let mut parsed = HashMap::with_capacity(all_shares.len());
            for (idx, bytes) in all_shares {
                let participant = Participant::new(idx)
                    .ok_or_else(|| anyhow::anyhow!("invalid participant index {idx}"))?;
                let share = completion_machine_ref
                    .read_share(&mut bytes.as_slice())
                    .map_err(|e| {
                        anyhow::anyhow!("failed to parse share from participant {}: {}", idx, e)
                    })?;
                parsed.insert(participant, share);
            }

            (
                session.completion_machine.take().unwrap(),
                parsed,
                unsigned_tx.clone(),
                session.vault_pub_key.clone(),
                session.msg_id.clone(),
                session.from_address.clone(),
            )
        };

        // All inputs validated; now consume the machine for combining.
        let tx = match completion_machine.complete(parsed) {
            Ok(tx) => tx,
            Err(e) => {
                self.abandon_session(
                    session_id,
                    &vault_pub_key,
                    &session_msg_id,
                    "combine_failed_after_machine_consumption",
                );
                return Err(anyhow::anyhow!("failed to combine threshold shares: {}", e));
            }
        };
        let tx_hash = hex::encode(tx.hash());
        let tx_hex = hex::encode(tx.serialize());
        let fee = unsigned_tx.fee;
        let spent_output_refs: Vec<OutputRef> = unsigned_tx
            .selected_inputs
            .iter()
            .map(|input| OutputRef {
                tx_hash: input.tx_hash.clone(),
                output_index: input.output_index,
            })
            .collect();

        self.store
            .bind_reservation_tx_hash(&vault_pub_key, session_id, msg_id, &tx_hash)
            .context("failed to bind reservation tx hash")?;

        self.store
            .save_outgoing_transfer(&OutgoingTransferRecord {
                tx_hash: tx_hash.clone(),
                tx_hex: tx_hex.clone(),
                msg_id: session_msg_id,
                vault_pub_key: vault_pub_key.clone(),
                from_address,
                to_address: unsigned_tx.to_address.clone(),
                amount: unsigned_tx.amount,
                fee,
                memo: unsigned_tx.memo.clone(),
                planned_height: chain_height,
                observed_height: None,
                status: "signed_pending_observation".to_string(),
                broadcasted_at: None,
                observed_at: None,
                created_at: chrono::Utc::now().timestamp(),
            })
            .context("failed to persist outgoing transfer attribution")?;

        let mut sessions = self.sessions.lock();
        let session = sessions.get_mut(session_id).ok_or_else(|| {
            anyhow::anyhow!(
                "signing session {} disappeared before combine completion",
                session_id
            )
        })?;
        session.tx_hash = Some(tx_hash.clone());
        session.tx_hex = Some(tx_hex.clone());
        session.fee = Some(fee);
        session.phase = SignPhase::Complete;

        Ok((tx_hash, tx_hex, fee, spent_output_refs))
    }

    pub fn cleanup_expired(&self) {
        let mut sessions = self.sessions.lock();
        let mut removed = 0usize;
        let mut released = 0usize;
        sessions.retain(|id, session| {
            let expired = session.created_at.elapsed() > SIGN_SESSION_TTL;
            if !expired {
                return true;
            }

            warn!(session_id = %id, "cleaning up expired signing session");
            match session.phase {
                SignPhase::Complete => {
                    removed += 1;
                    false
                }
                SignPhase::Preprocessed | SignPhase::PartialSigned => {
                    match self.store.release_reservations_for_session(
                        &session.vault_pub_key,
                        id,
                        &session.msg_id,
                    ) {
                        Ok(released_count) => {
                            released += released_count;
                            removed += 1;
                            false
                        }
                        Err(e) => {
                            warn!(
                                error = %e,
                                session_id = %id,
                                "failed to release reservations for expired signing session; retaining for retry"
                            );
                            true
                        }
                    }
                }
            }
        });
        if removed > 0 {
            debug!(removed, released, "cleaned up expired signing sessions");
        }
        if released > 0 {
            debug!(
                released,
                "released reserved outputs for expired signing sessions"
            );
        }
    }

    fn resolve_group_key_for_vault(&self, vault_pub_key: &str) -> Result<String> {
        let vault = self
            .store
            .get_vault(vault_pub_key)
            .context("failed to load vault registry mapping")?
            .ok_or_else(|| {
                anyhow::anyhow!(
                    "vault {} is not registered; keygen must complete successfully before signing",
                    vault_pub_key
                )
            })?;
        Ok(vault.group_key)
    }

    pub fn finalize_broadcast(
        &self,
        vault_pub_key: &str,
        msg_id: &str,
        tx_hash: &str,
    ) -> Result<FinalizeReservedForMsgResult> {
        self.store
            .finalize_reserved_for_msg(vault_pub_key, msg_id, tx_hash)
            .context("failed to finalize reserved outputs for broadcast")
    }

    pub async fn reconcile_reservations(&self) -> Result<()> {
        let vaults = self
            .store
            .list_vaults()
            .context("failed to list vaults for reservation reconciliation")?;
        for vault in vaults {
            let candidate_hashes = self
                .store
                .collect_reserved_tx_hashes(&vault.vault_pub_key)
                .with_context(|| {
                    format!(
                        "failed to collect reserved tx hashes for {}",
                        vault.vault_pub_key
                    )
                })?;
            let mut observed_hashes = HashSet::new();
            if !candidate_hashes.is_empty() {
                let query = candidate_hashes.iter().cloned().collect::<Vec<_>>();
                let txs = self
                    .daemon
                    .get_transactions(&query)
                    .await
                    .with_context(|| {
                        format!("failed to query transactions for {}", vault.vault_pub_key)
                    })?
                    .txs
                    .unwrap_or_default();
                for tx in txs {
                    if tx.in_pool.unwrap_or(false) || tx.block_height.unwrap_or(0) > 0 {
                        observed_hashes.insert(tx.tx_hash.to_ascii_lowercase());
                    }
                }
            }
            let now_ts = chrono::Utc::now().timestamp();
            let (finalized, released) = self
                .store
                .reconcile_reservations(&vault.vault_pub_key, &observed_hashes, now_ts)
                .with_context(|| {
                    format!(
                        "failed to reconcile reservations for {}",
                        vault.vault_pub_key
                    )
                })?;
            if finalized > 0 || released > 0 {
                info!(
                    vault = %vault.vault_pub_key,
                    finalized,
                    released,
                    "reconciled reserved outputs"
                );
            }

            let outgoing = self
                .store
                .load_outgoing_transfers(&vault.vault_pub_key)
                .with_context(|| {
                    format!(
                        "failed to load outgoing transfers for {}",
                        vault.vault_pub_key
                    )
                })?;
            if outgoing.is_empty() {
                continue;
            }

            let pending_hashes: HashSet<String> = outgoing
                .iter()
                .filter(|record| !record.status.eq_ignore_ascii_case("observed_mined"))
                .map(|record| record.tx_hash.to_ascii_lowercase())
                .filter(|hash| !hash.trim().is_empty())
                .collect();
            let mut observed_in_pool = HashSet::new();
            let mut observed_mined: HashMap<String, u64> = HashMap::new();
            if !pending_hashes.is_empty() {
                let query = pending_hashes.iter().cloned().collect::<Vec<_>>();
                let txs = self
                    .daemon
                    .get_transactions(&query)
                    .await
                    .with_context(|| {
                        format!(
                            "failed to query outgoing transactions for {}",
                            vault.vault_pub_key
                        )
                    })?
                    .txs
                    .unwrap_or_default();
                for tx in txs {
                    let normalized = tx.tx_hash.to_ascii_lowercase();
                    if let Some(height) = tx.block_height {
                        if height > 0 {
                            observed_mined.insert(normalized, height);
                            continue;
                        }
                    }
                    if tx.in_pool.unwrap_or(false) {
                        observed_in_pool.insert(normalized);
                    }
                }
            }

            let now_ts = chrono::Utc::now().timestamp();
            let mut marked_mined = 0usize;
            let mut marked_mempool = 0usize;
            let mut marked_pending = 0usize;
            for record in outgoing {
                let normalized = record.tx_hash.to_ascii_lowercase();
                let mut status = "signed_pending_observation";
                let mut observed_height = None;
                if let Some(height) = observed_mined.get(&normalized).copied() {
                    status = "observed_mined";
                    observed_height = Some(height);
                } else if observed_in_pool.contains(&normalized) {
                    status = "observed_mempool";
                }
                if record.status.eq_ignore_ascii_case(status)
                    && (observed_height.is_none() || record.observed_height == observed_height)
                {
                    continue;
                }
                self.store
                    .update_outgoing_transfer_status(
                        &vault.vault_pub_key,
                        &record.tx_hash,
                        status,
                        observed_height,
                        Some(now_ts),
                        None,
                    )
                    .with_context(|| {
                        format!(
                            "failed to update outgoing observation status for {}:{}",
                            vault.vault_pub_key, record.tx_hash
                        )
                    })?;
                match status {
                    "observed_mined" => marked_mined += 1,
                    "observed_mempool" => marked_mempool += 1,
                    _ => marked_pending += 1,
                }
            }
            if marked_mined > 0 || marked_mempool > 0 || marked_pending > 0 {
                info!(
                    vault = %vault.vault_pub_key,
                    marked_mined,
                    marked_mempool,
                    marked_pending,
                    "reconciled outgoing transfer observations"
                );
            }
        }
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn make_signer(store: Arc<Store>) -> SignCoordinator {
        SignCoordinator::new(
            store,
            DaemonClient::new("http://127.0.0.1:18081", 1024 * 1024),
            ChainNetworkMode::Mocknet,
            16,
        )
    }

    fn expired_session(phase: SignPhase, vault_pub_key: &str, msg_id: &str) -> SignSession {
        SignSession {
            phase,
            created_at: Instant::now() - SIGN_SESSION_TTL - Duration::from_secs(1),
            vault_pub_key: vault_pub_key.to_string(),
            msg_id: msg_id.to_string(),
            from_address: "48from".to_string(),
            participant_count: 1,
            local_participant_index: 1,
            peer_participants: Vec::new(),
            unsigned_tx: None,
            sign_machine: None,
            completion_machine: None,
            tx_hash: None,
            tx_hex: None,
            fee: None,
        }
    }

    fn reserved_output_for_session(
        tx_hash: &str,
        output_index: u32,
        session_id: &str,
        msg_id: &str,
    ) -> crate::state::store::TrackedOutput {
        crate::state::store::TrackedOutput {
            tx_hash: tx_hash.to_string(),
            output_index,
            amount: 100,
            height: 42,
            key_image: Some(format!("key-image-{output_index}")),
            state: crate::state::store::OutputState::Reserved,
            reservation: Some(crate::state::store::OutputReservation {
                session_id: session_id.to_string(),
                msg_id: msg_id.to_string(),
                reserved_at: 1_000,
                expires_at: 2_000,
                tx_hash_candidate: None,
            }),
            subaddress_major: 0,
            subaddress_minor: output_index,
            wallet_output_hex: Some(format!("wallet-output-{output_index}")),
            address: Some("48from".to_string()),
            note: None,
        }
    }

    #[test]
    fn cleanup_expired_releases_preprocessed_session_reservations() {
        let dir = tempfile::tempdir().unwrap();
        let store = Arc::new(Store::open(dir.path()).unwrap());
        let signer = make_signer(store.clone());
        let session_id = "session-preprocessed-expired";
        let msg_id = "msg-preprocessed-expired";
        let vault_pub_key = "thorpub1vault";

        store
            .save_outputs(
                vault_pub_key,
                &[reserved_output_for_session("tx-pre", 0, session_id, msg_id)],
            )
            .unwrap();
        signer.sessions.lock().insert(
            session_id.to_string(),
            expired_session(SignPhase::Preprocessed, vault_pub_key, msg_id),
        );

        signer.cleanup_expired();

        assert!(!signer.sessions.lock().contains_key(session_id));
        let outputs = store.load_outputs(vault_pub_key).unwrap();
        assert_eq!(outputs.len(), 1);
        assert_eq!(outputs[0].state, crate::state::store::OutputState::Unspent);
        assert!(outputs[0].reservation.is_none());
    }

    #[test]
    fn cleanup_expired_releases_partial_signed_session_reservations() {
        let dir = tempfile::tempdir().unwrap();
        let store = Arc::new(Store::open(dir.path()).unwrap());
        let signer = make_signer(store.clone());
        let session_id = "session-partial-expired";
        let msg_id = "msg-partial-expired";
        let vault_pub_key = "thorpub1vault";

        store
            .save_outputs(
                vault_pub_key,
                &[reserved_output_for_session(
                    "tx-partial",
                    1,
                    session_id,
                    msg_id,
                )],
            )
            .unwrap();
        signer.sessions.lock().insert(
            session_id.to_string(),
            expired_session(SignPhase::PartialSigned, vault_pub_key, msg_id),
        );

        signer.cleanup_expired();

        assert!(!signer.sessions.lock().contains_key(session_id));
        let outputs = store.load_outputs(vault_pub_key).unwrap();
        assert_eq!(outputs.len(), 1);
        assert_eq!(outputs[0].state, crate::state::store::OutputState::Unspent);
        assert!(outputs[0].reservation.is_none());
    }

    #[test]
    fn cleanup_expired_does_not_release_complete_session_reservations() {
        let dir = tempfile::tempdir().unwrap();
        let store = Arc::new(Store::open(dir.path()).unwrap());
        let signer = make_signer(store.clone());
        let session_id = "session-complete-expired";
        let msg_id = "msg-complete-expired";
        let vault_pub_key = "thorpub1vault";

        store
            .save_outputs(
                vault_pub_key,
                &[reserved_output_for_session(
                    "tx-complete",
                    2,
                    session_id,
                    msg_id,
                )],
            )
            .unwrap();
        signer.sessions.lock().insert(
            session_id.to_string(),
            expired_session(SignPhase::Complete, vault_pub_key, msg_id),
        );

        signer.cleanup_expired();

        assert!(!signer.sessions.lock().contains_key(session_id));
        let outputs = store.load_outputs(vault_pub_key).unwrap();
        assert_eq!(outputs.len(), 1);
        assert_eq!(outputs[0].state, crate::state::store::OutputState::Reserved);
        assert!(outputs[0].reservation.is_some());
    }

    #[test]
    fn cleanup_expired_retains_session_for_retry_on_release_failure() {
        let dir = tempfile::tempdir().unwrap();
        let store = Arc::new(Store::open(dir.path()).unwrap());
        let signer = make_signer(store.clone());
        let session_id = "session-release-fail";
        let msg_id = " ";
        let vault_pub_key = "thorpub1vault";

        store
            .save_outputs(
                vault_pub_key,
                &[reserved_output_for_session(
                    "tx-fail", 3, session_id, msg_id,
                )],
            )
            .unwrap();
        signer.sessions.lock().insert(
            session_id.to_string(),
            expired_session(SignPhase::Preprocessed, vault_pub_key, msg_id),
        );

        signer.cleanup_expired();

        assert!(signer.sessions.lock().contains_key(session_id));
        let outputs = store.load_outputs(vault_pub_key).unwrap();
        assert_eq!(outputs.len(), 1);
        assert_eq!(outputs[0].state, crate::state::store::OutputState::Reserved);
        assert!(outputs[0].reservation.is_some());
    }

    #[test]
    fn abandon_session_releases_reservations_and_removes_session() {
        let dir = tempfile::tempdir().unwrap();
        let store = Arc::new(Store::open(dir.path()).unwrap());
        let signer = make_signer(store.clone());
        let session_id = "session-abandon";
        let msg_id = "msg-abandon";
        let vault_pub_key = "thorpub1vault";

        store
            .save_outputs(
                vault_pub_key,
                &[reserved_output_for_session(
                    "tx-abandon",
                    4,
                    session_id,
                    msg_id,
                )],
            )
            .unwrap();
        signer.sessions.lock().insert(
            session_id.to_string(),
            SignSession {
                phase: SignPhase::PartialSigned,
                created_at: Instant::now(),
                vault_pub_key: vault_pub_key.to_string(),
                msg_id: msg_id.to_string(),
                from_address: "48from".to_string(),
                participant_count: 1,
                local_participant_index: 1,
                peer_participants: Vec::new(),
                unsigned_tx: None,
                sign_machine: None,
                completion_machine: None,
                tx_hash: None,
                tx_hex: None,
                fee: None,
            },
        );

        signer.abandon_session(session_id, vault_pub_key, msg_id, "test");

        assert!(!signer.sessions.lock().contains_key(session_id));
        let outputs = store.load_outputs(vault_pub_key).unwrap();
        assert_eq!(outputs.len(), 1);
        assert_eq!(outputs[0].state, crate::state::store::OutputState::Unspent);
        assert!(outputs[0].reservation.is_none());
    }

    #[test]
    fn resolve_group_key_prefers_registry_mapping() {
        let dir = tempfile::tempdir().unwrap();
        let store = Arc::new(Store::open(dir.path()).unwrap());
        store
            .register_vault("thorpub1vault", "deadbeefgroupkey", "48address", "test")
            .unwrap();

        let signer = make_signer(store);
        let resolved = signer.resolve_group_key_for_vault("thorpub1vault").unwrap();
        assert_eq!(resolved, "deadbeefgroupkey");
    }

    #[test]
    fn resolve_group_key_requires_registry_mapping() {
        let dir = tempfile::tempdir().unwrap();
        let store = Arc::new(Store::open(dir.path()).unwrap());
        let signer = make_signer(store);
        let err = signer
            .resolve_group_key_for_vault("thorpub1missing")
            .unwrap_err();
        assert!(
            err.to_string()
                .contains("keygen must complete successfully before signing"),
            "unexpected error: {err}"
        );
    }

    #[tokio::test]
    async fn prepare_rejects_when_session_capacity_reached() {
        let dir = tempfile::tempdir().unwrap();
        let store = Arc::new(Store::open(dir.path()).unwrap());
        let signer = SignCoordinator::new(
            store,
            DaemonClient::new("http://127.0.0.1:18081", 1024 * 1024),
            ChainNetworkMode::Mocknet,
            1,
        );

        signer.sessions.lock().insert(
            "existing".to_string(),
            SignSession {
                phase: SignPhase::Complete,
                created_at: Instant::now(),
                vault_pub_key: "thorpub1vault".to_string(),
                msg_id: "msg-1".to_string(),
                from_address: "48from".to_string(),
                participant_count: 1,
                local_participant_index: 1,
                peer_participants: Vec::new(),
                unsigned_tx: None,
                sign_machine: None,
                completion_machine: None,
                tx_hash: None,
                tx_hex: None,
                fee: None,
            },
        );

        let err = signer
            .prepare("thorpub1vault", "48to", 1, "", None, "ctx", "msg-2")
            .await
            .unwrap_err();
        assert!(err.to_string().contains("capacity exceeded"));
    }

    #[test]
    fn reserve_prepare_slot_releases_on_drop() {
        let dir = tempfile::tempdir().unwrap();
        let store = Arc::new(Store::open(dir.path()).unwrap());
        let signer = SignCoordinator::new(
            store,
            DaemonClient::new("http://127.0.0.1:18081", 1024 * 1024),
            ChainNetworkMode::Mocknet,
            1,
        );

        {
            let _slot = signer.reserve_prepare_slot().unwrap();
            assert_eq!(signer.inflight_prepare_count(), 1);
            let err = signer.reserve_prepare_slot().unwrap_err();
            assert!(err.to_string().contains("capacity exceeded"));
        }

        assert_eq!(signer.inflight_prepare_count(), 0);
        let _slot = signer.reserve_prepare_slot().unwrap();
        assert_eq!(signer.inflight_prepare_count(), 1);
    }

    #[test]
    fn reserve_prepare_slot_enforces_capacity_across_threads() {
        let dir = tempfile::tempdir().unwrap();
        let store = Arc::new(Store::open(dir.path()).unwrap());
        let signer = Arc::new(SignCoordinator::new(
            store,
            DaemonClient::new("http://127.0.0.1:18081", 1024 * 1024),
            ChainNetworkMode::Mocknet,
            2,
        ));
        let barrier = Arc::new(std::sync::Barrier::new(3));

        let mut handles = Vec::new();
        for _ in 0..2 {
            let signer = signer.clone();
            let barrier = barrier.clone();
            handles.push(std::thread::spawn(move || {
                let _slot = signer
                    .reserve_prepare_slot()
                    .expect("reserve should succeed");
                barrier.wait();
                barrier.wait();
            }));
        }

        barrier.wait();
        let err = signer.reserve_prepare_slot().unwrap_err();
        assert!(err.to_string().contains("capacity exceeded"));
        barrier.wait();

        for handle in handles {
            handle.join().expect("thread should join successfully");
        }
        assert_eq!(signer.inflight_prepare_count(), 0);
    }

    #[test]
    fn claim_prepare_msg_id_enforces_single_flight_under_concurrency() {
        let dir = tempfile::tempdir().unwrap();
        let store = Arc::new(Store::open(dir.path()).unwrap());
        let signer = Arc::new(SignCoordinator::new(
            store,
            DaemonClient::new("http://127.0.0.1:18081", 1024 * 1024),
            ChainNetworkMode::Mocknet,
            2,
        ));
        let start = Arc::new(std::sync::Barrier::new(3));
        let release = Arc::new(std::sync::Barrier::new(3));

        let mut handles = Vec::new();
        for _ in 0..2 {
            let signer = signer.clone();
            let start = start.clone();
            let release = release.clone();
            handles.push(std::thread::spawn(move || {
                start.wait();
                let claim = signer.claim_prepare_msg_id("msg-race");
                let success = claim.is_ok();
                release.wait();
                drop(claim);
                success
            }));
        }

        start.wait();
        release.wait();

        let successes = handles
            .into_iter()
            .map(|handle| handle.join().expect("thread should join successfully"))
            .filter(|success| *success)
            .count();
        assert_eq!(
            successes, 1,
            "duplicate msg_id claims must be single-flight"
        );
    }

    #[test]
    fn reserve_prepare_slot_backoff_transitions_and_caps_sleep() {
        assert_eq!(prepare_backoff_action(0), PrepareBackoffAction::Spin);
        assert_eq!(
            prepare_backoff_action(PREPARE_SPIN_RETRIES - 1),
            PrepareBackoffAction::Spin
        );
        assert_eq!(
            prepare_backoff_action(PREPARE_SPIN_RETRIES),
            PrepareBackoffAction::Yield
        );
        assert_eq!(
            prepare_backoff_action(PREPARE_SPIN_RETRIES + PREPARE_YIELD_RETRIES - 1),
            PrepareBackoffAction::Yield
        );
        assert_eq!(
            prepare_backoff_action(PREPARE_SPIN_RETRIES + PREPARE_YIELD_RETRIES),
            PrepareBackoffAction::Sleep(Duration::from_micros(PREPARE_SLEEP_BASE_MICROS))
        );
        assert_eq!(
            prepare_backoff_action(PREPARE_SPIN_RETRIES + PREPARE_YIELD_RETRIES + 64),
            PrepareBackoffAction::Sleep(Duration::from_micros(PREPARE_SLEEP_MAX_MICROS))
        );
    }

    #[test]
    fn normalize_participant_payloads_drops_self_and_out_of_range_entries() {
        let mut payloads = HashMap::new();
        payloads.insert(1, vec![0x01]);
        payloads.insert(2, vec![0x02]);
        payloads.insert(3, vec![0x03]);

        let normalized = normalize_participant_payloads("shares", 3, 2, payloads).unwrap();
        assert_eq!(normalized.len(), 2);
        assert_eq!(normalized.get(&1), Some(&vec![0x01]));
        assert_eq!(normalized.get(&3), Some(&vec![0x03]));
    }

    #[test]
    fn normalize_expected_peer_payloads_allows_empty_for_single_participant_signing() {
        let mut payloads = HashMap::new();
        payloads.insert(1, vec![0x01]);

        let normalized = normalize_expected_peer_payloads("shares", &[], payloads);
        assert!(normalized.is_empty());
    }

    #[test]
    fn normalize_expected_peer_payloads_filters_unexpected_entries() {
        let mut payloads = HashMap::new();
        payloads.insert(1, vec![0x01]);
        payloads.insert(2, vec![0x02]);
        payloads.insert(3, vec![0x03]);

        let normalized = normalize_expected_peer_payloads("shares", &[1, 3], payloads);
        assert_eq!(normalized.len(), 2);
        assert_eq!(normalized.get(&1), Some(&vec![0x01]));
        assert_eq!(normalized.get(&3), Some(&vec![0x03]));
    }
}
