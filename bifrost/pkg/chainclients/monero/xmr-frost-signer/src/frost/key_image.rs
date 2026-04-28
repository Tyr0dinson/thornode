use std::collections::HashMap;
use std::ops::Deref;
use std::sync::Arc;
use std::time::{Duration, Instant};

use anyhow::{Context, Result};
use curve25519_dalek::{edwards::EdwardsPoint, scalar::Scalar};
use mf_ciphersuite::group::GroupEncoding;
use mf_dalek_ff_group as dfg;
use modular_frost::{
    algorithm::{Algorithm, WriteAddendum},
    curve::{Ed25519 as FrostEd25519, Field, Group, PrimeField},
    sign::{
        AlgorithmMachine, AlgorithmSignMachine, AlgorithmSignatureMachine, PreprocessMachine,
        SignMachine, SignatureMachine, Writable,
    },
    FrostError, Participant, ThresholdKeys, ThresholdView,
};
use monero_generators::biased_hash_to_point;
use monero_wallet::WalletOutput;
use parking_lot::Mutex;
use rand::rngs::OsRng;
use rand_chacha::rand_core::{CryptoRng, RngCore};
use transcript::{RecommendedTranscript, Transcript};
use zeroize::{Zeroize, Zeroizing};

use crate::state::{store::TrackedOutput, OutputLifecycle, Store};

const KEY_IMAGE_SESSION_TTL: Duration = Duration::from_secs(120);

#[derive(Debug, Clone, PartialEq, Eq)]
enum KeyImagePhase {
    Preprocessed,
    PartialSigned,
}

#[derive(Clone, PartialEq, Eq, Zeroize, Debug)]
struct KeyImageAddendum {
    key_image_share: dfg::EdwardsPoint,
}

impl WriteAddendum for KeyImageAddendum {
    fn write<W: std::io::Write>(&self, writer: &mut W) -> std::io::Result<()> {
        writer.write_all(self.key_image_share.to_bytes().as_ref())
    }
}

#[derive(Clone, PartialEq, Eq, Debug)]
struct KeyImageSignature {
    key_image: dfg::EdwardsPoint,
    challenge: dfg::Scalar,
    response: dfg::Scalar,
}

#[derive(Debug)]
struct KeyImageAlgorithm {
    transcript: RecommendedTranscript,
    output_key: dfg::EdwardsPoint,
    key_image_generator: dfg::EdwardsPoint,
    key_image_shares: HashMap<[u8; 32], dfg::EdwardsPoint>,
    key_image: dfg::EdwardsPoint,
    initialized: bool,
    challenge: Option<dfg::Scalar>,
}

impl KeyImageAlgorithm {
    fn new(output_key: EdwardsPoint) -> Self {
        Self {
            transcript: RecommendedTranscript::new(b"THORChain XMR Key Image"),
            output_key: dfg::EdwardsPoint(output_key),
            key_image_generator: dfg::EdwardsPoint(biased_hash_to_point(
                output_key.compress().to_bytes(),
            )),
            key_image_shares: HashMap::new(),
            key_image: dfg::EdwardsPoint::identity(),
            initialized: false,
            challenge: None,
        }
    }

    fn challenge_for(
        &self,
        output_key: dfg::EdwardsPoint,
        nonces: &[Vec<dfg::EdwardsPoint>],
        key_image: dfg::EdwardsPoint,
        msg: &[u8],
    ) -> dfg::Scalar {
        // The final DLEQ proof must be reproducible from public proof inputs
        // alone. Do not inherit mutable transcript state from addendum
        // processing; different peers can process addenda in different orders.
        let mut transcript = RecommendedTranscript::new(b"THORChain XMR Key Image");
        transcript.domain_separate(b"key_image_dleq");
        transcript.append_message(b"xG", output_key.to_bytes());
        transcript.append_message(b"H", self.key_image_generator.to_bytes());
        transcript.append_message(b"xH", key_image.to_bytes());
        transcript.append_message(b"rG", nonces[0][0].to_bytes());
        transcript.append_message(b"rH", nonces[0][1].to_bytes());
        transcript.append_message(b"msg", msg);
        dfg::Scalar(Scalar::from_bytes_mod_order_wide(
            &transcript.challenge(b"c").into(),
        ))
    }
}

impl Algorithm<FrostEd25519> for KeyImageAlgorithm {
    type Transcript = RecommendedTranscript;
    type Addendum = KeyImageAddendum;
    type Signature = KeyImageSignature;

    fn transcript(&mut self) -> &mut Self::Transcript {
        &mut self.transcript
    }

    fn nonces(&self) -> Vec<Vec<dfg::EdwardsPoint>> {
        vec![vec![
            dfg::EdwardsPoint::generator(),
            self.key_image_generator,
        ]]
    }

    fn preprocess_addendum<R: RngCore + CryptoRng>(
        &mut self,
        _rng: &mut R,
        keys: &ThresholdKeys<FrostEd25519>,
    ) -> Self::Addendum {
        KeyImageAddendum {
            key_image_share: self.key_image_generator * keys.original_secret_share().deref(),
        }
    }

    fn read_addendum<R: std::io::Read>(&self, reader: &mut R) -> std::io::Result<Self::Addendum> {
        let mut bytes = [0u8; 32];
        reader.read_exact(&mut bytes)?;
        let point = Option::<dfg::EdwardsPoint>::from(dfg::EdwardsPoint::from_bytes(&bytes))
            .ok_or_else(|| std::io::Error::other("invalid key image share"))?;
        if point.to_bytes() != bytes {
            return Err(std::io::Error::other("non-canonical key image share"));
        }
        Ok(KeyImageAddendum {
            key_image_share: point,
        })
    }

    fn process_addendum(
        &mut self,
        view: &ThresholdView<FrostEd25519>,
        l: Participant,
        addendum: Self::Addendum,
    ) -> Result<(), FrostError> {
        if view.group_key().to_bytes() != self.output_key.to_bytes() {
            Err(FrostError::InternalError(
                "key image is being signed with a distinct key than intended",
            ))?;
        }

        if !self.initialized {
            self.transcript.domain_separate(b"key_image");
            self.transcript
                .append_message(b"output_key", self.output_key.to_bytes());
            self.initialized = true;
        }

        self.transcript.append_message(b"participant", l.to_bytes());
        self.transcript
            .append_message(b"key_image_share", addendum.key_image_share.to_bytes());

        let mut offset = dfg::Scalar::ZERO;
        if view.included().first().copied() == Some(l) {
            offset = view.offset();
        }

        let interpolated_share = (addendum.key_image_share
            * view
                .interpolation_factor(l)
                .ok_or(FrostError::InternalError(
                    "processing addendum for non-participant",
                ))?
            * view.scalar())
            + (self.key_image_generator * offset);

        self.key_image += interpolated_share;
        self.key_image_shares
            .insert(view.verification_share(l).to_bytes(), interpolated_share);
        Ok(())
    }

    fn sign_share(
        &mut self,
        view: &ThresholdView<FrostEd25519>,
        nonce_sums: &[Vec<dfg::EdwardsPoint>],
        mut nonces: Vec<Zeroizing<dfg::Scalar>>,
        msg: &[u8],
    ) -> dfg::Scalar {
        let challenge = self.challenge_for(view.group_key(), nonce_sums, self.key_image, msg);
        self.challenge = Some(challenge);
        let nonce = nonces.swap_remove(0);
        *nonce.deref() + (challenge * view.secret_share().deref())
    }

    fn verify(
        &self,
        group_key: dfg::EdwardsPoint,
        nonces: &[Vec<dfg::EdwardsPoint>],
        sum: dfg::Scalar,
    ) -> Option<Self::Signature> {
        let challenge = self.challenge?;
        if (dfg::EdwardsPoint::generator() * sum) != (nonces[0][0] + (group_key * challenge)) {
            return None;
        }
        if (self.key_image_generator * sum) != (nonces[0][1] + (self.key_image * challenge)) {
            return None;
        }

        Some(KeyImageSignature {
            key_image: self.key_image,
            challenge,
            response: sum,
        })
    }

    fn verify_share(
        &self,
        verification_share: dfg::EdwardsPoint,
        nonces: &[Vec<dfg::EdwardsPoint>],
        share: dfg::Scalar,
    ) -> Result<Vec<(dfg::Scalar, dfg::EdwardsPoint)>, ()> {
        let challenge = self.challenge.ok_or(())?;
        let key_image_share = *self
            .key_image_shares
            .get(&verification_share.to_bytes())
            .ok_or(())?;

        let mut weight_transcript =
            RecommendedTranscript::new(b"THORChain XMR KeyImage::verify_share");
        weight_transcript.append_message(b"G", dfg::EdwardsPoint::generator().to_bytes());
        weight_transcript.append_message(b"H", self.key_image_generator.to_bytes());
        weight_transcript.append_message(b"xG", verification_share.to_bytes());
        weight_transcript.append_message(b"xH", key_image_share.to_bytes());
        weight_transcript.append_message(b"rG", nonces[0][0].to_bytes());
        weight_transcript.append_message(b"rH", nonces[0][1].to_bytes());
        weight_transcript.append_message(b"c", challenge.to_repr());
        weight_transcript.append_message(b"s", share.to_repr());
        let weight = dfg::Scalar(Scalar::from_bytes_mod_order_wide(
            &weight_transcript.challenge(b"weight").into(),
        ));

        Ok(vec![
            (share, dfg::EdwardsPoint::generator()),
            (-dfg::Scalar::ONE, nonces[0][0]),
            (challenge, verification_share),
            (weight * share, self.key_image_generator),
            (-weight, nonces[0][1]),
            (weight * challenge, key_image_share),
        ])
    }
}

struct KeyImageSession {
    phase: KeyImagePhase,
    created_at: Instant,
    vault_pub_key: String,
    tx_hash: String,
    output_index: u32,
    msg_id: String,
    participant_count: u16,
    local_participant_index: u16,
    peer_participants: Vec<u16>,
    sign_machine: Option<AlgorithmSignMachine<FrostEd25519, KeyImageAlgorithm>>,
    completion_machine: Option<AlgorithmSignatureMachine<FrostEd25519, KeyImageAlgorithm>>,
}

pub struct KeyImageCoordinator {
    sessions: Mutex<HashMap<String, KeyImageSession>>,
    max_sessions: usize,
    store: Arc<Store>,
}

impl KeyImageCoordinator {
    pub fn new(store: Arc<Store>, max_sessions: usize) -> Self {
        Self {
            sessions: Mutex::new(HashMap::new()),
            max_sessions,
            store,
        }
    }

    pub fn session_exists(&self, session_id: &str) -> bool {
        self.sessions.lock().contains_key(session_id)
    }

    /// Drop all in-memory key-image sessions. Used when bifrost restarts so
    /// a fresh lifecycle never inherits stale half-finished ceremonies from a
    /// previous observer run.
    pub fn clear_all_sessions(&self) -> usize {
        let mut sessions = self.sessions.lock();
        let cleared = sessions.len();
        sessions.clear();
        cleared
    }

    pub fn cleanup_expired(&self) {
        let mut sessions = self.sessions.lock();
        sessions.retain(|_, session| session.created_at.elapsed() < KEY_IMAGE_SESSION_TTL);
    }

    fn resolve_group_key_for_vault(&self, vault_pub_key: &str) -> Result<String> {
        let vault = self
            .store
            .get_vault(vault_pub_key)
            .context("failed to load vault registry mapping")?
            .ok_or_else(|| anyhow::anyhow!("vault {vault_pub_key} is not registered"))?;
        Ok(vault.group_key)
    }

    fn tracked_output(
        &self,
        vault_pub_key: &str,
        tx_hash: &str,
        output_index: u32,
    ) -> Result<TrackedOutput> {
        self.store
            .load_outputs(vault_pub_key)
            .context("failed to load tracked outputs")?
            .into_iter()
            .find(|output| output.tx_hash == tx_hash && output.output_index == output_index)
            .ok_or_else(|| anyhow::anyhow!("tracked output {tx_hash}:{output_index} not found"))
    }

    fn load_wallet_output(
        &self,
        vault_pub_key: &str,
        tx_hash: &str,
        output_index: u32,
    ) -> Result<WalletOutput> {
        let tracked = self.tracked_output(vault_pub_key, tx_hash, output_index)?;
        let wallet_output_hex = tracked.wallet_output_hex.ok_or_else(|| {
            anyhow::anyhow!(
                "tracked output {tx_hash}:{output_index} is missing wallet output payload"
            )
        })?;
        let bytes = hex::decode(wallet_output_hex)
            .context("tracked output wallet payload is not valid hex")?;
        WalletOutput::read(&mut bytes.as_slice()).context("failed to decode wallet output payload")
    }

    pub fn prepare(
        &self,
        vault_pub_key: &str,
        tx_hash: &str,
        output_index: u32,
        _msg_id: &str,
    ) -> Result<(String, Vec<u8>)> {
        self.cleanup_expired();

        let wallet_output = self.load_wallet_output(vault_pub_key, tx_hash, output_index)?;
        let group_key = self.resolve_group_key_for_vault(vault_pub_key)?;
        let key_bytes = self
            .store
            .load_frost_keys(&group_key)
            .context("failed to load FROST keys from store")?
            .ok_or_else(|| anyhow::anyhow!("no FROST keys found for vault {vault_pub_key}"))?;
        let threshold_keys = ThresholdKeys::<FrostEd25519>::read(&mut key_bytes.as_slice())
            .context("failed to deserialize threshold keys")?
            .offset(dfg::Scalar(wallet_output.key_offset()));
        if threshold_keys.group_key().0 != wallet_output.key() {
            anyhow::bail!("tracked output is not owned by the loaded threshold keys");
        }

        let participant_count = threshold_keys.params().n();
        let local_participant_index = u16::from(threshold_keys.params().i());
        let session_id = uuid::Uuid::new_v4().to_string();
        let algorithm = KeyImageAlgorithm::new(wallet_output.key());
        let machine = AlgorithmMachine::new(algorithm, threshold_keys);
        let (sign_machine, preprocess) = machine.preprocess(&mut OsRng);

        let mut sessions = self.sessions.lock();
        if sessions.len() >= self.max_sessions {
            anyhow::bail!("too many concurrent key image sessions");
        }
        sessions.insert(
            session_id.clone(),
            KeyImageSession {
                phase: KeyImagePhase::Preprocessed,
                created_at: Instant::now(),
                vault_pub_key: vault_pub_key.to_string(),
                tx_hash: tx_hash.to_string(),
                output_index,
                msg_id: key_image_message(vault_pub_key, tx_hash, output_index),
                participant_count,
                local_participant_index,
                peer_participants: Vec::new(),
                sign_machine: Some(sign_machine),
                completion_machine: None,
            },
        );

        Ok((session_id, preprocess.serialize()))
    }

    pub fn partial_sign(
        &self,
        session_id: &str,
        all_preprocesses: HashMap<u16, Vec<u8>>,
    ) -> Result<Vec<u8>> {
        let (sign_machine, parsed, peer_participants, msg_id) = {
            let mut sessions = self.sessions.lock();
            let session = sessions
                .get_mut(session_id)
                .ok_or_else(|| anyhow::anyhow!("key image session {session_id} not found"))?;
            if session.phase != KeyImagePhase::Preprocessed {
                anyhow::bail!("key image session {session_id} is not ready for partial sign");
            }

            let sign_machine_ref = session.sign_machine.as_ref().ok_or_else(|| {
                anyhow::anyhow!("key image session {session_id} missing sign machine")
            })?;
            let all_preprocesses = normalize_participant_payloads(
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
                        anyhow::anyhow!("failed to parse preprocess from participant {idx}: {e}")
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
                session.msg_id.clone(),
            )
        };

        let (completion_machine, share) = sign_machine
            .sign(parsed, msg_id.as_bytes())
            .map_err(|e| anyhow::anyhow!("failed to produce key image partial signature: {e}"))?;

        let mut share_bytes = Vec::new();
        share
            .write(&mut share_bytes)
            .context("failed to serialize key image signature share")?;

        let mut sessions = self.sessions.lock();
        let session = sessions
            .get_mut(session_id)
            .ok_or_else(|| anyhow::anyhow!("key image session {session_id} disappeared"))?;
        session.completion_machine = Some(completion_machine);
        session.peer_participants = peer_participants;
        session.phase = KeyImagePhase::PartialSigned;
        Ok(share_bytes)
    }

    pub fn combine(
        &self,
        session_id: &str,
        all_shares: HashMap<u16, Vec<u8>>,
    ) -> Result<(String, String, String, String, u32)> {
        let (completion_machine, parsed, vault_pub_key, tx_hash, output_index) = {
            let mut sessions = self.sessions.lock();
            let session = sessions
                .get_mut(session_id)
                .ok_or_else(|| anyhow::anyhow!("key image session {session_id} not found"))?;
            if session.phase != KeyImagePhase::PartialSigned {
                anyhow::bail!("key image session {session_id} is not ready for combine");
            }

            let completion_machine_ref = session.completion_machine.as_ref().ok_or_else(|| {
                anyhow::anyhow!("key image session {session_id} missing completion machine")
            })?;
            let all_shares =
                normalize_expected_peer_payloads(&session.peer_participants, all_shares);

            let mut parsed = HashMap::with_capacity(all_shares.len());
            for (idx, bytes) in all_shares {
                let participant = Participant::new(idx)
                    .ok_or_else(|| anyhow::anyhow!("invalid participant index {idx}"))?;
                let share = completion_machine_ref
                    .read_share(&mut bytes.as_slice())
                    .map_err(|e| {
                        anyhow::anyhow!("failed to parse share from participant {idx}: {e}")
                    })?;
                parsed.insert(participant, share);
            }

            (
                session.completion_machine.take().unwrap(),
                parsed,
                session.vault_pub_key.clone(),
                session.tx_hash.clone(),
                session.output_index,
            )
        };

        let signature = completion_machine
            .complete(parsed)
            .map_err(|e| anyhow::anyhow!("failed to combine key image shares: {e}"))?;

        let key_image_hex = hex::encode(signature.key_image.to_bytes());
        OutputLifecycle::new(self.store.as_ref())
            .apply_key_images(
                &vault_pub_key,
                &[(tx_hash.clone(), output_index, key_image_hex.clone())],
                false,
            )
            .context("failed to persist combined key image")?;

        self.sessions.lock().remove(session_id);
        Ok((
            tx_hash,
            key_image_hex,
            hex::encode(signature.challenge.to_repr()),
            hex::encode(signature.response.to_repr()),
            output_index,
        ))
    }

    pub fn validate(
        &self,
        vault_pub_key: &str,
        tx_hash: &str,
        output_index: u32,
        key_image_hex: &str,
        proof_c_hex: &str,
        proof_s_hex: &str,
    ) -> Result<()> {
        let wallet_output = self.load_wallet_output(vault_pub_key, tx_hash, output_index)?;

        let key_image_bytes: [u8; 32] = hex::decode(key_image_hex)
            .context("key image is not valid hex")?
            .try_into()
            .map_err(|_| anyhow::anyhow!("key image must be 32 bytes"))?;
        let proof_c_bytes: [u8; 32] = hex::decode(proof_c_hex)
            .context("proof_c is not valid hex")?
            .try_into()
            .map_err(|_| anyhow::anyhow!("proof_c must be 32 bytes"))?;
        let proof_s_bytes: [u8; 32] = hex::decode(proof_s_hex)
            .context("proof_s is not valid hex")?
            .try_into()
            .map_err(|_| anyhow::anyhow!("proof_s must be 32 bytes"))?;

        let key_image =
            Option::<dfg::EdwardsPoint>::from(dfg::EdwardsPoint::from_bytes(&key_image_bytes))
                .ok_or_else(|| anyhow::anyhow!("key image is not a valid Edwards point"))?;
        if key_image.to_bytes() != key_image_bytes {
            anyhow::bail!("key image must be canonical");
        }

        let challenge = dfg::Scalar(
            Scalar::from_canonical_bytes(proof_c_bytes)
                .into_option()
                .ok_or_else(|| anyhow::anyhow!("proof_c is not a canonical scalar"))?,
        );
        let response = dfg::Scalar(
            Scalar::from_canonical_bytes(proof_s_bytes)
                .into_option()
                .ok_or_else(|| anyhow::anyhow!("proof_s is not a canonical scalar"))?,
        );

        let output_key = dfg::EdwardsPoint(wallet_output.key());
        let generator = dfg::EdwardsPoint(biased_hash_to_point(
            wallet_output.key().compress().to_bytes(),
        ));
        let r_g = (dfg::EdwardsPoint::generator() * response) - (output_key * challenge);
        let r_h = (generator * response) - (key_image * challenge);

        let algorithm = KeyImageAlgorithm::new(wallet_output.key());
        let derived = algorithm.challenge_for(
            output_key,
            &[vec![r_g, r_h]],
            key_image,
            key_image_message(vault_pub_key, tx_hash, output_index).as_bytes(),
        );
        if derived != challenge {
            anyhow::bail!("key image proof challenge mismatch");
        }

        Ok(())
    }
}

fn normalize_participant_payloads(
    participant_count: u16,
    local_participant_index: u16,
    payloads: HashMap<u16, Vec<u8>>,
) -> Result<HashMap<u16, Vec<u8>>> {
    super::payloads::normalize_participant_payloads(
        "key image",
        None,
        participant_count,
        local_participant_index,
        payloads,
    )
}

fn normalize_expected_peer_payloads(
    expected_peers: &[u16],
    payloads: HashMap<u16, Vec<u8>>,
) -> HashMap<u16, Vec<u8>> {
    super::payloads::normalize_expected_peer_payloads(None, expected_peers, payloads)
}

fn key_image_message(vault_pub_key: &str, tx_hash: &str, output_index: u32) -> String {
    format!("key-image|{vault_pub_key}|{tx_hash}|{output_index}")
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn challenge_for_is_stable_across_transcript_mutation() {
        let secret = dfg::Scalar(Scalar::from(7u64));
        let nonce = dfg::Scalar(Scalar::from(11u64));
        let output_key = dfg::EdwardsPoint::generator() * secret;
        let key_image_generator = dfg::EdwardsPoint(biased_hash_to_point(output_key.to_bytes()));
        let key_image = key_image_generator * secret;
        let r_g = dfg::EdwardsPoint::generator() * nonce;
        let r_h = key_image_generator * nonce;
        let msg = b"key-image|vault|tx|0";

        let clean = KeyImageAlgorithm::new(output_key.0);
        let expected = clean.challenge_for(output_key, &[vec![r_g, r_h]], key_image, msg);

        let mut mutated = KeyImageAlgorithm::new(output_key.0);
        mutated.transcript.domain_separate(b"key_image");
        mutated
            .transcript
            .append_message(b"participant", Participant::new(2).unwrap().to_bytes());
        mutated
            .transcript
            .append_message(b"key_image_share", key_image.to_bytes());

        let actual = mutated.challenge_for(output_key, &[vec![r_g, r_h]], key_image, msg);
        assert_eq!(actual, expected);
    }

    #[test]
    fn validate_roundtrip_accepts_publicly_reproducible_challenge() {
        let secret = dfg::Scalar(Scalar::from(5u64));
        let nonce = dfg::Scalar(Scalar::from(9u64));
        let output_key = dfg::EdwardsPoint::generator() * secret;
        let key_image_generator = dfg::EdwardsPoint(biased_hash_to_point(output_key.to_bytes()));
        let key_image = key_image_generator * secret;
        let r_g = dfg::EdwardsPoint::generator() * nonce;
        let r_h = key_image_generator * nonce;
        let msg = b"key-image|vault|tx|1";

        let mut signer_view = KeyImageAlgorithm::new(output_key.0);
        signer_view.transcript.domain_separate(b"key_image");
        signer_view
            .transcript
            .append_message(b"participant", Participant::new(1).unwrap().to_bytes());
        signer_view
            .transcript
            .append_message(b"key_image_share", key_image.to_bytes());

        let challenge = signer_view.challenge_for(output_key, &[vec![r_g, r_h]], key_image, msg);
        let response = nonce + (challenge * secret);

        let verifier_view = KeyImageAlgorithm::new(output_key.0);
        let derived = verifier_view.challenge_for(
            output_key,
            &[vec![
                (dfg::EdwardsPoint::generator() * response) - (output_key * challenge),
                (key_image_generator * response) - (key_image * challenge),
            ]],
            key_image,
            msg,
        );

        assert_eq!(derived, challenge);
    }

    #[test]
    fn validate_roundtrip_rejects_forged_key_image() {
        let secret = dfg::Scalar(Scalar::from(5u64));
        let nonce = dfg::Scalar(Scalar::from(9u64));
        let output_key = dfg::EdwardsPoint::generator() * secret;
        let key_image_generator = dfg::EdwardsPoint(biased_hash_to_point(output_key.to_bytes()));
        let key_image = key_image_generator * secret;
        let r_g = dfg::EdwardsPoint::generator() * nonce;
        let r_h = key_image_generator * nonce;
        let msg = b"key-image|vault|tx|1";

        let mut signer_view = KeyImageAlgorithm::new(output_key.0);
        signer_view.transcript.domain_separate(b"key_image");
        signer_view
            .transcript
            .append_message(b"participant", Participant::new(1).unwrap().to_bytes());
        signer_view
            .transcript
            .append_message(b"key_image_share", key_image.to_bytes());

        let challenge = signer_view.challenge_for(output_key, &[vec![r_g, r_h]], key_image, msg);
        let response = nonce + (challenge * secret);
        let forged_key_image = key_image + dfg::EdwardsPoint::generator();

        let verifier_view = KeyImageAlgorithm::new(output_key.0);
        let derived = verifier_view.challenge_for(
            output_key,
            &[vec![
                (dfg::EdwardsPoint::generator() * response) - (output_key * challenge),
                (key_image_generator * response) - (forged_key_image * challenge),
            ]],
            forged_key_image,
            msg,
        );

        assert_ne!(derived, challenge);
    }

    #[test]
    fn validate_roundtrip_rejects_forged_response() {
        let secret = dfg::Scalar(Scalar::from(5u64));
        let nonce = dfg::Scalar(Scalar::from(9u64));
        let output_key = dfg::EdwardsPoint::generator() * secret;
        let key_image_generator = dfg::EdwardsPoint(biased_hash_to_point(output_key.to_bytes()));
        let key_image = key_image_generator * secret;
        let r_g = dfg::EdwardsPoint::generator() * nonce;
        let r_h = key_image_generator * nonce;
        let msg = b"key-image|vault|tx|1";

        let mut signer_view = KeyImageAlgorithm::new(output_key.0);
        signer_view.transcript.domain_separate(b"key_image");
        signer_view
            .transcript
            .append_message(b"participant", Participant::new(1).unwrap().to_bytes());
        signer_view
            .transcript
            .append_message(b"key_image_share", key_image.to_bytes());

        let challenge = signer_view.challenge_for(output_key, &[vec![r_g, r_h]], key_image, msg);
        let response = nonce + (challenge * secret);
        let forged_response = response + dfg::Scalar(Scalar::ONE);

        let verifier_view = KeyImageAlgorithm::new(output_key.0);
        let derived = verifier_view.challenge_for(
            output_key,
            &[vec![
                (dfg::EdwardsPoint::generator() * forged_response) - (output_key * challenge),
                (key_image_generator * forged_response) - (key_image * challenge),
            ]],
            key_image,
            msg,
        );

        assert_ne!(derived, challenge);
    }
}
