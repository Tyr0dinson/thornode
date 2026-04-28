use std::collections::{HashMap, HashSet};
use std::sync::Arc;

use axum::{
    extract::{Path, Query, State},
    http::StatusCode,
    response::IntoResponse,
    Json,
};
use bech32::{decode as bech32_decode, encode as bech32_encode, FromBase32, ToBase32, Variant};
use monero_wallet::rpc::Rpc;
use monero_wallet::transaction::{Input, Pruned, Transaction};
use rand::rngs::OsRng;
use serde::de::DeserializeOwned;
use sha2::{Digest, Sha256};
use tracing::{error, info, warn};
use uuid::Uuid;

use super::outgoing_inventory::{reconcile_registered_tx_key_inventory, validate_outbound_request};
use super::types::*;
use crate::config::ChainNetworkMode;
use crate::frost::dkg::DkgCoordinator;
use crate::frost::key_image::KeyImageCoordinator;
use crate::frost::keys::ThresholdKeyShare;
use crate::frost::sign::SignCoordinator;
use crate::state::store::{
    IdempotencyClaimResult, OutgoingTransferRecord, OutgoingTransferStatus, OutputRef, OutputState,
    RegisteredKeyImageRef, ScanBlockMeta, TrackedOutput,
};
use crate::state::{
    ApplyKeyImagesOutcome, OutgoingLifecycle, OutgoingObservation, OutputLifecycle, VaultLifecycle,
    VaultSyncState,
};
use crate::tx::builder;
use crate::tx::daemon::DaemonClient;
use crate::tx::fee;
use crate::tx::scanner::Scanner;
use crate::xmr;

/// Shared application state passed to all route handlers.
pub struct AppState {
    pub dkg: DkgCoordinator,
    pub signer: SignCoordinator,
    pub key_images: KeyImageCoordinator,
    pub scanner: parking_lot::Mutex<Scanner>,
    pub scan_lifecycle: tokio::sync::Mutex<()>,
    pub daemon: DaemonClient,
    pub store: Arc<crate::state::Store>,
    pub chain_network: ChainNetworkMode,
    pub max_sign_scan_lag_blocks: u64,
    pub sim_helper_enabled: bool,
    pub sim_helper_wallet_rpc_url: Option<String>,
}

/// Helper to parse a HashMap<String, String> to HashMap<u16, Vec<u8>>.
fn parse_hex_map(input: &HashMap<String, String>) -> Result<HashMap<u16, Vec<u8>>, String> {
    input
        .iter()
        .map(|(k, v)| {
            let idx: u16 = k
                .parse()
                .map_err(|e| format!("invalid index {}: {}", k, e))?;
            let bytes = hex::decode(v).map_err(|e| format!("invalid hex for {}: {}", k, e))?;
            Ok((idx, bytes))
        })
        .collect()
}

#[derive(Debug)]
enum IdempotencyCheck {
    Fresh,
    Replay(StatusCode, serde_json::Value),
    Conflict,
    InProgress,
}

#[derive(Debug, Clone)]
struct IdempotencyPayloadHashes {
    canonical: String,
}

fn parse_status_code(code: u16) -> StatusCode {
    StatusCode::from_u16(code).unwrap_or(StatusCode::OK)
}

fn canonicalize_json_value(value: &serde_json::Value) -> serde_json::Value {
    match value {
        serde_json::Value::Array(items) => serde_json::Value::Array(
            items
                .iter()
                .map(canonicalize_json_value)
                .collect::<Vec<_>>(),
        ),
        serde_json::Value::Object(map) => {
            let mut entries = map
                .iter()
                .map(|(k, v)| (k.clone(), canonicalize_json_value(v)))
                .collect::<Vec<_>>();
            entries.sort_by(|a, b| a.0.cmp(&b.0));
            let mut canonical = serde_json::Map::new();
            for (k, v) in entries {
                canonical.insert(k, v);
            }
            serde_json::Value::Object(canonical)
        }
        _ => value.clone(),
    }
}

fn hash_json_canonical<T: serde::Serialize>(value: &T) -> Result<String, String> {
    let json =
        serde_json::to_value(value).map_err(|e| format!("failed to serialize payload: {e}"))?;
    let canonical = canonicalize_json_value(&json);
    let bytes =
        serde_json::to_vec(&canonical).map_err(|e| format!("failed to serialize payload: {e}"))?;
    let mut hasher = Sha256::new();
    hasher.update(bytes);
    Ok(hex::encode(hasher.finalize()))
}

fn idempotency_payload_hashes<T: serde::Serialize>(
    value: &T,
) -> Result<IdempotencyPayloadHashes, String> {
    let canonical = hash_json_canonical(value)?;
    Ok(IdempotencyPayloadHashes { canonical })
}

fn hash_value(value: &serde_json::Value) -> String {
    let bytes = serde_json::to_vec(value).unwrap_or_default();
    let mut hasher = Sha256::new();
    hasher.update(bytes);
    hex::encode(hasher.finalize())
}

fn check_idempotency(
    state: &AppState,
    route: &str,
    msg_id: &str,
    payload_hashes: &IdempotencyPayloadHashes,
) -> Result<IdempotencyCheck, String> {
    let now_ts = chrono::Utc::now().timestamp();
    match state
        .store
        .claim_idempotency(route, msg_id, &payload_hashes.canonical, now_ts)
        .map_err(|e| format!("failed to claim idempotency record: {e}"))?
    {
        IdempotencyClaimResult::Fresh => Ok(IdempotencyCheck::Fresh),
        IdempotencyClaimResult::Replay(record) => Ok(IdempotencyCheck::Replay(
            parse_status_code(record.status_code),
            record.response_json,
        )),
        IdempotencyClaimResult::Conflict => Ok(IdempotencyCheck::Conflict),
        IdempotencyClaimResult::InProgress => Ok(IdempotencyCheck::InProgress),
    }
}

fn persist_idempotency(
    state: &AppState,
    route: &str,
    msg_id: &str,
    payload_hash: &str,
    status: StatusCode,
    response_json: &serde_json::Value,
) {
    if status.is_server_error() {
        if let Err(e) = state
            .store
            .release_pending_idempotency(route, msg_id, payload_hash)
        {
            error!(
                route,
                msg_id,
                error = %e,
                "failed to release pending idempotency record"
            );
        }
        return;
    }
    if let Err(e) = state.store.complete_idempotency(
        route,
        msg_id,
        payload_hash,
        &hash_value(response_json),
        status.as_u16(),
        response_json,
    ) {
        error!(
            route,
            msg_id,
            error = %e,
            "failed to complete idempotency record"
        );
    }
}

type ApiError = (StatusCode, serde_json::Value);
type ApiResponse = (StatusCode, Json<serde_json::Value>);

fn json_value<T: serde::Serialize>(value: T) -> serde_json::Value {
    serde_json::to_value(value).unwrap()
}

fn json_response<T: serde::Serialize>(status: StatusCode, value: T) -> ApiResponse {
    (status, Json(json_value(value)))
}

fn ok_response<T: serde::Serialize>(value: T) -> ApiResponse {
    json_response(StatusCode::OK, value)
}

pub(crate) fn error_json(msg: impl Into<String>) -> serde_json::Value {
    json_value(ErrorResponse::new(msg.into()))
}

fn bad_request(msg: impl Into<String>) -> ApiError {
    (StatusCode::BAD_REQUEST, error_json(msg))
}

fn internal_error(msg: impl Into<String>) -> ApiError {
    (StatusCode::INTERNAL_SERVER_ERROR, error_json(msg))
}

fn error_response(status: StatusCode, msg: impl Into<String>) -> ApiResponse {
    (status, Json(error_json(msg)))
}

fn bad_request_response(msg: impl Into<String>) -> ApiResponse {
    error_response(StatusCode::BAD_REQUEST, msg)
}

fn internal_error_response(msg: impl Into<String>) -> ApiResponse {
    error_response(StatusCode::INTERNAL_SERVER_ERROR, msg)
}

fn required_msg_id(value: &str) -> Result<String, ApiError> {
    let msg_id = value.trim().to_string();
    if msg_id.is_empty() {
        return Err(bad_request("msg_id is required"));
    }
    Ok(msg_id)
}

fn is_capacity_error(message: &str) -> bool {
    message.to_ascii_lowercase().contains("capacity exceeded")
}

fn valid_vault_pub_key(value: &str) -> bool {
    let trimmed = value.trim();
    if trimmed.is_empty() {
        return false;
    }

    let Ok((hrp, data, _)) = bech32_decode(trimmed) else {
        return false;
    };
    if hrp != "thorpub" && hrp != "tthorpub" && hrp != "cthorpub" && hrp != "sthorpub" {
        return false;
    }
    !data.is_empty()
}

fn normalized_group_key_hex(value: &str) -> Result<String, ApiError> {
    let normalized = value.trim().to_ascii_lowercase();
    if normalized.len() != 64 {
        return Err(bad_request("group_key must be 32-byte hex"));
    }
    let decoded = match hex::decode(&normalized) {
        Ok(decoded) => decoded,
        Err(_) => {
            return Err(bad_request("group_key must be valid hex"));
        }
    };
    if decoded.len() != 32 {
        return Err(bad_request("group_key must be 32-byte hex"));
    }
    Ok(normalized)
}

const ED25519_PUBKEY_AMINO_PREFIX: &[u8] = &[0x16, 0x24, 0xde, 0x64, 0x20];

fn vault_pub_key_matches_group_key(vault_pub_key: &str, group_key: &str) -> Result<bool, ApiError> {
    let (_, data, _) = match bech32_decode(vault_pub_key.trim()) {
        Ok(decoded) => decoded,
        Err(_) => {
            return Err(bad_request("invalid vault_pub_key format"));
        }
    };

    let decoded =
        Vec::<u8>::from_base32(&data).map_err(|_| bad_request("invalid vault_pub_key format"))?;
    if decoded.len() != ED25519_PUBKEY_AMINO_PREFIX.len() + 32 {
        return Ok(false);
    }
    if !decoded.starts_with(ED25519_PUBKEY_AMINO_PREFIX) {
        return Ok(false);
    }

    let group_key =
        hex::decode(group_key).map_err(|_| bad_request("group_key must be valid hex"))?;
    Ok(decoded[ED25519_PUBKEY_AMINO_PREFIX.len()..] == group_key)
}

fn ensure_bootstrap_frost_key_material(
    state: &AppState,
    vault_pub_key: &str,
    group_key: &str,
    spend_key_hex: Option<&str>,
) -> Result<(), ApiError> {
    let Some(spend_key_hex) = spend_key_hex
        .map(str::trim)
        .filter(|value| !value.is_empty())
    else {
        return Ok(());
    };

    let derived_group_key = xmr::group_key_hex_from_spend_seed_hex(spend_key_hex)
        .map_err(|e| bad_request(format!("invalid spend_key_hex: {e}")))?;
    if derived_group_key != group_key {
        return Err(bad_request("spend_key_hex does not match group_key"));
    }
    if !vault_pub_key_matches_group_key(vault_pub_key, group_key)? {
        return Err(bad_request("vault_pub_key does not match group_key"));
    }

    let existing = state
        .store
        .load_frost_keys(group_key)
        .map_err(|e| internal_error(format!("failed to load frost key material: {e}")))?;
    if existing.is_some() {
        return Ok(());
    }

    let key_share = ThresholdKeyShare::from_spend_seed_hex(spend_key_hex)
        .map_err(|e| bad_request(format!("failed to derive frost key material: {e}")))?;
    state
        .store
        .save_frost_keys(group_key, &key_share.to_bytes())
        .map_err(|e| internal_error(format!("failed to persist frost key material: {e}")))
}

fn ensure_registered_vault_state(
    state: &AppState,
    vault_pub_key: &str,
    group_key: &str,
    address: &str,
    source: &str,
    birthday: Option<u64>,
) -> Result<bool, String> {
    let mut scanner = state.scanner.lock();
    let current_height = scanner.current_height();
    VaultLifecycle::new(state.store.as_ref())
        .ensure_registered(
            &mut scanner,
            state.chain_network,
            vault_pub_key,
            group_key,
            address,
            source,
            birthday,
            current_height,
        )
        .map_err(|e| format!("failed to ensure registered vault state: {e}"))
}

/// One block's worth of scan output bundled for batched commit.
///
/// `spent_key_images` are raw key-images observed in the block's transaction inputs,
/// not yet matched against any output. `added_outputs` are the vault outputs newly
/// discovered by the wallet scanner at `height`.
struct ScannedBlockEntry {
    height: u64,
    block_hash: String,
    parent_hash: String,
    spent_key_images: Vec<String>,
    spent_key_image_txs: HashMap<String, String>,
    added_outputs: Vec<TrackedOutput>,
}

struct CommitBatchResult {
    added_refs_per_block: Vec<Vec<OutputRef>>,
    outputs_after_scan: Vec<TrackedOutput>,
}

fn parse_pruned_transaction_hex(tx_hex: &str) -> Result<Transaction<Pruned>, String> {
    let tx_hex = tx_hex.trim();
    if tx_hex.is_empty() {
        return Err("tx_hex must not be empty".to_string());
    }
    let bytes = hex::decode(tx_hex).map_err(|e| format!("tx_hex is not valid hex: {e}"))?;
    Transaction::<Pruned>::read(&mut bytes.as_slice())
        .map_err(|e| format!("failed to decode pruned transaction: {e}"))
}

fn collect_spent_key_images_by_tx(
    transactions: &[Transaction<Pruned>],
    tx_hashes: &[String],
) -> HashMap<String, String> {
    let mut out = HashMap::new();
    for (idx, tx) in transactions.iter().enumerate() {
        let tx_hash = tx_hashes
            .get(idx)
            .map(|hash| normalize_tx_hash(hash))
            .unwrap_or_default();
        for input in &tx.prefix().inputs {
            if let Input::ToKey { key_image, .. } = input {
                let key_image = hex::encode(key_image.compress().to_bytes()).to_ascii_lowercase();
                out.entry(key_image).or_insert_with(|| tx_hash.clone());
            }
        }
    }
    out
}

fn load_registered_key_image_inventory(
    state: &Arc<AppState>,
    vault_pub_key: &str,
) -> Result<Vec<(String, u32, String)>, String> {
    state
        .store
        .load_registered_key_images(vault_pub_key)
        .map_err(|e| format!("failed to load registered key image inventory: {e}"))
        .map(|refs| {
            refs.into_iter()
                .map(|entry| (entry.tx_hash, entry.output_index, entry.key_image))
                .collect()
        })
}

fn apply_registered_key_image_inventory(
    state: &Arc<AppState>,
    vault_pub_key: &str,
    reconcile: bool,
) -> Result<(), String> {
    let key_images = load_registered_key_image_inventory(state, vault_pub_key)?;
    {
        let mut scanner = state.scanner.lock();
        let _ = scanner.apply_key_images(vault_pub_key, &key_images, reconcile);
    }
    OutputLifecycle::new(state.store.as_ref())
        .apply_key_images(vault_pub_key, &key_images, reconcile)
        .map_err(|e| format!("failed to apply registered key image inventory: {e}"))?;

    let outputs_after_apply = state.store.load_outputs(vault_pub_key).map_err(|e| {
        format!("failed to reload outputs after applying registered key images: {e}")
    })?;
    {
        let mut scanner = state.scanner.lock();
        scanner.merge_output_states(vault_pub_key, &outputs_after_apply);
    }

    if reconcile {
        replay_reconciled_key_image_spends(state, vault_pub_key, &key_images)?;
        let outputs_after_replay = state.store.load_outputs(vault_pub_key).map_err(|e| {
            format!("failed to reload outputs after replaying registered key images: {e}")
        })?;
        let mut scanner = state.scanner.lock();
        scanner.merge_output_states(vault_pub_key, &outputs_after_replay);
    }

    Ok(())
}

/// Unified scan-batch commit. One operation for N blocks (N=1 for normal scan,
/// up to RESCAN_MAX_BLOCKS_PER_SYNC for rescan). Preserves the distinction between
/// LOADING a key-image (attaching metadata to a tracked output) and MARKING a
/// key-image as SPENT (which requires observing the key-image in a block's
/// transaction input).
///
///   Phase 1 (per block): persist outputs + ScanBlockMeta (recording observed
///     spent_key_images, leaving spent_by_key_images empty) + scan_height.
///     No spend marking: a newly-scanned output may not yet have its key_image
///     bound from registered inventory.
///
///   Phase 2 (per batch, LOAD): apply registered key-image inventory with
///     reconcile=true — binds key_image metadata to tracked outputs and heals
///     legacy state. This NEVER marks an output as spent.
///
///   Phase 3 (per batch, MARK SPENT): replay persisted ScanBlockMeta against
///     the freshly-loaded key_images — for every historical block whose
///     spent_key_images intersect tracked outputs, mark those outputs Spent at
///     the observing block's height and backfill meta.spent_by_key_images.
///     Folded into apply_registered_key_image_inventory via reconcile=true.
///
///   Phase 4: prune scan block metadata beyond the reorg window.
fn commit_scan_batch(
    state: &Arc<AppState>,
    vault_pub_key: &str,
    blocks: &[ScannedBlockEntry],
    reorg_window: u64,
) -> Result<CommitBatchResult, String> {
    let output_lifecycle = OutputLifecycle::new(state.store.as_ref());
    let vault_lifecycle = VaultLifecycle::new(state.store.as_ref());
    let mut added_refs_per_block = Vec::with_capacity(blocks.len());

    for entry in blocks {
        let mut spent_key_images: Vec<String> = entry
            .spent_key_images
            .iter()
            .map(|key_image| key_image.trim().to_ascii_lowercase())
            .filter(|key_image| !key_image.is_empty())
            .collect();
        spent_key_images.sort();
        spent_key_images.dedup();

        let outputs_after_scan = state.scanner.lock().get_all_outputs(vault_pub_key);
        output_lifecycle
            .merge_scanned_outputs(vault_pub_key, &outputs_after_scan)
            .map_err(|e| format!("failed to persist outputs after scanner sync: {e}"))?;

        let added_refs: Vec<OutputRef> = entry
            .added_outputs
            .iter()
            .map(|output| OutputRef {
                tx_hash: output.tx_hash.clone(),
                output_index: output.output_index,
            })
            .collect();
        output_lifecycle
            .record_scan_block(
                vault_pub_key,
                &ScanBlockMeta {
                    height: entry.height,
                    block_hash: normalize_tx_hash(&entry.block_hash),
                    parent_hash: normalize_tx_hash(&entry.parent_hash),
                    added_outputs: added_refs.clone(),
                    spent_key_images,
                    spent_by_key_images: Vec::new(),
                },
            )
            .map_err(|e| {
                format!("failed to persist scan block metadata after scanner sync: {e}")
            })?;
        vault_lifecycle
            .advance_scan_height(vault_pub_key, entry.height)
            .map_err(|e| format!("failed to persist scan height after scanner sync: {e}"))?;

        added_refs_per_block.push(added_refs);
    }

    apply_registered_key_image_inventory(state, vault_pub_key, true)?;

    let top_height = blocks.last().map(|entry| entry.height).unwrap_or(0);
    let min_height = top_height.saturating_sub(reorg_window.saturating_sub(1));
    if let Err(e) = output_lifecycle.prune_scan_blocks_before(vault_pub_key, min_height) {
        warn!(
            error = %e,
            vault = %vault_pub_key,
            min_height,
            "failed to prune scan block metadata after scanner sync"
        );
    }

    let outputs_after_scan = state
        .store
        .load_outputs(vault_pub_key)
        .map_err(|e| format!("failed to reload outputs after commit_scan_batch: {e}"))?;

    Ok(CommitBatchResult {
        added_refs_per_block,
        outputs_after_scan,
    })
}

fn replay_reconciled_key_image_spends(
    state: &Arc<AppState>,
    vault_pub_key: &str,
    key_images: &[(String, u32, String)],
) -> Result<(), String> {
    OutputLifecycle::new(state.store.as_ref())
        .replay_reconciled_spends(vault_pub_key, key_images)
        .map_err(|e| format!("failed to replay reconciled key-image spends: {e}"))
}

fn unauthorized_spend_entries(
    state: &Arc<AppState>,
    vault_pub_key: &str,
    vault_address: &str,
    blocks: &[ScannedBlockEntry],
    outputs_after_scan: &[TrackedOutput],
    current_height: u64,
) -> Vec<TransferEntry> {
    if blocks.is_empty() || outputs_after_scan.is_empty() {
        return Vec::new();
    }

    let authorized_tx_hashes: HashSet<String> = state
        .store
        .load_outgoing_transfers(vault_pub_key)
        .unwrap_or_default()
        .into_iter()
        .map(|tx| normalize_tx_hash(&tx.tx_hash))
        .filter(|tx_hash| !tx_hash.is_empty())
        .collect();

    let mut spend_tx_by_height_and_key_image = HashMap::new();
    for block in blocks {
        for (key_image, spend_tx) in &block.spent_key_image_txs {
            spend_tx_by_height_and_key_image.insert(
                (block.height, key_image.trim().to_ascii_lowercase()),
                normalize_tx_hash(spend_tx),
            );
        }
    }

    let mut emitted = HashSet::new();
    let mut entries = Vec::new();
    for output in outputs_after_scan {
        let Some(spent_height) = output.spent_by_key_image_height else {
            continue;
        };
        let Some(key_image) = output.key_image.as_ref() else {
            continue;
        };
        let Some(spend_tx) = spend_tx_by_height_and_key_image
            .get(&(spent_height, key_image.trim().to_ascii_lowercase()))
            .cloned()
        else {
            continue;
        };
        if authorized_tx_hashes.contains(&spend_tx) || output.amount == 0 {
            continue;
        }

        let output_key = (
            spend_tx.clone(),
            output.tx_hash.clone(),
            output.output_index,
        );
        if !emitted.insert(output_key) {
            continue;
        }

        warn!(
            vault = %vault_pub_key,
            spend_tx = %spend_tx,
            output_tx = %output.tx_hash,
            output_index = output.output_index,
            amount = output.amount,
            spent_height,
            "detected unauthorized XMR spend of monitored vault output"
        );

        entries.push(TransferEntry {
            txid: spend_tx.clone(),
            address: xmr_unknown_address(&spend_tx, vault_address),
            from_address: Some(vault_address.to_string()),
            vault_pub_key: Some(vault_pub_key.to_string()),
            tx_keys: Vec::new(),
            amount: output.amount,
            fee: 0,
            height: spent_height,
            confirmations: current_height.saturating_sub(spent_height),
            note: "NOOP:UNAUTHORIZED".to_string(),
            locked: current_height.saturating_sub(spent_height) < 10,
        });
    }
    entries
}

fn xmr_unknown_address(tx_id: &str, to: &str) -> String {
    let mut hasher = Sha256::new();
    hasher.update(tx_id.trim().as_bytes());
    hasher.update(b"|");
    hasher.update(to.trim().as_bytes());
    let seed = hasher.finalize();
    bech32_encode(
        "xmrunknown",
        seed[..20].to_vec().to_base32(),
        Variant::Bech32,
    )
    .unwrap_or_else(|_| "xmrunknown1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq3b9l4f".to_string())
}

// ---- DKG Routes ----

pub async fn dkg_round1(
    State(state): State<Arc<AppState>>,
    Json(req): Json<DkgRound1Request>,
) -> impl IntoResponse {
    if !valid_vault_pub_key(&req.vault_pub_key) {
        return bad_request_response("invalid vault_pub_key format");
    }

    match state.dkg.round1(
        &req.session_id,
        &req.vault_pub_key,
        req.threshold,
        req.n,
        req.participant_index,
        &req.context,
    ) {
        Ok(commitments) => ok_response(DkgRound1Response {
            commitments: hex::encode(commitments),
        }),
        Err(e) => {
            error!(error = %e, "DKG round 1 failed");
            let msg = e.to_string();
            let status = if is_capacity_error(&msg) {
                StatusCode::SERVICE_UNAVAILABLE
            } else {
                StatusCode::BAD_REQUEST
            };
            error_response(status, msg)
        }
    }
}

pub async fn dkg_round2(
    State(state): State<Arc<AppState>>,
    Json(req): Json<DkgRound2Request>,
) -> impl IntoResponse {
    let all_commitments = match parse_hex_map(&req.all_commitments) {
        Ok(c) => c,
        Err(e) => return bad_request_response(e),
    };

    match state.dkg.round2(&req.session_id, all_commitments) {
        Ok(shares) => {
            let hex_shares: HashMap<String, String> = shares
                .into_iter()
                .map(|(k, v)| (k.to_string(), hex::encode(v)))
                .collect();
            ok_response(DkgRound2Response {
                encrypted_shares: hex_shares,
            })
        }
        Err(e) => {
            error!(error = %e, "DKG round 2 failed");
            bad_request_response(e.to_string())
        }
    }
}

pub async fn dkg_round3(
    State(state): State<Arc<AppState>>,
    Json(req): Json<DkgRound3Request>,
) -> impl IntoResponse {
    let all_shares = match parse_hex_map(&req.all_shares) {
        Ok(s) => s,
        Err(e) => return bad_request_response(e),
    };

    match state.dkg.round3(&req.session_id, all_shares) {
        Ok((address, group_key)) => ok_response(DkgRound3Response { address, group_key }),
        Err(e) => {
            error!(error = %e, "DKG round 3 failed");
            bad_request_response(e.to_string())
        }
    }
}

pub async fn dkg_finalize(
    State(state): State<Arc<AppState>>,
    Json(req): Json<DkgFinalizeRequest>,
) -> impl IntoResponse {
    match state
        .dkg
        .finalize(&req.session_id, &req.group_key, &req.address)
    {
        Ok(key_share_bytes) => ok_response(DkgFinalizeResponse {
            success: true,
            key_bytes_hex: hex::encode(&key_share_bytes),
        }),
        Err(e) => {
            error!(error = %e, "DKG finalize failed");
            bad_request_response(e.to_string())
        }
    }
}

pub async fn register_vault(
    State(state): State<Arc<AppState>>,
    Json(req): Json<RegisterVaultRequest>,
) -> impl IntoResponse {
    let vault_pub_key = req.vault_pub_key.trim().to_string();
    if !valid_vault_pub_key(&vault_pub_key) {
        return bad_request_response("invalid vault_pub_key format");
    }

    let group_key = match normalized_group_key_hex(&req.group_key) {
        Ok(group_key) => group_key,
        Err((status, response)) => return (status, Json(response)),
    };
    match vault_pub_key_matches_group_key(&vault_pub_key, &group_key) {
        Ok(true) => {}
        Ok(false) => {
            return bad_request_response("vault_pub_key does not match group_key");
        }
        Err((status, response)) => return (status, Json(response)),
    }

    let expected_address = match xmr::derive_vault_address(&group_key, state.chain_network) {
        Ok(address) => address,
        Err(e) => {
            return bad_request_response(format!(
                "failed to derive vault address from group_key: {e}"
            ));
        }
    };

    let provided_address = req.address.trim();
    if provided_address.is_empty() {
        return bad_request_response("address is required");
    }
    if provided_address != expected_address {
        return bad_request_response(
            "address does not match the derived vault address for group_key",
        );
    }

    let source = req
        .source
        .as_deref()
        .map(str::trim)
        .filter(|source| !source.is_empty())
        .unwrap_or("bifrost");
    let _scan_lifecycle = state.scan_lifecycle.lock().await;

    if let Err((status, response)) = ensure_bootstrap_frost_key_material(
        &state,
        &vault_pub_key,
        &group_key,
        req.spend_key_hex.as_deref(),
    ) {
        return (status, Json(response));
    }

    let registered = match ensure_registered_vault_state(
        &state,
        &vault_pub_key,
        &group_key,
        &expected_address,
        source,
        req.birthday,
    ) {
        Ok(registered) => registered,
        Err(e) => {
            error!(
                error = %e,
                vault = %vault_pub_key,
                "runtime vault registration failed"
            );
            return internal_error_response(e);
        }
    };

    // Plain vault registration calls (balance/account/consolidation paths) do
    // not carry inventory. Treat omitted inventory as "preserve existing"
    // rather than "clear and reconcile", otherwise a generic re-registration
    // can wipe previously synchronized key-images and tx-keys.
    if let Some(key_images) = req.key_images {
        let key_images: Vec<RegisteredKeyImageRef> = key_images
            .into_iter()
            .filter_map(|entry| {
                let tx_hash = normalize_tx_hash(&entry.tx_hash);
                let key_image = entry.key_image.trim().to_ascii_lowercase();
                if tx_hash.is_empty() || key_image.is_empty() {
                    return None;
                }
                Some(RegisteredKeyImageRef {
                    tx_hash,
                    output_index: entry.output_index,
                    key_image,
                })
            })
            .collect();
        if let Err(e) =
            VaultLifecycle::new(state.store.as_ref()).apply_inventory(&vault_pub_key, &key_images)
        {
            error!(
                error = %e,
                vault = %vault_pub_key,
                "failed to persist registered key image inventory"
            );
            return internal_error_response(format!(
                "failed to persist registered key image inventory: {e}"
            ));
        }
        if let Err(e) = apply_registered_key_image_inventory(&state, &vault_pub_key, true) {
            error!(
                error = %e,
                vault = %vault_pub_key,
                "failed to reconcile registered key image inventory"
            );
            return internal_error_response(format!(
                "failed to reconcile registered key image inventory: {e}"
            ));
        }
    }
    if let Some(tx_keys) = req.tx_keys {
        if let Err((status, response)) =
            reconcile_registered_tx_key_inventory(&state, &vault_pub_key, tx_keys).await
        {
            error!(
                vault = %vault_pub_key,
                status = %status,
                "failed to reconcile registered tx-key inventory"
            );
            return (status, Json(response));
        }
    }

    ok_response(RegisterVaultResponse { registered })
}

// ---- Signing Routes ----

pub async fn sign_prepare(
    State(state): State<Arc<AppState>>,
    Json(req): Json<SignPrepareRequest>,
) -> impl IntoResponse {
    let msg_id = match required_msg_id(&req.msg_id) {
        Ok(msg_id) => msg_id,
        Err((status, response)) => return (status, Json(response)),
    };
    let vault_pub_key_canonical = req.vault_pub_key.trim().to_string();
    if !valid_vault_pub_key(&vault_pub_key_canonical) {
        return bad_request_response("invalid vault_pub_key format");
    }
    let mut canonical_req = req.clone();
    canonical_req.msg_id = msg_id.clone();
    canonical_req.vault_pub_key = vault_pub_key_canonical.clone();
    let payload_hashes = match idempotency_payload_hashes(&canonical_req) {
        Ok(hashes) => hashes,
        Err(e) => return internal_error_response(e),
    };
    match check_idempotency(&state, "sign_prepare", &msg_id, &payload_hashes) {
        Ok(IdempotencyCheck::Replay(status, response)) => {
            // Verify the cached session_id still exists in memory. If the
            // sidecar restarted, the in-memory session is gone but the
            // idempotency cache survives in sled. Detect this and clear
            // the stale cache so a fresh session can be created.
            let cached_session_id = response
                .get("session_id")
                .and_then(|v| v.as_str())
                .map(|s| s.to_string());
            let session_valid = cached_session_id
                .as_ref()
                .map(|sid| state.signer.session_exists(sid))
                .unwrap_or(true); // No session_id in response (e.g. error) — return as-is.
            if session_valid {
                return (status, Json(response));
            }
            info!(
                msg_id,
                cached_session_id = ?cached_session_id,
                "sign_prepare cached session not found in memory, clearing stale idempotency"
            );
            if let Err(e) = state.store.clear_idempotency("sign_prepare", &msg_id) {
                error!(msg_id, error = %e, "failed to clear stale sign_prepare idempotency record");
                return internal_error_response(format!("failed to clear stale idempotency: {e}"));
            }
        }
        Ok(IdempotencyCheck::Conflict) => {
            info!(
                msg_id,
                "sign_prepare idempotency conflict: superseding stale record for retry"
            );
            if let Err(e) = state.store.clear_idempotency("sign_prepare", &msg_id) {
                error!(msg_id, error = %e, "failed to clear stale idempotency record");
                return internal_error_response(format!("failed to clear stale idempotency: {e}"));
            }
            match check_idempotency(&state, "sign_prepare", &msg_id, &payload_hashes) {
                Ok(IdempotencyCheck::Fresh) => {}
                other => {
                    error!(msg_id, result = ?other, "unexpected idempotency state after clear");
                    return internal_error_response("idempotency reset failed");
                }
            }
        }
        Ok(IdempotencyCheck::InProgress) => {
            return error_response(
                StatusCode::CONFLICT,
                "idempotency request already in progress for msg_id",
            );
        }
        Ok(IdempotencyCheck::Fresh) => {}
        Err(e) => return internal_error_response(e),
    }

    let tip_height = match state.daemon.get_height().await {
        Ok(height) => height,
        Err(e) => {
            error!(error = %e, "failed to fetch daemon height for sign staleness check");
            let response = error_json(format!(
                "unable to verify scanner freshness before signing: {e}"
            ));
            persist_idempotency(
                &state,
                "sign_prepare",
                &msg_id,
                &payload_hashes.canonical,
                StatusCode::SERVICE_UNAVAILABLE,
                &response,
            );
            return (StatusCode::SERVICE_UNAVAILABLE, Json(response));
        }
    };
    let scanned_height = match state.store.load_scan_height(&vault_pub_key_canonical) {
        Ok(Some(height)) => height,
        Ok(None) => 0,
        Err(e) => {
            error!(error = %e, vault = %vault_pub_key_canonical, "failed to load scan height for sign staleness check");
            let response = error_json(e.to_string());
            persist_idempotency(
                &state,
                "sign_prepare",
                &msg_id,
                &payload_hashes.canonical,
                StatusCode::INTERNAL_SERVER_ERROR,
                &response,
            );
            return (StatusCode::INTERNAL_SERVER_ERROR, Json(response));
        }
    };
    let lag = tip_height.saturating_sub(scanned_height);
    if scan_lag_exceeds(tip_height, scanned_height, state.max_sign_scan_lag_blocks) {
        let response = serde_json::json!({
            "error": format!(
                "vault {} scanner is stale; signing is blocked",
                vault_pub_key_canonical
            ),
            "tip_height": tip_height,
            "scanned_height": scanned_height,
            "lag_blocks": lag,
            "max_lag_blocks": state.max_sign_scan_lag_blocks,
        });
        persist_idempotency(
            &state,
            "sign_prepare",
            &msg_id,
            &payload_hashes.canonical,
            StatusCode::SERVICE_UNAVAILABLE,
            &response,
        );
        return (StatusCode::SERVICE_UNAVAILABLE, Json(response));
    }

    let (status, response_json) = match state
        .signer
        .prepare(
            &vault_pub_key_canonical,
            &req.to_address,
            req.amount_atomic,
            req.max_gas_atomic,
            req.sweep_if_final,
            &req.memo.unwrap_or_default(),
            req.fee_rate,
            &req.decoy_context,
            &msg_id,
            req.max_inputs,
            req.dust_threshold,
        )
        .await
    {
        Ok((session_id, preprocess)) => (
            StatusCode::OK,
            json_value(SignPrepareResponse {
                session_id,
                preprocess: hex::encode(preprocess),
            }),
        ),
        Err(e) => {
            // Use Debug formatter (`{:?}`) so anyhow includes the full error
            // chain — Display only shows the topmost message which collapses
            // useful root cause for builder failures.
            error!(error = ?e, msg_id, "sign prepare failed");
            let msg = format!("{e:#}");
            let status = if is_capacity_error(&msg) {
                StatusCode::SERVICE_UNAVAILABLE
            } else {
                StatusCode::INTERNAL_SERVER_ERROR
            };
            (status, error_json(msg))
        }
    };

    persist_idempotency(
        &state,
        "sign_prepare",
        &msg_id,
        &payload_hashes.canonical,
        status,
        &response_json,
    );
    (status, Json(response_json))
}

pub async fn sign_partial(
    State(state): State<Arc<AppState>>,
    Json(req): Json<SignPartialRequest>,
) -> impl IntoResponse {
    let msg_id = match required_msg_id(&req.msg_id) {
        Ok(msg_id) => msg_id,
        Err((status, response)) => return (status, Json(response)),
    };
    let mut canonical_req = req.clone();
    canonical_req.msg_id = msg_id.clone();
    let payload_hashes = match idempotency_payload_hashes(&canonical_req) {
        Ok(hashes) => hashes,
        Err(e) => return internal_error_response(e),
    };
    match check_idempotency(&state, "sign_partial", &msg_id, &payload_hashes) {
        Ok(IdempotencyCheck::Replay(status, response)) => return (status, Json(response)),
        Ok(IdempotencyCheck::Conflict) => {
            info!(
                msg_id,
                "sign_partial idempotency conflict: superseding stale record for retry"
            );
            if let Err(e) = state.store.clear_idempotency("sign_partial", &msg_id) {
                error!(msg_id, error = %e, "failed to clear stale sign_partial idempotency record");
                return internal_error_response(format!("failed to clear stale idempotency: {e}"));
            }
            match check_idempotency(&state, "sign_partial", &msg_id, &payload_hashes) {
                Ok(IdempotencyCheck::Fresh) => {}
                other => {
                    error!(msg_id, result = ?other, "unexpected sign_partial idempotency state after clear");
                    return internal_error_response("idempotency reset failed");
                }
            }
        }
        Ok(IdempotencyCheck::InProgress) => {
            return error_response(
                StatusCode::CONFLICT,
                "idempotency request already in progress for msg_id",
            );
        }
        Ok(IdempotencyCheck::Fresh) => {}
        Err(e) => return internal_error_response(e),
    }
    let all_preprocesses = match parse_hex_map(&req.all_preprocesses) {
        Ok(p) => p,
        Err(e) => {
            let response = error_json(e);
            persist_idempotency(
                &state,
                "sign_partial",
                &msg_id,
                &payload_hashes.canonical,
                StatusCode::BAD_REQUEST,
                &response,
            );
            return (StatusCode::BAD_REQUEST, Json(response));
        }
    };

    let (status, response_json) = match state.signer.partial_sign(&req.session_id, all_preprocesses)
    {
        Ok(share) => (
            StatusCode::OK,
            json_value(SignPartialResponse {
                signature_share: hex::encode(share),
            }),
        ),
        Err(e) => {
            error!(error = %e, "sign partial failed");
            bad_request(e.to_string())
        }
    };
    persist_idempotency(
        &state,
        "sign_partial",
        &msg_id,
        &payload_hashes.canonical,
        status,
        &response_json,
    );
    (status, Json(response_json))
}

pub async fn sign_combine(
    State(state): State<Arc<AppState>>,
    Json(req): Json<SignCombineRequest>,
) -> impl IntoResponse {
    let msg_id = match required_msg_id(&req.msg_id) {
        Ok(msg_id) => msg_id,
        Err((status, response)) => return (status, Json(response)),
    };
    let mut canonical_req = req.clone();
    canonical_req.msg_id = msg_id.clone();
    let payload_hashes = match idempotency_payload_hashes(&canonical_req) {
        Ok(hashes) => hashes,
        Err(e) => return internal_error_response(e),
    };
    match check_idempotency(&state, "sign_combine", &msg_id, &payload_hashes) {
        Ok(IdempotencyCheck::Replay(status, response)) => return (status, Json(response)),
        Ok(IdempotencyCheck::Conflict) => {
            info!(
                msg_id,
                "sign_combine idempotency conflict: superseding stale record for retry"
            );
            if let Err(e) = state.store.clear_idempotency("sign_combine", &msg_id) {
                error!(msg_id, error = %e, "failed to clear stale sign_combine idempotency record");
                return internal_error_response(format!("failed to clear stale idempotency: {e}"));
            }
            match check_idempotency(&state, "sign_combine", &msg_id, &payload_hashes) {
                Ok(IdempotencyCheck::Fresh) => {}
                other => {
                    error!(msg_id, result = ?other, "unexpected sign_combine idempotency state after clear");
                    return internal_error_response("idempotency reset failed");
                }
            }
        }
        Ok(IdempotencyCheck::InProgress) => {
            return error_response(
                StatusCode::CONFLICT,
                "idempotency request already in progress for msg_id",
            );
        }
        Ok(IdempotencyCheck::Fresh) => {}
        Err(e) => return internal_error_response(e),
    }

    let all_shares = match parse_hex_map(&req.all_shares) {
        Ok(s) => s,
        Err(e) => {
            let response = error_json(e);
            persist_idempotency(
                &state,
                "sign_combine",
                &msg_id,
                &payload_hashes.canonical,
                StatusCode::BAD_REQUEST,
                &response,
            );
            return (StatusCode::BAD_REQUEST, Json(response));
        }
    };

    let (status, response_json) = match state
        .signer
        .combine(&req.session_id, all_shares, &msg_id)
        .await
    {
        Ok((tx_hash, tx_hex, amount_atomic, fee_atomic, tx_keys)) => (
            StatusCode::OK,
            json_value(SignCombineResponse {
                tx_hash,
                tx_hex,
                amount_atomic,
                fee_atomic,
                tx_keys,
            }),
        ),
        Err(e) => {
            error!(error = %e, "sign combine failed");
            bad_request(e.to_string())
        }
    };
    persist_idempotency(
        &state,
        "sign_combine",
        &msg_id,
        &payload_hashes.canonical,
        status,
        &response_json,
    );
    (status, Json(response_json))
}

pub async fn key_image_prepare(
    State(state): State<Arc<AppState>>,
    Json(req): Json<KeyImagePrepareRequest>,
) -> impl IntoResponse {
    let msg_id = match required_msg_id(&req.msg_id) {
        Ok(msg_id) => msg_id,
        Err((status, response)) => return (status, Json(response)),
    };
    let mut canonical_req = req.clone();
    canonical_req.msg_id = msg_id.clone();
    let payload_hashes = match idempotency_payload_hashes(&canonical_req) {
        Ok(hashes) => hashes,
        Err(e) => return internal_error_response(e),
    };
    match check_idempotency(&state, "key_image_prepare", &msg_id, &payload_hashes) {
        Ok(IdempotencyCheck::Replay(status, response)) => {
            let cached_session_id = response
                .get("session_id")
                .and_then(|v| v.as_str())
                .map(|s| s.to_string());
            let session_valid = cached_session_id
                .as_ref()
                .map(|sid| state.key_images.session_exists(sid))
                .unwrap_or(true);
            if session_valid {
                return (status, Json(response));
            }
            info!(
                msg_id,
                cached_session_id = ?cached_session_id,
                "key_image_prepare cached session not found in memory, clearing stale idempotency"
            );
            if let Err(e) = state.store.clear_idempotency("key_image_prepare", &msg_id) {
                error!(
                    msg_id,
                    error = %e,
                    "failed to clear stale key_image_prepare idempotency record"
                );
                return internal_error_response(format!("failed to clear stale idempotency: {e}"));
            }
        }
        Ok(IdempotencyCheck::Conflict) => {
            if let Err(e) = state.store.clear_idempotency("key_image_prepare", &msg_id) {
                return internal_error_response(format!("failed to clear stale idempotency: {e}"));
            }
        }
        Ok(IdempotencyCheck::InProgress) => {
            return error_response(
                StatusCode::CONFLICT,
                "idempotency request already in progress for msg_id",
            );
        }
        Ok(IdempotencyCheck::Fresh) => {}
        Err(e) => return internal_error_response(e),
    }
    let vault_pub_key = req.vault_pub_key.trim().to_string();
    if !valid_vault_pub_key(&vault_pub_key) {
        let response = error_json("invalid vault_pub_key format");
        persist_idempotency(
            &state,
            "key_image_prepare",
            &msg_id,
            &payload_hashes.canonical,
            StatusCode::BAD_REQUEST,
            &response,
        );
        return (StatusCode::BAD_REQUEST, Json(response));
    }
    let tx_hash = req.tx_hash.trim().to_ascii_lowercase();
    if tx_hash.is_empty() {
        let response = error_json("tx_hash is required");
        persist_idempotency(
            &state,
            "key_image_prepare",
            &msg_id,
            &payload_hashes.canonical,
            StatusCode::BAD_REQUEST,
            &response,
        );
        return (StatusCode::BAD_REQUEST, Json(response));
    }

    let (status, response_json) =
        match state
            .key_images
            .prepare(&vault_pub_key, &tx_hash, req.output_index, &msg_id)
        {
            Ok((session_id, preprocess)) => (
                StatusCode::OK,
                json_value(KeyImagePrepareResponse {
                    session_id,
                    preprocess: hex::encode(preprocess),
                }),
            ),
            Err(e) => bad_request(e.to_string()),
        };
    persist_idempotency(
        &state,
        "key_image_prepare",
        &msg_id,
        &payload_hashes.canonical,
        status,
        &response_json,
    );
    (status, Json(response_json))
}

pub async fn key_image_partial(
    State(state): State<Arc<AppState>>,
    Json(req): Json<KeyImagePartialRequest>,
) -> impl IntoResponse {
    let msg_id = match required_msg_id(&req.msg_id) {
        Ok(msg_id) => msg_id,
        Err((status, response)) => return (status, Json(response)),
    };
    let mut canonical_req = req.clone();
    canonical_req.msg_id = msg_id.clone();
    let payload_hashes = match idempotency_payload_hashes(&canonical_req) {
        Ok(hashes) => hashes,
        Err(e) => return internal_error_response(e),
    };
    match check_idempotency(&state, "key_image_partial", &msg_id, &payload_hashes) {
        Ok(IdempotencyCheck::Replay(status, response)) => return (status, Json(response)),
        Ok(IdempotencyCheck::Conflict) => {
            if let Err(e) = state.store.clear_idempotency("key_image_partial", &msg_id) {
                return internal_error_response(format!("failed to clear stale idempotency: {e}"));
            }
        }
        Ok(IdempotencyCheck::InProgress) => {
            return error_response(
                StatusCode::CONFLICT,
                "idempotency request already in progress for msg_id",
            );
        }
        Ok(IdempotencyCheck::Fresh) => {}
        Err(e) => return internal_error_response(e),
    }

    let all_preprocesses = match parse_hex_map(&req.all_preprocesses) {
        Ok(p) => p,
        Err(e) => {
            let response = error_json(e);
            persist_idempotency(
                &state,
                "key_image_partial",
                &msg_id,
                &payload_hashes.canonical,
                StatusCode::BAD_REQUEST,
                &response,
            );
            return (StatusCode::BAD_REQUEST, Json(response));
        }
    };

    let (status, response_json) = match state
        .key_images
        .partial_sign(&req.session_id, all_preprocesses)
    {
        Ok(share) => (
            StatusCode::OK,
            json_value(KeyImagePartialResponse {
                signature_share: hex::encode(share),
            }),
        ),
        Err(e) => bad_request(e.to_string()),
    };
    persist_idempotency(
        &state,
        "key_image_partial",
        &msg_id,
        &payload_hashes.canonical,
        status,
        &response_json,
    );
    (status, Json(response_json))
}

pub async fn key_image_combine(
    State(state): State<Arc<AppState>>,
    Json(req): Json<KeyImageCombineRequest>,
) -> impl IntoResponse {
    let msg_id = match required_msg_id(&req.msg_id) {
        Ok(msg_id) => msg_id,
        Err((status, response)) => return (status, Json(response)),
    };
    let mut canonical_req = req.clone();
    canonical_req.msg_id = msg_id.clone();
    let payload_hashes = match idempotency_payload_hashes(&canonical_req) {
        Ok(hashes) => hashes,
        Err(e) => return internal_error_response(e),
    };
    match check_idempotency(&state, "key_image_combine", &msg_id, &payload_hashes) {
        Ok(IdempotencyCheck::Replay(status, response)) => return (status, Json(response)),
        Ok(IdempotencyCheck::Conflict) => {
            if let Err(e) = state.store.clear_idempotency("key_image_combine", &msg_id) {
                return internal_error_response(format!("failed to clear stale idempotency: {e}"));
            }
        }
        Ok(IdempotencyCheck::InProgress) => {
            return error_response(
                StatusCode::CONFLICT,
                "idempotency request already in progress for msg_id",
            );
        }
        Ok(IdempotencyCheck::Fresh) => {}
        Err(e) => return internal_error_response(e),
    }

    let all_shares = match parse_hex_map(&req.all_shares) {
        Ok(p) => p,
        Err(e) => {
            let response = error_json(e);
            persist_idempotency(
                &state,
                "key_image_combine",
                &msg_id,
                &payload_hashes.canonical,
                StatusCode::BAD_REQUEST,
                &response,
            );
            return (StatusCode::BAD_REQUEST, Json(response));
        }
    };

    let (status, response_json) = match state.key_images.combine(&req.session_id, all_shares) {
        Ok((tx_hash, key_image, proof_c, proof_s, output_index)) => (
            StatusCode::OK,
            json_value(KeyImageCombineResponse {
                tx_hash,
                output_index,
                key_image,
                proof_c,
                proof_s,
            }),
        ),
        Err(e) => bad_request(e.to_string()),
    };
    persist_idempotency(
        &state,
        "key_image_combine",
        &msg_id,
        &payload_hashes.canonical,
        status,
        &response_json,
    );
    (status, Json(response_json))
}

pub async fn key_image_validate(
    State(state): State<Arc<AppState>>,
    Json(req): Json<KeyImageValidateRequest>,
) -> impl IntoResponse {
    let vault_pub_key = req.vault_pub_key.trim().to_string();
    if !valid_vault_pub_key(&vault_pub_key) {
        return bad_request_response("invalid vault_pub_key format");
    }
    let tx_hash = req.tx_hash.trim().to_ascii_lowercase();
    if tx_hash.is_empty() {
        return bad_request_response("tx_hash is required");
    }

    match state.key_images.validate(
        &vault_pub_key,
        &tx_hash,
        req.output_index,
        req.key_image.trim(),
        req.proof_c.trim(),
        req.proof_s.trim(),
    ) {
        Ok(()) => ok_response(KeyImageValidateResponse { valid: true }),
        Err(e) => bad_request_response(e.to_string()),
    }
}

pub async fn broadcast(
    State(state): State<Arc<AppState>>,
    Json(req): Json<BroadcastRequest>,
) -> impl IntoResponse {
    let msg_id = match required_msg_id(&req.msg_id) {
        Ok(msg_id) => msg_id,
        Err((status, response)) => return (status, Json(response)),
    };
    let vault_pub_key_canonical = req.vault_pub_key.trim().to_string();
    if vault_pub_key_canonical.is_empty() {
        return bad_request_response("vault_pub_key is required");
    }
    if !valid_vault_pub_key(&vault_pub_key_canonical) {
        return bad_request_response("invalid vault_pub_key format");
    }

    let mut canonical_req = req.clone();
    canonical_req.msg_id = msg_id.clone();
    canonical_req.vault_pub_key = vault_pub_key_canonical.clone();
    let payload_hashes = match idempotency_payload_hashes(&canonical_req) {
        Ok(hashes) => hashes,
        Err(e) => return internal_error_response(e),
    };
    match check_idempotency(&state, "broadcast", &msg_id, &payload_hashes) {
        Ok(IdempotencyCheck::Replay(status, response)) => return (status, Json(response)),
        Ok(IdempotencyCheck::Conflict) => {
            return error_response(
                StatusCode::CONFLICT,
                "idempotency conflict: msg_id reused with different payload",
            );
        }
        Ok(IdempotencyCheck::InProgress) => {
            return error_response(
                StatusCode::CONFLICT,
                "idempotency request already in progress for msg_id",
            );
        }
        Ok(IdempotencyCheck::Fresh) => {}
        Err(e) => return internal_error_response(e),
    }

    let (status, response_json) = match state.daemon.send_raw_transaction(&req.tx_hex).await {
        Ok(resp) => {
            if resp.status == "OK" {
                let caller_tx_hash = req.tx_hash.trim();
                let tx_hash = resp
                    .tx_hash
                    .or_else(|| {
                        resp.tx_hash_list
                            .and_then(|hashes| hashes.into_iter().next())
                    })
                    .map(|hash| hash.trim().to_string())
                    .filter(|hash| !hash.is_empty())
                    .or_else(|| {
                        if caller_tx_hash.is_empty() {
                            None
                        } else {
                            warn!(
                                msg_id,
                                caller_tx_hash,
                                "daemon broadcast omitted tx hash; falling back to caller-supplied tx hash"
                            );
                            Some(caller_tx_hash.to_string())
                        }
                    });
                if let Some(tx_hash) = tx_hash {
                    if !caller_tx_hash.is_empty() && !caller_tx_hash.eq_ignore_ascii_case(&tx_hash)
                    {
                        warn!(
                            msg_id,
                            caller_tx_hash,
                            daemon_tx_hash = %tx_hash,
                            "broadcast caller tx_hash mismatched daemon tx_hash; using daemon tx_hash"
                        );
                    }

                    match state.signer.finalize_broadcast(
                        &vault_pub_key_canonical,
                        &msg_id,
                        &tx_hash,
                    ) {
                        Ok(result) => {
                            if result.missing_candidate > 0 || result.candidate_mismatch > 0 {
                                warn!(
                                    msg_id,
                                    finalized = result.finalized,
                                    missing_candidate = result.missing_candidate,
                                    candidate_mismatch = result.candidate_mismatch,
                                    "broadcast finalized with unmatched reserved outputs"
                                );
                            }
                        }
                        Err(e) => {
                            error!(error = %e, msg_id, "failed to finalize reserved outputs after broadcast");
                        }
                    }
                    if let Err(e) = OutgoingLifecycle::new(state.store.as_ref())
                        .reconcile_observation(
                            &vault_pub_key_canonical,
                            &tx_hash,
                            OutgoingObservation::ObservedInMempool,
                            Some(chrono::Utc::now().timestamp()),
                            Some(chrono::Utc::now().timestamp()),
                        )
                    {
                        error!(error = %e, "failed to update outgoing transfer status after broadcast");
                    }

                    (StatusCode::OK, json_value(BroadcastResponse { tx_hash }))
                } else {
                    error!("daemon broadcast returned success without tx hash");
                    (
                        StatusCode::BAD_GATEWAY,
                        error_json("daemon broadcast returned success without tx hash"),
                    )
                }
            } else {
                let reason = resp.reason.unwrap_or_else(|| resp.status.clone());
                let is_double_spend = resp.double_spend.unwrap_or(false);
                let status_lower = resp.status.to_ascii_lowercase();
                let reason_lower = reason.to_ascii_lowercase();
                // "not relayed" is ambiguous: monerod can return it for a tx
                // that really is already in pool/chain, but it can also mean
                // the broadcast was rejected and the tx never became visible.
                // Only treat it as success after a positive lookup below.
                let mut already_known = is_double_spend
                    || status_lower.contains("already")
                    || reason_lower.contains("already")
                    || reason_lower.contains("double spend");

                // Positive lookup fallback: when monerod returns Failed with
                // an empty/ambiguous reason for an already-known tx, query the
                // pool/chain by the caller-supplied tx hash. This makes the
                // broadcast endpoint robust to monerod's inconsistent reason
                // strings and prevents false 400s on idempotent retries.
                let caller_tx_hash_for_lookup = req.tx_hash.trim();
                if !already_known && !caller_tx_hash_for_lookup.is_empty() {
                    match state
                        .daemon
                        .get_transactions(&[caller_tx_hash_for_lookup.to_ascii_lowercase()])
                        .await
                    {
                        Ok(result) => {
                            let found = result
                                .txs
                                .as_ref()
                                .map(|txs| {
                                    txs.iter().any(|tx| {
                                        tx.in_pool.unwrap_or(false) || tx.block_height.is_some()
                                    })
                                })
                                .unwrap_or(false);
                            if found {
                                info!(
                                    msg_id,
                                    caller_tx_hash = caller_tx_hash_for_lookup,
                                    reason = %reason,
                                    "broadcast rejected but tx already in pool/chain — treating as success"
                                );
                                already_known = true;
                            }
                        }
                        Err(e) => {
                            warn!(error = %e, caller_tx_hash = caller_tx_hash_for_lookup, "post-broadcast tx lookup failed");
                        }
                    }
                }

                if already_known {
                    let caller_tx_hash = req.tx_hash.trim();
                    if !caller_tx_hash.is_empty() {
                        info!(
                            msg_id,
                            caller_tx_hash,
                            reason = %reason,
                            "broadcast rejected as already known — treating as success"
                        );
                        if let Err(e) = state.signer.finalize_broadcast(
                            &vault_pub_key_canonical,
                            &msg_id,
                            caller_tx_hash,
                        ) {
                            error!(error = %e, msg_id, "failed to finalize reserved outputs after already-known broadcast");
                        }
                        if let Err(e) = OutgoingLifecycle::new(state.store.as_ref())
                            .reconcile_observation(
                                &vault_pub_key_canonical,
                                caller_tx_hash,
                                OutgoingObservation::ObservedInMempool,
                                Some(chrono::Utc::now().timestamp()),
                                Some(chrono::Utc::now().timestamp()),
                            )
                        {
                            error!(error = %e, "failed to update outgoing transfer status after already-known broadcast");
                        }
                        (
                            StatusCode::OK,
                            json_value(BroadcastResponse {
                                tx_hash: caller_tx_hash.to_string(),
                            }),
                        )
                    } else {
                        warn!(msg_id, reason = %reason, "broadcast rejected as already known but no caller tx_hash to fall back on");
                        (StatusCode::BAD_REQUEST, error_json(reason))
                    }
                } else {
                    (StatusCode::BAD_REQUEST, error_json(reason))
                }
            }
        }
        Err(e) => {
            error!(error = %e, "broadcast failed");
            (StatusCode::INTERNAL_SERVER_ERROR, error_json(e.to_string()))
        }
    };
    // Only cache successful broadcast responses. Caching failures causes the
    // bifrost to retry the same broadcast forever and always get the cached
    // failure back, even after the underlying issue is resolved.
    // On failure, clear the pending claim so retries can proceed immediately
    // instead of being blocked by the in-progress lock for 10 minutes.
    if status == StatusCode::OK {
        persist_idempotency(
            &state,
            "broadcast",
            &msg_id,
            &payload_hashes.canonical,
            status,
            &response_json,
        );
    } else if let Err(e) = state.store.clear_idempotency("broadcast", &msg_id) {
        warn!(msg_id, error = %e, "failed to clear pending broadcast idempotency after failure");
    }
    (status, Json(response_json))
}

// ---- Scanner/Balance Routes ----

pub async fn get_balance(
    State(state): State<Arc<AppState>>,
    Path(vault_pub_key): Path<String>,
) -> impl IntoResponse {
    let (balance, unlocked_balance) = {
        let scanner = state.scanner.lock();
        scanner.get_balance(&vault_pub_key)
    };
    let spendable_balance = unlocked_balance;

    ok_response(BalanceResponse {
        balance,
        unlocked_balance,
        spendable_balance,
    })
}

#[derive(Debug, serde::Deserialize)]
pub struct TransfersQuery {
    pub height: Option<u64>,
    pub mempool: Option<bool>,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, serde::Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum OutputsView {
    Spendable,
    All,
}

impl Default for OutputsView {
    fn default() -> Self {
        Self::Spendable
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, serde::Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum OutputsSource {
    Scanner,
    Store,
}

impl Default for OutputsSource {
    fn default() -> Self {
        Self::Scanner
    }
}

#[derive(Debug, Default, serde::Deserialize)]
pub struct OutputsQuery {
    #[serde(default)]
    pub view: OutputsView,
    #[serde(default)]
    pub source: OutputsSource,
}

const MEMPOOL_TRANSFER_BATCH_SIZE: usize = 100;
pub(crate) fn normalize_tx_hash(hash: &str) -> String {
    hash.trim().to_ascii_lowercase()
}

fn is_observed_mined_record(tx: &OutgoingTransferRecord) -> bool {
    tx.is_observed_mined()
}

fn outgoing_effective_height(tx: &OutgoingTransferRecord) -> u64 {
    tx.effective_height()
}

fn should_emit_outgoing_on_scanner_sync(
    tx: &crate::state::store::OutgoingTransferRecord,
    req_height: u64,
) -> bool {
    if !is_observed_mined_record(tx) {
        return false;
    }
    let effective_height = outgoing_effective_height(tx);
    effective_height <= req_height && tx.reported_effective_height != Some(effective_height)
}

fn transfer_entry_from_outgoing_record(
    tx: &OutgoingTransferRecord,
    current_height: u64,
    mempool: bool,
) -> TransferEntry {
    let height = outgoing_effective_height(tx);
    let confirmations = if mempool {
        0
    } else {
        current_height.saturating_sub(height)
    };
    TransferEntry {
        txid: tx.tx_hash.clone(),
        address: tx.to_address.clone(),
        from_address: Some(tx.from_address.clone()),
        vault_pub_key: Some(tx.vault_pub_key.clone()),
        tx_keys: tx.tx_keys.clone(),
        amount: tx.amount,
        fee: tx.fee,
        height,
        confirmations,
        note: tx.memo.clone(),
        locked: mempool || confirmations < 10,
    }
}

fn outgoing_state_entry_from_record(
    tx: OutgoingTransferRecord,
) -> crate::api::types::OutgoingTransferStateEntry {
    let snapshot = OutgoingLifecycle::snapshot(&tx);
    crate::api::types::OutgoingTransferStateEntry {
        tx_hash: tx.tx_hash,
        tx_keys: tx.tx_keys,
        registered_inventory: snapshot.registered_inventory,
        has_owned_change: snapshot.has_owned_change,
        local_change_materialized: snapshot.local_change_materialized,
        key_images_ready: snapshot.key_images_ready,
        from_address: tx.from_address,
        to_address: tx.to_address,
        amount: tx.amount,
        fee: tx.fee,
        memo: tx.memo,
        planned_height: tx.planned_height,
        observed_height: snapshot.observed_height,
        status: snapshot.status.to_string(),
        pending_reason: snapshot.pending_reason.map(str::to_string),
        reported_effective_height: snapshot.reported_effective_height,
        created_at: tx.created_at,
    }
}

fn build_mempool_outgoing_entries(
    records: &[OutgoingTransferRecord],
    in_pool_hashes: &HashSet<String>,
) -> Vec<TransferEntry> {
    let mut outgoing = Vec::new();
    let mut seen = HashSet::new();
    for tx in records {
        let normalized = normalize_tx_hash(&tx.tx_hash);
        if !in_pool_hashes.contains(&normalized) {
            continue;
        }
        if !seen.insert(normalized) {
            continue;
        }
        outgoing.push(transfer_entry_from_outgoing_record(tx, 0, true));
    }
    outgoing
}

fn promote_outgoing_records_observed_in_block(
    store: &crate::state::Store,
    vault_pub_key: &str,
    block_tx_hashes: &HashSet<String>,
    height: u64,
) -> anyhow::Result<()> {
    if block_tx_hashes.is_empty() {
        return Ok(());
    }

    let records = store.load_outgoing_transfers(vault_pub_key)?;
    if records.is_empty() {
        return Ok(());
    }

    let now_ts = chrono::Utc::now().timestamp();
    let outgoing_lifecycle = OutgoingLifecycle::new(store);
    for record in records {
        let normalized = normalize_tx_hash(&record.tx_hash);
        if normalized.is_empty() || !block_tx_hashes.contains(&normalized) {
            continue;
        }
        if is_observed_mined_record(&record) && record.observed_height == Some(height) {
            continue;
        }
        outgoing_lifecycle.reconcile_observation(
            vault_pub_key,
            &record.tx_hash,
            OutgoingObservation::ObservedMined(height),
            Some(now_ts),
            None,
        )?;
    }

    Ok(())
}

fn transfer_entry_from_tracked_output(
    output: &TrackedOutput,
    vault_pub_key: &str,
    current_height: u64,
) -> TransferEntry {
    let confirmations = current_height.saturating_sub(output.height);
    TransferEntry {
        txid: output.tx_hash.clone(),
        address: output.address.clone().unwrap_or_default(),
        from_address: None,
        vault_pub_key: Some(vault_pub_key.to_string()),
        tx_keys: Vec::new(),
        amount: output.amount,
        fee: 0,
        height: output.height,
        confirmations,
        note: output.note.clone().unwrap_or_default(),
        locked: confirmations < 10,
    }
}

fn transfer_entries_from_added_refs(
    outputs: &[TrackedOutput],
    refs: &[OutputRef],
    vault_pub_key: &str,
    current_height: u64,
) -> Vec<TransferEntry> {
    let by_ref: HashMap<(String, u32), &TrackedOutput> = outputs
        .iter()
        .map(|output| ((output.tx_hash.clone(), output.output_index), output))
        .collect();
    refs.iter()
        .filter_map(|r| by_ref.get(&(r.tx_hash.clone(), r.output_index)))
        .filter(|output| !output.is_spent())
        .map(|output| transfer_entry_from_tracked_output(output, vault_pub_key, current_height))
        .collect()
}

fn output_refs_at_height(outputs: &[TrackedOutput], height: u64) -> Vec<OutputRef> {
    outputs
        .iter()
        .filter(|output| output.height == height && !output.is_spent())
        .map(|output| OutputRef {
            tx_hash: output.tx_hash.clone(),
            output_index: output.output_index,
        })
        .collect()
}

pub(crate) fn hash_matches(actual: &str, expected: &str) -> bool {
    actual.trim().eq_ignore_ascii_case(expected.trim())
}

pub(crate) fn is_valid_chain_tx_hash(hash: &str) -> bool {
    let normalized = hash.trim();
    normalized.len() == 64 && hex::decode(normalized).is_ok()
}

fn scan_lag_exceeds(tip_height: u64, scanned_height: u64, max_lag_blocks: u64) -> bool {
    tip_height.saturating_sub(scanned_height) > max_lag_blocks
}

const RESCAN_MAX_BLOCKS_PER_SYNC: u64 = 500;

fn rescan_target_height(scan_height: u64, validated_height: u64, chain_tip: u64) -> u64 {
    let rescan_tip = std::cmp::max(validated_height, chain_tip);
    std::cmp::min(
        scan_height.saturating_add(RESCAN_MAX_BLOCKS_PER_SYNC),
        rescan_tip,
    )
}

fn outputs_for_view(
    scanner: &Scanner,
    vault_pub_key: &str,
    view: OutputsView,
) -> Vec<TrackedOutput> {
    match view {
        OutputsView::Spendable => scanner.get_outputs(vault_pub_key),
        OutputsView::All => scanner.get_all_outputs(vault_pub_key),
    }
}

fn stored_outputs_for_view(
    state: &AppState,
    vault_pub_key: &str,
    view: OutputsView,
) -> Result<Vec<TrackedOutput>, String> {
    let outputs = state
        .store
        .load_outputs(vault_pub_key)
        .map_err(|e| format!("failed to load outputs: {e}"))?;

    Ok(match view {
        OutputsView::All => outputs,
        OutputsView::Spendable => {
            let current_height = state.scanner.lock().current_height();
            outputs
                .into_iter()
                .filter(|output| output.is_available_for_spend_at(current_height))
                .collect()
        }
    })
}

fn output_entry_from_tracked(o: TrackedOutput) -> OutputEntry {
    let state = match o.state {
        OutputState::Unspent => "unspent".to_string(),
        OutputState::Reserved => "reserved".to_string(),
        OutputState::Spent => "spent".to_string(),
    };
    OutputEntry {
        tx_hash: o.tx_hash,
        output_index: o.output_index,
        amount: o.amount,
        height: o.height,
        key_image: o.key_image,
        state,
        subaddress_major: o.subaddress_major,
        subaddress_minor: o.subaddress_minor,
    }
}

pub async fn scanner_sync(
    State(state): State<Arc<AppState>>,
    Json(req): Json<ScannerSyncRequest>,
) -> impl IntoResponse {
    let _scan_lifecycle = state.scan_lifecycle.lock().await;
    if req.block_hash.trim().is_empty() || req.parent_hash.trim().is_empty() {
        return bad_request_response("block_hash and parent_hash are required");
    }

    let canonical_header = match state.daemon.get_block_header_by_height(req.height).await {
        Ok(resp) => resp.block_header,
        Err(e) => {
            error!(
                error = %e,
                height = req.height,
                "failed to fetch canonical block header for scanner sync"
            );
            return error_response(
                StatusCode::SERVICE_UNAVAILABLE,
                format!("scanner sync unavailable for height {}: {}", req.height, e),
            );
        }
    };

    if !hash_matches(&canonical_header.hash, &req.block_hash)
        || !hash_matches(&canonical_header.prev_hash, &req.parent_hash)
    {
        return error_response(
            StatusCode::CONFLICT,
            format!(
                "scanner sync hash mismatch at height {}: expected {}<-{}, got {}<-{}",
                req.height,
                req.block_hash.trim(),
                req.parent_hash.trim(),
                canonical_header.hash.trim(),
                canonical_header.prev_hash.trim()
            ),
        );
    }

    let effective_height = canonical_header.height;
    state.scanner.lock().set_current_height(effective_height);

    let vaults = match state.store.list_vaults() {
        Ok(v) => v,
        Err(e) => {
            error!(error = %e, "failed to list vaults for scanner sync");
            return internal_error_response(e.to_string());
        }
    };
    let rescan_chain_tip = if vaults.iter().any(|vault| vault.rescan) {
        match state.daemon.get_height().await {
            Ok(height) => height,
            Err(e) => {
                warn!(
                    error = %e,
                    validated_height = effective_height,
                    "failed to fetch daemon tip for scanner rescan; using validated height"
                );
                effective_height
            }
        }
    } else {
        effective_height
    };
    let rpc = match state.daemon.oxide_rpc().await {
        Ok(rpc) => rpc,
        Err(e) => {
            error!(error = %e, "failed to initialize monero-oxide rpc client for scanner sync");
            return internal_error_response(e.to_string());
        }
    };

    let mut incoming = Vec::new();
    let mut outgoing = Vec::new();
    let reorg_window = if req.reorg_window == 0 {
        120
    } else {
        req.reorg_window
    };

    for vault in &vaults {
        // If vault is in rescan mode, self-scan from stored height to chain tip.
        // When rescan is already at tip (e.g. a freshly-registered vault whose
        // birthday equals the current height), flip rescan=false and fall
        // through to normal scan so this same request still processes the
        // block via the normal path. When an actual batch runs, skip normal
        // scan for this cycle — the next scanner_sync will pick it up.
        if vault.rescan {
            let vault_scan_height = state
                .store
                .load_scan_height(&vault.vault_pub_key)
                .ok()
                .flatten()
                .unwrap_or(vault.birthday);
            let tip = std::cmp::max(effective_height, rescan_chain_tip);
            if vault_scan_height >= tip {
                info!(vault = %vault.vault_pub_key, "rescan complete — already at chain tip");
                let _ = VaultLifecycle::new(state.store.as_ref())
                    .complete_rescan_at_tip(&vault.vault_pub_key, vault.birthday);
                // Fall through to normal scan below.
            } else {
                let end = rescan_target_height(vault_scan_height, effective_height, tip);
                info!(
                    vault = %vault.vault_pub_key,
                    from = vault_scan_height + 1,
                    to = end,
                    tip,
                    validated_height = effective_height,
                    "rescan: scanning blocks for vault"
                );
                let mut batch: Vec<ScannedBlockEntry> = Vec::new();
                for h in (vault_scan_height + 1)..=end {
                    let rescan_block = match rpc.get_scannable_block_by_number(h as usize).await {
                        Ok(b) => b,
                        Err(e) => {
                            warn!(error = %e, vault = %vault.vault_pub_key, height = h, "rescan: failed to fetch block, stopping batch");
                            break;
                        }
                    };
                    let block_hash = normalize_tx_hash(&hex::encode(rescan_block.block.hash()));
                    let parent_hash =
                        normalize_tx_hash(&hex::encode(rescan_block.block.header.previous));
                    let block_tx_hashes: Vec<String> = rescan_block
                        .block
                        .transactions
                        .iter()
                        .map(|tx_hash| normalize_tx_hash(&hex::encode(tx_hash)))
                        .collect();
                    let block_transactions = rescan_block.transactions.clone();
                    let added = match state.scanner.lock().scan_block_for_vault_with_added(
                        &vault.vault_pub_key,
                        h,
                        rescan_block,
                    ) {
                        Ok(a) => a,
                        Err(e) => {
                            warn!(error = %e, vault = %vault.vault_pub_key, height = h, "rescan: scan failed, stopping batch");
                            break;
                        }
                    };
                    if !added.is_empty() {
                        info!(vault = %vault.vault_pub_key, height = h, added = added.len(), "rescan: found outputs");
                        for output in &added {
                            if !output.is_spent() {
                                incoming.push(transfer_entry_from_tracked_output(
                                    output,
                                    &vault.vault_pub_key,
                                    effective_height,
                                ));
                            }
                        }
                    }
                    let spent_key_image_txs =
                        collect_spent_key_images_by_tx(&block_transactions, &block_tx_hashes);
                    let spent_key_images: Vec<String> =
                        spent_key_image_txs.keys().cloned().collect();
                    batch.push(ScannedBlockEntry {
                        height: h,
                        block_hash,
                        parent_hash,
                        spent_key_images,
                        spent_key_image_txs,
                        added_outputs: added,
                    });
                }
                if !batch.is_empty() {
                    match commit_scan_batch(&state, &vault.vault_pub_key, &batch, reorg_window) {
                        Ok(persisted) => {
                            outgoing.extend(unauthorized_spend_entries(
                                &state,
                                &vault.vault_pub_key,
                                &vault.address,
                                &batch,
                                &persisted.outputs_after_scan,
                                effective_height,
                            ));
                        }
                        Err(e) => {
                            warn!(
                                error = %e,
                                vault = %vault.vault_pub_key,
                                "rescan: failed to commit scan batch"
                            );
                        }
                    }
                }
                // Check if caught up
                let new_height = state
                    .store
                    .load_scan_height(&vault.vault_pub_key)
                    .ok()
                    .flatten()
                    .unwrap_or(0);
                if new_height >= tip {
                    info!(vault = %vault.vault_pub_key, "rescan complete — caught up to chain tip");
                    let _ = VaultLifecycle::new(state.store.as_ref())
                        .complete_rescan_at_tip(&vault.vault_pub_key, vault.birthday);
                }
                // Skip normal scan while an active rescan batch is in progress.
                continue;
            }
        }

        let vault_lifecycle = VaultLifecycle::new(state.store.as_ref());
        let output_lifecycle = OutputLifecycle::new(state.store.as_ref());
        let (mut outputs, mut scan_height) = {
            let mut scanner = state.scanner.lock();
            match vault_lifecycle.rehydrate_scanner(
                &mut scanner,
                state.chain_network,
                &vault.vault_pub_key,
                &vault.group_key,
                vault.birthday,
                0,
            ) {
                Ok(state) => state,
                Err(e) => {
                    error!(
                        error = %e,
                        vault = %vault.vault_pub_key,
                        "failed to initialize scanner state for scanner sync"
                    );
                    return internal_error_response(e.to_string());
                }
            }
        };

        let mut stored_meta = match state
            .store
            .load_scan_block_meta(&vault.vault_pub_key, req.height)
        {
            Ok(meta) => meta,
            Err(e) => {
                error!(
                    error = %e,
                    vault = %vault.vault_pub_key,
                    height = req.height,
                    "failed to load scan block metadata for scanner sync"
                );
                return internal_error_response(e.to_string());
            }
        };

        // When no ScanBlockMeta exists but outputs exist at this height (e.g.
        // from a rescan that saved outputs without saving meta), synthesize
        // meta from the existing outputs so the recovery path reports them.
        if stored_meta.is_none() && scan_height >= req.height {
            let existing_refs = output_refs_at_height(&outputs, req.height);
            if !existing_refs.is_empty() {
                info!(
                    vault = %vault.vault_pub_key,
                    height = req.height,
                    outputs = existing_refs.len(),
                    "synthesizing scan block meta from rescan outputs"
                );
                let synthesized = ScanBlockMeta {
                    height: req.height,
                    block_hash: req.block_hash.trim().to_string(),
                    parent_hash: req.parent_hash.trim().to_string(),
                    added_outputs: existing_refs,
                    spent_key_images: Vec::new(),
                    spent_by_key_images: Vec::new(),
                };
                let _ = output_lifecycle.record_scan_block(&vault.vault_pub_key, &synthesized);
                stored_meta = Some(synthesized);
            }
        }

        let historical_height_pruned = stored_meta.is_none()
            && scan_height > req.height
            && scan_height.saturating_sub(req.height) >= reorg_window;
        let meta_matches = stored_meta.as_ref().is_some_and(|meta| {
            hash_matches(&meta.block_hash, &req.block_hash)
                && hash_matches(&meta.parent_hash, &req.parent_hash)
        });
        let needs_rollback = stored_meta
            .as_ref()
            .is_some_and(|meta| !meta_matches && meta.height == req.height)
            || (!historical_height_pruned
                && !meta_matches
                && stored_meta.is_none()
                && scan_height >= req.height);

        if needs_rollback {
            if let Err(e) = output_lifecycle.rollback_from_height(&vault.vault_pub_key, req.height)
            {
                error!(
                    error = %e,
                    vault = %vault.vault_pub_key,
                    height = req.height,
                    "failed to rollback scanner state"
                );
                return internal_error_response(e.to_string());
            }
            let rehydrated = {
                let mut scanner = state.scanner.lock();
                vault_lifecycle.rehydrate_scanner(
                    &mut scanner,
                    state.chain_network,
                    &vault.vault_pub_key,
                    &vault.group_key,
                    vault.birthday,
                    0,
                )
            };
            match rehydrated {
                Ok((rehydrated_outputs, rehydrated_scan_height)) => {
                    outputs = rehydrated_outputs;
                    scan_height = rehydrated_scan_height;
                }
                Err(e) => {
                    error!(
                        error = %e,
                        vault = %vault.vault_pub_key,
                        "failed to reinitialize scanner after rollback"
                    );
                    return internal_error_response(e.to_string());
                }
            }
        }

        if historical_height_pruned {
            info!(
                vault = %vault.vault_pub_key,
                height = req.height,
                scan_height,
                reorg_window,
                "skipping rollback for historical height outside retained scan metadata window"
            );
        } else if meta_matches {
            if let Some(mut meta) = stored_meta {
                let discovered_refs = output_refs_at_height(&outputs, req.height);
                let known_refs: HashSet<(String, u32)> = meta
                    .added_outputs
                    .iter()
                    .map(|r| (r.tx_hash.clone(), r.output_index))
                    .collect();
                let recovered_refs: Vec<OutputRef> = discovered_refs
                    .into_iter()
                    .filter(|r| !known_refs.contains(&(r.tx_hash.clone(), r.output_index)))
                    .collect();

                if !recovered_refs.is_empty() {
                    warn!(
                        vault = %vault.vault_pub_key,
                        height = req.height,
                        recovered = recovered_refs.len(),
                        "scanner sync recovered missing added outputs from persisted state"
                    );
                    meta.added_outputs.extend(recovered_refs);
                    if let Err(e) = output_lifecycle.record_scan_block(&vault.vault_pub_key, &meta)
                    {
                        error!(
                            error = %e,
                            vault = %vault.vault_pub_key,
                            height = req.height,
                            "failed to heal scan block metadata during scanner sync"
                        );
                        return internal_error_response(e.to_string());
                    }
                }

                incoming.extend(transfer_entries_from_added_refs(
                    &outputs,
                    &meta.added_outputs,
                    &vault.vault_pub_key,
                    effective_height,
                ));
            }
        } else {
            let block = match rpc.get_scannable_block_by_number(req.height as usize).await {
                Ok(block) => block,
                Err(e) => {
                    error!(
                        error = %e,
                        vault = %vault.vault_pub_key,
                        height = req.height,
                        "failed to fetch scannable block for scanner sync"
                    );
                    return error_response(
                        StatusCode::SERVICE_UNAVAILABLE,
                        format!("scanner sync unavailable for height {}: {}", req.height, e),
                    );
                }
            };
            let block_tx_hash_vec: Vec<String> = block
                .block
                .transactions
                .iter()
                .map(|tx_hash| normalize_tx_hash(&hex::encode(tx_hash)))
                .filter(|hash| !hash.is_empty())
                .collect();
            let block_tx_hashes: HashSet<String> = block_tx_hash_vec.iter().cloned().collect();
            if let Err(e) = promote_outgoing_records_observed_in_block(
                &state.store,
                &vault.vault_pub_key,
                &block_tx_hashes,
                req.height,
            ) {
                error!(
                    error = %e,
                    vault = %vault.vault_pub_key,
                    height = req.height,
                    "failed to promote outgoing transfer observations from scanned block"
                );
                return internal_error_response(e.to_string());
            }
            let block_transactions = block.transactions.clone();

            let added_outputs = match state.scanner.lock().scan_block_for_vault_with_added(
                &vault.vault_pub_key,
                req.height,
                block,
            ) {
                Ok(added) => added,
                Err(e) => {
                    error!(
                        error = %e,
                        vault = %vault.vault_pub_key,
                        height = req.height,
                        "failed to scan block for vault during scanner sync"
                    );
                    return internal_error_response(e.to_string());
                }
            };
            let spent_key_image_txs =
                collect_spent_key_images_by_tx(&block_transactions, &block_tx_hash_vec);
            let spent_key_images: Vec<String> = spent_key_image_txs.keys().cloned().collect();
            let batch = vec![ScannedBlockEntry {
                height: req.height,
                block_hash: req.block_hash.trim().to_string(),
                parent_hash: req.parent_hash.trim().to_string(),
                spent_key_images,
                spent_key_image_txs,
                added_outputs: added_outputs.clone(),
            }];
            let persisted =
                match commit_scan_batch(&state, &vault.vault_pub_key, &batch, reorg_window) {
                    Ok(result) => result,
                    Err(e) => {
                        error!(
                            error = %e,
                            vault = %vault.vault_pub_key,
                            height = req.height,
                            "failed to persist scanner sync state"
                        );
                        return internal_error_response(e);
                    }
                };
            // Include outputs newly discovered by this scan.
            incoming.extend(
                added_outputs
                    .iter()
                    .filter(|output| !output.is_spent())
                    .map(|output| {
                        transfer_entry_from_tracked_output(
                            output,
                            &vault.vault_pub_key,
                            effective_height,
                        )
                    }),
            );
            // Also include outputs at this height that were already in the store
            // (e.g. found by the background scanner) but not by this scan call.
            // Without this, outputs discovered by the background scanner race
            // ahead of bifrost's scanner_sync and are never reported as incoming.
            let batch_added_refs = persisted
                .added_refs_per_block
                .first()
                .cloned()
                .unwrap_or_default();
            let added_set: HashSet<(String, u32)> = batch_added_refs
                .iter()
                .map(|r| (r.tx_hash.clone(), r.output_index))
                .collect();
            let store_refs_at_height =
                output_refs_at_height(&persisted.outputs_after_scan, req.height);
            for sref in &store_refs_at_height {
                if !added_set.contains(&(sref.tx_hash.clone(), sref.output_index)) {
                    if let Some(output) = persisted
                        .outputs_after_scan
                        .iter()
                        .find(|o| o.tx_hash == sref.tx_hash && o.output_index == sref.output_index)
                    {
                        if !output.is_spent() {
                            incoming.push(transfer_entry_from_tracked_output(
                                output,
                                &vault.vault_pub_key,
                                effective_height,
                            ));
                        }
                    }
                }
            }
            outgoing.extend(unauthorized_spend_entries(
                &state,
                &vault.vault_pub_key,
                &vault.address,
                &batch,
                &persisted.outputs_after_scan,
                effective_height,
            ));
        }

        match state.store.load_outgoing_transfers(&vault.vault_pub_key) {
            Ok(outbound) => {
                for tx in outbound
                    .into_iter()
                    .filter(|tx| should_emit_outgoing_on_scanner_sync(tx, req.height))
                {
                    let observed_height = outgoing_effective_height(&tx);
                    outgoing.push(transfer_entry_from_outgoing_record(
                        &tx,
                        effective_height,
                        false,
                    ));
                    if let Err(e) = OutgoingLifecycle::new(state.store.as_ref()).mark_reported(
                        &vault.vault_pub_key,
                        &tx.tx_hash,
                        observed_height,
                    ) {
                        error!(
                            error = %e,
                            vault = %vault.vault_pub_key,
                            tx_hash = %tx.tx_hash,
                            observed_height,
                            "failed to mark outgoing transfer reported during scanner sync"
                        );
                        return internal_error_response(e.to_string());
                    }
                }
            }
            Err(e) => {
                error!(
                    error = %e,
                    vault = %vault.vault_pub_key,
                    "failed to load outgoing transfers for scanner sync"
                );
                return internal_error_response(e.to_string());
            }
        }
    }

    // Persist scanner in-memory outputs to store for ALL vaults.
    // The meta_matches path skips merge_scanned_outputs, so outputs
    // found by the wallet scanner during register_vault stay in memory
    // but never reach the persistent store. This sync ensures the
    // output store stays consistent across all nodes.
    for vault in &vaults {
        let all_outputs = state.scanner.lock().get_all_outputs(&vault.vault_pub_key);
        if let Err(e) = state
            .store
            .merge_scanned_outputs(&vault.vault_pub_key, &all_outputs)
        {
            warn!(
                error = %e,
                vault = %vault.vault_pub_key,
                "failed to persist scanner outputs at end of scanner_sync"
            );
        }
    }

    ok_response(TransfersResponse { incoming, outgoing })
}

pub async fn scanner_reset(
    State(state): State<Arc<AppState>>,
    Json(req): Json<crate::api::types::ScannerResetRequest>,
) -> impl IntoResponse {
    let _scan_lifecycle = state.scan_lifecycle.lock().await;
    let vault_pub_key = req.vault_pub_key.trim().to_string();
    let requested_birthday_height = req.birthday_height;
    if vault_pub_key.is_empty() {
        return bad_request_response("vault_pub_key is required");
    }

    let mut scanner = state.scanner.lock();
    if let Err(e) = VaultLifecycle::new(state.store.as_ref()).reset(&mut scanner, &vault_pub_key) {
        error!(error = %e, vault = %vault_pub_key, "failed to reset vault state during scanner reset");
        return internal_error_response(e.to_string());
    }

    info!(
        vault = %vault_pub_key,
        requested_birthday_height,
        "scanner reset complete — vault fully purged, awaiting re-registration"
    );

    (
        StatusCode::OK,
        Json(serde_json::json!({
            "vault_pub_key": vault_pub_key,
            "status": "reset",
        })),
    )
}

/// Drop ALL in-memory FROST signing sessions. Called by the bifrost on its
/// own startup so a fresh bifrost lifecycle never inherits half-completed
/// sessions left behind by the previous bifrost lifecycle. Does NOT touch
/// keyshares or sled state — only the in-memory sign/key-image session maps
/// plus the sign prepare inflight state. Returns the count of cleared sessions.
pub async fn clear_sessions(State(state): State<Arc<AppState>>) -> impl IntoResponse {
    let cleared = state.signer.clear_all_sessions() + state.key_images.clear_all_sessions();
    (
        StatusCode::OK,
        Json(serde_json::json!({
            "cleared_sessions": cleared,
            "status": "ok"
        })),
    )
}

pub async fn get_transfers(
    State(state): State<Arc<AppState>>,
    Query(query): Query<TransfersQuery>,
) -> impl IntoResponse {
    if query.mempool.unwrap_or(false) {
        return get_mempool_transfers(state).await;
    }

    let height = query.height.unwrap_or(0);

    let vaults = match state.store.list_vaults() {
        Ok(v) => v,
        Err(e) => {
            error!(error = %e, "failed to list vaults");
            return internal_error_response(e.to_string());
        }
    };

    let mut incoming = Vec::new();
    let mut outgoing = Vec::new();
    let current_height = state.scanner.lock().current_height();

    for vault in &vaults {
        let scanner = state.scanner.lock();
        let transfers = scanner.get_transfers(&vault.vault_pub_key, height);
        drop(scanner);

        for t in transfers {
            incoming.push(TransferEntry {
                txid: t.txid,
                address: t.address,
                from_address: t.from_address,
                vault_pub_key: t.vault_pub_key,
                tx_keys: Vec::new(),
                amount: t.amount,
                fee: t.fee,
                height: t.height,
                confirmations: t.confirmations,
                note: t.note,
                locked: t.locked,
            });
        }

        if let Ok(outbound) = state.store.load_outgoing_transfers(&vault.vault_pub_key) {
            for tx in outbound.into_iter().filter(|tx| {
                is_observed_mined_record(tx) && outgoing_effective_height(tx) >= height
            }) {
                outgoing.push(transfer_entry_from_outgoing_record(
                    &tx,
                    current_height,
                    false,
                ));
            }
        }
    }

    ok_response(TransfersResponse { incoming, outgoing })
}

async fn get_mempool_transfers(state: Arc<AppState>) -> (StatusCode, Json<serde_json::Value>) {
    let vaults = match state.store.list_vaults() {
        Ok(v) => v,
        Err(e) => {
            error!(error = %e, "failed to list vaults for mempool transfers");
            return internal_error_response(e.to_string());
        }
    };

    let mut records = Vec::new();
    for vault in &vaults {
        match state.store.load_outgoing_transfers(&vault.vault_pub_key) {
            Ok(mut outbound) => records.append(&mut outbound),
            Err(e) => {
                error!(
                    error = %e,
                    vault = %vault.vault_pub_key,
                    "failed to load outgoing transfers for mempool filtering"
                );
                return internal_error_response(e.to_string());
            }
        }
    }
    records.sort_by_key(|record| record.created_at);

    let mut in_pool_hashes = HashSet::new();
    let hashes: Vec<String> = records
        .iter()
        .map(|record| record.tx_hash.clone())
        .filter(|hash| !hash.trim().is_empty())
        .collect();
    for batch in hashes.chunks(MEMPOOL_TRANSFER_BATCH_SIZE) {
        if batch.is_empty() {
            continue;
        }
        let txs = match state.daemon.get_transactions(batch).await {
            Ok(resp) => resp.txs.unwrap_or_default(),
            Err(e) => {
                error!(error = %e, "failed to query daemon mempool transactions");
                return internal_error_response(e.to_string());
            }
        };
        for tx in txs {
            if tx.in_pool.unwrap_or(false) {
                in_pool_hashes.insert(normalize_tx_hash(&tx.tx_hash));
            }
        }
    }

    let outgoing = build_mempool_outgoing_entries(&records, &in_pool_hashes);
    ok_response(TransfersResponse {
        incoming: Vec::new(),
        outgoing,
    })
}

pub async fn get_addresses(
    State(state): State<Arc<AppState>>,
    Path(vault_pub_key): Path<String>,
) -> impl IntoResponse {
    let addresses = {
        let scanner = state.scanner.lock();
        scanner.get_addresses(&vault_pub_key)
    };

    ok_response(AddressesResponse { addresses })
}

pub async fn get_outputs(
    State(state): State<Arc<AppState>>,
    Path(vault_pub_key): Path<String>,
    Query(query): Query<OutputsQuery>,
) -> impl IntoResponse {
    let outputs = match query.source {
        OutputsSource::Scanner => {
            let scanner = state.scanner.lock();
            outputs_for_view(&scanner, &vault_pub_key, query.view)
        }
        OutputsSource::Store => match stored_outputs_for_view(&state, &vault_pub_key, query.view) {
            Ok(outputs) => outputs,
            Err(e) => return internal_error_response(e),
        },
    };

    let entries: Vec<OutputEntry> = outputs.into_iter().map(output_entry_from_tracked).collect();

    ok_response(OutputsResponse { outputs: entries })
}

pub async fn get_outbounds(
    State(state): State<Arc<AppState>>,
    Path(vault_pub_key): Path<String>,
) -> impl IntoResponse {
    let outputs = match state.store.load_outputs(&vault_pub_key) {
        Ok(outputs) => outputs,
        Err(e) => {
            error!(error = %e, vault = %vault_pub_key, "failed to load outputs before refreshing outgoing transfers");
            return internal_error_response(format!("failed to load tracked outputs: {e}"));
        }
    };
    if let Err(e) = OutgoingLifecycle::new(state.store.as_ref())
        .reconcile_output_readiness_with_outputs(&vault_pub_key, &outputs)
    {
        error!(error = %e, vault = %vault_pub_key, "failed to refresh outgoing readiness from tracked outputs");
        return internal_error_response(format!(
            "failed to refresh outgoing transfer readiness: {e}"
        ));
    }
    match state.store.load_outgoing_transfers(&vault_pub_key) {
        Ok(outgoing) => {
            let entries = outgoing
                .into_iter()
                .map(outgoing_state_entry_from_record)
                .collect();
            ok_response(crate::api::types::OutgoingTransfersStateResponse {
                vault_pub_key,
                outgoing: entries,
            })
        }
        Err(e) => {
            error!(error = %e, vault = %vault_pub_key, "failed to load outgoing transfers");
            internal_error_response(format!("failed to load outgoing transfers: {e}"))
        }
    }
}

pub async fn prune_outbounds(
    State(state): State<Arc<AppState>>,
    Json(req): Json<crate::api::types::PruneOutgoingTransfersRequest>,
) -> impl IntoResponse {
    let vault_pub_key = req.vault_pub_key.trim().to_string();
    if vault_pub_key.is_empty() {
        return bad_request_response("vault_pub_key is required");
    }
    let tx_hashes: HashSet<String> = req
        .tx_hashes
        .iter()
        .map(|hash| normalize_tx_hash(hash))
        .filter(|hash| !hash.is_empty())
        .collect();
    if tx_hashes.is_empty() {
        return bad_request_response("tx_hashes must not be empty");
    }

    match OutgoingLifecycle::new(state.store.as_ref()).prune_stale(
        &vault_pub_key,
        &tx_hashes,
        req.observed_mined_only,
        req.tx_key_backed_only,
    ) {
        Ok(removed) => ok_response(crate::api::types::PruneOutgoingTransfersResponse { removed }),
        Err(e) => {
            error!(error = %e, vault = %vault_pub_key, "failed to prune outgoing transfers");
            internal_error_response(format!("failed to prune outgoing transfers: {e}"))
        }
    }
}

pub async fn apply_key_images(
    State(state): State<Arc<AppState>>,
    Json(req): Json<crate::api::types::ApplyKeyImagesRequest>,
) -> impl IntoResponse {
    let vault_pub_key = req.vault_pub_key.trim().to_string();
    if vault_pub_key.is_empty() {
        return bad_request_response("vault_pub_key is required");
    }
    if req.reconcile && req.spend {
        return bad_request_response("reconcile and spend modes are mutually exclusive");
    }
    if req.key_images.is_empty() && !req.reconcile {
        return bad_request_response("key_images must not be empty unless reconcile=true");
    }

    let key_images: Vec<(String, u32, String)> = req
        .key_images
        .iter()
        .map(|entry| {
            (
                normalize_tx_hash(&entry.tx_hash),
                entry.output_index,
                entry.key_image.trim().to_ascii_lowercase(),
            )
        })
        .collect();

    let output_lifecycle = OutputLifecycle::new(state.store.as_ref());
    let ApplyKeyImagesOutcome { applied, not_found } = if req.spend {
        let Some(spent_height) = req.spent_height.filter(|height| *height > 0) else {
            return bad_request_response("spent_height is required when spend=true");
        };
        let spent_key_images: Vec<String> = key_images
            .iter()
            .map(|(_, _, key_image)| key_image.clone())
            .filter(|key_image| !key_image.is_empty())
            .collect();
        if spent_key_images.is_empty() {
            return bad_request_response("spend=true requires at least one key_image");
        }

        {
            let mut scanner = state.scanner.lock();
            scanner.mark_spent_key_images(&vault_pub_key, &spent_key_images, spent_height);
        }

        match output_lifecycle.mark_spent_on_chain(&vault_pub_key, &spent_key_images, spent_height)
        {
            Ok(result) => result,
            Err(e) => {
                error!(error = %e, vault = vault_pub_key, "failed to persist spent key images");
                return internal_error_response(format!("failed to persist spent key images: {e}"));
            }
        }
    } else {
        {
            let mut scanner = state.scanner.lock();
            scanner.apply_key_images(&vault_pub_key, &key_images, req.reconcile);
        }

        match output_lifecycle.apply_key_images(&vault_pub_key, &key_images, req.reconcile) {
            Ok(result) => result,
            Err(e) => {
                error!(error = %e, vault = vault_pub_key, "failed to persist applied key images");
                return internal_error_response(format!("failed to persist key images: {e}"));
            }
        }
    };

    let outputs_after_apply = match state.store.load_outputs(&vault_pub_key) {
        Ok(outputs) => outputs,
        Err(e) => {
            error!(error = %e, vault = vault_pub_key, "failed to reload outputs after apply-key-images");
            return internal_error_response(format!("failed to reload outputs: {e}"));
        }
    };
    {
        let mut scanner = state.scanner.lock();
        scanner.merge_output_states(&vault_pub_key, &outputs_after_apply);
    }
    if req.reconcile {
        if let Err(e) = replay_reconciled_key_image_spends(&state, &vault_pub_key, &key_images) {
            error!(
                error = %e,
                vault = vault_pub_key,
                "failed to replay historical spends after apply-key-images reconcile"
            );
            return internal_error_response(format!(
                "failed to replay historical spends after key-image reconcile: {e}"
            ));
        }
    }

    let outputs_after = match state.store.load_outputs(&vault_pub_key) {
        Ok(outputs) => outputs,
        Err(e) => {
            error!(error = %e, vault = vault_pub_key, "failed to reload outputs after key-image replay");
            return internal_error_response(format!("failed to reload outputs: {e}"));
        }
    };
    {
        let mut scanner = state.scanner.lock();
        scanner.merge_output_states(&vault_pub_key, &outputs_after);
    }

    ok_response(crate::api::types::ApplyKeyImagesResponse { applied, not_found })
}

fn build_vault_inventory_fingerprint(
    vault_pub_key: &str,
    birthday: u64,
    registered_key_images: &[RegisteredKeyImageRef],
    outgoing: &[OutgoingTransferRecord],
) -> String {
    VaultLifecycle::build_inventory_fingerprint(
        vault_pub_key,
        birthday,
        registered_key_images,
        outgoing,
    )
}

pub async fn validate_outbound(
    State(state): State<Arc<AppState>>,
    Json(req): Json<crate::api::types::ValidateOutboundRequest>,
) -> impl IntoResponse {
    match validate_outbound_request(&state, req).await {
        Ok(response) => ok_response(response),
        Err((status, response)) => (status, Json(response)),
    }
}

pub async fn list_vault_keys(State(state): State<Arc<AppState>>) -> impl IntoResponse {
    let vaults = match state.store.list_vaults() {
        Ok(v) => v,
        Err(e) => {
            error!(error = %e, "failed to list vaults");
            return internal_error_response(format!("failed to list vaults: {e}"));
        }
    };

    let entries: Vec<crate::api::types::VaultKeysEntry> = vaults
        .iter()
        .map(|v| {
            let has_keys_by_pubkey = state
                .store
                .load_frost_keys(&v.vault_pub_key)
                .map(|k| k.is_some())
                .unwrap_or(false);
            let has_keys_by_group = if !has_keys_by_pubkey {
                state
                    .store
                    .load_frost_keys(&v.group_key)
                    .map(|k| k.is_some())
                    .unwrap_or(false)
            } else {
                true
            };
            crate::api::types::VaultKeysEntry {
                vault_pub_key: v.vault_pub_key.clone(),
                has_frost_keys: has_keys_by_group,
            }
        })
        .collect();

    ok_response(crate::api::types::ListVaultKeysResponse { vaults: entries })
}

pub async fn list_vault_states(State(state): State<Arc<AppState>>) -> impl IntoResponse {
    let mut vaults = match state.store.list_vaults() {
        Ok(v) => v,
        Err(e) => {
            error!(error = %e, "failed to list vaults");
            return internal_error_response(format!("failed to list vaults: {e}"));
        }
    };

    vaults.sort_by(|left, right| left.vault_pub_key.cmp(&right.vault_pub_key));

    let mut entries = Vec::with_capacity(vaults.len());
    for vault in vaults {
        let snapshot = match VaultLifecycle::new(state.store.as_ref()).snapshot(&vault) {
            Ok(snapshot) => snapshot,
            Err(e) => {
                error!(error = %e, vault = %vault.vault_pub_key, "failed to build vault lifecycle snapshot");
                return internal_error_response(format!(
                    "failed to build vault state for {}: {e}",
                    vault.vault_pub_key
                ));
            }
        };
        entries.push(crate::api::types::VaultStateEntry {
            inventory_fingerprint: snapshot.inventory_fingerprint,
            registered_key_image_count: snapshot.registered_key_image_count,
            registered_tx_key_count: snapshot.registered_tx_key_count,
            vault_pub_key: snapshot.vault_pub_key,
            address: snapshot.address,
            birthday: snapshot.birthday,
            rescan: snapshot.rescan,
            sync_state: match snapshot.state {
                VaultSyncState::SyncingToTip => "syncing_to_tip".to_string(),
                VaultSyncState::AtTip => "at_tip".to_string(),
            },
            scan_height: snapshot.scan_height,
        });
    }

    ok_response(crate::api::types::ListVaultStatesResponse { vaults: entries })
}

pub async fn key_export(
    State(state): State<Arc<AppState>>,
    Path(vault_pub_key): Path<String>,
) -> impl IntoResponse {
    let vault_pub_key = vault_pub_key.trim().to_string();
    if vault_pub_key.is_empty() {
        return bad_request_response("vault_pub_key is required");
    }
    let frost_bytes = state
        .store
        .load_frost_keys(&vault_pub_key)
        .ok()
        .flatten()
        .or_else(|| {
            // Try looking up by group_key if the vault_pub_key didn't match
            state.store.list_vaults().ok().and_then(|vaults| {
                vaults
                    .iter()
                    .find(|v| v.vault_pub_key == vault_pub_key)
                    .and_then(|v| state.store.load_frost_keys(&v.group_key).ok().flatten())
            })
        });
    match frost_bytes {
        Some(raw_bytes) => ok_response(crate::api::types::KeyExportResponse {
            key_bytes_hex: hex::encode(raw_bytes),
        }),
        None => error_response(
            StatusCode::NOT_FOUND,
            format!("no FROST keys found for vault {vault_pub_key}"),
        ),
    }
}

pub async fn key_restore(
    State(state): State<Arc<AppState>>,
    Json(req): Json<crate::api::types::KeyRestoreRequest>,
) -> impl IntoResponse {
    let vault_pub_key = req.vault_pub_key.trim().to_string();
    if vault_pub_key.is_empty() {
        return bad_request_response("vault_pub_key is required");
    }
    let key_bytes = match hex::decode(req.key_bytes_hex.trim()) {
        Ok(b) if !b.is_empty() => b,
        Ok(_) => return bad_request_response("key_bytes_hex must not be empty"),
        Err(e) => return bad_request_response(format!("invalid key_bytes_hex: {e}")),
    };

    let save_key = state
        .store
        .list_vaults()
        .ok()
        .and_then(|vaults| {
            vaults
                .iter()
                .find(|v| v.vault_pub_key == vault_pub_key)
                .map(|v| v.group_key.clone())
        })
        .unwrap_or_else(|| vault_pub_key.clone());

    if let Ok(Some(_)) = state.store.load_frost_keys(&save_key) {
        return ok_response(crate::api::types::KeyRestoreResponse { restored: false });
    }

    if let Err(e) = state.store.save_frost_keys(&save_key, &key_bytes) {
        error!(error = %e, vault = vault_pub_key, save_key, "failed to restore FROST keys");
        return internal_error_response(format!("failed to save restored keys: {e}"));
    }

    info!(
        vault = vault_pub_key,
        save_key, "restored FROST keys from backup"
    );
    ok_response(crate::api::types::KeyRestoreResponse { restored: true })
}

pub async fn get_fee_rate(State(state): State<Arc<AppState>>) -> impl IntoResponse {
    let rpc = match state.daemon.oxide_rpc().await {
        Ok(rpc) => rpc,
        Err(e) => {
            error!(error = %e, "failed to initialize monero-oxide rpc client");
            return internal_error_response(e.to_string());
        }
    };

    match fee::estimate_fee_rate(&rpc).await {
        Ok(rate) => match fee::per_weight(&rate) {
            Ok(per_weight) => ok_response(FeeRateResponse {
                fee_rate_per_weight: per_weight,
            }),
            Err(e) => internal_error_response(e.to_string()),
        },
        Err(e) => {
            error!(error = %e, "fee rate estimation failed");
            internal_error_response(e.to_string())
        }
    }
}

pub async fn get_chain_height(State(state): State<Arc<AppState>>) -> impl IntoResponse {
    match state.daemon.get_height().await {
        Ok(height) => ok_response(ChainHeightResponse { height }),
        Err(e) => {
            error!(error = %e, "failed to query daemon chain height");
            error_response(
                StatusCode::SERVICE_UNAVAILABLE,
                format!("daemon chain height unavailable: {e}"),
            )
        }
    }
}

pub async fn get_chain_block(
    State(state): State<Arc<AppState>>,
    Path(height): Path<u64>,
) -> impl IntoResponse {
    let tip_height = match state.daemon.get_height().await {
        Ok(height) => height,
        Err(e) => {
            error!(error = %e, "failed to query daemon chain tip");
            return error_response(
                StatusCode::SERVICE_UNAVAILABLE,
                format!("daemon chain tip unavailable: {e}"),
            );
        }
    };
    if height > tip_height {
        return error_response(
            StatusCode::CONFLICT,
            format!("requested height {height} is above chain tip {tip_height}"),
        );
    }

    match state.daemon.get_block_by_height(height).await {
        Ok(block) => ok_response(ChainBlockResponse {
            height: block.block_header.height,
            reward: block.block_header.reward,
            hash: block.block_header.hash,
            previous_hash: block.block_header.prev_hash,
            tx_hashes: block.tx_hashes.unwrap_or_default(),
        }),
        Err(e) => {
            error!(error = %e, height, "failed to query daemon chain block");
            error_response(
                StatusCode::SERVICE_UNAVAILABLE,
                format!("daemon chain block unavailable at height {height}: {e}"),
            )
        }
    }
}

pub async fn get_chain_tx_exists(
    State(state): State<Arc<AppState>>,
    Path(tx_hash): Path<String>,
) -> impl IntoResponse {
    let normalized = tx_hash.trim().to_ascii_lowercase();
    if !is_valid_chain_tx_hash(&normalized) {
        return bad_request_response("tx_hash must be a 64-character hex string");
    }

    let query = vec![normalized.clone()];
    let result = match state.daemon.get_transactions(&query).await {
        Ok(result) => result,
        Err(e) => {
            error!(error = %e, tx_hash = %normalized, "failed to query daemon transaction");
            return error_response(
                StatusCode::SERVICE_UNAVAILABLE,
                format!("daemon transaction query unavailable for {normalized}: {e}"),
            );
        }
    };

    if result
        .txs
        .unwrap_or_default()
        .iter()
        .any(|tx| tx.tx_hash.trim().eq_ignore_ascii_case(&normalized))
    {
        return ok_response(ChainTxExistsResponse { exists: true });
    }
    if result
        .missed_tx
        .unwrap_or_default()
        .iter()
        .any(|hash| hash.trim().eq_ignore_ascii_case(&normalized))
    {
        return ok_response(ChainTxExistsResponse { exists: false });
    }

    error!(
        tx_hash = %normalized,
        "ambiguous daemon get_transactions response: no tx entry and no missed marker"
    );
    internal_error_response(format!(
        "ambiguous daemon get_transactions response for {normalized}"
    ))
}

#[derive(Debug, Clone)]
struct SimAccount {
    account_id: String,
    group_key_hex: String,
    address: String,
}

#[derive(Debug, serde::Serialize)]
struct JsonRpcRequest<P: serde::Serialize> {
    jsonrpc: &'static str,
    id: &'static str,
    method: &'static str,
    params: P,
}

#[derive(Debug, serde::Deserialize)]
struct JsonRpcResponse<R> {
    result: Option<R>,
    error: Option<JsonRpcError>,
}

#[derive(Debug, serde::Deserialize)]
struct JsonRpcError {
    code: i64,
    message: String,
}

#[derive(Debug, serde::Serialize)]
struct WalletRefreshParams {}

#[derive(Debug, serde::Serialize)]
struct WalletTransferParams {
    destinations: Vec<WalletDestination>,
}

#[derive(Debug, serde::Serialize)]
struct WalletDestination {
    amount: u64,
    address: String,
}

#[derive(Debug, serde::Deserialize)]
struct WalletTransferResult {
    tx_hash: Option<String>,
    tx_hash_list: Option<Vec<String>>,
}

fn ensure_sim_helper_enabled(state: &AppState) -> Result<(), (StatusCode, serde_json::Value)> {
    if state.sim_helper_enabled {
        return Ok(());
    }
    Err((
        StatusCode::NOT_FOUND,
        error_json("simulation helper disabled"),
    ))
}

fn sim_account_from_spend_key(
    spend_key_hex: &str,
    chain_network: ChainNetworkMode,
) -> Result<SimAccount, String> {
    let group_key_hex = xmr::group_key_hex_from_spend_seed_hex(spend_key_hex)
        .map_err(|e| format!("invalid spend_key_hex: {e}"))?;
    let address = xmr::derive_address_from_spend_seed_hex(spend_key_hex, chain_network)
        .map_err(|e| format!("failed to derive XMR address: {e}"))?;
    Ok(SimAccount {
        account_id: group_key_hex.clone(),
        group_key_hex,
        address,
    })
}

async fn sync_sim_account(state: &AppState, account: &SimAccount) -> Result<(), String> {
    state
        .store
        .register_vault(
            &account.account_id,
            &account.group_key_hex,
            &account.address,
            "sim_helper",
            0,
        )
        .map_err(|e| format!("failed to register sim account: {e}"))?;

    let mut outputs = state
        .store
        .load_outputs(&account.account_id)
        .map_err(|e| format!("failed to load sim account outputs: {e}"))?;
    let mut scan_height = state
        .store
        .load_scan_height(&account.account_id)
        .map_err(|e| format!("failed to load sim account scan height: {e}"))?
        .unwrap_or(0);

    let tip_height = state
        .daemon
        .get_height()
        .await
        .map_err(|e| format!("failed to query daemon height: {e}"))?;
    let rewound_chain = tip_height < scan_height;
    if rewound_chain {
        warn!(
            account = %account.account_id,
            stored_scan_height = scan_height,
            tip_height,
            "resetting sim helper scan state after daemon height rewind"
        );
        state
            .store
            .rollback_scan_state_from_height(&account.account_id, 1)
            .map_err(|e| format!("failed to reset sim account scan state: {e}"))?;
        outputs = state
            .store
            .load_outputs(&account.account_id)
            .map_err(|e| format!("failed to reload sim account outputs: {e}"))?;
        scan_height = state
            .store
            .load_scan_height(&account.account_id)
            .map_err(|e| format!("failed to reload sim account scan height: {e}"))?
            .unwrap_or(0);
    }

    {
        let mut scanner = state.scanner.lock();
        if rewound_chain || !scanner.has_vault(&account.account_id) {
            scanner
                .register_vault(
                    &account.account_id,
                    &account.group_key_hex,
                    state.chain_network,
                    outputs.clone(),
                    Some(scan_height),
                )
                .map_err(|e| format!("failed to initialize sim account scanner: {e}"))?;
        }
        scanner.merge_output_states(&account.account_id, &outputs);
        scanner.set_current_height(scan_height);
    }

    state.scanner.lock().set_current_height(tip_height);
    if tip_height <= scan_height {
        return Ok(());
    }

    let rpc = state
        .daemon
        .oxide_rpc()
        .await
        .map_err(|e| format!("failed to initialize monero RPC client: {e}"))?;

    for height in (scan_height + 1)..=tip_height {
        let block = rpc
            .get_scannable_block_by_number(height as usize)
            .await
            .map_err(|e| format!("failed to fetch scannable block {height}: {e}"))?;
        state
            .scanner
            .lock()
            .scan_block_for_vault_with_added(&account.account_id, height, block)
            .map_err(|e| format!("failed to scan block {height}: {e}"))?;
    }

    let outputs = state.scanner.lock().get_all_outputs(&account.account_id);
    state
        .store
        .merge_scanned_outputs(&account.account_id, &outputs)
        .map_err(|e| format!("failed to persist sim account outputs: {e}"))?;
    state
        .store
        .save_scan_height(&account.account_id, tip_height)
        .map_err(|e| format!("failed to persist sim account scan height: {e}"))?;
    Ok(())
}

async fn wallet_rpc_call<P: serde::Serialize, R: DeserializeOwned>(
    wallet_rpc_url: &str,
    method: &'static str,
    params: P,
) -> Result<R, String> {
    let client = reqwest::Client::builder()
        .timeout(std::time::Duration::from_secs(30))
        .build()
        .map_err(|e| format!("failed to build wallet-rpc client: {e}"))?;
    let resp = client
        .post(format!("{}/json_rpc", wallet_rpc_url.trim_end_matches('/')))
        .json(&JsonRpcRequest {
            jsonrpc: "2.0",
            id: "0",
            method,
            params,
        })
        .send()
        .await
        .map_err(|e| format!("wallet-rpc {method} request failed: {e}"))?;
    let status = resp.status();
    let body = resp
        .bytes()
        .await
        .map_err(|e| format!("failed to read wallet-rpc {method} response: {e}"))?;
    if !status.is_success() {
        return Err(format!(
            "wallet-rpc {method} returned {status}: {}",
            String::from_utf8_lossy(&body)
        ));
    }

    let decoded: JsonRpcResponse<R> = serde_json::from_slice(&body)
        .map_err(|e| format!("failed to decode wallet-rpc {method} response: {e}"))?;
    if let Some(result) = decoded.result {
        return Ok(result);
    }
    if let Some(error) = decoded.error {
        return Err(format!(
            "wallet-rpc {method} failed with {}: {}",
            error.code, error.message
        ));
    }
    Err(format!(
        "wallet-rpc {method} returned neither result nor error"
    ))
}

pub async fn sim_get_balance(
    State(state): State<Arc<AppState>>,
    Json(req): Json<SimBalanceRequest>,
) -> impl IntoResponse {
    if let Err(err) = ensure_sim_helper_enabled(&state) {
        return (err.0, Json(err.1));
    }

    let account = match sim_account_from_spend_key(&req.spend_key_hex, state.chain_network) {
        Ok(account) => account,
        Err(e) => return bad_request_response(e),
    };
    if let Err(e) = sync_sim_account(&state, &account).await {
        error!(error = %e, account = %account.account_id, "failed to sync sim balance");
        return error_response(StatusCode::SERVICE_UNAVAILABLE, e);
    }

    let (balance_atomic, unlocked_balance_atomic) = {
        let scanner = state.scanner.lock();
        scanner.get_balance(&account.account_id)
    };

    ok_response(SimBalanceResponse {
        address: account.address,
        balance_atomic,
        unlocked_balance_atomic,
    })
}

pub async fn sim_transfer(
    State(state): State<Arc<AppState>>,
    Json(req): Json<SimTransferRequest>,
) -> impl IntoResponse {
    if let Err(err) = ensure_sim_helper_enabled(&state) {
        return (err.0, Json(err.1));
    }
    if req.to_address.trim().is_empty() {
        return bad_request_response("to_address is required");
    }
    if req.amount_atomic == 0 {
        return bad_request_response("amount_atomic must be > 0");
    }

    let account = match sim_account_from_spend_key(&req.spend_key_hex, state.chain_network) {
        Ok(account) => account,
        Err(e) => return bad_request_response(e),
    };
    if let Err(e) = sync_sim_account(&state, &account).await {
        error!(error = %e, account = %account.account_id, "failed to sync sim transfer account");
        return error_response(StatusCode::SERVICE_UNAVAILABLE, e);
    }

    let tip_height = match state.daemon.get_height().await {
        Ok(height) => height,
        Err(e) => {
            return error_response(
                StatusCode::SERVICE_UNAVAILABLE,
                format!("daemon height unavailable: {e}"),
            )
        }
    };
    let header = match state.daemon.get_block_header_by_height(tip_height).await {
        Ok(header) => header.block_header,
        Err(e) => {
            return error_response(
                StatusCode::SERVICE_UNAVAILABLE,
                format!("daemon header unavailable: {e}"),
            )
        }
    };
    let rpc = match state.daemon.oxide_rpc().await {
        Ok(rpc) => rpc,
        Err(e) => return error_response(StatusCode::SERVICE_UNAVAILABLE, e.to_string()),
    };
    let fee_rate = match fee::estimate_fee_rate(&rpc).await {
        Ok(rate) => rate,
        Err(e) => return error_response(StatusCode::SERVICE_UNAVAILABLE, e.to_string()),
    };
    let available_outputs = state.scanner.lock().get_outputs(&account.account_id);
    let unsigned = match builder::build_unsigned_transaction(
        &rpc,
        req.to_address.trim(),
        req.amount_atomic,
        0,
        false,
        req.memo.as_deref().unwrap_or_default(),
        fee_rate,
        &format!("sim-helper:{}", Uuid::new_v4()),
        header.major_version,
        tip_height as usize,
        &available_outputs,
        Some(&account.address),
        &HashSet::new(),
        state.chain_network,
        None,
        None,
    )
    .await
    {
        Ok(unsigned) => unsigned,
        Err(e) => return bad_request_response(e.to_string()),
    };

    let refs = unsigned
        .selected_inputs
        .iter()
        .map(|input| OutputRef {
            tx_hash: input.tx_hash.clone(),
            output_index: input.output_index,
        })
        .collect::<Vec<_>>();
    let ref_pairs = refs
        .iter()
        .map(|r| (r.tx_hash.clone(), r.output_index))
        .collect::<Vec<_>>();
    let session_id = Uuid::new_v4().to_string();
    let msg_id = session_id.clone();
    let output_lifecycle = OutputLifecycle::new(state.store.as_ref());
    if let Err(e) = output_lifecycle.reserve(
        &account.account_id,
        &refs,
        &session_id,
        &msg_id,
        chrono::Utc::now().timestamp() + 300,
    ) {
        return error_response(StatusCode::CONFLICT, e.to_string());
    }

    let spend_scalar = match xmr::spend_scalar_from_seed_hex(&req.spend_key_hex) {
        Ok(scalar) => scalar,
        Err(e) => {
            let _ = output_lifecycle.release_session(&account.account_id, &session_id, &msg_id);
            return bad_request_response(e.to_string());
        }
    };
    let tx = match unsigned.signable.sign(&mut OsRng, &spend_scalar) {
        Ok(tx) => tx,
        Err(e) => {
            let _ = output_lifecycle.release_session(&account.account_id, &session_id, &msg_id);
            return bad_request_response(e.to_string());
        }
    };
    let tx_hash = hex::encode(tx.hash());
    let tx_hex = hex::encode(tx.serialize());
    if let Err(e) = output_lifecycle.bind_reservation_tx_hash(
        &account.account_id,
        &session_id,
        &msg_id,
        &tx_hash,
    ) {
        let _ = output_lifecycle.release_session(&account.account_id, &session_id, &msg_id);
        return internal_error_response(format!("failed to bind sim transfer reservation: {e}"));
    }

    match state.daemon.send_raw_transaction(&tx_hex).await {
        Ok(resp) if resp.status == "OK" => {
            if let Err(e) =
                output_lifecycle.finalize_for_msg(&account.account_id, &msg_id, &tx_hash)
            {
                error!(error = %e, tx_hash = %tx_hash, "failed to finalize sim transfer reservation");
            }
            state
                .scanner
                .lock()
                .mark_spent_by_refs(&account.account_id, &ref_pairs);
            ok_response(SimTransferResponse { tx_hash })
        }
        Ok(resp) => {
            let _ = output_lifecycle.release_session(&account.account_id, &session_id, &msg_id);
            bad_request_response(resp.reason.unwrap_or(resp.status))
        }
        Err(e) => {
            let _ = output_lifecycle.release_session(&account.account_id, &session_id, &msg_id);
            error_response(
                StatusCode::SERVICE_UNAVAILABLE,
                format!("failed to broadcast sim transfer: {e}"),
            )
        }
    }
}

pub async fn sim_faucet(
    State(state): State<Arc<AppState>>,
    Json(req): Json<SimFaucetRequest>,
) -> impl IntoResponse {
    if let Err(err) = ensure_sim_helper_enabled(&state) {
        return (err.0, Json(err.1));
    }
    if req.to_address.trim().is_empty() {
        return bad_request_response("to_address is required");
    }
    if req.amount_atomic == 0 {
        return bad_request_response("amount_atomic must be > 0");
    }

    let Some(wallet_rpc_url) = state.sim_helper_wallet_rpc_url.as_deref() else {
        return internal_error_response("sim helper wallet-rpc URL not configured");
    };

    if let Err(e) =
        wallet_rpc_call::<_, serde_json::Value>(wallet_rpc_url, "refresh", WalletRefreshParams {})
            .await
    {
        return error_response(
            StatusCode::SERVICE_UNAVAILABLE,
            format!("wallet-rpc refresh failed: {e}"),
        );
    }

    let transfer = match wallet_rpc_call::<_, WalletTransferResult>(
        wallet_rpc_url,
        "transfer",
        WalletTransferParams {
            destinations: vec![WalletDestination {
                amount: req.amount_atomic,
                address: req.to_address.trim().to_string(),
            }],
        },
    )
    .await
    {
        Ok(transfer) => transfer,
        Err(e) => return error_response(StatusCode::SERVICE_UNAVAILABLE, e),
    };

    let tx_hash = transfer
        .tx_hash
        .or_else(|| {
            transfer
                .tx_hash_list
                .and_then(|mut hashes| hashes.drain(..).next())
        })
        .filter(|hash| !hash.trim().is_empty());
    match tx_hash {
        Some(tx_hash) => ok_response(SimTransferResponse { tx_hash }),
        None => error_response(
            StatusCode::BAD_GATEWAY,
            "wallet-rpc transfer returned success without tx_hash",
        ),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::Arc;

    use axum::{body::to_bytes, http::Uri, response::IntoResponse};
    use curve25519_dalek::{constants::ED25519_BASEPOINT_TABLE, Scalar};
    use monero_wallet::transaction::{Input, Pruned, Timelock, Transaction, TransactionPrefix};
    use tempfile::TempDir;

    const VALID_VAULT_PUB_KEY: &str =
        "thorpub1addwnpepq0rf954ne8ahjl0es3k32prvngy6tsye4cr8wdqsrmmdqcsvyzrujqexwxp";
    const BOOTSTRAP_VAULT_PUB_KEY: &str =
        "tthorpub1zcjduepqzupdpx7334mjeuuggnvxv3ctkghu0qcz2eml30tgu9ukw58z6uusmmxmys";
    const BOOTSTRAP_GROUP_KEY_HEX: &str =
        "1702d09bd18d772cf38844d866470bb22fc783025677f8bd68e1796750e2d739";
    const BOOTSTRAP_SPEND_KEY_HEX: &str =
        "ca82c0463f083f8f0d0bead33828c2cbae6d45350c28bd11c7023e532537640e";

    fn sample_outgoing(
        tx_hash: &str,
        vault_pub_key: &str,
        from: &str,
        to: &str,
        created_at: i64,
    ) -> crate::state::store::OutgoingTransferRecord {
        crate::state::store::OutgoingTransferRecord {
            tx_hash: tx_hash.to_string(),
            tx_hex: "deadbeef".to_string(),
            tx_keys: vec![],
            registered_inventory: false,
            has_owned_change: None,
            local_change_materialized: None,
            key_images_ready: None,
            msg_id: "msg-id".to_string(),
            vault_pub_key: vault_pub_key.to_string(),
            from_address: from.to_string(),
            to_address: to.to_string(),
            amount: 12345,
            fee: 99,
            memo: "OUT:TEST".to_string(),
            planned_height: 77,
            observed_height: Some(77),
            status: OutgoingTransferStatus::ObservedMined,
            broadcasted_at: Some(created_at),
            observed_at: Some(created_at),
            reported_effective_height: None,
            created_at,
        }
    }

    fn sample_reserved_output(msg_id: &str) -> TrackedOutput {
        TrackedOutput {
            tx_hash: "input-hash-1".to_string(),
            output_index: 0,
            amount: 1_000_000_000,
            height: 100,
            key_image: Some("key-image-0".to_string()),
            spent_by_key_image_height: None,
            state: OutputState::Reserved,
            reservation: Some(crate::state::store::OutputReservation {
                session_id: "session-1".to_string(),
                msg_id: msg_id.to_string(),
                reserved_at: 1000,
                expires_at: 2000,
                tx_hash_candidate: None,
            }),
            subaddress_major: 0,
            subaddress_minor: 0,
            wallet_output_hex: Some("wallet-output-0".to_string()),
            address: None,
            note: None,
        }
    }

    fn sample_output(tx_hash: &str, output_index: u32, state: OutputState) -> TrackedOutput {
        TrackedOutput {
            tx_hash: tx_hash.to_string(),
            output_index,
            amount: 1_000_000_000,
            height: 100,
            key_image: Some(format!("key-image-{output_index}")),
            spent_by_key_image_height: None,
            state,
            reservation: None,
            subaddress_major: 0,
            subaddress_minor: output_index,
            wallet_output_hex: Some(format!("wallet-output-{output_index}")),
            address: None,
            note: None,
        }
    }

    fn test_group_key_hex() -> String {
        let base = Scalar::from(7u64);
        hex::encode((&base * ED25519_BASEPOINT_TABLE).compress().to_bytes())
    }

    fn point_from_scalar(value: u64) -> curve25519_dalek::edwards::EdwardsPoint {
        &Scalar::from(value) * ED25519_BASEPOINT_TABLE
    }

    fn test_key_image_hex(value: u64) -> String {
        hex::encode(point_from_scalar(value).compress().to_bytes())
    }

    fn test_pruned_tx_hex_with_key_image(value: u64) -> String {
        let tx = Transaction::<Pruned>::V2 {
            prefix: TransactionPrefix {
                additional_timelock: Timelock::None,
                inputs: vec![Input::ToKey {
                    amount: None,
                    key_offsets: vec![1],
                    key_image: point_from_scalar(value),
                }],
                outputs: vec![],
                extra: vec![],
            },
            proofs: None,
        };
        hex::encode(tx.serialize())
    }

    fn outputs_test_state(
        outputs: Vec<TrackedOutput>,
    ) -> (Arc<AppState>, tempfile::TempDir, String) {
        let data_dir = tempfile::tempdir().expect("failed to create tempdir");
        let store = Arc::new(
            crate::state::Store::open(data_dir.path()).expect("failed to open test store"),
        );
        let mut scanner = Scanner::new();
        let vault_pub_key = "thorpub1vaulttest".to_string();
        scanner
            .register_vault(
                &vault_pub_key,
                &test_group_key_hex(),
                crate::config::ChainNetworkMode::Mocknet,
                outputs,
                None,
            )
            .expect("failed to register test vault");
        scanner.set_current_height(200);

        let daemon = DaemonClient::new("http://127.0.0.1:18081", 1024 * 1024);
        let app_state = Arc::new(AppState {
            dkg: DkgCoordinator::new(store.clone(), crate::config::ChainNetworkMode::Mocknet, 4),
            signer: SignCoordinator::new(
                store.clone(),
                daemon.clone(),
                crate::config::ChainNetworkMode::Mocknet,
                4,
            ),
            key_images: KeyImageCoordinator::new(store.clone(), 4),
            scanner: parking_lot::Mutex::new(scanner),
            scan_lifecycle: tokio::sync::Mutex::new(()),
            daemon,
            store,
            chain_network: crate::config::ChainNetworkMode::Mocknet,
            max_sign_scan_lag_blocks: 3,
            sim_helper_enabled: false,
            sim_helper_wallet_rpc_url: None,
        });
        (app_state, data_dir, vault_pub_key)
    }

    async fn do_get(
        state: Arc<AppState>,
        vault_pub_key: String,
        query: Query<OutputsQuery>,
    ) -> (StatusCode, serde_json::Value) {
        let response = get_outputs(State(state), Path(vault_pub_key), query)
            .await
            .into_response();
        let status = response.status();
        let body = to_bytes(response.into_body(), usize::MAX)
            .await
            .expect("failed to read response body");
        let json = if body.is_empty() {
            serde_json::json!({})
        } else {
            serde_json::from_slice(&body).expect("failed to parse json")
        };
        (status, json)
    }

    fn chain_test_state(daemon_url: &str) -> (Arc<AppState>, TempDir) {
        let data_dir = tempfile::tempdir().expect("failed to create tempdir");
        let store = Arc::new(
            crate::state::Store::open(data_dir.path()).expect("failed to open test store"),
        );
        let daemon = DaemonClient::new(daemon_url, 1024 * 1024);
        let app_state = Arc::new(AppState {
            dkg: DkgCoordinator::new(store.clone(), crate::config::ChainNetworkMode::Mocknet, 4),
            signer: SignCoordinator::new(
                store.clone(),
                daemon.clone(),
                crate::config::ChainNetworkMode::Mocknet,
                4,
            ),
            key_images: KeyImageCoordinator::new(store.clone(), 4),
            scanner: parking_lot::Mutex::new(Scanner::new()),
            scan_lifecycle: tokio::sync::Mutex::new(()),
            daemon,
            store,
            chain_network: crate::config::ChainNetworkMode::Mocknet,
            max_sign_scan_lag_blocks: 3,
            sim_helper_enabled: false,
            sim_helper_wallet_rpc_url: None,
        });
        (app_state, data_dir)
    }

    async fn response_to_status_json(
        response: axum::response::Response,
    ) -> (StatusCode, serde_json::Value) {
        let status = response.status();
        let body = to_bytes(response.into_body(), usize::MAX)
            .await
            .expect("failed to read response body");
        let json = if body.is_empty() {
            serde_json::json!({})
        } else {
            serde_json::from_slice(&body).expect("failed to parse json response body")
        };
        (status, json)
    }

    #[tokio::test]
    async fn apply_key_images_spend_marks_matching_outputs() {
        let mut already_spent = sample_output(
            "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
            1,
            OutputState::Spent,
        );
        already_spent.spent_by_key_image_height = None;
        let (state, _data_dir, vault_pub_key) = outputs_test_state(vec![
            sample_output(
                "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
                0,
                OutputState::Unspent,
            ),
            already_spent,
        ]);
        state
            .store
            .save_outputs(
                &vault_pub_key,
                &state.scanner.lock().get_all_outputs(&vault_pub_key),
            )
            .expect("failed to seed outputs");

        let response = apply_key_images(
            State(state.clone()),
            Json(crate::api::types::ApplyKeyImagesRequest {
                vault_pub_key: vault_pub_key.clone(),
                reconcile: false,
                spend: true,
                spent_height: Some(123),
                key_images: vec![
                    crate::api::types::KeyImageEntry {
                        tx_hash: String::new(),
                        output_index: 0,
                        key_image: "key-image-0".to_string(),
                    },
                    crate::api::types::KeyImageEntry {
                        tx_hash: String::new(),
                        output_index: 0,
                        key_image: "key-image-1".to_string(),
                    },
                    crate::api::types::KeyImageEntry {
                        tx_hash: String::new(),
                        output_index: 0,
                        key_image: "key-image-missing".to_string(),
                    },
                ],
            }),
        )
        .await
        .into_response();
        let (status, body) = response_to_status_json(response).await;
        assert_eq!(status, StatusCode::OK);
        assert_eq!(body["applied"], serde_json::json!(2));
        assert_eq!(body["not_found"], serde_json::json!(1));

        let outputs = state
            .store
            .load_outputs(&vault_pub_key)
            .expect("failed to reload outputs");
        let by_index: HashMap<_, _> = outputs
            .into_iter()
            .map(|output| (output.output_index, output))
            .collect();

        assert_eq!(by_index.get(&0).unwrap().state, OutputState::Spent);
        assert_eq!(
            by_index.get(&0).unwrap().spent_by_key_image_height,
            Some(123)
        );
        assert_eq!(by_index.get(&1).unwrap().state, OutputState::Spent);
        assert_eq!(
            by_index.get(&1).unwrap().spent_by_key_image_height,
            Some(123)
        );
    }

    #[tokio::test]
    async fn apply_key_images_spend_requires_chain_height() {
        let (state, _data_dir, vault_pub_key) = outputs_test_state(vec![sample_output(
            "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
            0,
            OutputState::Unspent,
        )]);
        state
            .store
            .save_outputs(
                &vault_pub_key,
                &state.scanner.lock().get_all_outputs(&vault_pub_key),
            )
            .expect("failed to seed outputs");

        let response = apply_key_images(
            State(state),
            Json(crate::api::types::ApplyKeyImagesRequest {
                vault_pub_key,
                reconcile: false,
                spend: true,
                spent_height: None,
                key_images: vec![crate::api::types::KeyImageEntry {
                    tx_hash: String::new(),
                    output_index: 0,
                    key_image: "key-image-0".to_string(),
                }],
            }),
        )
        .await
        .into_response();
        let (status, body) = response_to_status_json(response).await;
        assert_eq!(status, StatusCode::BAD_REQUEST);
        assert_eq!(
            body["error"].as_str().unwrap_or_default(),
            "spent_height is required when spend=true"
        );
    }

    #[test]
    fn commit_scan_batch_single_block_marks_spends_and_persists_meta() {
        let key_image = test_key_image_hex(9);
        let output = TrackedOutput {
            tx_hash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa".to_string(),
            output_index: 0,
            amount: 50,
            height: 100,
            key_image: Some(key_image.clone()),
            spent_by_key_image_height: None,
            state: OutputState::Unspent,
            reservation: None,
            subaddress_major: 0,
            subaddress_minor: 0,
            wallet_output_hex: Some("aa".to_string()),
            address: Some("48vault".to_string()),
            note: None,
        };
        let (state, _data_dir, vault_pub_key) = outputs_test_state(vec![output.clone()]);
        state
            .store
            .save_outputs(
                &vault_pub_key,
                &state.scanner.lock().get_all_outputs(&vault_pub_key),
            )
            .expect("failed to seed outputs");
        state
            .store
            .save_registered_key_images(
                &vault_pub_key,
                &[RegisteredKeyImageRef {
                    tx_hash: output.tx_hash.clone(),
                    output_index: output.output_index,
                    key_image: key_image.clone(),
                }],
            )
            .expect("failed to seed registered key image inventory");

        let block_transactions =
            vec![
                parse_pruned_transaction_hex(&test_pruned_tx_hex_with_key_image(9))
                    .expect("failed to build test transaction"),
            ];
        let spent_key_image_txs = collect_spent_key_images_by_tx(
            &block_transactions,
            &["abababababababababababababababababababababababababababababababab".to_string()],
        );
        let spent_key_images: Vec<String> = spent_key_image_txs.keys().cloned().collect();
        let batch = vec![ScannedBlockEntry {
            height: 121,
            block_hash: "hash121".to_string(),
            parent_hash: "hash120".to_string(),
            spent_key_images,
            spent_key_image_txs,
            added_outputs: Vec::new(),
        }];
        let persisted = commit_scan_batch(&state, &vault_pub_key, &batch, 120)
            .expect("failed to commit scan batch");
        assert_eq!(persisted.added_refs_per_block.len(), 1);
        assert!(persisted.added_refs_per_block[0].is_empty());

        let outputs = state
            .store
            .load_outputs(&vault_pub_key)
            .expect("failed to load outputs");
        assert_eq!(outputs.len(), 1);
        assert_eq!(outputs[0].state, OutputState::Spent);
        assert_eq!(outputs[0].spent_by_key_image_height, Some(121));

        let meta = state
            .store
            .load_scan_block_meta(&vault_pub_key, 121)
            .expect("failed to load scan block meta")
            .expect("scan block meta missing");
        assert_eq!(meta.height, 121);
        assert_eq!(meta.block_hash, "hash121");
        assert_eq!(meta.parent_hash, "hash120");
        assert_eq!(meta.added_outputs.len(), 0);
        assert_eq!(meta.spent_key_images, vec![test_key_image_hex(9)]);
        assert_eq!(meta.spent_by_key_images.len(), 1);
        assert_eq!(meta.spent_by_key_images[0].tx_hash, output.tx_hash);
        assert_eq!(
            meta.spent_by_key_images[0].output_index,
            output.output_index
        );
    }

    #[test]
    fn commit_scan_batch_loads_key_image_from_registered_inventory_and_marks_spent() {
        let key_image = test_key_image_hex(11);
        let output = TrackedOutput {
            tx_hash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb".to_string(),
            output_index: 0,
            amount: 50,
            height: 100,
            key_image: None,
            spent_by_key_image_height: None,
            state: OutputState::Unspent,
            reservation: None,
            subaddress_major: 0,
            subaddress_minor: 0,
            wallet_output_hex: Some("aa".to_string()),
            address: Some("48vault".to_string()),
            note: None,
        };
        let (state, _data_dir, vault_pub_key) = outputs_test_state(vec![output.clone()]);
        state
            .store
            .save_outputs(
                &vault_pub_key,
                &state.scanner.lock().get_all_outputs(&vault_pub_key),
            )
            .expect("failed to seed outputs");
        state
            .store
            .save_registered_key_images(
                &vault_pub_key,
                &[RegisteredKeyImageRef {
                    tx_hash: output.tx_hash.clone(),
                    output_index: output.output_index,
                    key_image: key_image.clone(),
                }],
            )
            .expect("failed to seed registered key image inventory");

        let block_transactions =
            vec![
                parse_pruned_transaction_hex(&test_pruned_tx_hex_with_key_image(11))
                    .expect("failed to build test transaction"),
            ];
        let spent_key_image_txs = collect_spent_key_images_by_tx(
            &block_transactions,
            &["babababababababababababababababababababababababababababababababa".to_string()],
        );
        let spent_key_images: Vec<String> = spent_key_image_txs.keys().cloned().collect();
        let batch = vec![ScannedBlockEntry {
            height: 121,
            block_hash: "hash121".to_string(),
            parent_hash: "hash120".to_string(),
            spent_key_images,
            spent_key_image_txs,
            added_outputs: Vec::new(),
        }];
        commit_scan_batch(&state, &vault_pub_key, &batch, 120)
            .expect("failed to commit scan batch");

        let outputs = state
            .store
            .load_outputs(&vault_pub_key)
            .expect("failed to load outputs");
        assert_eq!(outputs.len(), 1);
        assert_eq!(outputs[0].key_image.as_deref(), Some(key_image.as_str()));
        assert_eq!(outputs[0].state, OutputState::Spent);
        assert_eq!(outputs[0].spent_by_key_image_height, Some(121));
    }

    #[test]
    fn commit_scan_batch_multi_block_marks_historical_spend_in_rescan_window() {
        let key_image = test_key_image_hex(13);
        let mut output = TrackedOutput {
            tx_hash: "cdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcd".to_string(),
            output_index: 0,
            amount: 50,
            height: 100,
            key_image: None,
            spent_by_key_image_height: None,
            state: OutputState::Unspent,
            reservation: None,
            subaddress_major: 0,
            subaddress_minor: 0,
            wallet_output_hex: Some("aa".to_string()),
            address: Some("48vault".to_string()),
            note: None,
        };
        let (state, _data_dir, vault_pub_key) = outputs_test_state(vec![output.clone()]);
        state
            .store
            .save_outputs(
                &vault_pub_key,
                &state.scanner.lock().get_all_outputs(&vault_pub_key),
            )
            .expect("failed to seed outputs");
        state
            .store
            .save_registered_key_images(
                &vault_pub_key,
                &[RegisteredKeyImageRef {
                    tx_hash: output.tx_hash.clone(),
                    output_index: output.output_index,
                    key_image: key_image.clone(),
                }],
            )
            .expect("failed to seed registered key image inventory");

        let mut batch: Vec<ScannedBlockEntry> = Vec::new();
        for height in 101..=250 {
            let spent_key_image_txs = if height == 110 {
                let txs = vec![
                    parse_pruned_transaction_hex(&test_pruned_tx_hex_with_key_image(13))
                        .expect("failed to build test transaction"),
                ];
                collect_spent_key_images_by_tx(
                    &txs,
                    &[
                        "cacacacacacacacacacacacacacacacacacacacacacacacacacacacacacacaca"
                            .to_string(),
                    ],
                )
            } else {
                HashMap::new()
            };
            let spent_key_images: Vec<String> = spent_key_image_txs.keys().cloned().collect();
            batch.push(ScannedBlockEntry {
                height,
                block_hash: format!("hash{height}"),
                parent_hash: format!("hash{}", height - 1),
                spent_key_images,
                spent_key_image_txs,
                added_outputs: Vec::new(),
            });
        }
        commit_scan_batch(&state, &vault_pub_key, &batch, 120)
            .expect("failed to commit scan batch");

        let outputs = state
            .store
            .load_outputs(&vault_pub_key)
            .expect("failed to load outputs after commit");
        output.key_image = Some(key_image.clone());
        assert_eq!(outputs.len(), 1);
        assert_eq!(outputs[0].key_image.as_deref(), Some(key_image.as_str()));
        assert_eq!(outputs[0].state, OutputState::Spent);
        assert_eq!(outputs[0].spent_by_key_image_height, Some(110));
    }

    #[test]
    fn unauthorized_spend_entries_emit_full_output_to_unknown_destination() {
        let (state, _data_dir, vault_pub_key) = outputs_test_state(vec![]);
        let spend_tx =
            "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa".to_string();
        let key_image = test_key_image_hex(21);
        let output = TrackedOutput {
            tx_hash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb".to_string(),
            output_index: 2,
            amount: 123_456_789,
            height: 100,
            key_image: Some(key_image.clone()),
            spent_by_key_image_height: Some(121),
            state: OutputState::Spent,
            reservation: None,
            subaddress_major: 0,
            subaddress_minor: 0,
            wallet_output_hex: Some("aa".to_string()),
            address: Some("48vault".to_string()),
            note: None,
        };
        let mut spent_key_image_txs = HashMap::new();
        spent_key_image_txs.insert(key_image, spend_tx.clone());
        let batch = vec![ScannedBlockEntry {
            height: 121,
            block_hash: "hash121".to_string(),
            parent_hash: "hash120".to_string(),
            spent_key_images: Vec::new(),
            spent_key_image_txs,
            added_outputs: Vec::new(),
        }];

        let entries =
            unauthorized_spend_entries(&state, &vault_pub_key, "48vault", &batch, &[output], 130);

        assert_eq!(entries.len(), 1);
        assert_eq!(entries[0].txid, spend_tx);
        assert_eq!(entries[0].from_address.as_deref(), Some("48vault"));
        assert!(entries[0].address.starts_with("xmrunknown1"));
        assert_eq!(entries[0].amount, 123_456_789);
        assert_eq!(entries[0].fee, 0);
        assert_eq!(entries[0].note, "NOOP:UNAUTHORIZED");
        assert!(entries[0].tx_keys.is_empty());
    }

    #[test]
    fn unauthorized_spend_entries_skip_authorized_outgoing_records() {
        let (state, _data_dir, vault_pub_key) = outputs_test_state(vec![]);
        let spend_tx =
            "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc".to_string();
        state
            .store
            .save_outgoing_transfer(&sample_outgoing(
                &spend_tx,
                &vault_pub_key,
                "48vault",
                "48dest",
                1,
            ))
            .expect("failed to seed outgoing transfer");
        let key_image = test_key_image_hex(22);
        let output = TrackedOutput {
            tx_hash: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd".to_string(),
            output_index: 0,
            amount: 123,
            height: 100,
            key_image: Some(key_image.clone()),
            spent_by_key_image_height: Some(121),
            state: OutputState::Spent,
            reservation: None,
            subaddress_major: 0,
            subaddress_minor: 0,
            wallet_output_hex: Some("aa".to_string()),
            address: Some("48vault".to_string()),
            note: None,
        };
        let mut spent_key_image_txs = HashMap::new();
        spent_key_image_txs.insert(key_image, spend_tx);
        let batch = vec![ScannedBlockEntry {
            height: 121,
            block_hash: "hash121".to_string(),
            parent_hash: "hash120".to_string(),
            spent_key_images: Vec::new(),
            spent_key_image_txs,
            added_outputs: Vec::new(),
        }];

        let entries =
            unauthorized_spend_entries(&state, &vault_pub_key, "48vault", &batch, &[output], 130);

        assert!(entries.is_empty());
    }

    #[tokio::test]
    async fn apply_key_images_reconcile_replays_historical_spends() {
        let (state, _data_dir) = chain_test_state("http://127.0.0.1:18081");
        let vault_pub_key = VALID_VAULT_PUB_KEY.to_string();
        let mut output = sample_output(
            "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
            0,
            OutputState::Unspent,
        );
        output.height = 119;
        output.key_image = None;

        state
            .scanner
            .lock()
            .register_vault(
                &vault_pub_key,
                &test_group_key_hex(),
                crate::config::ChainNetworkMode::Mocknet,
                vec![output.clone()],
                Some(119),
            )
            .expect("failed to register scanner vault");
        state.scanner.lock().set_current_height(120);
        state
            .store
            .save_outputs(&vault_pub_key, &[output.clone()])
            .expect("failed to seed outputs");
        state
            .store
            .save_scan_height(&vault_pub_key, 120)
            .expect("failed to seed scan height");
        state
            .store
            .save_scan_block_meta(
                &vault_pub_key,
                &ScanBlockMeta {
                    height: 120,
                    block_hash: "hash120".to_string(),
                    parent_hash: "hash119".to_string(),
                    added_outputs: vec![],
                    spent_key_images: vec![test_key_image_hex(7)],
                    spent_by_key_images: vec![],
                },
            )
            .expect("failed to seed scan block metadata");

        let response = apply_key_images(
            State(state.clone()),
            Json(crate::api::types::ApplyKeyImagesRequest {
                vault_pub_key: vault_pub_key.clone(),
                reconcile: true,
                spend: false,
                spent_height: None,
                key_images: vec![crate::api::types::KeyImageEntry {
                    tx_hash: output.tx_hash.clone(),
                    output_index: output.output_index,
                    key_image: test_key_image_hex(7),
                }],
            }),
        )
        .await
        .into_response();
        let (status, body) = response_to_status_json(response).await;
        assert_eq!(status, StatusCode::OK, "body={body}");
        assert_eq!(body["applied"], serde_json::json!(1));
        assert_eq!(body["not_found"], serde_json::json!(0));

        let outputs = state
            .store
            .load_outputs(&vault_pub_key)
            .expect("failed to reload outputs");
        let expected_key_image = test_key_image_hex(7);
        assert_eq!(outputs.len(), 1);
        assert_eq!(outputs[0].state, OutputState::Spent);
        assert_eq!(outputs[0].spent_by_key_image_height, Some(120));
        assert_eq!(
            outputs[0].key_image.as_deref(),
            Some(expected_key_image.as_str())
        );
        let meta = state
            .store
            .load_scan_block_meta(&vault_pub_key, 120)
            .expect("failed to load scan block metadata")
            .expect("scan block metadata missing");
        assert_eq!(meta.spent_key_images, vec![expected_key_image]);
        assert_eq!(meta.spent_by_key_images.len(), 1);
        assert_eq!(meta.spent_by_key_images[0].tx_hash, output.tx_hash);
        assert_eq!(
            meta.spent_by_key_images[0].output_index,
            output.output_index
        );
    }

    #[test]
    fn build_mempool_outgoing_entries_filters_and_normalizes_hashes() {
        let records = vec![
            sample_outgoing(
                "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
                "thorpub1a",
                "4froma",
                "8toa",
                1,
            ),
            sample_outgoing(
                "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
                "thorpub1b",
                "4fromb",
                "8tob",
                2,
            ),
        ];
        let in_pool_hashes = HashSet::from([normalize_tx_hash(
            "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
        )]);

        let outgoing = build_mempool_outgoing_entries(&records, &in_pool_hashes);
        assert_eq!(outgoing.len(), 1);
        assert_eq!(
            outgoing[0].txid,
            "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
        );
        assert_eq!(outgoing[0].confirmations, 0);
        assert!(outgoing[0].locked);
    }

    #[test]
    fn transfer_entry_from_outgoing_record_sets_mempool_fields() {
        let record = sample_outgoing(
            "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
            "thorpub1c",
            "4fromc",
            "8toc",
            3,
        );
        let mempool_entry = transfer_entry_from_outgoing_record(&record, 100, true);
        assert_eq!(mempool_entry.confirmations, 0);
        assert!(mempool_entry.locked);

        let confirmed_entry = transfer_entry_from_outgoing_record(&record, 100, false);
        assert_eq!(confirmed_entry.confirmations, 23);
        assert!(!confirmed_entry.locked);
    }

    #[test]
    fn promote_outgoing_records_observed_in_block_marks_matching_pending_record_mined() {
        let (state, _data_dir) = chain_test_state("http://127.0.0.1:18081");
        let mut record = sample_outgoing(
            "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
            VALID_VAULT_PUB_KEY,
            "48fromd",
            "48tod",
            4,
        );
        record.status = OutgoingTransferStatus::SignedPendingObservation;
        record.observed_height = None;
        record.observed_at = None;

        state
            .store
            .save_outgoing_transfer(&record)
            .expect("failed to seed outgoing transfer");

        let block_hashes = HashSet::from([normalize_tx_hash(&record.tx_hash)]);
        promote_outgoing_records_observed_in_block(
            &state.store,
            VALID_VAULT_PUB_KEY,
            &block_hashes,
            120,
        )
        .expect("promotion should succeed");

        let updated = state
            .store
            .load_outgoing_transfer(VALID_VAULT_PUB_KEY, &record.tx_hash)
            .expect("failed to load updated outgoing transfer")
            .expect("updated outgoing transfer should exist");
        assert_eq!(updated.status, OutgoingTransferStatus::ObservedMined);
        assert_eq!(updated.observed_height, Some(120));
        assert!(updated.observed_at.is_some());
    }

    #[tokio::test]
    async fn scanner_sync_emits_retroactive_observed_outgoing_once_without_selected_inputs() {
        let daemon_server = axum_test::TestServer::builder()
            .http_transport()
            .build(axum::Router::new().route(
                "/json_rpc",
                axum::routing::post(|Json(payload): Json<serde_json::Value>| async move {
                    let method = payload
                        .get("method")
                        .and_then(|value| value.as_str())
                        .unwrap_or_default();
                    match method {
                        "get_block_header_by_height" => (
                            StatusCode::OK,
                            Json(serde_json::json!({
                                "result": {
                                    "block_header": {
                                        "hash": "hash120",
                                        "prev_hash": "hash119",
                                        "height": 120,
                                        "timestamp": 1,
                                        "major_version": 16,
                                        "minor_version": 16,
                                        "nonce": 1,
                                        "reward": 1,
                                        "num_txes": 0
                                    }
                                }
                            })),
                        ),
                        _ => (
                            StatusCode::NOT_FOUND,
                            Json(serde_json::json!({"error": "unsupported method"})),
                        ),
                    }
                }),
            ))
            .expect("failed to start daemon test server");
        let daemon_url = daemon_server
            .server_address()
            .expect("daemon test server address missing")
            .to_string();
        let (state, _data_dir) = chain_test_state(&daemon_url);

        state
            .store
            .register_vault(
                VALID_VAULT_PUB_KEY,
                &test_group_key_hex(),
                "48vault",
                "test",
                0,
            )
            .expect("failed to register vault");
        state
            .store
            .save_outputs(VALID_VAULT_PUB_KEY, &[])
            .expect("failed to seed outputs");
        state
            .store
            .save_scan_block_meta(
                VALID_VAULT_PUB_KEY,
                &ScanBlockMeta {
                    height: 120,
                    block_hash: "hash120".to_string(),
                    parent_hash: "hash119".to_string(),
                    added_outputs: vec![],
                    spent_key_images: vec![],
                    spent_by_key_images: vec![],
                },
            )
            .expect("failed to seed scan block metadata");
        state
            .store
            .save_scan_height(VALID_VAULT_PUB_KEY, 120)
            .expect("failed to seed scan height");

        let mut record = sample_outgoing(
            "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
            VALID_VAULT_PUB_KEY,
            "48from-retro",
            "48to-retro",
            1,
        );
        record.observed_height = Some(77);
        record.reported_effective_height = None;
        state
            .store
            .save_outgoing_transfer(&record)
            .expect("failed to seed outgoing transfer");

        let req = ScannerSyncRequest {
            height: 120,
            tip_height: 120,
            block_hash: "hash120".to_string(),
            parent_hash: "hash119".to_string(),
            reorg_window: 120,
        };
        let response = scanner_sync(State(state.clone()), Json(req))
            .await
            .into_response();
        let (status, body) = response_to_status_json(response).await;
        assert_eq!(status, StatusCode::OK, "body={body}");
        let outgoing = body["outgoing"]
            .as_array()
            .expect("outgoing should be an array");
        assert_eq!(outgoing.len(), 1);
        assert_eq!(
            outgoing[0]["txid"].as_str().unwrap_or_default(),
            record.tx_hash
        );
        let retired_field = ["spent", "output", "refs"].join("_");
        assert!(
            outgoing[0].get(&retired_field).is_none(),
            "selected inputs should no longer be emitted"
        );

        let updated = state
            .store
            .load_outgoing_transfer(VALID_VAULT_PUB_KEY, &record.tx_hash)
            .expect("failed to load outgoing transfer")
            .expect("outgoing transfer should exist");
        assert_eq!(updated.reported_effective_height, Some(77));

        let second = scanner_sync(
            State(state),
            Json(ScannerSyncRequest {
                height: 120,
                tip_height: 120,
                block_hash: "hash120".to_string(),
                parent_hash: "hash119".to_string(),
                reorg_window: 120,
            }),
        )
        .await
        .into_response();
        let (status2, body2) = response_to_status_json(second).await;
        assert_eq!(status2, StatusCode::OK);
        assert_eq!(
            body2["outgoing"]
                .as_array()
                .expect("outgoing should be an array")
                .len(),
            0
        );
    }

    #[test]
    fn scan_lag_exceeds_uses_saturating_difference() {
        assert!(!scan_lag_exceeds(100, 98, 2));
        assert!(scan_lag_exceeds(100, 97, 2));
        assert!(!scan_lag_exceeds(5, 9, 0));
    }

    #[test]
    fn valid_vault_pub_key_accepts_known_thor_and_tthor_keys() {
        let thor_pub =
            "thorpub1addwnpepq0rf954ne8ahjl0es3k32prvngy6tsye4cr8wdqsrmmdqcsvyzrujqexwxp";
        let tthor_pub =
            "tthorpub1addwnpepqt7qug8vk9r3saw8n4r803ydj2g3dqwx0mvq5akhnze86fc536xcycgtrnv";

        assert!(valid_vault_pub_key(thor_pub));
        assert!(valid_vault_pub_key(tthor_pub));
    }

    #[test]
    fn valid_vault_pub_key_rejects_invalid_checksum_hrp_and_empty_values() {
        let valid = "tthorpub1addwnpepqt7qug8vk9r3saw8n4r803ydj2g3dqwx0mvq5akhnze86fc536xcycgtrnv";
        let mut invalid_checksum = valid.to_string();
        let last = invalid_checksum
            .pop()
            .expect("test fixture must be non-empty");
        invalid_checksum.push(if last == 'q' { 'p' } else { 'q' });

        assert!(!valid_vault_pub_key(&invalid_checksum));
        assert!(!valid_vault_pub_key(
            "cosmos100000000000000000000000000000000708mjz"
        ));
        assert!(!valid_vault_pub_key(""));
        assert!(!valid_vault_pub_key("thorpub1vaulttest"));
    }

    #[test]
    fn vault_pub_key_match_requires_expected_group_key_payload() {
        assert!(
            vault_pub_key_matches_group_key(BOOTSTRAP_VAULT_PUB_KEY, BOOTSTRAP_GROUP_KEY_HEX)
                .expect("tthorpub match check should succeed")
        );
        assert!(
            !vault_pub_key_matches_group_key(BOOTSTRAP_VAULT_PUB_KEY, &test_group_key_hex())
                .expect("mismatch check should succeed")
        );
    }

    #[tokio::test]
    async fn register_vault_bootstraps_missing_frost_keys_from_spend_seed() {
        let (state, _data_dir) = chain_test_state("http://127.0.0.1:18081");
        let group_key = xmr::group_key_hex_from_spend_seed_hex(BOOTSTRAP_SPEND_KEY_HEX)
            .expect("group key should derive");
        let address = xmr::derive_vault_address(&group_key, state.chain_network)
            .expect("address should derive");

        let response = register_vault(
            State(state.clone()),
            Json(RegisterVaultRequest {
                vault_pub_key: BOOTSTRAP_VAULT_PUB_KEY.to_string(),
                group_key: group_key.clone(),
                address,
                source: Some("test".to_string()),
                spend_key_hex: Some(BOOTSTRAP_SPEND_KEY_HEX.to_string()),
                birthday: None,
                key_images: None,
                tx_keys: None,
            }),
        )
        .await
        .into_response();
        let (status, body) = response_to_status_json(response).await;
        assert_eq!(status, StatusCode::OK);
        assert_eq!(body["registered"], serde_json::json!(true));

        let stored = state
            .store
            .load_frost_keys(&group_key)
            .expect("key lookup should succeed")
            .expect("bootstrapped key material should exist");
        let key_share = ThresholdKeyShare::from_stored_bytes(&stored)
            .expect("stored bootstrap key material should deserialize");
        assert_eq!(
            key_share
                .group_key_hex()
                .expect("group key should be readable"),
            BOOTSTRAP_GROUP_KEY_HEX
        );
    }

    #[tokio::test]
    async fn register_vault_persists_registered_key_image_inventory() {
        let (state, _data_dir) = chain_test_state("http://127.0.0.1:18081");
        let group_key = xmr::group_key_hex_from_spend_seed_hex(BOOTSTRAP_SPEND_KEY_HEX)
            .expect("group key should derive");
        let address = xmr::derive_vault_address(&group_key, state.chain_network)
            .expect("address should derive");
        let key_image = test_key_image_hex(17);
        let tx_hash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa";

        let response = register_vault(
            State(state.clone()),
            Json(RegisterVaultRequest {
                vault_pub_key: BOOTSTRAP_VAULT_PUB_KEY.to_string(),
                group_key,
                address,
                source: Some("test".to_string()),
                spend_key_hex: Some(BOOTSTRAP_SPEND_KEY_HEX.to_string()),
                birthday: Some(100),
                key_images: Some(vec![KeyImageEntry {
                    tx_hash: tx_hash.to_string(),
                    output_index: 0,
                    key_image: key_image.clone(),
                }]),
                tx_keys: None,
            }),
        )
        .await
        .into_response();
        let (status, body) = response_to_status_json(response).await;
        assert_eq!(status, StatusCode::OK, "body={body}");

        let stored = state
            .store
            .load_registered_key_images(BOOTSTRAP_VAULT_PUB_KEY)
            .expect("registered key images should load");
        assert_eq!(stored.len(), 1);
        assert_eq!(stored[0].tx_hash, tx_hash);
        assert_eq!(stored[0].output_index, 0);
        assert_eq!(stored[0].key_image, key_image);
    }

    #[tokio::test]
    async fn register_vault_replaces_zero_scan_height_with_birthday() {
        let (state, _data_dir) = chain_test_state("http://127.0.0.1:18081");
        let group_key = xmr::group_key_hex_from_spend_seed_hex(BOOTSTRAP_SPEND_KEY_HEX)
            .expect("group key should derive");
        let address = xmr::derive_vault_address(&group_key, state.chain_network)
            .expect("address should derive");

        state
            .store
            .save_scan_height(BOOTSTRAP_VAULT_PUB_KEY, 0)
            .expect("stale zero scan height should save");

        let response = register_vault(
            State(state.clone()),
            Json(RegisterVaultRequest {
                vault_pub_key: BOOTSTRAP_VAULT_PUB_KEY.to_string(),
                group_key,
                address,
                source: Some("test".to_string()),
                spend_key_hex: Some(BOOTSTRAP_SPEND_KEY_HEX.to_string()),
                birthday: Some(321),
                key_images: None,
                tx_keys: None,
            }),
        )
        .await
        .into_response();
        let (status, body) = response_to_status_json(response).await;
        assert_eq!(status, StatusCode::OK, "body={body}");

        let persisted_height = state
            .store
            .load_scan_height(BOOTSTRAP_VAULT_PUB_KEY)
            .expect("scan height load should succeed");
        assert_eq!(persisted_height, Some(321));
    }

    #[tokio::test]
    async fn register_vault_applies_registered_tx_key_inventory() {
        let tx_hash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa";
        let synthetic = crate::tx::validation::synthetic_untracked_outbound_case_for_tests();
        let expected_tx_hex = synthetic.tx_hex.clone();
        let expected_tx_keys = synthetic.tx_keys.clone();
        let expected_to_address = synthetic.to_address.clone();
        let expected_amount = synthetic.amount;
        let expected_fee = synthetic.fee;
        let daemon_server = axum_test::TestServer::builder()
            .http_transport()
            .build(axum::Router::new().route(
                "/get_transactions",
                axum::routing::post(move |Json(req): Json<serde_json::Value>| {
                    let tx_hex = expected_tx_hex.clone();
                    async move {
                        let requested = req
                            .get("txs_hashes")
                            .and_then(serde_json::Value::as_array)
                            .and_then(|hashes| hashes.first())
                            .and_then(serde_json::Value::as_str)
                            .unwrap_or_default()
                            .to_ascii_lowercase();
                        if requested == tx_hash {
                            (
                                StatusCode::OK,
                                Json(serde_json::json!({
                                    "txs": [{
                                        "tx_hash": tx_hash,
                                        "as_hex": tx_hex,
                                        "block_height": 111,
                                        "in_pool": false
                                    }],
                                    "status": "OK"
                                })),
                            )
                        } else {
                            (
                                StatusCode::OK,
                                Json(serde_json::json!({
                                    "txs": [],
                                    "missed_tx": [requested],
                                    "status": "OK"
                                })),
                            )
                        }
                    }
                }),
            ))
            .expect("failed to start daemon test server");
        let daemon_url = daemon_server
            .server_address()
            .expect("daemon test server address missing")
            .to_string();
        let (state, _data_dir) = chain_test_state(&daemon_url);
        let group_key = xmr::group_key_hex_from_spend_seed_hex(BOOTSTRAP_SPEND_KEY_HEX)
            .expect("group key should derive");
        let address = xmr::derive_vault_address(&group_key, state.chain_network)
            .expect("address should derive");

        let response = register_vault(
            State(state.clone()),
            Json(RegisterVaultRequest {
                vault_pub_key: BOOTSTRAP_VAULT_PUB_KEY.to_string(),
                group_key,
                address,
                source: Some("test".to_string()),
                spend_key_hex: Some(BOOTSTRAP_SPEND_KEY_HEX.to_string()),
                birthday: Some(100),
                key_images: None,
                tx_keys: Some(vec![RegisterTxKeyEntry {
                    tx_hash: tx_hash.to_string(),
                    tx_keys: expected_tx_keys.clone(),
                    from_address: None,
                    to_address: expected_to_address.clone(),
                    amount: expected_amount,
                    fee: expected_fee,
                    memo: Some("OUT:parent".to_string()),
                }]),
            }),
        )
        .await
        .into_response();
        let (status, body) = response_to_status_json(response).await;
        assert_eq!(status, StatusCode::OK, "body={body}");

        let records = state
            .store
            .load_outgoing_transfers(BOOTSTRAP_VAULT_PUB_KEY)
            .expect("outgoing transfers should load");
        assert_eq!(records.len(), 1);
        assert_eq!(records[0].tx_hash, tx_hash);
        assert_eq!(records[0].tx_keys, expected_tx_keys);
        assert_eq!(records[0].to_address, expected_to_address);
        assert_eq!(records[0].amount, expected_amount);
        assert_eq!(records[0].fee, expected_fee);
        assert_eq!(records[0].memo, "OUT:parent");
        assert_eq!(records[0].status, OutgoingTransferStatus::ObservedMined);
        assert_eq!(records[0].observed_height, Some(111));
    }

    #[tokio::test]
    async fn register_vault_without_inventory_preserves_existing_metadata() {
        let (state, _data_dir) = chain_test_state("http://127.0.0.1:18081");
        let group_key = xmr::group_key_hex_from_spend_seed_hex(BOOTSTRAP_SPEND_KEY_HEX)
            .expect("group key should derive");
        let address = xmr::derive_vault_address(&group_key, state.chain_network)
            .expect("address should derive");
        let vault_pub_key = BOOTSTRAP_VAULT_PUB_KEY;
        let tx_hash =
            "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa".to_string();
        let key_image = test_key_image_hex(23);
        let tx_key = "11".repeat(32);

        state
            .store
            .save_registered_key_images(
                vault_pub_key,
                &[RegisteredKeyImageRef {
                    tx_hash: tx_hash.clone(),
                    output_index: 0,
                    key_image: key_image.clone(),
                }],
            )
            .expect("registered key images should save");

        let mut output = sample_output(&tx_hash, 0, OutputState::Unspent);
        output.key_image = Some(key_image.clone());
        state
            .store
            .save_outputs(vault_pub_key, std::slice::from_ref(&output))
            .expect("outputs should save");

        let mut outgoing = sample_outgoing(
            "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
            vault_pub_key,
            "48from",
            "48to",
            111,
        );
        outgoing.tx_keys = vec![tx_key.clone()];
        state
            .store
            .save_outgoing_transfer(&outgoing)
            .expect("outgoing transfer should save");

        let response = register_vault(
            State(state.clone()),
            Json(RegisterVaultRequest {
                vault_pub_key: vault_pub_key.to_string(),
                group_key,
                address,
                source: Some("test".to_string()),
                spend_key_hex: Some(BOOTSTRAP_SPEND_KEY_HEX.to_string()),
                birthday: Some(100),
                key_images: None,
                tx_keys: None,
            }),
        )
        .await
        .into_response();
        let (status, body) = response_to_status_json(response).await;
        assert_eq!(status, StatusCode::OK, "body={body}");

        let stored = state
            .store
            .load_registered_key_images(vault_pub_key)
            .expect("registered key images should load");
        assert_eq!(stored.len(), 1);
        assert_eq!(stored[0].tx_hash, tx_hash);
        assert_eq!(stored[0].key_image, key_image);

        let outputs = state
            .store
            .load_outputs(vault_pub_key)
            .expect("outputs should load");
        assert_eq!(outputs.len(), 1);
        assert_eq!(outputs[0].key_image.as_deref(), Some(key_image.as_str()));

        let records = state
            .store
            .load_outgoing_transfers(vault_pub_key)
            .expect("outgoing transfers should load");
        assert_eq!(records.len(), 1);
        assert_eq!(records[0].tx_keys, vec![tx_key]);
    }

    #[test]
    fn required_msg_id_rejects_missing_or_whitespace() {
        let (status, response) = required_msg_id("").expect_err("empty msg_id should fail");
        assert_eq!(status, StatusCode::BAD_REQUEST);
        assert_eq!(
            response
                .get("error")
                .and_then(serde_json::Value::as_str)
                .unwrap_or_default(),
            "msg_id is required"
        );

        let (status, response) = required_msg_id("   ").expect_err("whitespace msg_id should fail");
        assert_eq!(status, StatusCode::BAD_REQUEST);
        assert_eq!(
            response
                .get("error")
                .and_then(serde_json::Value::as_str)
                .unwrap_or_default(),
            "msg_id is required"
        );
    }

    #[test]
    fn required_msg_id_trims_non_empty_value() {
        let msg_id = required_msg_id("  abc-123  ").expect("trimmed msg_id should be accepted");
        assert_eq!(msg_id, "abc-123");
    }

    #[test]
    fn idempotency_payload_hashes_replay_for_reordered_map_payload() {
        let req_a = SignPartialRequest {
            msg_id: "msg-1".to_string(),
            session_id: "session-1".to_string(),
            all_preprocesses: HashMap::from([
                ("1".to_string(), "aa".to_string()),
                ("2".to_string(), "bb".to_string()),
            ]),
        };
        let req_b = SignPartialRequest {
            msg_id: "msg-1".to_string(),
            session_id: "session-1".to_string(),
            all_preprocesses: HashMap::from([
                ("2".to_string(), "bb".to_string()),
                ("1".to_string(), "aa".to_string()),
            ]),
        };
        let req_c = SignPartialRequest {
            msg_id: "msg-1".to_string(),
            session_id: "session-1".to_string(),
            all_preprocesses: HashMap::from([
                ("1".to_string(), "aa".to_string()),
                ("2".to_string(), "cc".to_string()),
            ]),
        };

        let hashes_a = idempotency_payload_hashes(&req_a).expect("hashing req_a should succeed");
        let hashes_b = idempotency_payload_hashes(&req_b).expect("hashing req_b should succeed");
        let hashes_c = idempotency_payload_hashes(&req_c).expect("hashing req_c should succeed");
        assert_eq!(hashes_a.canonical, hashes_b.canonical);
        assert_ne!(hashes_a.canonical, hashes_c.canonical);

        let (state, _data_dir, _vault_pub_key) = outputs_test_state(vec![]);
        let replay_payload = serde_json::json!({ "status": "ok" });
        match check_idempotency(state.as_ref(), "sign_partial", "msg-1", &hashes_a)
            .expect("idempotency claim should succeed")
        {
            IdempotencyCheck::Fresh => {}
            other => panic!("expected fresh claim, got {other:?}"),
        }
        persist_idempotency(
            state.as_ref(),
            "sign_partial",
            "msg-1",
            &hashes_a.canonical,
            StatusCode::OK,
            &replay_payload,
        );

        match check_idempotency(state.as_ref(), "sign_partial", "msg-1", &hashes_b)
            .expect("idempotency check should succeed")
        {
            IdempotencyCheck::Replay(status, response) => {
                assert_eq!(status, StatusCode::OK);
                assert_eq!(response, replay_payload);
            }
            other => panic!("expected replay, got {:?}", other),
        }

        match check_idempotency(state.as_ref(), "sign_partial", "msg-1", &hashes_c)
            .expect("idempotency check should succeed")
        {
            IdempotencyCheck::Conflict => {}
            other => panic!("expected conflict, got {:?}", other),
        }
    }

    #[test]
    fn sign_prepare_idempotency_replays_across_msg_id_whitespace_variants() {
        let req_a = SignPrepareRequest {
            msg_id: "msg-prepare-1 ".to_string(),
            vault_pub_key: VALID_VAULT_PUB_KEY.to_string(),
            to_address: "44Affq5kSiGBoZ...".to_string(),
            amount_atomic: 42,
            max_gas_atomic: 0,
            sweep_if_final: false,
            memo: Some("OUT:TEST".to_string()),
            fee_rate: Some(10),
            decoy_context: "ctx".to_string(),
            max_inputs: None,
            dust_threshold: None,
        };
        let req_b = SignPrepareRequest {
            msg_id: "msg-prepare-1".to_string(),
            ..req_a.clone()
        };
        let msg_id_a = required_msg_id(&req_a.msg_id).expect("msg_id should be valid");
        let msg_id_b = required_msg_id(&req_b.msg_id).expect("msg_id should be valid");
        assert_eq!(msg_id_a, msg_id_b);

        let mut canonical_req_a = req_a.clone();
        canonical_req_a.msg_id = msg_id_a.clone();
        canonical_req_a.vault_pub_key = canonical_req_a.vault_pub_key.trim().to_string();
        let mut canonical_req_b = req_b.clone();
        canonical_req_b.msg_id = msg_id_b.clone();
        canonical_req_b.vault_pub_key = canonical_req_b.vault_pub_key.trim().to_string();

        let hashes_a =
            idempotency_payload_hashes(&canonical_req_a).expect("hashing req_a should succeed");
        let hashes_b =
            idempotency_payload_hashes(&canonical_req_b).expect("hashing req_b should succeed");
        assert_eq!(hashes_a.canonical, hashes_b.canonical);

        let (state, _data_dir, _vault_pub_key) = outputs_test_state(vec![]);
        let replay_payload = serde_json::json!({ "status": "ok" });
        match check_idempotency(state.as_ref(), "sign_prepare", &msg_id_a, &hashes_a)
            .expect("idempotency claim should succeed")
        {
            IdempotencyCheck::Fresh => {}
            other => panic!("expected fresh claim, got {other:?}"),
        }
        persist_idempotency(
            state.as_ref(),
            "sign_prepare",
            &msg_id_a,
            &hashes_a.canonical,
            StatusCode::OK,
            &replay_payload,
        );

        match check_idempotency(state.as_ref(), "sign_prepare", &msg_id_b, &hashes_b)
            .expect("idempotency check should succeed")
        {
            IdempotencyCheck::Replay(status, response) => {
                assert_eq!(status, StatusCode::OK);
                assert_eq!(response, replay_payload);
            }
            other => panic!("expected replay, got {:?}", other),
        }
    }

    #[tokio::test]
    async fn sign_partial_replays_across_msg_id_whitespace_variants() {
        let (state, _data_dir) = chain_test_state("http://127.0.0.1:18081");
        let req_with_space = SignPartialRequest {
            msg_id: "msg-partial-space ".to_string(),
            session_id: "session-missing".to_string(),
            all_preprocesses: HashMap::from([("1".to_string(), "aa".to_string())]),
        };
        let response = sign_partial(State(state.clone()), Json(req_with_space))
            .await
            .into_response();
        let (status_a, body_a) = response_to_status_json(response).await;
        assert_eq!(status_a, StatusCode::BAD_REQUEST);

        let req_trimmed = SignPartialRequest {
            msg_id: "msg-partial-space".to_string(),
            session_id: "session-missing".to_string(),
            all_preprocesses: HashMap::from([("1".to_string(), "aa".to_string())]),
        };
        let response = sign_partial(State(state), Json(req_trimmed))
            .await
            .into_response();
        let (status_b, body_b) = response_to_status_json(response).await;
        assert_eq!(status_b, status_a);
        assert_eq!(body_b, body_a);
        assert_ne!(status_b, StatusCode::CONFLICT);
    }

    #[tokio::test]
    async fn key_image_prepare_clears_stale_replay_when_cached_session_is_missing() {
        let (state, _data_dir) = chain_test_state("http://127.0.0.1:18081");
        let req = KeyImagePrepareRequest {
            msg_id: "msg-key-image-prepare".to_string(),
            vault_pub_key: VALID_VAULT_PUB_KEY.to_string(),
            tx_hash: "3a5df81c293b28dbc6df7a6f54505926bfc0d8a5c4a122b97179f2211804e9fd".to_string(),
            output_index: 0,
        };
        let msg_id = required_msg_id(&req.msg_id).expect("msg_id should be valid");
        let mut canonical_req = req.clone();
        canonical_req.msg_id = msg_id.clone();
        let hashes =
            idempotency_payload_hashes(&canonical_req).expect("hashing request should succeed");
        let replay_payload = serde_json::json!({
            "session_id": "session-missing",
            "preprocess": "deadbeef"
        });

        match check_idempotency(state.as_ref(), "key_image_prepare", &msg_id, &hashes)
            .expect("idempotency claim should succeed")
        {
            IdempotencyCheck::Fresh => {}
            other => panic!("expected fresh claim, got {other:?}"),
        }
        persist_idempotency(
            state.as_ref(),
            "key_image_prepare",
            &msg_id,
            &hashes.canonical,
            StatusCode::OK,
            &replay_payload,
        );

        let response = key_image_prepare(State(state), Json(req))
            .await
            .into_response();
        let (status, body) = response_to_status_json(response).await;
        assert_eq!(status, StatusCode::BAD_REQUEST);
        assert_ne!(body, replay_payload);
        assert_ne!(body.get("session_id"), replay_payload.get("session_id"));
    }

    #[tokio::test]
    async fn sign_partial_in_progress_idempotency_returns_409() {
        let (state, _data_dir) = chain_test_state("http://127.0.0.1:18081");
        let req = SignPartialRequest {
            msg_id: "msg-in-progress".to_string(),
            session_id: "session-1".to_string(),
            all_preprocesses: HashMap::from([("1".to_string(), "aa".to_string())]),
        };
        let payload_hashes =
            idempotency_payload_hashes(&req).expect("payload hashing should succeed");
        let claimed = state
            .store
            .claim_idempotency(
                "sign_partial",
                &req.msg_id,
                &payload_hashes.canonical,
                chrono::Utc::now().timestamp(),
            )
            .expect("idempotency claim should succeed");
        assert!(matches!(claimed, IdempotencyClaimResult::Fresh));

        let response = sign_partial(State(state), Json(req)).await.into_response();
        let (status, body) = response_to_status_json(response).await;
        assert_eq!(status, StatusCode::CONFLICT);
        assert_eq!(
            body["error"].as_str().unwrap_or_default(),
            "idempotency request already in progress for msg_id"
        );
    }

    #[tokio::test]
    async fn broadcast_ok_without_daemon_hash_falls_back_to_caller_hash() {
        let daemon_server = axum_test::TestServer::builder()
            .http_transport()
            .build(axum::Router::new().route(
                "/send_raw_transaction",
                axum::routing::post(|| async {
                    (
                        StatusCode::OK,
                        Json(serde_json::json!({
                            "status": "OK",
                        })),
                    )
                }),
            ))
            .expect("failed to start daemon test server");
        let daemon_url = daemon_server
            .server_address()
            .expect("daemon test server address missing")
            .to_string();
        let (state, _data_dir) = chain_test_state(&daemon_url);

        let req = BroadcastRequest {
            tx_hex: "deadbeef".to_string(),
            tx_hash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa".to_string(),
            vault_pub_key: VALID_VAULT_PUB_KEY.to_string(),
            msg_id: "msg-broadcast-missing-hash".to_string(),
        };
        let response = broadcast(State(state), Json(req)).await.into_response();
        let (status, body) = response_to_status_json(response).await;
        assert_eq!(status, StatusCode::OK);
        assert_eq!(
            body["tx_hash"].as_str().unwrap_or_default(),
            "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
        );
    }

    #[tokio::test]
    async fn broadcast_ok_without_any_tx_hash_returns_502() {
        let daemon_server = axum_test::TestServer::builder()
            .http_transport()
            .build(axum::Router::new().route(
                "/send_raw_transaction",
                axum::routing::post(|| async {
                    (
                        StatusCode::OK,
                        Json(serde_json::json!({
                            "status": "OK",
                        })),
                    )
                }),
            ))
            .expect("failed to start daemon test server");
        let daemon_url = daemon_server
            .server_address()
            .expect("daemon test server address missing")
            .to_string();
        let (state, _data_dir) = chain_test_state(&daemon_url);

        let req = BroadcastRequest {
            tx_hex: "deadbeef".to_string(),
            tx_hash: String::new(),
            vault_pub_key: VALID_VAULT_PUB_KEY.to_string(),
            msg_id: "msg-broadcast-missing-hash-empty".to_string(),
        };
        let response = broadcast(State(state), Json(req)).await.into_response();
        let (status, body) = response_to_status_json(response).await;
        assert_eq!(status, StatusCode::BAD_GATEWAY);
        assert_eq!(
            body["error"].as_str().unwrap_or_default(),
            "daemon broadcast returned success without tx hash"
        );
    }

    #[tokio::test]
    async fn broadcast_not_relayed_without_positive_lookup_returns_error() {
        let daemon_server = axum_test::TestServer::builder()
            .http_transport()
            .build(
                axum::Router::new()
                    .route(
                        "/send_raw_transaction",
                        axum::routing::post(|| async {
                            (
                                StatusCode::OK,
                                Json(serde_json::json!({
                                    "status": "Failed",
                                    "reason": "not relayed",
                                })),
                            )
                        }),
                    )
                    .route(
                        "/get_transactions",
                        axum::routing::post(|| async {
                            (
                                StatusCode::OK,
                                Json(serde_json::json!({
                                    "status": "OK",
                                    "missed_tx": ["aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"],
                                })),
                            )
                        }),
                    ),
            )
            .expect("failed to start daemon test server");
        let daemon_url = daemon_server
            .server_address()
            .expect("daemon test server address missing")
            .to_string();
        let (state, _data_dir) = chain_test_state(&daemon_url);

        let req = BroadcastRequest {
            tx_hex: "deadbeef".to_string(),
            tx_hash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa".to_string(),
            vault_pub_key: VALID_VAULT_PUB_KEY.to_string(),
            msg_id: "msg-broadcast-not-relayed-missing".to_string(),
        };
        let response = broadcast(State(state), Json(req)).await.into_response();
        let (status, body) = response_to_status_json(response).await;
        assert_eq!(status, StatusCode::BAD_REQUEST);
        assert_eq!(body["error"].as_str().unwrap_or_default(), "not relayed");
    }

    #[tokio::test]
    async fn broadcast_not_relayed_with_positive_lookup_returns_success() {
        let daemon_hash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa";
        let daemon_server = axum_test::TestServer::builder()
            .http_transport()
            .build(
                axum::Router::new()
                    .route(
                        "/send_raw_transaction",
                        axum::routing::post(|| async {
                            (
                                StatusCode::OK,
                                Json(serde_json::json!({
                                    "status": "Failed",
                                    "reason": "not relayed",
                                })),
                            )
                        }),
                    )
                    .route(
                        "/get_transactions",
                        axum::routing::post(move || async move {
                            (
                                StatusCode::OK,
                                Json(serde_json::json!({
                                    "status": "OK",
                                    "txs": [{
                                        "tx_hash": daemon_hash,
                                        "as_hex": "deadbeef",
                                        "as_json": null,
                                        "block_height": null,
                                        "in_pool": true,
                                    }],
                                })),
                            )
                        }),
                    ),
            )
            .expect("failed to start daemon test server");
        let daemon_url = daemon_server
            .server_address()
            .expect("daemon test server address missing")
            .to_string();
        let (state, _data_dir) = chain_test_state(&daemon_url);

        let req = BroadcastRequest {
            tx_hex: "deadbeef".to_string(),
            tx_hash: daemon_hash.to_string(),
            vault_pub_key: VALID_VAULT_PUB_KEY.to_string(),
            msg_id: "msg-broadcast-not-relayed-found".to_string(),
        };
        let response = broadcast(State(state), Json(req)).await.into_response();
        let (status, body) = response_to_status_json(response).await;
        assert_eq!(status, StatusCode::OK);
        assert_eq!(body["tx_hash"].as_str().unwrap_or_default(), daemon_hash);
    }

    #[tokio::test]
    async fn broadcast_requires_non_empty_vault_pub_key() {
        let daemon_server = axum_test::TestServer::builder()
            .http_transport()
            .build(axum::Router::new().route(
                "/send_raw_transaction",
                axum::routing::post(|| async {
                    (
                        StatusCode::OK,
                        Json(serde_json::json!({
                            "status": "OK",
                            "tx_hash": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
                        })),
                    )
                }),
            ))
            .expect("failed to start daemon test server");
        let daemon_url = daemon_server
            .server_address()
            .expect("daemon test server address missing")
            .to_string();
        let (state, _data_dir) = chain_test_state(&daemon_url);

        let req = BroadcastRequest {
            tx_hex: "deadbeef".to_string(),
            tx_hash: String::new(),
            vault_pub_key: "   ".to_string(),
            msg_id: "msg-broadcast-missing-vault".to_string(),
        };
        let response = broadcast(State(state), Json(req)).await.into_response();
        let (status, body) = response_to_status_json(response).await;
        assert_eq!(status, StatusCode::BAD_REQUEST);
        assert_eq!(
            body["error"].as_str().unwrap_or_default(),
            "vault_pub_key is required"
        );
    }

    #[tokio::test]
    async fn broadcast_with_unbound_reservation_does_not_mark_spent() {
        let daemon_server = axum_test::TestServer::builder()
            .http_transport()
            .build(axum::Router::new().route(
                "/send_raw_transaction",
                axum::routing::post(|| async {
                    (
                        StatusCode::OK,
                        Json(serde_json::json!({
                            "status": "OK",
                            "tx_hash": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
                        })),
                    )
                }),
            ))
            .expect("failed to start daemon test server");
        let daemon_url = daemon_server
            .server_address()
            .expect("daemon test server address missing")
            .to_string();
        let (state, _data_dir) = chain_test_state(&daemon_url);

        state
            .store
            .save_outputs(
                VALID_VAULT_PUB_KEY,
                &[sample_reserved_output("msg-unbound-reservation")],
            )
            .expect("failed to seed reserved output");

        let req = BroadcastRequest {
            tx_hex: "deadbeef".to_string(),
            tx_hash: String::new(),
            vault_pub_key: VALID_VAULT_PUB_KEY.to_string(),
            msg_id: "msg-unbound-reservation".to_string(),
        };
        let response = broadcast(State(state.clone()), Json(req))
            .await
            .into_response();
        let (status, body) = response_to_status_json(response).await;
        assert_eq!(status, StatusCode::OK);
        assert_eq!(
            body["tx_hash"].as_str().unwrap_or_default(),
            "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
        );

        let outputs = state
            .store
            .load_outputs(VALID_VAULT_PUB_KEY)
            .expect("failed to reload outputs");
        assert_eq!(outputs.len(), 1);
        assert_eq!(outputs[0].state, OutputState::Reserved);
        assert!(outputs[0].reservation.is_some());
    }

    #[tokio::test]
    async fn broadcast_trims_vault_pub_key_for_stateful_updates() {
        let daemon_hash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa";
        let daemon_server = axum_test::TestServer::builder()
            .http_transport()
            .build(axum::Router::new().route(
                "/send_raw_transaction",
                axum::routing::post(move || async move {
                    (
                        StatusCode::OK,
                        Json(serde_json::json!({
                            "status": "OK",
                            "tx_hash": daemon_hash,
                        })),
                    )
                }),
            ))
            .expect("failed to start daemon test server");
        let daemon_url = daemon_server
            .server_address()
            .expect("daemon test server address missing")
            .to_string();
        let (state, _data_dir) = chain_test_state(&daemon_url);

        state
            .store
            .save_outgoing_transfer(&sample_outgoing(
                daemon_hash,
                VALID_VAULT_PUB_KEY,
                "4from",
                "8to",
                1,
            ))
            .expect("failed to seed outgoing record");

        let req = BroadcastRequest {
            tx_hex: "deadbeef".to_string(),
            tx_hash: String::new(),
            vault_pub_key: format!("  {VALID_VAULT_PUB_KEY}  "),
            msg_id: "msg-broadcast-vault-trim".to_string(),
        };
        let response = broadcast(State(state.clone()), Json(req))
            .await
            .into_response();
        let (status, body) = response_to_status_json(response).await;
        assert_eq!(status, StatusCode::OK);
        assert_eq!(body["tx_hash"].as_str().unwrap_or_default(), daemon_hash);

        let records = state
            .store
            .load_outgoing_transfers(VALID_VAULT_PUB_KEY)
            .expect("failed to load outgoing transfers");
        let updated = records
            .iter()
            .find(|record| record.tx_hash.eq_ignore_ascii_case(daemon_hash))
            .expect("daemon hash outgoing record should exist");
        assert_eq!(updated.status, OutgoingTransferStatus::ObservedMempool);
    }

    #[tokio::test]
    async fn broadcast_uses_daemon_hash_for_status_updates_on_mismatch() {
        let daemon_hash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa";
        let caller_hash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb";
        let daemon_server = axum_test::TestServer::builder()
            .http_transport()
            .build(axum::Router::new().route(
                "/send_raw_transaction",
                axum::routing::post(move || async move {
                    (
                        StatusCode::OK,
                        Json(serde_json::json!({
                            "status": "OK",
                            "tx_hash": daemon_hash,
                        })),
                    )
                }),
            ))
            .expect("failed to start daemon test server");
        let daemon_url = daemon_server
            .server_address()
            .expect("daemon test server address missing")
            .to_string();
        let (state, _data_dir) = chain_test_state(&daemon_url);

        state
            .store
            .save_outgoing_transfer(&sample_outgoing(
                daemon_hash,
                VALID_VAULT_PUB_KEY,
                "4from-daemon",
                "8to-daemon",
                1,
            ))
            .expect("failed to seed daemon outgoing record");
        state
            .store
            .save_outgoing_transfer(&sample_outgoing(
                caller_hash,
                VALID_VAULT_PUB_KEY,
                "4from-caller",
                "8to-caller",
                2,
            ))
            .expect("failed to seed caller outgoing record");

        let req = BroadcastRequest {
            tx_hex: "deadbeef".to_string(),
            tx_hash: caller_hash.to_string(),
            vault_pub_key: VALID_VAULT_PUB_KEY.to_string(),
            msg_id: "msg-broadcast-hash-mismatch".to_string(),
        };
        let response = broadcast(State(state.clone()), Json(req))
            .await
            .into_response();
        let (status, body) = response_to_status_json(response).await;
        assert_eq!(status, StatusCode::OK);
        assert_eq!(body["tx_hash"].as_str().unwrap_or_default(), daemon_hash);

        let records = state
            .store
            .load_outgoing_transfers(VALID_VAULT_PUB_KEY)
            .expect("failed to load outgoing transfers");
        let daemon_record = records
            .iter()
            .find(|record| record.tx_hash.eq_ignore_ascii_case(daemon_hash))
            .expect("daemon outgoing record should exist");
        let caller_record = records
            .iter()
            .find(|record| record.tx_hash.eq_ignore_ascii_case(caller_hash))
            .expect("caller outgoing record should exist");
        assert_eq!(
            daemon_record.status,
            OutgoingTransferStatus::ObservedMempool
        );
        assert_eq!(caller_record.status, OutgoingTransferStatus::ObservedMined);
    }

    #[tokio::test]
    async fn scanner_sync_ignores_tip_height_for_confirmation_math() {
        let daemon_server = axum_test::TestServer::builder()
            .http_transport()
            .build(axum::Router::new().route(
                "/json_rpc",
                axum::routing::post(|Json(req): Json<serde_json::Value>| async move {
                    let method = req
                        .get("method")
                        .and_then(serde_json::Value::as_str)
                        .unwrap_or_default();
                    match method {
                        "get_block_header_by_height" => (
                            StatusCode::OK,
                            Json(serde_json::json!({
                                "result": {
                                    "block_header": {
                                        "height": 120,
                                        "hash": "hash120",
                                        "prev_hash": "hash119",
                                        "timestamp": 1,
                                        "major_version": 16,
                                        "minor_version": 16,
                                        "nonce": 1,
                                        "reward": 1,
                                        "num_txes": 0
                                    }
                                }
                            })),
                        ),
                        _ => (
                            StatusCode::NOT_FOUND,
                            Json(serde_json::json!({"error": "unsupported method"})),
                        ),
                    }
                }),
            ))
            .expect("failed to start daemon test server");
        let daemon_url = daemon_server
            .server_address()
            .expect("daemon test server address missing")
            .to_string();
        let (state, _data_dir) = chain_test_state(&daemon_url);

        let output = TrackedOutput {
            tx_hash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa".to_string(),
            output_index: 0,
            amount: 500,
            height: 100,
            key_image: None,
            spent_by_key_image_height: None,
            state: OutputState::Unspent,
            reservation: None,
            subaddress_major: 0,
            subaddress_minor: 0,
            wallet_output_hex: Some("aa".to_string()),
            address: Some("48vault".to_string()),
            note: None,
        };
        state
            .store
            .register_vault(
                VALID_VAULT_PUB_KEY,
                &test_group_key_hex(),
                "48vault",
                "test",
                0,
            )
            .expect("failed to register vault");
        state
            .store
            .save_outputs(VALID_VAULT_PUB_KEY, std::slice::from_ref(&output))
            .expect("failed to seed outputs");
        state
            .store
            .save_scan_block_meta(
                VALID_VAULT_PUB_KEY,
                &ScanBlockMeta {
                    height: 120,
                    block_hash: "hash120".to_string(),
                    parent_hash: "hash119".to_string(),
                    added_outputs: vec![OutputRef {
                        tx_hash: output.tx_hash.clone(),
                        output_index: output.output_index,
                    }],
                    spent_key_images: vec![],
                    spent_by_key_images: vec![],
                },
            )
            .expect("failed to seed scan block metadata");
        state
            .store
            .save_scan_height(VALID_VAULT_PUB_KEY, 120)
            .expect("failed to seed scan height");

        let req = ScannerSyncRequest {
            height: 120,
            tip_height: 9_999,
            block_hash: "hash120".to_string(),
            parent_hash: "hash119".to_string(),
            reorg_window: 120,
        };
        let response = scanner_sync(State(state), Json(req)).await.into_response();
        let (status, body) = response_to_status_json(response).await;
        assert_eq!(status, StatusCode::OK);

        let incoming = body["incoming"]
            .as_array()
            .expect("incoming should be an array");
        assert_eq!(incoming.len(), 1);
        assert_eq!(incoming[0]["confirmations"], serde_json::json!(20u64));
    }

    #[test]
    fn rescan_target_height_prefers_chain_tip_with_larger_catchup_budget() {
        assert_eq!(rescan_target_height(100, 120, 170), 170);
        assert_eq!(
            rescan_target_height(100, 120, 10_000),
            100 + RESCAN_MAX_BLOCKS_PER_SYNC
        );
        assert_eq!(rescan_target_height(600, 650, 640), 650);
    }

    #[tokio::test]
    async fn scanner_reset_wipes_all_vault_state_except_frost_keyshares() {
        let (state, _data_dir) = chain_test_state("http://127.0.0.1:18081");
        state
            .store
            .register_vault(
                VALID_VAULT_PUB_KEY,
                &test_group_key_hex(),
                "48vault-reset",
                "test",
                321,
            )
            .expect("failed to register vault");

        state
            .store
            .save_outputs(
                VALID_VAULT_PUB_KEY,
                &[sample_output(
                    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
                    0,
                    OutputState::Unspent,
                )],
            )
            .expect("failed to seed outputs");
        state
            .store
            .save_registered_key_images(
                VALID_VAULT_PUB_KEY,
                &[RegisteredKeyImageRef {
                    tx_hash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
                        .to_string(),
                    output_index: 0,
                    key_image: test_key_image_hex(3),
                }],
            )
            .expect("failed to seed registered key images");
        state
            .store
            .save_scan_block_meta(
                VALID_VAULT_PUB_KEY,
                &ScanBlockMeta {
                    height: 400,
                    block_hash: "hash400".to_string(),
                    parent_hash: "hash399".to_string(),
                    added_outputs: vec![],
                    spent_key_images: vec![test_key_image_hex(3)],
                    spent_by_key_images: vec![],
                },
            )
            .expect("failed to seed scan block meta");
        state
            .store
            .save_scan_height(VALID_VAULT_PUB_KEY, 400)
            .expect("failed to seed scan height");
        let mut retained = sample_outgoing(
            "abababababababababababababababababababababababababababababababab",
            VALID_VAULT_PUB_KEY,
            "48from",
            "48to",
            1,
        );
        retained.tx_keys = vec!["11".repeat(32)];
        retained.registered_inventory = true;
        state
            .store
            .save_outgoing_transfer(&retained)
            .expect("failed to seed tx-key-backed outgoing transfer");
        state
            .store
            .save_outgoing_transfer(&sample_outgoing(
                "cdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcd",
                VALID_VAULT_PUB_KEY,
                "48from",
                "48to",
                2,
            ))
            .expect("failed to seed ephemeral outgoing transfer");

        // Seed scanner in-memory state so we can confirm it gets dropped.
        state
            .scanner
            .lock()
            .register_vault(
                VALID_VAULT_PUB_KEY,
                &test_group_key_hex(),
                crate::config::ChainNetworkMode::Mocknet,
                vec![],
                Some(321),
            )
            .expect("failed to register scanner state");

        let response = scanner_reset(
            State(state.clone()),
            Json(crate::api::types::ScannerResetRequest {
                vault_pub_key: VALID_VAULT_PUB_KEY.to_string(),
                birthday_height: 321,
            }),
        )
        .await
        .into_response();
        let (status, body) = response_to_status_json(response).await;
        assert_eq!(status, StatusCode::OK);
        assert_eq!(body["status"].as_str(), Some("reset"));

        let outputs = state
            .store
            .load_outputs(VALID_VAULT_PUB_KEY)
            .expect("failed to load outputs");
        assert!(outputs.is_empty(), "outputs should be cleared on reset");

        let key_images = state
            .store
            .load_registered_key_images(VALID_VAULT_PUB_KEY)
            .expect("failed to load registered key images");
        assert!(
            key_images.is_empty(),
            "registered key-image inventory should be cleared on reset"
        );

        let meta = state
            .store
            .load_scan_block_meta(VALID_VAULT_PUB_KEY, 400)
            .expect("failed to load scan block meta after reset");
        assert!(meta.is_none(), "scan block meta should be cleared on reset");

        let scan_height = state
            .store
            .load_scan_height(VALID_VAULT_PUB_KEY)
            .expect("failed to load scan height");
        assert_eq!(scan_height, None, "scan height should be deleted on reset");

        let outgoing = state
            .store
            .load_outgoing_transfers(VALID_VAULT_PUB_KEY)
            .expect("failed to load outgoing transfers");
        assert!(
            outgoing.is_empty(),
            "all outgoing transfers (including tx-key backed) should be cleared on reset"
        );

        let vault = state
            .store
            .list_vaults()
            .expect("failed to list vaults")
            .into_iter()
            .find(|v| v.vault_pub_key == VALID_VAULT_PUB_KEY);
        assert!(
            vault.is_none(),
            "vault record should be deleted on reset — bifrost must re-register"
        );

        assert!(
            !state.scanner.lock().has_vault(VALID_VAULT_PUB_KEY),
            "in-memory scanner state should be dropped on reset"
        );
    }

    #[tokio::test]
    async fn list_vault_states_ignores_local_attestation_tx_keys() {
        let (state, _data_dir) = chain_test_state("http://127.0.0.1:18081");
        state
            .store
            .register_vault(
                VALID_VAULT_PUB_KEY,
                &test_group_key_hex(),
                "48vault-reset",
                "test",
                321,
            )
            .expect("failed to register vault");

        let mut registered = sample_outgoing(
            "abababababababababababababababababababababababababababababababab",
            VALID_VAULT_PUB_KEY,
            "48from",
            "48to",
            1,
        );
        registered.tx_keys = vec!["11".repeat(32)];
        registered.registered_inventory = true;
        state
            .store
            .save_outgoing_transfer(&registered)
            .expect("failed to seed registered tx-key outbound");

        let mut local = sample_outgoing(
            "cdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcd",
            VALID_VAULT_PUB_KEY,
            "48from",
            "48to",
            2,
        );
        local.tx_keys = vec!["22".repeat(32)];
        state
            .store
            .save_outgoing_transfer(&local)
            .expect("failed to seed locally validated outbound");

        let response = list_vault_states(State(state.clone()))
            .await
            .into_response();
        let (status, body) = response_to_status_json(response).await;
        assert_eq!(status, StatusCode::OK);

        let vaults = body["vaults"]
            .as_array()
            .expect("vaults should be an array");
        assert_eq!(vaults.len(), 1);
        assert_eq!(vaults[0]["registered_tx_key_count"], serde_json::json!(1));
        assert_eq!(vaults[0]["sync_state"], serde_json::json!("syncing_to_tip"));
        assert_eq!(
            vaults[0]["inventory_fingerprint"],
            serde_json::json!(build_vault_inventory_fingerprint(
                VALID_VAULT_PUB_KEY,
                321,
                &[],
                &[registered, local],
            ))
        );
    }

    #[tokio::test]
    async fn get_outbounds_and_prune_outbounds_expose_and_trim_state() {
        let (state, _data_dir) = chain_test_state("http://127.0.0.1:18081");
        let mut stale = sample_outgoing(
            "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
            VALID_VAULT_PUB_KEY,
            "48from-a",
            "8to-a",
            1,
        );
        stale.tx_keys = vec!["aa".repeat(32)];
        let mut keep = sample_outgoing(
            "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
            VALID_VAULT_PUB_KEY,
            "48from-b",
            "8to-b",
            2,
        );
        keep.tx_keys = vec!["bb".repeat(32)];
        state
            .store
            .save_outgoing_transfer(&stale)
            .expect("failed to seed stale outgoing transfer");
        state
            .store
            .save_outgoing_transfer(&keep)
            .expect("failed to seed keep outgoing transfer");

        let listed = get_outbounds(State(state.clone()), Path(VALID_VAULT_PUB_KEY.to_string()))
            .await
            .into_response();
        let (list_status, body) = response_to_status_json(listed).await;
        assert_eq!(list_status, StatusCode::OK);
        assert_eq!(body["outgoing"].as_array().map(|v| v.len()), Some(2));

        let pruned = prune_outbounds(
            State(state.clone()),
            Json(crate::api::types::PruneOutgoingTransfersRequest {
                vault_pub_key: VALID_VAULT_PUB_KEY.to_string(),
                tx_hashes: vec![stale.tx_hash.clone()],
                observed_mined_only: true,
                tx_key_backed_only: true,
            }),
        )
        .await
        .into_response();
        let (prune_status, prune_body) = response_to_status_json(pruned).await;
        assert_eq!(prune_status, StatusCode::OK);
        assert_eq!(prune_body["removed"].as_array().map(|v| v.len()), Some(1));

        let remaining = state
            .store
            .load_outgoing_transfers(VALID_VAULT_PUB_KEY)
            .expect("failed to load outgoing transfers");
        assert_eq!(remaining.len(), 1);
        assert_eq!(remaining[0].tx_hash, keep.tx_hash);
    }

    #[tokio::test]
    async fn registered_tx_key_inventory_prune_preserves_local_signed_outbound() {
        let (state, _data_dir) = chain_test_state("http://127.0.0.1:18081");

        let mut local_signed = sample_outgoing(
            "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
            VALID_VAULT_PUB_KEY,
            "48from-a",
            "8to-a",
            1,
        );
        local_signed.tx_keys = vec!["aa".repeat(32)];
        local_signed.status = OutgoingTransferStatus::SignedPendingObservation;
        local_signed.observed_height = None;
        local_signed.registered_inventory = true;

        let mut stale_registered = sample_outgoing(
            "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
            VALID_VAULT_PUB_KEY,
            "48from-b",
            "8to-b",
            2,
        );
        stale_registered.tx_keys = vec!["bb".repeat(32)];
        stale_registered.registered_inventory = true;
        stale_registered.msg_id = format!("attestation:{}", stale_registered.tx_hash);

        state
            .store
            .save_outgoing_transfer(&local_signed)
            .expect("failed to seed local signed outgoing transfer");
        state
            .store
            .save_outgoing_transfer(&stale_registered)
            .expect("failed to seed stale registered outgoing transfer");

        reconcile_registered_tx_key_inventory(&state, VALID_VAULT_PUB_KEY, Vec::new())
            .await
            .expect("empty registered inventory should reconcile");

        let records = state
            .store
            .load_outgoing_transfers(VALID_VAULT_PUB_KEY)
            .expect("outgoing transfers should load");
        assert_eq!(records.len(), 1);
        assert_eq!(records[0].tx_hash, local_signed.tx_hash);
        assert_eq!(
            records[0].status,
            OutgoingTransferStatus::SignedPendingObservation
        );
        assert!(records[0].registered_inventory);
    }

    #[tokio::test]
    async fn scanner_sync_recovers_missing_added_outputs_from_persisted_state() {
        let daemon_server = axum_test::TestServer::builder()
            .http_transport()
            .build(axum::Router::new().route(
                "/json_rpc",
                axum::routing::post(|Json(req): Json<serde_json::Value>| async move {
                    let method = req
                        .get("method")
                        .and_then(serde_json::Value::as_str)
                        .unwrap_or_default();
                    match method {
                        "get_block_header_by_height" => (
                            StatusCode::OK,
                            Json(serde_json::json!({
                                "result": {
                                    "block_header": {
                                        "height": 120,
                                        "hash": "hash120",
                                        "prev_hash": "hash119",
                                        "timestamp": 1,
                                        "major_version": 16,
                                        "minor_version": 16,
                                        "nonce": 1,
                                        "reward": 1,
                                        "num_txes": 0
                                    }
                                }
                            })),
                        ),
                        _ => (
                            StatusCode::NOT_FOUND,
                            Json(serde_json::json!({"error": "unsupported method"})),
                        ),
                    }
                }),
            ))
            .expect("failed to start daemon test server");
        let daemon_url = daemon_server
            .server_address()
            .expect("daemon test server address missing")
            .to_string();
        let (state, _data_dir) = chain_test_state(&daemon_url);

        let output = TrackedOutput {
            tx_hash: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc".to_string(),
            output_index: 1,
            amount: 321,
            height: 120,
            key_image: None,
            spent_by_key_image_height: None,
            state: OutputState::Unspent,
            reservation: None,
            subaddress_major: 0,
            subaddress_minor: 0,
            wallet_output_hex: Some("cc".to_string()),
            address: Some("48vault".to_string()),
            note: Some("r:00002".to_string()),
        };
        state
            .store
            .register_vault(
                VALID_VAULT_PUB_KEY,
                &test_group_key_hex(),
                "48vault",
                "test",
                0,
            )
            .expect("failed to register vault");
        state
            .store
            .save_outputs(VALID_VAULT_PUB_KEY, std::slice::from_ref(&output))
            .expect("failed to seed outputs");
        state
            .store
            .save_scan_block_meta(
                VALID_VAULT_PUB_KEY,
                &ScanBlockMeta {
                    height: 120,
                    block_hash: "hash120".to_string(),
                    parent_hash: "hash119".to_string(),
                    added_outputs: vec![],
                    spent_key_images: vec![],
                    spent_by_key_images: vec![],
                },
            )
            .expect("failed to seed scan block metadata");
        state
            .store
            .save_scan_height(VALID_VAULT_PUB_KEY, 120)
            .expect("failed to seed scan height");

        let req = ScannerSyncRequest {
            height: 120,
            tip_height: 9_999,
            block_hash: "hash120".to_string(),
            parent_hash: "hash119".to_string(),
            reorg_window: 120,
        };
        let response = scanner_sync(State(state.clone()), Json(req))
            .await
            .into_response();
        let (status, body) = response_to_status_json(response).await;
        assert_eq!(status, StatusCode::OK);

        let incoming = body["incoming"]
            .as_array()
            .expect("incoming should be an array");
        assert_eq!(incoming.len(), 1);
        assert_eq!(incoming[0]["txid"], serde_json::json!(output.tx_hash));
        assert_eq!(incoming[0]["note"], serde_json::json!("r:00002"));

        let healed = state
            .store
            .load_scan_block_meta(VALID_VAULT_PUB_KEY, 120)
            .expect("failed to load healed scan block metadata")
            .expect("healed scan block metadata missing");
        assert_eq!(healed.added_outputs.len(), 1);
        assert_eq!(healed.added_outputs[0].tx_hash, output.tx_hash);
        assert_eq!(healed.added_outputs[0].output_index, output.output_index);
    }

    #[tokio::test]
    async fn scanner_sync_reports_rescan_outputs_without_block_meta() {
        let daemon_server = axum_test::TestServer::builder()
            .http_transport()
            .build(axum::Router::new().route(
                "/json_rpc",
                axum::routing::post(|Json(req): Json<serde_json::Value>| async move {
                    let method = req
                        .get("method")
                        .and_then(serde_json::Value::as_str)
                        .unwrap_or_default();
                    match method {
                        "get_block_header_by_height" => (
                            StatusCode::OK,
                            Json(serde_json::json!({
                                "result": {
                                    "block_header": {
                                        "height": 120,
                                        "hash": "hash120",
                                        "prev_hash": "hash119",
                                        "timestamp": 1,
                                        "major_version": 16,
                                        "minor_version": 16,
                                        "nonce": 1,
                                        "reward": 1,
                                        "num_txes": 0
                                    }
                                }
                            })),
                        ),
                        _ => (
                            StatusCode::NOT_FOUND,
                            Json(serde_json::json!({"error": "unsupported method"})),
                        ),
                    }
                }),
            ))
            .expect("failed to start daemon test server");
        let daemon_url = daemon_server
            .server_address()
            .expect("daemon test server address missing")
            .to_string();
        let (state, _data_dir) = chain_test_state(&daemon_url);

        let output = TrackedOutput {
            tx_hash: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd".to_string(),
            output_index: 0,
            amount: 500,
            height: 120,
            key_image: None,
            spent_by_key_image_height: None,
            state: OutputState::Unspent,
            reservation: None,
            subaddress_major: 0,
            subaddress_minor: 0,
            wallet_output_hex: Some("dd".to_string()),
            address: Some("48vault".to_string()),
            note: None,
        };
        state
            .store
            .register_vault(
                VALID_VAULT_PUB_KEY,
                &test_group_key_hex(),
                "48vault",
                "test",
                0,
            )
            .expect("register vault");

        // Simulate rescan: output in store, scan_height advanced, but NO ScanBlockMeta.
        state
            .store
            .save_outputs(VALID_VAULT_PUB_KEY, std::slice::from_ref(&output))
            .expect("save outputs");
        state
            .store
            .save_scan_height(VALID_VAULT_PUB_KEY, 120)
            .expect("save scan height");
        // Intentionally NOT saving ScanBlockMeta — this is what rescan omits.

        let req = ScannerSyncRequest {
            height: 120,
            tip_height: 9_999,
            block_hash: "hash120".to_string(),
            parent_hash: "hash119".to_string(),
            reorg_window: 120,
        };
        let response = scanner_sync(State(state.clone()), Json(req))
            .await
            .into_response();
        let (status, body) = response_to_status_json(response).await;
        assert_eq!(status, StatusCode::OK);

        let incoming = body["incoming"]
            .as_array()
            .expect("incoming should be an array");
        assert_eq!(
            incoming.len(),
            1,
            "rescan-added output should appear as incoming even without ScanBlockMeta; got: {incoming:?}"
        );
        assert_eq!(incoming[0]["txid"], serde_json::json!(output.tx_hash));
        assert_eq!(incoming[0]["amount"], serde_json::json!(output.amount));
    }

    #[tokio::test]
    async fn scanner_sync_uses_persisted_birthday_when_scan_height_is_missing() {
        let daemon_server = axum_test::TestServer::builder()
            .http_transport()
            .build(axum::Router::new().route(
                "/json_rpc",
                axum::routing::post(|Json(req): Json<serde_json::Value>| async move {
                    let method = req
                        .get("method")
                        .and_then(serde_json::Value::as_str)
                        .unwrap_or_default();
                    match method {
                        "get_block_header_by_height" => (
                            StatusCode::OK,
                            Json(serde_json::json!({
                                "result": {
                                    "block_header": {
                                        "height": 120,
                                        "hash": "hash120",
                                        "prev_hash": "hash119",
                                        "timestamp": 1,
                                        "major_version": 16,
                                        "minor_version": 16,
                                        "nonce": 1,
                                        "reward": 1,
                                        "num_txes": 0
                                    }
                                }
                            })),
                        ),
                        _ => (
                            StatusCode::NOT_FOUND,
                            Json(serde_json::json!({"error": "unsupported method"})),
                        ),
                    }
                }),
            ))
            .expect("failed to start daemon test server");
        let daemon_url = daemon_server
            .server_address()
            .expect("daemon test server address missing")
            .to_string();
        let (state, _data_dir) = chain_test_state(&daemon_url);
        let vault_pub_key = BOOTSTRAP_VAULT_PUB_KEY;
        let group_key = BOOTSTRAP_GROUP_KEY_HEX.to_string();
        let address = xmr::derive_vault_address(&group_key, state.chain_network)
            .expect("address should derive");

        let output = TrackedOutput {
            tx_hash: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee".to_string(),
            output_index: 0,
            amount: 500,
            height: 120,
            key_image: None,
            spent_by_key_image_height: None,
            state: OutputState::Unspent,
            reservation: None,
            subaddress_major: 0,
            subaddress_minor: 0,
            wallet_output_hex: Some("ee".to_string()),
            address: Some("48vault".to_string()),
            note: None,
        };
        let response = register_vault(
            State(state.clone()),
            Json(RegisterVaultRequest {
                vault_pub_key: vault_pub_key.to_string(),
                group_key,
                address,
                source: Some("test".to_string()),
                spend_key_hex: Some(BOOTSTRAP_SPEND_KEY_HEX.to_string()),
                birthday: Some(120),
                key_images: None,
                tx_keys: None,
            }),
        )
        .await
        .into_response();
        let (status, _) = response_to_status_json(response).await;
        assert_eq!(status, StatusCode::OK);

        state
            .store
            .save_outputs(vault_pub_key, std::slice::from_ref(&output))
            .expect("save outputs");

        let req = ScannerSyncRequest {
            height: 120,
            tip_height: 9_999,
            block_hash: "hash120".to_string(),
            parent_hash: "hash119".to_string(),
            reorg_window: 120,
        };
        let response = scanner_sync(State(state.clone()), Json(req))
            .await
            .into_response();
        let (status, body) = response_to_status_json(response).await;
        assert_eq!(status, StatusCode::OK);

        let incoming = body["incoming"]
            .as_array()
            .expect("incoming should be an array");
        assert_eq!(incoming.len(), 1);
        assert_eq!(incoming[0]["txid"], serde_json::json!(output.tx_hash));

        let persisted_height = state
            .store
            .load_scan_height(vault_pub_key)
            .expect("scan height load should succeed");
        assert_eq!(persisted_height, Some(120));
    }

    #[tokio::test]
    async fn scanner_sync_skips_rollback_for_pruned_historical_height_without_meta() {
        let daemon_server = axum_test::TestServer::builder()
            .http_transport()
            .build(axum::Router::new().route(
                "/json_rpc",
                axum::routing::post(|Json(req): Json<serde_json::Value>| async move {
                    let method = req
                        .get("method")
                        .and_then(serde_json::Value::as_str)
                        .unwrap_or_default();
                    match method {
                        "get_block_header_by_height" => {
                            let height = req
                                .get("params")
                                .and_then(|params| params.get("height"))
                                .and_then(serde_json::Value::as_u64)
                                .unwrap_or_default();
                            (
                                StatusCode::OK,
                                Json(serde_json::json!({
                                    "result": {
                                        "block_header": {
                                            "height": height,
                                            "hash": format!("hash{height}"),
                                            "prev_hash": format!("hash{}", height.saturating_sub(1)),
                                            "timestamp": 1,
                                            "major_version": 16,
                                            "minor_version": 16,
                                            "nonce": 1,
                                            "reward": 1,
                                            "num_txes": 0
                                        }
                                    }
                                })),
                            )
                        }
                        _ => (
                            StatusCode::NOT_FOUND,
                            Json(serde_json::json!({"error": "unsupported method"})),
                        ),
                    }
                }),
            ))
            .expect("failed to start daemon test server");
        let daemon_url = daemon_server
            .server_address()
            .expect("daemon test server address missing")
            .to_string();
        let (state, _data_dir) = chain_test_state(&daemon_url);

        let old_output = TrackedOutput {
            tx_hash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa".to_string(),
            output_index: 0,
            amount: 500,
            height: 120,
            key_image: None,
            spent_by_key_image_height: None,
            state: OutputState::Unspent,
            reservation: None,
            subaddress_major: 0,
            subaddress_minor: 0,
            wallet_output_hex: Some("aa".to_string()),
            address: Some("48vault".to_string()),
            note: None,
        };
        let recent_output = TrackedOutput {
            tx_hash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb".to_string(),
            output_index: 0,
            amount: 700,
            height: 350,
            key_image: None,
            spent_by_key_image_height: None,
            state: OutputState::Unspent,
            reservation: None,
            subaddress_major: 0,
            subaddress_minor: 0,
            wallet_output_hex: Some("bb".to_string()),
            address: Some("48vault".to_string()),
            note: None,
        };

        state
            .store
            .register_vault(
                VALID_VAULT_PUB_KEY,
                &test_group_key_hex(),
                "48vault",
                "test",
                0,
            )
            .expect("register vault");
        state
            .store
            .save_outputs(
                VALID_VAULT_PUB_KEY,
                &[old_output.clone(), recent_output.clone()],
            )
            .expect("save outputs");
        state
            .store
            .save_scan_height(VALID_VAULT_PUB_KEY, 400)
            .expect("save scan height");
        state
            .store
            .save_scan_block_meta(
                VALID_VAULT_PUB_KEY,
                &ScanBlockMeta {
                    height: 400,
                    block_hash: "hash400".to_string(),
                    parent_hash: "hash399".to_string(),
                    added_outputs: vec![],
                    spent_key_images: vec![],
                    spent_by_key_images: vec![],
                },
            )
            .expect("save recent scan block meta");

        let response = scanner_sync(
            State(state.clone()),
            Json(ScannerSyncRequest {
                height: 120,
                tip_height: 9_999,
                block_hash: "hash120".to_string(),
                parent_hash: "hash119".to_string(),
                reorg_window: 120,
            }),
        )
        .await
        .into_response();
        let (status, body) = response_to_status_json(response).await;
        assert_eq!(status, StatusCode::OK, "body: {body:?}");

        let persisted_height = state
            .store
            .load_scan_height(VALID_VAULT_PUB_KEY)
            .expect("scan height load should succeed");
        assert_eq!(
            persisted_height,
            Some(400),
            "historical scanner_sync outside the retained reorg window must not rewind scan height"
        );

        let outputs = state
            .store
            .load_outputs(VALID_VAULT_PUB_KEY)
            .expect("outputs should still load");
        assert_eq!(outputs.len(), 2);
        assert!(
            outputs
                .iter()
                .any(|output| output.tx_hash == old_output.tx_hash),
            "historical output should remain present after pruned-height scanner_sync"
        );
        assert!(
            outputs
                .iter()
                .any(|output| output.tx_hash == recent_output.tx_hash),
            "recent output should remain present after pruned-height scanner_sync"
        );
    }

    #[tokio::test]
    async fn scanner_sync_balance_unlocks_follow_validated_height_not_tip_height() {
        let daemon_server = axum_test::TestServer::builder()
            .http_transport()
            .build(axum::Router::new().route(
                "/json_rpc",
                axum::routing::post(|Json(req): Json<serde_json::Value>| async move {
                    let method = req
                        .get("method")
                        .and_then(serde_json::Value::as_str)
                        .unwrap_or_default();
                    match method {
                        "get_block_header_by_height" => {
                            let height = req
                                .get("params")
                                .and_then(|params| params.get("height"))
                                .and_then(serde_json::Value::as_u64)
                                .unwrap_or_default();
                            let (hash, prev_hash) = match height {
                                109 => ("hash109", "hash108"),
                                115 => ("hash115", "hash114"),
                                _ => ("unknown", "unknown"),
                            };
                            (
                                StatusCode::OK,
                                Json(serde_json::json!({
                                    "result": {
                                        "block_header": {
                                            "height": height,
                                            "hash": hash,
                                            "prev_hash": prev_hash,
                                            "timestamp": 1,
                                            "major_version": 16,
                                            "minor_version": 16,
                                            "nonce": 1,
                                            "reward": 1,
                                            "num_txes": 0
                                        }
                                    }
                                })),
                            )
                        }
                        _ => (
                            StatusCode::NOT_FOUND,
                            Json(serde_json::json!({"error": "unsupported method"})),
                        ),
                    }
                }),
            ))
            .expect("failed to start daemon test server");
        let daemon_url = daemon_server
            .server_address()
            .expect("daemon test server address missing")
            .to_string();
        let (state, _data_dir) = chain_test_state(&daemon_url);

        let output = TrackedOutput {
            tx_hash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb".to_string(),
            output_index: 0,
            amount: 777,
            height: 105,
            key_image: None,
            spent_by_key_image_height: None,
            state: OutputState::Unspent,
            reservation: None,
            subaddress_major: 0,
            subaddress_minor: 0,
            wallet_output_hex: Some("bb".to_string()),
            address: Some("48vault".to_string()),
            note: None,
        };
        state
            .store
            .register_vault(
                VALID_VAULT_PUB_KEY,
                &test_group_key_hex(),
                "48vault",
                "test",
                0,
            )
            .expect("failed to register vault");
        state
            .store
            .save_outputs(VALID_VAULT_PUB_KEY, &[output])
            .expect("failed to seed outputs");
        state
            .store
            .save_scan_block_meta(
                VALID_VAULT_PUB_KEY,
                &ScanBlockMeta {
                    height: 109,
                    block_hash: "hash109".to_string(),
                    parent_hash: "hash108".to_string(),
                    added_outputs: vec![],
                    spent_key_images: vec![],
                    spent_by_key_images: vec![],
                },
            )
            .expect("failed to seed scan metadata for height 109");
        state
            .store
            .save_scan_block_meta(
                VALID_VAULT_PUB_KEY,
                &ScanBlockMeta {
                    height: 115,
                    block_hash: "hash115".to_string(),
                    parent_hash: "hash114".to_string(),
                    added_outputs: vec![],
                    spent_key_images: vec![],
                    spent_by_key_images: vec![],
                },
            )
            .expect("failed to seed scan metadata for height 115");
        state
            .store
            .save_scan_height(VALID_VAULT_PUB_KEY, 115)
            .expect("failed to seed scan height");

        let first = scanner_sync(
            State(state.clone()),
            Json(ScannerSyncRequest {
                height: 109,
                tip_height: 9_999,
                block_hash: "hash109".to_string(),
                parent_hash: "hash108".to_string(),
                reorg_window: 120,
            }),
        )
        .await
        .into_response();
        let (status, _) = response_to_status_json(first).await;
        assert_eq!(status, StatusCode::OK);

        let first_balance =
            get_balance(State(state.clone()), Path(VALID_VAULT_PUB_KEY.to_string()))
                .await
                .into_response();
        let (status, body) = response_to_status_json(first_balance).await;
        assert_eq!(status, StatusCode::OK);
        assert_eq!(body["balance"], serde_json::json!(777u64));
        assert_eq!(body["unlocked_balance"], serde_json::json!(0u64));
        assert_eq!(body["spendable_balance"], serde_json::json!(0u64));

        let second = scanner_sync(
            State(state.clone()),
            Json(ScannerSyncRequest {
                height: 115,
                tip_height: 9_999,
                block_hash: "hash115".to_string(),
                parent_hash: "hash114".to_string(),
                reorg_window: 120,
            }),
        )
        .await
        .into_response();
        let (status, _) = response_to_status_json(second).await;
        assert_eq!(status, StatusCode::OK);

        let second_balance = get_balance(State(state), Path(VALID_VAULT_PUB_KEY.to_string()))
            .await
            .into_response();
        let (status, body) = response_to_status_json(second_balance).await;
        assert_eq!(status, StatusCode::OK);
        assert_eq!(body["balance"], serde_json::json!(777u64));
        assert_eq!(body["unlocked_balance"], serde_json::json!(777u64));
        assert_eq!(body["spendable_balance"], serde_json::json!(777u64));
    }

    #[tokio::test]
    async fn get_outputs_defaults_to_spendable_view() {
        let outputs = vec![
            sample_output(
                "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
                0,
                OutputState::Unspent,
            ),
            sample_output(
                "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
                1,
                OutputState::Reserved,
            ),
            sample_output(
                "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
                2,
                OutputState::Spent,
            ),
        ];
        let (state, _data_dir, vault_pub_key) = outputs_test_state(outputs);
        let uri: Uri = format!("/v1/monero/outputs/{vault_pub_key}")
            .parse()
            .expect("invalid test uri");
        let query = Query::<OutputsQuery>::try_from_uri(&uri)
            .expect("failed to parse default outputs query");
        let (status, body) = do_get(state, vault_pub_key, query).await;

        assert_eq!(status, StatusCode::OK);
        let entries = body["outputs"]
            .as_array()
            .expect("outputs should be an array");
        assert_eq!(entries.len(), 1);
        assert_eq!(entries[0]["state"], serde_json::json!("unspent"));
    }

    #[tokio::test]
    async fn get_outputs_view_all_includes_reserved_and_spent_states() {
        let outputs = vec![
            sample_output(
                "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
                0,
                OutputState::Unspent,
            ),
            sample_output(
                "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
                1,
                OutputState::Reserved,
            ),
            sample_output(
                "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
                2,
                OutputState::Spent,
            ),
        ];
        let (state, _data_dir, vault_pub_key) = outputs_test_state(outputs);
        let uri: Uri = format!("/v1/monero/outputs/{vault_pub_key}?view=all")
            .parse()
            .expect("invalid test uri");
        let query = Query::<OutputsQuery>::try_from_uri(&uri)
            .expect("failed to parse lifecycle outputs query");
        let (status, body) = do_get(state, vault_pub_key, query).await;

        assert_eq!(status, StatusCode::OK);
        let entries = body["outputs"]
            .as_array()
            .expect("outputs should be an array");
        assert_eq!(entries.len(), 3);

        let mut seen = HashSet::new();
        for entry in entries {
            seen.insert(entry["state"].as_str().expect("state should be a string"));
        }
        assert!(seen.contains("unspent"));
        assert!(seen.contains("reserved"));
        assert!(seen.contains("spent"));
    }

    #[tokio::test]
    async fn get_outputs_source_store_reads_persisted_outputs() {
        let scanner_outputs = vec![sample_output(
            "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
            0,
            OutputState::Unspent,
        )];
        let (state, _data_dir, vault_pub_key) = outputs_test_state(scanner_outputs);

        let mut persisted = sample_output(
            "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
            0,
            OutputState::Spent,
        );
        persisted.key_image = Some("persisted-key-image".to_string());
        state
            .store
            .save_outputs(&vault_pub_key, &[persisted])
            .expect("failed to seed persisted outputs");

        let uri: Uri = format!("/v1/monero/outputs/{vault_pub_key}?view=all&source=store")
            .parse()
            .expect("invalid test uri");
        let query = Query::<OutputsQuery>::try_from_uri(&uri)
            .expect("failed to parse persisted outputs query");
        let (status, body) = do_get(state, vault_pub_key, query).await;

        assert_eq!(status, StatusCode::OK);
        let entries = body["outputs"]
            .as_array()
            .expect("outputs should be an array");
        assert_eq!(entries.len(), 1);
        assert_eq!(entries[0]["state"], serde_json::json!("spent"));
        assert_eq!(
            entries[0]["key_image"],
            serde_json::json!("persisted-key-image")
        );
    }

    #[tokio::test]
    async fn get_outputs_spendable_view_excludes_locked_unspent_outputs() {
        let mut locked = sample_output(
            "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
            3,
            OutputState::Unspent,
        );
        locked.height = 195;

        let outputs = vec![
            sample_output(
                "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
                0,
                OutputState::Unspent,
            ),
            locked,
        ];
        let (state, _data_dir, vault_pub_key) = outputs_test_state(outputs);
        let uri: Uri = format!("/v1/monero/outputs/{vault_pub_key}")
            .parse()
            .expect("invalid test uri");
        let query = Query::<OutputsQuery>::try_from_uri(&uri)
            .expect("failed to parse default outputs query");
        let (status, body) = do_get(state, vault_pub_key, query).await;

        assert_eq!(status, StatusCode::OK);
        let entries = body["outputs"]
            .as_array()
            .expect("outputs should be an array");
        assert_eq!(entries.len(), 1);
        assert_eq!(
            entries[0]["tx_hash"],
            serde_json::json!("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
        );
    }

    #[tokio::test]
    async fn get_outputs_invalid_view_returns_bad_request() {
        let uri: Uri = "/v1/monero/outputs/thorpub1vaulttest?view=invalid"
            .parse()
            .expect("invalid test uri");
        let rejection = Query::<OutputsQuery>::try_from_uri(&uri)
            .expect_err("invalid query value should be rejected");
        assert_eq!(rejection.into_response().status(), StatusCode::BAD_REQUEST);
    }

    #[tokio::test]
    async fn get_chain_height_returns_tip_height() {
        let daemon_server = axum_test::TestServer::builder()
            .http_transport()
            .build(axum::Router::new().route(
                "/json_rpc",
                axum::routing::post(|| async {
                    (
                        StatusCode::OK,
                        Json(serde_json::json!({
                            "result": {
                                "height": 321,
                                "top_block_hash": "abc123",
                                "target_height": 321,
                                "difficulty": 1,
                                "status": "OK"
                            }
                        })),
                    )
                }),
            ))
            .expect("failed to start daemon test server");
        let daemon_url = daemon_server
            .server_address()
            .expect("daemon test server address missing")
            .to_string();
        let (state, _data_dir) = chain_test_state(&daemon_url);

        let response = get_chain_height(State(state)).await.into_response();
        let (status, body) = response_to_status_json(response).await;
        assert_eq!(status, StatusCode::OK);
        assert_eq!(body["height"], serde_json::json!(320u64));
    }

    #[tokio::test]
    async fn get_chain_block_returns_block_payload() {
        let daemon_server = axum_test::TestServer::builder()
            .http_transport()
            .build(axum::Router::new().route(
                "/json_rpc",
                axum::routing::post(|Json(req): Json<serde_json::Value>| async move {
                    let method = req
                        .get("method")
                        .and_then(serde_json::Value::as_str)
                        .unwrap_or_default();
                    match method {
                        "get_info" => (
                            StatusCode::OK,
                            Json(serde_json::json!({
                                "result": {
                                    "height": 222,
                                    "top_block_hash": "tip",
                                    "target_height": 222,
                                    "difficulty": 1,
                                    "status": "OK"
                                }
                            })),
                        ),
                        "get_block" => (
                            StatusCode::OK,
                            Json(serde_json::json!({
                                "result": {
                                    "block_header": {
                                        "height": 200,
                                        "hash": "h200",
                                        "prev_hash": "h199",
                                        "timestamp": 1,
                                        "major_version": 16,
                                        "minor_version": 16,
                                        "nonce": 1,
                                        "reward": 640000000000u64,
                                        "num_txes": 2
                                    },
                                    "tx_hashes": ["tx1", "tx2"]
                                }
                            })),
                        ),
                        _ => (
                            StatusCode::NOT_FOUND,
                            Json(serde_json::json!({"error": "unsupported method"})),
                        ),
                    }
                }),
            ))
            .expect("failed to start daemon test server");
        let daemon_url = daemon_server
            .server_address()
            .expect("daemon test server address missing")
            .to_string();
        let (state, _data_dir) = chain_test_state(&daemon_url);

        let response = get_chain_block(State(state), Path(200))
            .await
            .into_response();
        let (status, body) = response_to_status_json(response).await;
        assert_eq!(status, StatusCode::OK);
        assert_eq!(body["height"], serde_json::json!(200u64));
        assert_eq!(body["hash"], serde_json::json!("h200"));
        assert_eq!(body["previous_hash"], serde_json::json!("h199"));
        let hashes = body["tx_hashes"]
            .as_array()
            .expect("tx_hashes must be array");
        assert_eq!(hashes.len(), 2);
    }

    #[tokio::test]
    async fn get_chain_block_conflicts_when_height_above_tip() {
        let daemon_server = axum_test::TestServer::builder()
            .http_transport()
            .build(axum::Router::new().route(
                "/json_rpc",
                axum::routing::post(|| async {
                    (
                        StatusCode::OK,
                        Json(serde_json::json!({
                            "result": {
                                "height": 10,
                                "top_block_hash": "tip",
                                "target_height": 10,
                                "difficulty": 1,
                                "status": "OK"
                            }
                        })),
                    )
                }),
            ))
            .expect("failed to start daemon test server");
        let daemon_url = daemon_server
            .server_address()
            .expect("daemon test server address missing")
            .to_string();
        let (state, _data_dir) = chain_test_state(&daemon_url);

        let response = get_chain_block(State(state), Path(11))
            .await
            .into_response();
        let (status, body) = response_to_status_json(response).await;
        assert_eq!(status, StatusCode::CONFLICT);
        assert!(body["error"]
            .as_str()
            .unwrap_or_default()
            .contains("above chain tip"));
    }

    #[tokio::test]
    async fn get_chain_tx_exists_returns_true_or_false() {
        let daemon_server = axum_test::TestServer::builder()
            .http_transport()
            .build(axum::Router::new().route(
                "/get_transactions",
                axum::routing::post(|Json(req): Json<serde_json::Value>| async move {
                    let hash = req
                        .get("txs_hashes")
                        .and_then(serde_json::Value::as_array)
                        .and_then(|hashes| hashes.first())
                        .and_then(serde_json::Value::as_str)
                        .unwrap_or_default()
                        .to_string();
                    let payload = match hash.as_str() {
                        "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" => {
                            serde_json::json!({
                                "txs": [{
                                    "tx_hash": hash,
                                    "as_hex": "deadbeef",
                                    "block_height": 77,
                                    "in_pool": false
                                }],
                                "status": "OK"
                            })
                        }
                        _ => serde_json::json!({
                            "txs": [],
                            "missed_tx": [hash],
                            "status": "OK"
                        }),
                    };
                    (StatusCode::OK, Json(payload))
                }),
            ))
            .expect("failed to start daemon test server");
        let daemon_url = daemon_server
            .server_address()
            .expect("daemon test server address missing")
            .to_string();
        let (state, _data_dir) = chain_test_state(&daemon_url);

        let found = get_chain_tx_exists(
            State(state.clone()),
            Path("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa".to_string()),
        )
        .await
        .into_response();
        let (status, body) = response_to_status_json(found).await;
        assert_eq!(status, StatusCode::OK);
        assert_eq!(body["exists"], serde_json::json!(true));

        let missed = get_chain_tx_exists(
            State(state),
            Path("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb".to_string()),
        )
        .await
        .into_response();
        let (status, body) = response_to_status_json(missed).await;
        assert_eq!(status, StatusCode::OK);
        assert_eq!(body["exists"], serde_json::json!(false));
    }

    #[tokio::test]
    async fn validate_outbound_reconcile_failure_is_best_effort() {
        let tx_hash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa";
        let synthetic = crate::tx::validation::synthetic_untracked_outbound_case_for_tests();
        let daemon_server = axum_test::TestServer::builder()
            .http_transport()
            .build(axum::Router::new().route(
                "/get_transactions",
                axum::routing::post(move |Json(req): Json<serde_json::Value>| {
                    let tx_hash = tx_hash.to_string();
                    let tx_hex = synthetic.tx_hex.clone();
                    async move {
                        let hash = req
                            .get("txs_hashes")
                            .and_then(serde_json::Value::as_array)
                            .and_then(|hashes| hashes.first())
                            .and_then(serde_json::Value::as_str)
                            .unwrap_or_default()
                            .to_string();
                        let payload = if hash.eq_ignore_ascii_case(&tx_hash) {
                            serde_json::json!({
                                "txs": [{
                                    "tx_hash": tx_hash,
                                    "as_hex": tx_hex,
                                    "block_height": 77,
                                    "in_pool": false
                                }],
                                "status": "OK"
                            })
                        } else {
                            serde_json::json!({
                                "txs": [],
                                "missed_tx": [hash],
                                "status": "OK"
                            })
                        };
                        (StatusCode::OK, Json(payload))
                    }
                }),
            ))
            .expect("failed to start daemon test server");
        let daemon_url = daemon_server
            .server_address()
            .expect("daemon test server address missing")
            .to_string();
        let (state, _data_dir) = chain_test_state(&daemon_url);

        state
            .scanner
            .lock()
            .register_vault(
                VALID_VAULT_PUB_KEY,
                &test_group_key_hex(),
                crate::config::ChainNetworkMode::Mocknet,
                vec![],
                None,
            )
            .expect("failed to register vault scanner state");

        let response = validate_outbound(
            State(state.clone()),
            Json(crate::api::types::ValidateOutboundRequest {
                vault_pub_key: VALID_VAULT_PUB_KEY.to_string(),
                tx_hash: tx_hash.to_string(),
                tx_keys: synthetic.tx_keys.clone(),
                registered_inventory: false,
                from_address: None,
                to_address: synthetic.to_address.clone(),
                amount: synthetic.amount,
                fee: synthetic.fee,
                memo: Some("OUT:TEST".to_string()),
                reconcile: true,
            }),
        )
        .await
        .into_response();
        let (status, body) = response_to_status_json(response).await;
        assert_eq!(status, StatusCode::OK, "body={body}");
        assert_eq!(body["tracked"], serde_json::json!(false));
        assert_eq!(body["marked"], serde_json::json!(0));
        assert_eq!(body["not_found"], serde_json::json!(0));

        let saved = state
            .store
            .load_outgoing_transfer(VALID_VAULT_PUB_KEY, tx_hash)
            .expect("failed to load outgoing transfer")
            .expect("outgoing transfer should exist");
        assert_eq!(saved.tx_keys, synthetic.tx_keys);
        // Compatibility validation can be driven by peer observation before
        // THORChain vault inventory has caught up. Keep it non-inventory until
        // register_vault replays the canonical tx-key inventory.
        assert!(!saved.registered_inventory);
        assert_eq!(saved.to_address, synthetic.to_address);
        assert_eq!(saved.amount, synthetic.amount);
        assert_eq!(saved.fee, synthetic.fee);
        assert_eq!(saved.status, OutgoingTransferStatus::ObservedMined);
    }

    #[tokio::test]
    async fn validate_outbound_reconcile_keeps_balance_unchanged() {
        let tx_hash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa";
        let synthetic = crate::tx::validation::synthetic_untracked_outbound_case_for_tests();
        let daemon_server = axum_test::TestServer::builder()
            .http_transport()
            .build(axum::Router::new().route(
                "/get_transactions",
                axum::routing::post(move |Json(req): Json<serde_json::Value>| {
                    let tx_hash = tx_hash.to_string();
                    let tx_hex = synthetic.tx_hex.clone();
                    async move {
                        let hash = req
                            .get("txs_hashes")
                            .and_then(serde_json::Value::as_array)
                            .and_then(|hashes| hashes.first())
                            .and_then(serde_json::Value::as_str)
                            .unwrap_or_default()
                            .to_string();
                        let payload = if hash.eq_ignore_ascii_case(&tx_hash) {
                            serde_json::json!({
                                "txs": [{
                                    "tx_hash": tx_hash,
                                    "as_hex": tx_hex,
                                    "block_height": 77,
                                    "in_pool": false
                                }],
                                "status": "OK"
                            })
                        } else {
                            serde_json::json!({
                                "txs": [],
                                "missed_tx": [hash],
                                "status": "OK"
                            })
                        };
                        (StatusCode::OK, Json(payload))
                    }
                }),
            ))
            .expect("failed to start daemon test server");
        let daemon_url = daemon_server
            .server_address()
            .expect("daemon test server address missing")
            .to_string();
        let (state, _data_dir) = chain_test_state(&daemon_url);

        let output = sample_output(
            "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
            0,
            OutputState::Unspent,
        );
        state
            .scanner
            .lock()
            .register_vault(
                VALID_VAULT_PUB_KEY,
                &test_group_key_hex(),
                crate::config::ChainNetworkMode::Mocknet,
                vec![output.clone()],
                None,
            )
            .expect("failed to register vault scanner state");
        state.scanner.lock().set_current_height(200);
        state
            .store
            .save_outputs(VALID_VAULT_PUB_KEY, std::slice::from_ref(&output))
            .expect("failed to seed outputs");

        let before = get_balance(State(state.clone()), Path(VALID_VAULT_PUB_KEY.to_string()))
            .await
            .into_response();
        let (before_status, before_body) = response_to_status_json(before).await;
        assert_eq!(before_status, StatusCode::OK);
        assert_eq!(before_body["balance"], serde_json::json!(1_000_000_000u64));
        assert_eq!(
            before_body["unlocked_balance"],
            serde_json::json!(1_000_000_000u64)
        );

        let response = validate_outbound(
            State(state.clone()),
            Json(crate::api::types::ValidateOutboundRequest {
                vault_pub_key: VALID_VAULT_PUB_KEY.to_string(),
                tx_hash: tx_hash.to_string(),
                tx_keys: synthetic.tx_keys.clone(),
                registered_inventory: false,
                from_address: None,
                to_address: synthetic.to_address.clone(),
                amount: synthetic.amount,
                fee: synthetic.fee,
                memo: Some("OUT:TEST".to_string()),
                reconcile: true,
            }),
        )
        .await
        .into_response();
        let (status, body) = response_to_status_json(response).await;
        assert_eq!(status, StatusCode::OK, "body={body}");
        assert_eq!(body["tracked"], serde_json::json!(false));
        assert_eq!(body["marked"], serde_json::json!(0));
        assert_eq!(body["not_found"], serde_json::json!(0));

        let after = get_balance(State(state.clone()), Path(VALID_VAULT_PUB_KEY.to_string()))
            .await
            .into_response();
        let (after_status, after_body) = response_to_status_json(after).await;
        assert_eq!(after_status, StatusCode::OK);
        assert_eq!(after_body["balance"], serde_json::json!(1_000_000_000u64));
        assert_eq!(
            after_body["unlocked_balance"],
            serde_json::json!(1_000_000_000u64)
        );

        let outputs = state
            .store
            .load_outputs(VALID_VAULT_PUB_KEY)
            .expect("failed to reload outputs");
        assert_eq!(outputs.len(), 1);
        assert_eq!(outputs[0].state, OutputState::Unspent);
    }

    #[tokio::test]
    async fn get_chain_tx_exists_rejects_ambiguous_daemon_result() {
        let daemon_server = axum_test::TestServer::builder()
            .http_transport()
            .build(axum::Router::new().route(
                "/get_transactions",
                axum::routing::post(|| async {
                    (
                        StatusCode::OK,
                        Json(serde_json::json!({
                            "txs": [],
                            "missed_tx": [],
                            "status": "OK"
                        })),
                    )
                }),
            ))
            .expect("failed to start daemon test server");
        let daemon_url = daemon_server
            .server_address()
            .expect("daemon test server address missing")
            .to_string();
        let (state, _data_dir) = chain_test_state(&daemon_url);

        let response = get_chain_tx_exists(
            State(state),
            Path("cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc".to_string()),
        )
        .await
        .into_response();
        let (status, body) = response_to_status_json(response).await;
        assert_eq!(status, StatusCode::INTERNAL_SERVER_ERROR);
        assert!(body["error"]
            .as_str()
            .unwrap_or_default()
            .contains("ambiguous"));
    }
}
