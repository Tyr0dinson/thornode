use std::collections::HashMap;
use std::sync::Arc;
use std::time::{Duration, Instant};

use anyhow::{anyhow, Context, Result};
use parking_lot::Mutex;
use tracing::{debug, info, warn};

use ciphersuite::group::GroupEncoding;
use dalek_ff_group::{Ed25519, Scalar};
use dkg_pedpop::{
    Commitments, EncryptedMessage, EncryptionKeyMessage, KeyGenMachine, KeyMachine, Participant,
    SecretShare, SecretShareMachine, ThresholdParams,
};

use super::keys::ThresholdKeyShare;
use crate::config::ChainNetworkMode;
use crate::state::Store;
use crate::xmr;

/// TTL for DKG sessions before cleanup.
const DKG_SESSION_TTL: Duration = Duration::from_secs(600); // 10 minutes

/// Type aliases for Serai message types parameterized over Ed25519.
type CommitmentMsg = EncryptionKeyMessage<Ed25519, Commitments<Ed25519>>;
type ShareMsg = EncryptedMessage<Ed25519, SecretShare<Scalar>>;

/// State machine phase for DKG.
#[derive(Debug, Clone, PartialEq)]
enum DkgPhase {
    /// Round 1 complete, waiting for all commitments from peers.
    AwaitingRound2,
    /// Round 2 complete, waiting for all encrypted shares from peers.
    AwaitingRound3,
    /// Round 3 complete, waiting for explicit finalize confirmation.
    AwaitingFinalize,
    /// DKG complete.
    Complete,
    /// DKG failed (machine was consumed but crypto failed).
    Failed,
}

/// State for a single DKG session.
///
/// Holds the Serai PedPoP state machines between rounds. Each machine is
/// consumed on the round transition, so they're stored as `Option` and
/// taken via `.take()`.
struct DkgSession {
    params: ThresholdParams,
    phase: DkgPhase,
    created_at: Instant,
    vault_pub_key: String,

    // Serai state machines (only one active at a time, consumed on transition)
    round2_machine: Option<SecretShareMachine<Ed25519>>,
    round3_machine: Option<KeyMachine<Ed25519>>,

    // Finalization payload (set after round3, persisted on finalize only).
    pending_key_share_bytes: Option<Vec<u8>>,
    pending_group_key_hex: Option<String>,
    pending_address: Option<String>,

    // Final results
    group_key_hex: Option<String>,
    address: Option<String>,
}

/// DKG coordinator managing multiple concurrent DKG sessions.
pub struct DkgCoordinator {
    sessions: Mutex<HashMap<String, DkgSession>>,
    max_sessions: usize,
    store: Arc<Store>,
    chain_network: ChainNetworkMode,
}

impl DkgCoordinator {
    pub fn new(store: Arc<Store>, chain_network: ChainNetworkMode, max_sessions: usize) -> Self {
        DkgCoordinator {
            sessions: Mutex::new(HashMap::new()),
            max_sessions,
            store,
            chain_network,
        }
    }

    /// DKG Round 1: Generate coefficients and commitments using PedPoP.
    ///
    /// Creates a `KeyGenMachine`, generates polynomial coefficients with
    /// Schnorr proofs of knowledge, and returns the serialized commitment
    /// message (commitments + PoK + encryption key) to broadcast.
    pub fn round1(
        &self,
        session_id: &str,
        vault_pub_key: &str,
        threshold: u16,
        n: u16,
        participant_index: u16,
        context: &str,
    ) -> Result<Vec<u8>> {
        let mut sessions = self.sessions.lock();

        if sessions.contains_key(session_id) {
            anyhow::bail!("DKG session {} already exists", session_id);
        }
        if sessions.len() >= self.max_sessions {
            anyhow::bail!(
                "DKG session capacity exceeded (max active: {})",
                self.max_sessions
            );
        }

        info!(
            session_id,
            threshold, n, participant_index, "DKG round 1: generating PedPoP commitments"
        );

        let i = Participant::new(participant_index)
            .ok_or_else(|| anyhow!("invalid participant index: must be > 0"))?;
        let params = ThresholdParams::new(threshold, n, i)
            .map_err(|e| anyhow!("invalid threshold params: {}", e))?;

        // Hash the context string to a fixed 32-byte value
        let context_hash: [u8; 32] = {
            use sha2::{Digest, Sha256};
            Sha256::digest(context.as_bytes()).into()
        };

        // PedPoP Round 1: generate polynomial coefficients, Schnorr PoK, and encryption key
        let machine = KeyGenMachine::<Ed25519>::new(params, context_hash);
        let (secret_share_machine, commitment_msg) =
            machine.generate_coefficients(&mut rand::rngs::OsRng);

        // Serialize the commitment message for broadcast
        let commitments_bytes = commitment_msg.serialize();

        let session = DkgSession {
            params,
            phase: DkgPhase::AwaitingRound2,
            created_at: Instant::now(),
            vault_pub_key: vault_pub_key.to_string(),
            round2_machine: Some(secret_share_machine),
            round3_machine: None,
            pending_key_share_bytes: None,
            pending_group_key_hex: None,
            pending_address: None,
            group_key_hex: None,
            address: None,
        };

        sessions.insert(session_id.to_string(), session);

        Ok(commitments_bytes)
    }

    /// DKG Round 2: Verify commitments and generate encrypted secret shares.
    ///
    /// Takes all participants' commitment messages from round 1, verifies
    /// their Schnorr proofs of knowledge (batch verification), and generates
    /// encrypted secret shares for each peer using PedPoP's ECDH + ChaCha20
    /// encryption layer with per-message ephemeral keys.
    pub fn round2(
        &self,
        session_id: &str,
        all_commitments: HashMap<u16, Vec<u8>>,
    ) -> Result<HashMap<u16, Vec<u8>>> {
        let mut sessions = self.sessions.lock();

        let session = sessions
            .get_mut(session_id)
            .ok_or_else(|| anyhow!("DKG session {} not found", session_id))?;

        if session.phase != DkgPhase::AwaitingRound2 {
            anyhow::bail!(
                "DKG session {} not in correct phase for round 2 (current: {:?})",
                session_id,
                session.phase
            );
        }

        let our_idx = u16::from(session.params.i());

        info!(
            session_id,
            num_commitments = all_commitments.len(),
            our_idx,
            "DKG round 2: verifying commitments and generating encrypted shares"
        );

        // Parse commitment messages from all OTHER participants.
        // Serai's PedPoP expects a map WITHOUT our own entry.
        let mut parsed: HashMap<Participant, CommitmentMsg> = HashMap::new();
        for (idx, bytes) in &all_commitments {
            if *idx == our_idx {
                continue;
            }
            let participant = Participant::new(*idx)
                .ok_or_else(|| anyhow!("invalid participant index {}", idx))?;
            let msg = CommitmentMsg::read(&mut bytes.as_slice(), session.params).map_err(|e| {
                anyhow!(
                    "failed to parse commitments from participant {}: {}",
                    idx,
                    e
                )
            })?;
            parsed.insert(participant, msg);
        }

        // Take ownership of the SecretShareMachine (it's consumed on transition)
        let machine = session
            .round2_machine
            .take()
            .ok_or_else(|| anyhow!("round 2 machine already consumed"))?;

        // PedPoP: verify all Schnorr PoKs via batch verification, then generate
        // encrypted secret shares with per-message ephemeral keys + Schnorr PoPs
        let (key_machine, encrypted_shares) =
            match machine.generate_secret_shares(&mut rand::rngs::OsRng, parsed) {
                Ok(result) => result,
                Err(e) => {
                    session.phase = DkgPhase::Failed;
                    anyhow::bail!(
                        "PedPoP commitment verification / share generation failed: {:?}",
                        e
                    );
                }
            };

        // Serialize encrypted shares for each participant
        let shares_bytes: HashMap<u16, Vec<u8>> = encrypted_shares
            .into_iter()
            .map(|(p, msg)| (u16::from(p), msg.serialize()))
            .collect();

        session.round3_machine = Some(key_machine);
        session.phase = DkgPhase::AwaitingRound3;

        Ok(shares_bytes)
    }

    /// DKG Round 3: Process encrypted shares and derive the group key.
    ///
    /// Decrypts received shares via ECDH, verifies them against the polynomial
    /// commitments from round 1, combines into the final threshold key share,
    /// and stages finalize payload. Returns (address, group_key_hex).
    pub fn round3(
        &self,
        session_id: &str,
        all_shares: HashMap<u16, Vec<u8>>,
    ) -> Result<(String, String)> {
        let mut sessions = self.sessions.lock();

        let session = sessions
            .get_mut(session_id)
            .ok_or_else(|| anyhow!("DKG session {} not found", session_id))?;

        if session.phase != DkgPhase::AwaitingRound3 {
            anyhow::bail!(
                "DKG session {} not in correct phase for round 3 (current: {:?})",
                session_id,
                session.phase
            );
        }

        let our_idx = u16::from(session.params.i());

        info!(
            session_id,
            num_shares = all_shares.len(),
            "DKG round 3: computing final threshold key share"
        );

        // Parse encrypted share messages from all OTHER participants
        let mut parsed: HashMap<Participant, ShareMsg> = HashMap::new();
        for (idx, bytes) in &all_shares {
            if *idx == our_idx {
                continue;
            }
            let participant = Participant::new(*idx)
                .ok_or_else(|| anyhow!("invalid participant index {}", idx))?;
            let msg = ShareMsg::read(&mut bytes.as_slice(), session.params)
                .map_err(|e| anyhow!("failed to parse share from participant {}: {}", idx, e))?;
            parsed.insert(participant, msg);
        }

        // Take ownership of the KeyMachine
        let machine = session
            .round3_machine
            .take()
            .ok_or_else(|| anyhow!("round 3 machine already consumed"))?;

        // PedPoP: decrypt shares, verify against commitments, compute threshold key
        let blame_machine = match machine.calculate_share(&mut rand::rngs::OsRng, parsed) {
            Ok(bm) => bm,
            Err(e) => {
                session.phase = DkgPhase::Failed;
                anyhow::bail!("PedPoP share verification failed: {:?}", e);
            }
        };

        // Complete the DKG. In production, this should only be called after
        // confirming with all participants that they completed successfully
        // (consensus confirmation is handled by the Go orchestrator).
        let threshold_keys = blame_machine.complete();

        // Extract group public key
        let group_key = threshold_keys.group_key();
        let group_key_hex = hex::encode(group_key.to_bytes().as_ref());

        let address = xmr::derive_vault_address(&group_key_hex, self.chain_network)
            .context("failed to derive vault address from group key")?;

        // Stage key share for explicit finalize.
        let key_share = ThresholdKeyShare::from_threshold_keys(&threshold_keys);
        session.pending_key_share_bytes = Some(key_share.to_bytes());
        session.pending_group_key_hex = Some(group_key_hex.clone());
        session.pending_address = Some(address.clone());
        session.phase = DkgPhase::AwaitingFinalize;

        info!(
            session_id,
            group_key = %group_key_hex,
            "DKG round3 complete: pending explicit finalize"
        );

        Ok((address, group_key_hex))
    }

    pub fn finalize(
        &self,
        session_id: &str,
        expected_group_key: &str,
        expected_address: &str,
    ) -> Result<Vec<u8>> {
        let mut sessions = self.sessions.lock();

        let session = sessions
            .get_mut(session_id)
            .ok_or_else(|| anyhow!("DKG session {} not found", session_id))?;

        if session.phase != DkgPhase::AwaitingFinalize {
            anyhow::bail!(
                "DKG session {} not in correct phase for finalize (current: {:?})",
                session_id,
                session.phase
            );
        }

        let pending_group_key = session
            .pending_group_key_hex
            .clone()
            .ok_or_else(|| anyhow!("missing pending group key for finalize"))?;
        let pending_address = session
            .pending_address
            .clone()
            .ok_or_else(|| anyhow!("missing pending address for finalize"))?;
        let key_share_bytes = session
            .pending_key_share_bytes
            .clone()
            .ok_or_else(|| anyhow!("missing pending key share for finalize"))?;

        let expected_group_key = expected_group_key.trim();
        let expected_address = expected_address.trim();
        if expected_group_key != pending_group_key {
            anyhow::bail!("finalize group key mismatch");
        }
        if expected_address != pending_address {
            anyhow::bail!("finalize address mismatch");
        }

        // Vault registration is owned by the bifrost, which calls the
        // /v1/monero/vaults/register endpoint after keygen completes using
        // the frost-derived vault pubkey. DKG finalize only persists the
        // key share material (keyed by group_key). Registering here would
        // use session.vault_pub_key (the bifrost-provided identifier, which
        // is the scheduled eddsa pubkey from thornode) and create a stale
        // vault entry that diverges from the bifrost's frost-keyed registration.
        self.store
            .save_frost_keys(&pending_group_key, &key_share_bytes)
            .context("failed to persist FROST key share during finalize")?;

        session.group_key_hex = Some(pending_group_key.clone());
        session.address = Some(pending_address.clone());
        session.pending_key_share_bytes = None;
        session.pending_group_key_hex = None;
        session.pending_address = None;
        session.phase = DkgPhase::Complete;

        info!(
            session_id,
            group_key = %pending_group_key,
            vault_pub_key = %session.vault_pub_key,
            "DKG finalize complete: key share persisted"
        );

        Ok(key_share_bytes)
    }

    /// Clean up expired DKG sessions.
    pub fn cleanup_expired(&self) {
        let mut sessions = self.sessions.lock();
        let before = sessions.len();
        sessions.retain(|id, session| {
            let expired = session.created_at.elapsed() > DKG_SESSION_TTL;
            if expired {
                warn!(session_id = %id, "cleaning up expired DKG session");
            }
            !expired
        });
        let removed = before - sessions.len();
        if removed > 0 {
            debug!(removed, "cleaned up expired DKG sessions");
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use modular_frost::{curve::Ed25519 as FrostEd25519, ThresholdKeys as FrostThresholdKeys};
    use monero_wallet::address::MoneroAddress;
    use std::path::PathBuf;

    /// Helper: create a DkgCoordinator backed by a temp store.
    fn make_coordinator() -> (DkgCoordinator, tempfile::TempDir) {
        let dir = tempfile::tempdir().unwrap();
        let store = Arc::new(Store::open(dir.path()).unwrap());
        (
            DkgCoordinator::new(store, ChainNetworkMode::Mocknet, 64),
            dir,
        )
    }

    /// Run a full t-of-n PedPoP DKG and return the group key + coordinators.
    fn run_dkg(threshold: u16, n: u16) -> (String, Vec<(DkgCoordinator, tempfile::TempDir)>) {
        let context = "test-keygen-session";
        let session_id = "test-session";
        let vault_pub_key = "thorpub1vaulttest";

        let coordinators: Vec<_> = (0..n).map(|_| make_coordinator()).collect();

        // Round 1: each participant generates commitments
        let mut commitments_map: HashMap<u16, Vec<u8>> = HashMap::new();
        for idx in 1..=n {
            let commitments = coordinators[(idx - 1) as usize]
                .0
                .round1(session_id, vault_pub_key, threshold, n, idx, context)
                .expect("round 1 should succeed");
            commitments_map.insert(idx, commitments);
        }

        // Round 2: each participant receives ALL commitments, produces shares
        let mut shares_from: Vec<HashMap<u16, Vec<u8>>> = Vec::new();
        for idx in 1..=n {
            let shares = coordinators[(idx - 1) as usize]
                .0
                .round2(session_id, commitments_map.clone())
                .expect("round 2 should succeed");
            shares_from.push(shares);
        }

        // Round 3: each participant receives shares addressed to them
        let mut group_keys: Vec<String> = Vec::new();
        for idx in 1..=n {
            let mut shares_for_me: HashMap<u16, Vec<u8>> = HashMap::new();
            for sender in 1..=n {
                if sender == idx {
                    continue;
                }
                if let Some(share) = shares_from[(sender - 1) as usize].get(&idx) {
                    shares_for_me.insert(sender, share.clone());
                }
            }

            let (address, group_key) = coordinators[(idx - 1) as usize]
                .0
                .round3(session_id, shares_for_me)
                .expect("round 3 should succeed");
            coordinators[(idx - 1) as usize]
                .0
                .finalize(session_id, &group_key, &address)
                .expect("finalize should succeed");

            assert!(address.starts_with('4'));
            let parsed = MoneroAddress::from_str_with_unchecked_network(&address).unwrap();
            assert!(!parsed.is_guaranteed());
            assert!(!parsed.is_subaddress());
            assert_eq!(group_key.len(), 64, "group key should be 64 hex chars");
            group_keys.push(group_key);
        }

        // All participants must derive the same group key
        for gk in &group_keys[1..] {
            assert_eq!(
                &group_keys[0], gk,
                "all participants must agree on group key"
            );
        }

        (group_keys[0].clone(), coordinators)
    }

    fn run_single_participant_until_finalize(
        coord: &DkgCoordinator,
        session_id: &str,
        vault_pub_key: &str,
    ) -> (String, String) {
        coord
            .round1(session_id, vault_pub_key, 1, 1, 1, "ctx")
            .expect("round1 should succeed");
        coord
            .round2(session_id, HashMap::new())
            .expect("round2 should succeed");
        coord
            .round3(session_id, HashMap::new())
            .expect("round3 should succeed")
    }

    /// Full 2-of-3 PedPoP DKG with key persistence roundtrip.
    #[test]
    fn test_full_dkg_2_of_3() {
        let (group_key, coordinators) = run_dkg(2, 3);

        // Verify each participant's key share can be loaded and deserialized
        for (i, (coord, _dir)) in coordinators.iter().enumerate() {
            let key_data = coord
                .store
                .load_frost_keys(&group_key)
                .expect("should load")
                .expect("key should exist");
            let key_share = ThresholdKeyShare::from_stored_bytes(&key_data)
                .unwrap_or_else(|e| panic!("participant {} key parse failed: {}", i + 1, e));
            let restored = key_share
                .to_threshold_keys()
                .unwrap_or_else(|e| panic!("participant {} key deser failed: {}", i + 1, e));
            let restored_hex = hex::encode(restored.group_key().to_bytes().as_ref());
            assert_eq!(restored_hex, group_key);
        }
    }

    /// Full 3-of-5 PedPoP DKG.
    #[test]
    fn test_full_dkg_3_of_5() {
        let (group_key, _coordinators) = run_dkg(3, 5);
        assert_eq!(group_key.len(), 64);
    }

    #[test]
    fn test_pedpop_key_bytes_parse_in_modular_frost_stack() {
        let (group_key, coordinators) = run_dkg(2, 3);

        for (coord, _) in &coordinators {
            let bytes = coord
                .store
                .load_frost_keys(&group_key)
                .expect("key load should succeed")
                .expect("key should exist");
            let parsed = FrostThresholdKeys::<FrostEd25519>::read(&mut bytes.as_slice())
                .expect("parse modular-frost keys");
            assert_eq!(
                hex::encode(parsed.group_key().to_bytes().as_ref()),
                group_key
            );
        }
    }

    /// Verify that duplicate session IDs are rejected.
    #[test]
    fn test_duplicate_session_rejected() {
        let (coord, _dir) = make_coordinator();
        coord
            .round1("dup", "thorpub1vaulttest", 2, 3, 1, "ctx")
            .unwrap();
        let err = coord.round1("dup", "thorpub1vaulttest", 2, 3, 1, "ctx");
        assert!(err.is_err());
        assert!(err.unwrap_err().to_string().contains("already exists"));
    }

    #[test]
    fn test_session_capacity_rejected() {
        let dir = tempfile::tempdir().unwrap();
        let store = Arc::new(Store::open(dir.path()).unwrap());
        let coord = DkgCoordinator::new(store, ChainNetworkMode::Mocknet, 1);

        coord
            .round1("sess-1", "thorpub1vaulttest", 2, 3, 1, "ctx")
            .unwrap();
        let err = coord
            .round1("sess-2", "thorpub1vaulttest", 2, 3, 1, "ctx")
            .unwrap_err();
        assert!(err.to_string().contains("capacity exceeded"));
    }

    /// Verify phase enforcement.
    #[test]
    fn test_wrong_phase_rejected() {
        let (coord, _dir) = make_coordinator();
        coord
            .round1("sess", "thorpub1vaulttest", 2, 3, 1, "ctx")
            .unwrap();

        // Round 3 before round 2 should fail
        let err = coord.round3("sess", HashMap::new());
        assert!(err.is_err());
        assert!(err
            .unwrap_err()
            .to_string()
            .contains("not in correct phase"));
    }

    #[test]
    fn test_round3_does_not_persist_before_finalize() {
        let (coord, _dir) = make_coordinator();
        let session_id = "sess";
        coord
            .round1(session_id, "thorpub1vaulttest", 1, 1, 1, "ctx")
            .unwrap();
        coord.round2(session_id, HashMap::new()).unwrap();
        let (address, group_key) = coord.round3(session_id, HashMap::new()).unwrap();

        let key_before = coord.store.load_frost_keys(&group_key).unwrap();
        assert!(
            key_before.is_none(),
            "key material must not persist before finalize"
        );

        let mismatch_err = coord
            .finalize(session_id, &group_key, "wrong-address")
            .unwrap_err();
        assert!(mismatch_err.to_string().contains("address mismatch"));

        let mismatch_err = coord
            .finalize(session_id, "00deadbeef", &address)
            .unwrap_err();
        assert!(mismatch_err.to_string().contains("group key mismatch"));

        coord.finalize(session_id, &group_key, &address).unwrap();
        let key_after = coord.store.load_frost_keys(&group_key).unwrap();
        assert!(
            key_after.is_some(),
            "key material must persist after finalize"
        );
    }

    #[test]
    fn test_finalize_fails_when_key_save_fails() {
        let (coord, dir) = make_coordinator();
        let session_id = "sess-save-fail";
        let vault_pub_key = "thorpub1vaulttest";
        let (address, group_key) =
            run_single_participant_until_finalize(&coord, session_id, vault_pub_key);

        let keys_path: PathBuf = dir.path().join("keys");
        std::fs::write(&keys_path, b"not-a-directory").expect("failed to poison keys path");

        let err = coord
            .finalize(session_id, &group_key, &address)
            .expect_err("finalize should fail when key save fails")
            .to_string();
        assert!(err.contains("failed to persist FROST key share"));

        assert!(
            coord.store.load_frost_keys(&group_key).unwrap().is_none(),
            "failed finalize must not leave persisted key file"
        );
    }

    /// Expired sessions should be cleaned up.
    #[test]
    fn test_cleanup_expired() {
        let (coord, _dir) = make_coordinator();
        coord
            .round1("sess", "thorpub1vaulttest", 2, 3, 1, "ctx")
            .unwrap();

        // Manually expire the session by setting created_at to the past
        {
            let mut sessions = coord.sessions.lock();
            let session = sessions.get_mut("sess").unwrap();
            session.created_at = Instant::now() - DKG_SESSION_TTL - Duration::from_secs(1);
        }

        coord.cleanup_expired();

        let sessions = coord.sessions.lock();
        assert!(sessions.is_empty());
    }

    #[test]
    fn test_cleanup_expired_unfinalized_session_does_not_persist() {
        let (coord, _dir) = make_coordinator();
        let session_id = "sess-unfinalized";
        coord
            .round1(session_id, "thorpub1vaulttest", 1, 1, 1, "ctx")
            .unwrap();
        coord.round2(session_id, HashMap::new()).unwrap();
        let (_address, group_key) = coord.round3(session_id, HashMap::new()).unwrap();

        {
            let mut sessions = coord.sessions.lock();
            let session = sessions.get_mut(session_id).unwrap();
            session.created_at = Instant::now() - DKG_SESSION_TTL - Duration::from_secs(1);
        }

        coord.cleanup_expired();
        let key = coord.store.load_frost_keys(&group_key).unwrap();
        assert!(
            key.is_none(),
            "expired unfinalized session must not persist key material"
        );
    }

    #[test]
    fn test_finalize_returns_key_share_bytes() {
        let (coord, _dir) = make_coordinator();
        let session_id = "sess-return";
        coord
            .round1(session_id, "thorpub1vaultreturn", 1, 1, 1, "ctx")
            .unwrap();
        coord.round2(session_id, HashMap::new()).unwrap();
        let (address, group_key) = coord.round3(session_id, HashMap::new()).unwrap();

        let returned_bytes = coord
            .finalize(session_id, &group_key, &address)
            .expect("finalize should succeed");
        assert!(
            !returned_bytes.is_empty(),
            "finalize must return non-empty key share bytes"
        );

        let stored_bytes = coord
            .store
            .load_frost_keys(&group_key)
            .unwrap()
            .expect("key material must be persisted after finalize");
        assert_eq!(
            returned_bytes, stored_bytes,
            "finalize return value must match persisted key material"
        );
    }

    /// The bifrost passes the scheduled thornode pool pubkey (eddsa) as
    /// vault_pub_key when initiating DKG. After DKG completes, the FROST
    /// group key produces a DIFFERENT vault pubkey (derived from the group
    /// key bytes). The bifrost then explicitly registers the vault under
    /// the FROST-derived pubkey via the /v1/monero/vaults/register endpoint.
    ///
    /// Bug: finalize currently registers the vault under the bifrost-provided
    /// (eddsa) vault_pub_key. Combined with the bifrost's explicit frost-key
    /// registration, this creates TWO vault entries in the store for a single
    /// keygen — one under the eddsa key and one under the frost key.
    ///
    /// Fix: finalize must NOT register a vault. The bifrost owns vault
    /// registration post-keygen using the frost-derived key.
    #[test]
    fn test_finalize_does_not_register_vault_under_bifrost_provided_pubkey() {
        let (coord, _dir) = make_coordinator();
        let session_id = "sess-no-register";
        // Simulates the eddsa key passed by the bifrost during keygen.
        let bifrost_provided_vault_pub_key = "thorpub1eddsakeyfrombifrost";
        let (address, group_key) = run_single_participant_until_finalize(
            &coord,
            session_id,
            bifrost_provided_vault_pub_key,
        );

        coord
            .finalize(session_id, &group_key, &address)
            .expect("finalize should succeed");

        let stored_vault = coord
            .store
            .get_vault(bifrost_provided_vault_pub_key)
            .expect("store lookup should succeed");
        assert!(
            stored_vault.is_none(),
            "finalize must NOT register a vault under the bifrost-provided vault_pub_key. \
             Registration is owned by the bifrost (via the register-vault HTTP endpoint) \
             and must use the frost-derived pubkey, not the eddsa key from DKG round1."
        );
    }
}
