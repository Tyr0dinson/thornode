use std::collections::{HashMap, HashSet};
use std::sync::Arc;

use axum::{
    extract::{Path, Query, State},
    http::StatusCode,
    response::IntoResponse,
    Json,
};
use bech32::{decode as bech32_decode, FromBase32};
use monero_wallet::rpc::Rpc;
use rand::rngs::OsRng;
use serde::de::DeserializeOwned;
use sha2::{Digest, Sha256};
use tracing::{error, info, warn};
use uuid::Uuid;

use super::types::*;
use crate::config::ChainNetworkMode;
use crate::frost::dkg::DkgCoordinator;
use crate::frost::keys::ThresholdKeyShare;
use crate::frost::sign::SignCoordinator;
use crate::state::store::{
    IdempotencyClaimResult, OutputRef, OutputState, ScanBlockMeta, TrackedOutput,
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
    pub scanner: parking_lot::Mutex<Scanner>,
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

fn error_json(msg: impl Into<String>) -> serde_json::Value {
    serde_json::to_value(ErrorResponse::new(msg.into())).unwrap()
}

fn required_msg_id(value: &str) -> Result<String, (StatusCode, serde_json::Value)> {
    let msg_id = value.trim().to_string();
    if msg_id.is_empty() {
        return Err((StatusCode::BAD_REQUEST, error_json("msg_id is required")));
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
    if hrp != "thorpub" && hrp != "tthorpub" {
        return false;
    }
    !data.is_empty()
}

fn normalized_group_key_hex(value: &str) -> Result<String, (StatusCode, serde_json::Value)> {
    let normalized = value.trim().to_ascii_lowercase();
    if normalized.len() != 64 {
        return Err((
            StatusCode::BAD_REQUEST,
            error_json("group_key must be 32-byte hex"),
        ));
    }
    let decoded = match hex::decode(&normalized) {
        Ok(decoded) => decoded,
        Err(_) => {
            return Err((
                StatusCode::BAD_REQUEST,
                error_json("group_key must be valid hex"),
            ));
        }
    };
    if decoded.len() != 32 {
        return Err((
            StatusCode::BAD_REQUEST,
            error_json("group_key must be 32-byte hex"),
        ));
    }
    Ok(normalized)
}

const ED25519_PUBKEY_AMINO_PREFIX: &[u8] = &[0x16, 0x24, 0xde, 0x64, 0x20];

fn vault_pub_key_matches_group_key(
    vault_pub_key: &str,
    group_key: &str,
) -> Result<bool, (StatusCode, serde_json::Value)> {
    let (_, data, _) = match bech32_decode(vault_pub_key.trim()) {
        Ok(decoded) => decoded,
        Err(_) => {
            return Err((
                StatusCode::BAD_REQUEST,
                error_json("invalid vault_pub_key format"),
            ));
        }
    };

    let decoded = Vec::<u8>::from_base32(&data).map_err(|_| {
        (
            StatusCode::BAD_REQUEST,
            error_json("invalid vault_pub_key format"),
        )
    })?;
    if decoded.len() != ED25519_PUBKEY_AMINO_PREFIX.len() + 32 {
        return Ok(false);
    }
    if !decoded.starts_with(ED25519_PUBKEY_AMINO_PREFIX) {
        return Ok(false);
    }

    let group_key = hex::decode(group_key).map_err(|_| {
        (
            StatusCode::BAD_REQUEST,
            error_json("group_key must be valid hex"),
        )
    })?;
    Ok(decoded[ED25519_PUBKEY_AMINO_PREFIX.len()..] == group_key)
}

fn ensure_bootstrap_frost_key_material(
    state: &AppState,
    vault_pub_key: &str,
    group_key: &str,
    spend_key_hex: Option<&str>,
) -> Result<(), (StatusCode, serde_json::Value)> {
    let Some(spend_key_hex) = spend_key_hex
        .map(str::trim)
        .filter(|value| !value.is_empty())
    else {
        return Ok(());
    };

    let derived_group_key = xmr::group_key_hex_from_spend_seed_hex(spend_key_hex).map_err(|e| {
        (
            StatusCode::BAD_REQUEST,
            error_json(format!("invalid spend_key_hex: {e}")),
        )
    })?;
    if derived_group_key != group_key {
        return Err((
            StatusCode::BAD_REQUEST,
            error_json("spend_key_hex does not match group_key"),
        ));
    }
    if !vault_pub_key_matches_group_key(vault_pub_key, group_key)? {
        return Err((
            StatusCode::BAD_REQUEST,
            error_json("vault_pub_key does not match group_key"),
        ));
    }

    let existing = state.store.load_frost_keys(group_key).map_err(|e| {
        (
            StatusCode::INTERNAL_SERVER_ERROR,
            error_json(format!("failed to load frost key material: {e}")),
        )
    })?;
    if existing.is_some() {
        return Ok(());
    }

    let key_share = ThresholdKeyShare::from_spend_seed_hex(spend_key_hex).map_err(|e| {
        (
            StatusCode::BAD_REQUEST,
            error_json(format!("failed to derive frost key material: {e}")),
        )
    })?;
    state
        .store
        .save_frost_keys(group_key, &key_share.to_bytes())
        .map_err(|e| {
            (
                StatusCode::INTERNAL_SERVER_ERROR,
                error_json(format!("failed to persist frost key material: {e}")),
            )
        })
}

fn ensure_registered_vault_state(
    state: &AppState,
    vault_pub_key: &str,
    group_key: &str,
    address: &str,
    source: &str,
    birthday: Option<u64>,
) -> Result<bool, String> {
    let registered = state
        .store
        .register_vault(vault_pub_key, group_key, address, source)
        .map_err(|e| format!("failed to register vault mapping: {e}"))?;

    let outputs = state
        .store
        .load_outputs(vault_pub_key)
        .map_err(|e| format!("failed to load vault outputs: {e}"))?;
    let scan_height = state
        .store
        .load_scan_height(vault_pub_key)
        .map_err(|e| format!("failed to load vault scan height: {e}"))?
        .unwrap_or_else(|| {
            let b = birthday.unwrap_or(0);
            if b > 0 {
                b
            } else {
                state.scanner.lock().current_height()
            }
        });

    let mut scanner = state.scanner.lock();
    scanner
        .register_vault(
            vault_pub_key,
            group_key,
            state.chain_network,
            outputs.clone(),
            Some(scan_height),
        )
        .map_err(|e| format!("failed to initialize vault scanner state: {e}"))?;
    scanner.merge_output_states(vault_pub_key, &outputs);

    Ok(registered)
}

// ---- DKG Routes ----

pub async fn dkg_round1(
    State(state): State<Arc<AppState>>,
    Json(req): Json<DkgRound1Request>,
) -> impl IntoResponse {
    if !valid_vault_pub_key(&req.vault_pub_key) {
        return (
            StatusCode::BAD_REQUEST,
            Json(error_json("invalid vault_pub_key format")),
        );
    }

    match state.dkg.round1(
        &req.session_id,
        &req.vault_pub_key,
        req.threshold,
        req.n,
        req.participant_index,
        &req.context,
    ) {
        Ok(commitments) => (
            StatusCode::OK,
            Json(
                serde_json::to_value(DkgRound1Response {
                    commitments: hex::encode(commitments),
                })
                .unwrap(),
            ),
        ),
        Err(e) => {
            error!(error = %e, "DKG round 1 failed");
            let msg = e.to_string();
            let status = if is_capacity_error(&msg) {
                StatusCode::SERVICE_UNAVAILABLE
            } else {
                StatusCode::BAD_REQUEST
            };
            (
                status,
                Json(serde_json::to_value(ErrorResponse::new(msg)).unwrap()),
            )
        }
    }
}

pub async fn dkg_round2(
    State(state): State<Arc<AppState>>,
    Json(req): Json<DkgRound2Request>,
) -> impl IntoResponse {
    let all_commitments = match parse_hex_map(&req.all_commitments) {
        Ok(c) => c,
        Err(e) => {
            return (
                StatusCode::BAD_REQUEST,
                Json(serde_json::to_value(ErrorResponse::new(e)).unwrap()),
            );
        }
    };

    match state.dkg.round2(&req.session_id, all_commitments) {
        Ok(shares) => {
            let hex_shares: HashMap<String, String> = shares
                .into_iter()
                .map(|(k, v)| (k.to_string(), hex::encode(v)))
                .collect();
            (
                StatusCode::OK,
                Json(
                    serde_json::to_value(DkgRound2Response {
                        encrypted_shares: hex_shares,
                    })
                    .unwrap(),
                ),
            )
        }
        Err(e) => {
            error!(error = %e, "DKG round 2 failed");
            (
                StatusCode::BAD_REQUEST,
                Json(serde_json::to_value(ErrorResponse::new(e.to_string())).unwrap()),
            )
        }
    }
}

pub async fn dkg_round3(
    State(state): State<Arc<AppState>>,
    Json(req): Json<DkgRound3Request>,
) -> impl IntoResponse {
    let all_shares = match parse_hex_map(&req.all_shares) {
        Ok(s) => s,
        Err(e) => {
            return (
                StatusCode::BAD_REQUEST,
                Json(serde_json::to_value(ErrorResponse::new(e)).unwrap()),
            );
        }
    };

    match state.dkg.round3(&req.session_id, all_shares) {
        Ok((address, group_key)) => (
            StatusCode::OK,
            Json(serde_json::to_value(DkgRound3Response { address, group_key }).unwrap()),
        ),
        Err(e) => {
            error!(error = %e, "DKG round 3 failed");
            (
                StatusCode::BAD_REQUEST,
                Json(serde_json::to_value(ErrorResponse::new(e.to_string())).unwrap()),
            )
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
        Ok(key_share_bytes) => (
            StatusCode::OK,
            Json(serde_json::to_value(DkgFinalizeResponse {
                success: true,
                key_bytes_hex: hex::encode(&key_share_bytes),
            }).unwrap()),
        ),
        Err(e) => {
            error!(error = %e, "DKG finalize failed");
            (
                StatusCode::BAD_REQUEST,
                Json(serde_json::to_value(ErrorResponse::new(e.to_string())).unwrap()),
            )
        }
    }
}

pub async fn register_vault(
    State(state): State<Arc<AppState>>,
    Json(req): Json<RegisterVaultRequest>,
) -> impl IntoResponse {
    let vault_pub_key = req.vault_pub_key.trim().to_string();
    if !valid_vault_pub_key(&vault_pub_key) {
        return (
            StatusCode::BAD_REQUEST,
            Json(error_json("invalid vault_pub_key format")),
        );
    }

    let group_key = match normalized_group_key_hex(&req.group_key) {
        Ok(group_key) => group_key,
        Err((status, response)) => return (status, Json(response)),
    };
    match vault_pub_key_matches_group_key(&vault_pub_key, &group_key) {
        Ok(true) => {}
        Ok(false) => {
            return (
                StatusCode::BAD_REQUEST,
                Json(error_json("vault_pub_key does not match group_key")),
            );
        }
        Err((status, response)) => return (status, Json(response)),
    }

    let expected_address = match xmr::derive_vault_address(&group_key, state.chain_network) {
        Ok(address) => address,
        Err(e) => {
            return (
                StatusCode::BAD_REQUEST,
                Json(error_json(format!(
                    "failed to derive vault address from group_key: {e}"
                ))),
            );
        }
    };

    let provided_address = req.address.trim();
    if provided_address.is_empty() {
        return (
            StatusCode::BAD_REQUEST,
            Json(error_json("address is required")),
        );
    }
    if provided_address != expected_address {
        return (
            StatusCode::BAD_REQUEST,
            Json(error_json(
                "address does not match the derived vault address for group_key",
            )),
        );
    }

    let source = req
        .source
        .as_deref()
        .map(str::trim)
        .filter(|source| !source.is_empty())
        .unwrap_or("bifrost");

    if let Err((status, response)) = ensure_bootstrap_frost_key_material(
        &state,
        &vault_pub_key,
        &group_key,
        req.spend_key_hex.as_deref(),
    ) {
        return (status, Json(response));
    }

    match ensure_registered_vault_state(
        &state,
        &vault_pub_key,
        &group_key,
        &expected_address,
        source,
        req.birthday,
    ) {
        Ok(registered) => (
            StatusCode::OK,
            Json(serde_json::to_value(RegisterVaultResponse { registered }).unwrap()),
        ),
        Err(e) => {
            error!(
                error = %e,
                vault = %vault_pub_key,
                "runtime vault registration failed"
            );
            (StatusCode::INTERNAL_SERVER_ERROR, Json(error_json(e)))
        }
    }
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
        return (
            StatusCode::BAD_REQUEST,
            Json(error_json("invalid vault_pub_key format")),
        );
    }
    let mut canonical_req = req.clone();
    canonical_req.msg_id = msg_id.clone();
    canonical_req.vault_pub_key = vault_pub_key_canonical.clone();
    let payload_hashes = match idempotency_payload_hashes(&canonical_req) {
        Ok(hashes) => hashes,
        Err(e) => {
            return (StatusCode::INTERNAL_SERVER_ERROR, Json(error_json(e)));
        }
    };
    match check_idempotency(&state, "sign_prepare", &msg_id, &payload_hashes) {
        Ok(IdempotencyCheck::Replay(status, response)) => return (status, Json(response)),
        Ok(IdempotencyCheck::Conflict) => {
            return (
                StatusCode::CONFLICT,
                Json(error_json(
                    "idempotency conflict: msg_id reused with different payload",
                )),
            );
        }
        Ok(IdempotencyCheck::InProgress) => {
            return (
                StatusCode::CONFLICT,
                Json(error_json(
                    "idempotency request already in progress for msg_id",
                )),
            );
        }
        Ok(IdempotencyCheck::Fresh) => {}
        Err(e) => {
            return (StatusCode::INTERNAL_SERVER_ERROR, Json(error_json(e)));
        }
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
            &req.memo.unwrap_or_default(),
            req.fee_rate,
            &req.decoy_context,
            &msg_id,
        )
        .await
    {
        Ok((session_id, preprocess)) => (
            StatusCode::OK,
            serde_json::to_value(SignPrepareResponse {
                session_id,
                preprocess: hex::encode(preprocess),
            })
            .unwrap(),
        ),
        Err(e) => {
            error!(error = %e, msg_id, "sign prepare failed");
            let msg = e.to_string();
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
        Err(e) => {
            return (StatusCode::INTERNAL_SERVER_ERROR, Json(error_json(e)));
        }
    };
    match check_idempotency(&state, "sign_partial", &msg_id, &payload_hashes) {
        Ok(IdempotencyCheck::Replay(status, response)) => return (status, Json(response)),
        Ok(IdempotencyCheck::Conflict) => {
            return (
                StatusCode::CONFLICT,
                Json(error_json(
                    "idempotency conflict: msg_id reused with different payload",
                )),
            );
        }
        Ok(IdempotencyCheck::InProgress) => {
            return (
                StatusCode::CONFLICT,
                Json(error_json(
                    "idempotency request already in progress for msg_id",
                )),
            );
        }
        Ok(IdempotencyCheck::Fresh) => {}
        Err(e) => {
            return (StatusCode::INTERNAL_SERVER_ERROR, Json(error_json(e)));
        }
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
            serde_json::to_value(SignPartialResponse {
                signature_share: hex::encode(share),
            })
            .unwrap(),
        ),
        Err(e) => {
            error!(error = %e, "sign partial failed");
            (StatusCode::BAD_REQUEST, error_json(e.to_string()))
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
        Err(e) => {
            return (StatusCode::INTERNAL_SERVER_ERROR, Json(error_json(e)));
        }
    };
    match check_idempotency(&state, "sign_combine", &msg_id, &payload_hashes) {
        Ok(IdempotencyCheck::Replay(status, response)) => return (status, Json(response)),
        Ok(IdempotencyCheck::Conflict) => {
            return (
                StatusCode::CONFLICT,
                Json(error_json(
                    "idempotency conflict: msg_id reused with different payload",
                )),
            );
        }
        Ok(IdempotencyCheck::InProgress) => {
            return (
                StatusCode::CONFLICT,
                Json(error_json(
                    "idempotency request already in progress for msg_id",
                )),
            );
        }
        Ok(IdempotencyCheck::Fresh) => {}
        Err(e) => {
            return (StatusCode::INTERNAL_SERVER_ERROR, Json(error_json(e)));
        }
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
        Ok((tx_hash, tx_hex, fee_atomic, output_refs)) => (
            StatusCode::OK,
            serde_json::to_value(SignCombineResponse {
                tx_hash,
                tx_hex,
                fee_atomic,
                spent_output_refs: output_refs
                    .into_iter()
                    .map(|r| crate::api::types::SpentOutputRef {
                        tx_hash: r.tx_hash,
                        output_index: r.output_index,
                    })
                    .collect(),
            })
            .unwrap(),
        ),
        Err(e) => {
            error!(error = %e, "sign combine failed");
            (StatusCode::BAD_REQUEST, error_json(e.to_string()))
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
        return (
            StatusCode::BAD_REQUEST,
            Json(error_json("vault_pub_key is required")),
        );
    }
    if !valid_vault_pub_key(&vault_pub_key_canonical) {
        return (
            StatusCode::BAD_REQUEST,
            Json(error_json("invalid vault_pub_key format")),
        );
    }

    let mut canonical_req = req.clone();
    canonical_req.msg_id = msg_id.clone();
    canonical_req.vault_pub_key = vault_pub_key_canonical.clone();
    let payload_hashes = match idempotency_payload_hashes(&canonical_req) {
        Ok(hashes) => hashes,
        Err(e) => {
            return (StatusCode::INTERNAL_SERVER_ERROR, Json(error_json(e)));
        }
    };
    match check_idempotency(&state, "broadcast", &msg_id, &payload_hashes) {
        Ok(IdempotencyCheck::Replay(status, response)) => return (status, Json(response)),
        Ok(IdempotencyCheck::Conflict) => {
            return (
                StatusCode::CONFLICT,
                Json(error_json(
                    "idempotency conflict: msg_id reused with different payload",
                )),
            );
        }
        Ok(IdempotencyCheck::InProgress) => {
            return (
                StatusCode::CONFLICT,
                Json(error_json(
                    "idempotency request already in progress for msg_id",
                )),
            );
        }
        Ok(IdempotencyCheck::Fresh) => {}
        Err(e) => {
            return (StatusCode::INTERNAL_SERVER_ERROR, Json(error_json(e)));
        }
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
                    if let Err(e) = state.store.update_outgoing_transfer_status(
                        &vault_pub_key_canonical,
                        &tx_hash,
                        OUTGOING_STATUS_MEMPOOL,
                        None,
                        Some(chrono::Utc::now().timestamp()),
                        Some(chrono::Utc::now().timestamp()),
                    ) {
                        error!(error = %e, "failed to update outgoing transfer status after broadcast");
                    }

                    (
                        StatusCode::OK,
                        serde_json::to_value(BroadcastResponse { tx_hash }).unwrap(),
                    )
                } else {
                    error!("daemon broadcast returned success without tx hash");
                    (
                        StatusCode::BAD_GATEWAY,
                        error_json("daemon broadcast returned success without tx hash"),
                    )
                }
            } else {
                let msg = resp.reason.unwrap_or_else(|| resp.status);
                (StatusCode::BAD_REQUEST, error_json(msg))
            }
        }
        Err(e) => {
            error!(error = %e, "broadcast failed");
            (StatusCode::INTERNAL_SERVER_ERROR, error_json(e.to_string()))
        }
    };
    persist_idempotency(
        &state,
        "broadcast",
        &msg_id,
        &payload_hashes.canonical,
        status,
        &response_json,
    );
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

    (
        StatusCode::OK,
        Json(
            serde_json::to_value(BalanceResponse {
                balance,
                unlocked_balance,
                spendable_balance,
            })
            .unwrap(),
        ),
    )
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

#[derive(Debug, Default, serde::Deserialize)]
pub struct OutputsQuery {
    #[serde(default)]
    pub view: OutputsView,
}

const MEMPOOL_TRANSFER_BATCH_SIZE: usize = 100;
const OUTGOING_STATUS_MEMPOOL: &str = "observed_mempool";
const OUTGOING_STATUS_MINED: &str = "observed_mined";

fn normalize_tx_hash(hash: &str) -> String {
    hash.trim().to_ascii_lowercase()
}

fn is_observed_mined_record(tx: &crate::state::store::OutgoingTransferRecord) -> bool {
    tx.status.eq_ignore_ascii_case(OUTGOING_STATUS_MINED)
}

fn outgoing_effective_height(tx: &crate::state::store::OutgoingTransferRecord) -> u64 {
    tx.observed_height.unwrap_or(tx.planned_height)
}

fn transfer_entry_from_outgoing_record(
    tx: &crate::state::store::OutgoingTransferRecord,
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
        amount: tx.amount,
        fee: tx.fee,
        height,
        confirmations,
        note: tx.memo.clone(),
        locked: mempool || confirmations < 10,
    }
}

fn build_mempool_outgoing_entries(
    records: &[crate::state::store::OutgoingTransferRecord],
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
    for record in records {
        let normalized = normalize_tx_hash(&record.tx_hash);
        if normalized.is_empty() || !block_tx_hashes.contains(&normalized) {
            continue;
        }
        if is_observed_mined_record(&record) && record.observed_height == Some(height) {
            continue;
        }
        store.update_outgoing_transfer_status(
            vault_pub_key,
            &record.tx_hash,
            OUTGOING_STATUS_MINED,
            Some(height),
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

fn hash_matches(actual: &str, expected: &str) -> bool {
    actual.trim().eq_ignore_ascii_case(expected.trim())
}

fn is_valid_chain_tx_hash(hash: &str) -> bool {
    let normalized = hash.trim();
    normalized.len() == 64 && hex::decode(normalized).is_ok()
}

fn scan_lag_exceeds(tip_height: u64, scanned_height: u64, max_lag_blocks: u64) -> bool {
    tip_height.saturating_sub(scanned_height) > max_lag_blocks
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

fn output_entry_from_tracked(o: TrackedOutput) -> OutputEntry {
    let spent = o.is_spent();
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
        spent,
        state,
        subaddress_major: o.subaddress_major,
        subaddress_minor: o.subaddress_minor,
    }
}

pub async fn scanner_sync(
    State(state): State<Arc<AppState>>,
    Json(req): Json<ScannerSyncRequest>,
) -> impl IntoResponse {
    if req.block_hash.trim().is_empty() || req.parent_hash.trim().is_empty() {
        return (
            StatusCode::BAD_REQUEST,
            Json(error_json("block_hash and parent_hash are required")),
        );
    }

    let canonical_header = match state.daemon.get_block_header_by_height(req.height).await {
        Ok(resp) => resp.block_header,
        Err(e) => {
            error!(
                error = %e,
                height = req.height,
                "failed to fetch canonical block header for scanner sync"
            );
            return (
                StatusCode::SERVICE_UNAVAILABLE,
                Json(error_json(format!(
                    "scanner sync unavailable for height {}: {}",
                    req.height, e
                ))),
            );
        }
    };

    if !hash_matches(&canonical_header.hash, &req.block_hash)
        || !hash_matches(&canonical_header.prev_hash, &req.parent_hash)
    {
        return (
            StatusCode::CONFLICT,
            Json(error_json(format!(
                "scanner sync hash mismatch at height {}: expected {}<-{}, got {}<-{}",
                req.height,
                req.block_hash.trim(),
                req.parent_hash.trim(),
                canonical_header.hash.trim(),
                canonical_header.prev_hash.trim()
            ))),
        );
    }

    let effective_height = canonical_header.height;
    state.scanner.lock().set_current_height(effective_height);

    let vaults = match state.store.list_vaults() {
        Ok(v) => v,
        Err(e) => {
            error!(error = %e, "failed to list vaults for scanner sync");
            return (
                StatusCode::INTERNAL_SERVER_ERROR,
                Json(error_json(e.to_string())),
            );
        }
    };
    let rpc = match state.daemon.oxide_rpc().await {
        Ok(rpc) => rpc,
        Err(e) => {
            error!(error = %e, "failed to initialize monero-oxide rpc client for scanner sync");
            return (
                StatusCode::INTERNAL_SERVER_ERROR,
                Json(error_json(e.to_string())),
            );
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
        let mut outputs = match state.store.load_outputs(&vault.vault_pub_key) {
            Ok(outputs) => outputs,
            Err(e) => {
                error!(
                    error = %e,
                    vault = %vault.vault_pub_key,
                    "failed to load outputs for scanner sync"
                );
                return (
                    StatusCode::INTERNAL_SERVER_ERROR,
                    Json(error_json(e.to_string())),
                );
            }
        };
        let mut scan_height = match state.store.load_scan_height(&vault.vault_pub_key) {
            Ok(Some(height)) => height,
            Ok(None) => 0,
            Err(e) => {
                error!(
                    error = %e,
                    vault = %vault.vault_pub_key,
                    "failed to load scan height for scanner sync"
                );
                return (
                    StatusCode::INTERNAL_SERVER_ERROR,
                    Json(error_json(e.to_string())),
                );
            }
        };

        if let Err(e) = state.scanner.lock().register_vault(
            &vault.vault_pub_key,
            &vault.group_key,
            state.chain_network,
            outputs.clone(),
            Some(scan_height),
        ) {
            error!(
                error = %e,
                vault = %vault.vault_pub_key,
                "failed to initialize scanner state for scanner sync"
            );
            return (
                StatusCode::INTERNAL_SERVER_ERROR,
                Json(error_json(e.to_string())),
            );
        }

        let stored_meta = match state
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
                return (
                    StatusCode::INTERNAL_SERVER_ERROR,
                    Json(error_json(e.to_string())),
                );
            }
        };

        let meta_matches = stored_meta.as_ref().is_some_and(|meta| {
            hash_matches(&meta.block_hash, &req.block_hash)
                && hash_matches(&meta.parent_hash, &req.parent_hash)
        });
        let needs_rollback = stored_meta
            .as_ref()
            .is_some_and(|meta| !meta_matches && meta.height == req.height)
            || (!meta_matches && stored_meta.is_none() && scan_height >= req.height);

        if needs_rollback {
            if let Err(e) = state
                .store
                .rollback_scan_state_from_height(&vault.vault_pub_key, req.height)
            {
                error!(
                    error = %e,
                    vault = %vault.vault_pub_key,
                    height = req.height,
                    "failed to rollback scanner state"
                );
                return (
                    StatusCode::INTERNAL_SERVER_ERROR,
                    Json(error_json(e.to_string())),
                );
            }
            outputs = match state.store.load_outputs(&vault.vault_pub_key) {
                Ok(outputs) => outputs,
                Err(e) => {
                    error!(
                        error = %e,
                        vault = %vault.vault_pub_key,
                        "failed to reload outputs after scanner rollback"
                    );
                    return (
                        StatusCode::INTERNAL_SERVER_ERROR,
                        Json(error_json(e.to_string())),
                    );
                }
            };
            scan_height = match state.store.load_scan_height(&vault.vault_pub_key) {
                Ok(Some(height)) => height,
                Ok(None) => 0,
                Err(e) => {
                    error!(
                        error = %e,
                        vault = %vault.vault_pub_key,
                        "failed to reload scan height after scanner rollback"
                    );
                    return (
                        StatusCode::INTERNAL_SERVER_ERROR,
                        Json(error_json(e.to_string())),
                    );
                }
            };
            if let Err(e) = state.scanner.lock().register_vault(
                &vault.vault_pub_key,
                &vault.group_key,
                state.chain_network,
                outputs.clone(),
                Some(scan_height),
            ) {
                error!(
                    error = %e,
                    vault = %vault.vault_pub_key,
                    "failed to reinitialize scanner after rollback"
                );
                return (
                    StatusCode::INTERNAL_SERVER_ERROR,
                    Json(error_json(e.to_string())),
                );
            }
        }

        if meta_matches {
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
                    if let Err(e) = state
                        .store
                        .save_scan_block_meta(&vault.vault_pub_key, &meta)
                    {
                        error!(
                            error = %e,
                            vault = %vault.vault_pub_key,
                            height = req.height,
                            "failed to heal scan block metadata during scanner sync"
                        );
                        return (
                            StatusCode::INTERNAL_SERVER_ERROR,
                            Json(error_json(e.to_string())),
                        );
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
                    return (
                        StatusCode::SERVICE_UNAVAILABLE,
                        Json(error_json(format!(
                            "scanner sync unavailable for height {}: {}",
                            req.height, e
                        ))),
                    );
                }
            };
            let block_tx_hashes: HashSet<String> = block
                .block
                .transactions
                .iter()
                .map(|tx_hash| normalize_tx_hash(&hex::encode(tx_hash)))
                .filter(|hash| !hash.is_empty())
                .collect();
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
                return (
                    StatusCode::INTERNAL_SERVER_ERROR,
                    Json(error_json(e.to_string())),
                );
            }

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
                    return (
                        StatusCode::INTERNAL_SERVER_ERROR,
                        Json(error_json(e.to_string())),
                    );
                }
            };
            let outputs_after_scan = state.scanner.lock().get_all_outputs(&vault.vault_pub_key);
            if let Err(e) = state
                .store
                .merge_scanned_outputs(&vault.vault_pub_key, &outputs_after_scan)
            {
                error!(
                    error = %e,
                    vault = %vault.vault_pub_key,
                    "failed to persist outputs after scanner sync"
                );
                return (
                    StatusCode::INTERNAL_SERVER_ERROR,
                    Json(error_json(e.to_string())),
                );
            }
            let added_refs: Vec<OutputRef> = added_outputs
                .iter()
                .map(|output| OutputRef {
                    tx_hash: output.tx_hash.clone(),
                    output_index: output.output_index,
                })
                .collect();
            if let Err(e) = state.store.save_scan_block_meta(
                &vault.vault_pub_key,
                &ScanBlockMeta {
                    height: req.height,
                    block_hash: req.block_hash.trim().to_string(),
                    parent_hash: req.parent_hash.trim().to_string(),
                    added_outputs: added_refs.clone(),
                },
            ) {
                error!(
                    error = %e,
                    vault = %vault.vault_pub_key,
                    height = req.height,
                    "failed to persist scan block metadata after scanner sync"
                );
                return (
                    StatusCode::INTERNAL_SERVER_ERROR,
                    Json(error_json(e.to_string())),
                );
            }
            if let Err(e) = state
                .store
                .save_scan_height(&vault.vault_pub_key, req.height)
            {
                error!(
                    error = %e,
                    vault = %vault.vault_pub_key,
                    height = req.height,
                    "failed to persist scan height after scanner sync"
                );
                return (
                    StatusCode::INTERNAL_SERVER_ERROR,
                    Json(error_json(e.to_string())),
                );
            }
            let min_height = req.height.saturating_sub(reorg_window.saturating_sub(1));
            if let Err(e) = state
                .store
                .prune_scan_block_meta_before(&vault.vault_pub_key, min_height)
            {
                warn!(
                    error = %e,
                    vault = %vault.vault_pub_key,
                    min_height,
                    "failed to prune scan block metadata after scanner sync"
                );
            }
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
        }

        match state.store.load_outgoing_transfers(&vault.vault_pub_key) {
            Ok(outbound) => {
                for tx in outbound.into_iter().filter(|tx| {
                    is_observed_mined_record(tx) && outgoing_effective_height(tx) == req.height
                }) {
                    outgoing.push(transfer_entry_from_outgoing_record(
                        &tx,
                        effective_height,
                        false,
                    ));
                }
            }
            Err(e) => {
                error!(
                    error = %e,
                    vault = %vault.vault_pub_key,
                    "failed to load outgoing transfers for scanner sync"
                );
                return (
                    StatusCode::INTERNAL_SERVER_ERROR,
                    Json(error_json(e.to_string())),
                );
            }
        }
    }

    (
        StatusCode::OK,
        Json(serde_json::to_value(TransfersResponse { incoming, outgoing }).unwrap()),
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
            return (
                StatusCode::INTERNAL_SERVER_ERROR,
                Json(serde_json::to_value(ErrorResponse::new(e.to_string())).unwrap()),
            );
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

    (
        StatusCode::OK,
        Json(serde_json::to_value(TransfersResponse { incoming, outgoing }).unwrap()),
    )
}

async fn get_mempool_transfers(state: Arc<AppState>) -> (StatusCode, Json<serde_json::Value>) {
    let vaults = match state.store.list_vaults() {
        Ok(v) => v,
        Err(e) => {
            error!(error = %e, "failed to list vaults for mempool transfers");
            return (
                StatusCode::INTERNAL_SERVER_ERROR,
                Json(serde_json::to_value(ErrorResponse::new(e.to_string())).unwrap()),
            );
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
                return (
                    StatusCode::INTERNAL_SERVER_ERROR,
                    Json(serde_json::to_value(ErrorResponse::new(e.to_string())).unwrap()),
                );
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
                return (
                    StatusCode::INTERNAL_SERVER_ERROR,
                    Json(serde_json::to_value(ErrorResponse::new(e.to_string())).unwrap()),
                );
            }
        };
        for tx in txs {
            if tx.in_pool.unwrap_or(false) {
                in_pool_hashes.insert(normalize_tx_hash(&tx.tx_hash));
            }
        }
    }

    let outgoing = build_mempool_outgoing_entries(&records, &in_pool_hashes);
    (
        StatusCode::OK,
        Json(
            serde_json::to_value(TransfersResponse {
                incoming: Vec::new(),
                outgoing,
            })
            .unwrap(),
        ),
    )
}

pub async fn get_addresses(
    State(state): State<Arc<AppState>>,
    Path(vault_pub_key): Path<String>,
) -> impl IntoResponse {
    let addresses = {
        let scanner = state.scanner.lock();
        scanner.get_addresses(&vault_pub_key)
    };

    (
        StatusCode::OK,
        Json(serde_json::to_value(AddressesResponse { addresses }).unwrap()),
    )
}

pub async fn get_outputs(
    State(state): State<Arc<AppState>>,
    Path(vault_pub_key): Path<String>,
    Query(query): Query<OutputsQuery>,
) -> impl IntoResponse {
    let outputs = {
        let scanner = state.scanner.lock();
        outputs_for_view(&scanner, &vault_pub_key, query.view)
    };

    let entries: Vec<OutputEntry> = outputs.into_iter().map(output_entry_from_tracked).collect();

    (
        StatusCode::OK,
        Json(serde_json::to_value(OutputsResponse { outputs: entries }).unwrap()),
    )
}

pub async fn mark_outputs_spent(
    State(state): State<Arc<AppState>>,
    Json(req): Json<crate::api::types::MarkSpentRequest>,
) -> impl IntoResponse {
    let vault_pub_key = req.vault_pub_key.trim().to_string();
    if vault_pub_key.is_empty() {
        return (
            StatusCode::BAD_REQUEST,
            Json(error_json("vault_pub_key is required")),
        );
    }
    let tx_hash = req.tx_hash.trim().to_ascii_lowercase();
    if tx_hash.is_empty() {
        return (
            StatusCode::BAD_REQUEST,
            Json(error_json("tx_hash is required")),
        );
    }
    if req.spent_refs.is_empty() {
        return (
            StatusCode::BAD_REQUEST,
            Json(error_json("spent_refs must not be empty")),
        );
    }

    if !req.skip_verification {
        match state.daemon.get_transactions(&[tx_hash.clone()]).await {
            Ok(result) => {
                let found = result
                    .txs
                    .as_ref()
                    .map(|txs| {
                        txs.iter()
                            .any(|tx| tx.in_pool.unwrap_or(false) || tx.block_height.is_some())
                    })
                    .unwrap_or(false);
                if !found {
                    return (
                        StatusCode::NOT_FOUND,
                        Json(error_json(format!(
                            "transaction {tx_hash} not found in mempool or on-chain"
                        ))),
                    );
                }
            }
            Err(e) => {
                error!(error = %e, tx_hash, "failed to verify transaction for mark-spent");
                return (
                    StatusCode::SERVICE_UNAVAILABLE,
                    Json(error_json(format!("failed to verify tx: {e}"))),
                );
            }
        }
    }

    let refs: Vec<(String, u32)> = req
        .spent_refs
        .iter()
        .map(|r| (r.tx_hash.clone(), r.output_index))
        .collect();

    {
        let mut scanner = state.scanner.lock();
        scanner.mark_spent_by_refs(&vault_pub_key, &refs);
    }

    let outputs_after = {
        let scanner = state.scanner.lock();
        scanner.get_all_outputs(&vault_pub_key)
    };
    let marked = refs.len();
    let mut not_found = 0usize;
    for (ref_tx, ref_idx) in &refs {
        let found = outputs_after
            .iter()
            .any(|o| o.tx_hash == *ref_tx && o.output_index == *ref_idx && o.is_spent());
        if !found {
            not_found += 1;
        }
    }

    if let Err(e) = state
        .store
        .merge_scanned_outputs(&vault_pub_key, &outputs_after)
    {
        error!(error = %e, vault = vault_pub_key, "failed to persist outputs after mark-spent");
        return (
            StatusCode::INTERNAL_SERVER_ERROR,
            Json(error_json(format!("failed to persist: {e}"))),
        );
    }

    // If the caller provided outbound metadata, create an OutgoingTransferRecord
    // so that get_transfers returns this outbound for non-signing nodes.
    if let (Some(to_address), Some(amount), Some(fee)) =
        (req.to_address.as_deref(), req.amount, req.fee)
    {
        let from_address = req
            .from_address
            .clone()
            .unwrap_or_default();
        let memo = req.memo.clone().unwrap_or_default();
        let current_height = state.scanner.lock().current_height();

        let record = crate::state::store::OutgoingTransferRecord {
            tx_hash: tx_hash.clone(),
            tx_hex: String::new(),
            msg_id: format!("attestation:{tx_hash}"),
            vault_pub_key: vault_pub_key.clone(),
            from_address,
            to_address: to_address.to_string(),
            amount,
            fee,
            memo,
            planned_height: current_height,
            observed_height: None,
            status: "attested_pending_observation".to_string(),
            broadcasted_at: None,
            observed_at: None,
            created_at: chrono::Utc::now().timestamp(),
        };
        if let Err(e) = state.store.save_outgoing_transfer(&record) {
            error!(
                error = %e,
                vault = vault_pub_key,
                tx_hash,
                "failed to persist outgoing transfer record from attestation"
            );
        } else {
            info!(
                vault = vault_pub_key,
                tx_hash,
                "created outgoing transfer record from attestation"
            );
        }
    }

    (
        StatusCode::OK,
        Json(
            serde_json::to_value(crate::api::types::MarkSpentResponse {
                marked: marked - not_found,
                not_found,
            })
            .unwrap(),
        ),
    )
}

pub async fn list_vault_keys(
    State(state): State<Arc<AppState>>,
) -> impl IntoResponse {
    let vaults = match state.store.list_vaults() {
        Ok(v) => v,
        Err(e) => {
            error!(error = %e, "failed to list vaults");
            return (
                StatusCode::INTERNAL_SERVER_ERROR,
                Json(error_json(format!("failed to list vaults: {e}"))),
            );
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

    (
        StatusCode::OK,
        Json(
            serde_json::to_value(crate::api::types::ListVaultKeysResponse { vaults: entries })
                .unwrap(),
        ),
    )
}

pub async fn key_export(
    State(state): State<Arc<AppState>>,
    Path(vault_pub_key): Path<String>,
) -> impl IntoResponse {
    let vault_pub_key = vault_pub_key.trim().to_string();
    if vault_pub_key.is_empty() {
        return (
            StatusCode::BAD_REQUEST,
            Json(error_json("vault_pub_key is required")),
        );
    }
    let frost_bytes = state.store.load_frost_keys(&vault_pub_key).ok().flatten()
        .or_else(|| {
            // Try looking up by group_key if the vault_pub_key didn't match
            state.store.list_vaults().ok()
                .and_then(|vaults| {
                    vaults.iter()
                        .find(|v| v.vault_pub_key == vault_pub_key)
                        .and_then(|v| state.store.load_frost_keys(&v.group_key).ok().flatten())
                })
        });
    match frost_bytes {
        Some(raw_bytes) => (
            StatusCode::OK,
            Json(
                serde_json::to_value(crate::api::types::KeyExportResponse {
                    key_bytes_hex: hex::encode(raw_bytes),
                })
                .unwrap(),
            ),
        ),
        None => (
            StatusCode::NOT_FOUND,
            Json(error_json(format!(
                "no FROST keys found for vault {vault_pub_key}"
            ))),
        ),
    }
}

pub async fn key_restore(
    State(state): State<Arc<AppState>>,
    Json(req): Json<crate::api::types::KeyRestoreRequest>,
) -> impl IntoResponse {
    let vault_pub_key = req.vault_pub_key.trim().to_string();
    if vault_pub_key.is_empty() {
        return (
            StatusCode::BAD_REQUEST,
            Json(error_json("vault_pub_key is required")),
        );
    }
    let key_bytes = match hex::decode(req.key_bytes_hex.trim()) {
        Ok(b) if !b.is_empty() => b,
        Ok(_) => {
            return (
                StatusCode::BAD_REQUEST,
                Json(error_json("key_bytes_hex must not be empty")),
            );
        }
        Err(e) => {
            return (
                StatusCode::BAD_REQUEST,
                Json(error_json(format!("invalid key_bytes_hex: {e}"))),
            );
        }
    };

    let save_key = state.store.list_vaults().ok()
        .and_then(|vaults| {
            vaults.iter()
                .find(|v| v.vault_pub_key == vault_pub_key)
                .map(|v| v.group_key.clone())
        })
        .unwrap_or_else(|| vault_pub_key.clone());

    if let Ok(Some(_)) = state.store.load_frost_keys(&save_key) {
        return (
            StatusCode::OK,
            Json(
                serde_json::to_value(crate::api::types::KeyRestoreResponse { restored: false })
                    .unwrap(),
            ),
        );
    }

    if let Err(e) = state.store.save_frost_keys(&save_key, &key_bytes) {
        error!(error = %e, vault = vault_pub_key, save_key, "failed to restore FROST keys");
        return (
            StatusCode::INTERNAL_SERVER_ERROR,
            Json(error_json(format!("failed to save restored keys: {e}"))),
        );
    }

    info!(vault = vault_pub_key, save_key, "restored FROST keys from backup");
    (
        StatusCode::OK,
        Json(
            serde_json::to_value(crate::api::types::KeyRestoreResponse { restored: true })
                .unwrap(),
        ),
    )
}

pub async fn get_fee_rate(State(state): State<Arc<AppState>>) -> impl IntoResponse {
    let rpc = match state.daemon.oxide_rpc().await {
        Ok(rpc) => rpc,
        Err(e) => {
            error!(error = %e, "failed to initialize monero-oxide rpc client");
            return (
                StatusCode::INTERNAL_SERVER_ERROR,
                Json(serde_json::to_value(ErrorResponse::new(e.to_string())).unwrap()),
            );
        }
    };

    match fee::estimate_fee_rate(&rpc).await {
        Ok(rate) => match fee::per_weight(&rate) {
            Ok(per_weight) => (
                StatusCode::OK,
                Json(
                    serde_json::to_value(FeeRateResponse {
                        fee_rate_per_weight: per_weight,
                    })
                    .unwrap(),
                ),
            ),
            Err(e) => (
                StatusCode::INTERNAL_SERVER_ERROR,
                Json(serde_json::to_value(ErrorResponse::new(e.to_string())).unwrap()),
            ),
        },
        Err(e) => {
            error!(error = %e, "fee rate estimation failed");
            (
                StatusCode::INTERNAL_SERVER_ERROR,
                Json(serde_json::to_value(ErrorResponse::new(e.to_string())).unwrap()),
            )
        }
    }
}

pub async fn get_chain_height(State(state): State<Arc<AppState>>) -> impl IntoResponse {
    match state.daemon.get_height().await {
        Ok(height) => (
            StatusCode::OK,
            Json(serde_json::to_value(ChainHeightResponse { height }).unwrap()),
        ),
        Err(e) => {
            error!(error = %e, "failed to query daemon chain height");
            (
                StatusCode::SERVICE_UNAVAILABLE,
                Json(error_json(format!("daemon chain height unavailable: {e}"))),
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
            return (
                StatusCode::SERVICE_UNAVAILABLE,
                Json(error_json(format!("daemon chain tip unavailable: {e}"))),
            );
        }
    };
    if height > tip_height {
        return (
            StatusCode::CONFLICT,
            Json(error_json(format!(
                "requested height {height} is above chain tip {tip_height}"
            ))),
        );
    }

    match state.daemon.get_block_by_height(height).await {
        Ok(block) => (
            StatusCode::OK,
            Json(
                serde_json::to_value(ChainBlockResponse {
                    height: block.block_header.height,
                    reward: block.block_header.reward,
                    hash: block.block_header.hash,
                    previous_hash: block.block_header.prev_hash,
                    tx_hashes: block.tx_hashes.unwrap_or_default(),
                })
                .unwrap(),
            ),
        ),
        Err(e) => {
            error!(error = %e, height, "failed to query daemon chain block");
            (
                StatusCode::SERVICE_UNAVAILABLE,
                Json(error_json(format!(
                    "daemon chain block unavailable at height {height}: {e}"
                ))),
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
        return (
            StatusCode::BAD_REQUEST,
            Json(error_json("tx_hash must be a 64-character hex string")),
        );
    }

    let query = vec![normalized.clone()];
    let result = match state.daemon.get_transactions(&query).await {
        Ok(result) => result,
        Err(e) => {
            error!(error = %e, tx_hash = %normalized, "failed to query daemon transaction");
            return (
                StatusCode::SERVICE_UNAVAILABLE,
                Json(error_json(format!(
                    "daemon transaction query unavailable for {normalized}: {e}"
                ))),
            );
        }
    };

    if result
        .txs
        .unwrap_or_default()
        .iter()
        .any(|tx| tx.tx_hash.trim().eq_ignore_ascii_case(&normalized))
    {
        return (
            StatusCode::OK,
            Json(serde_json::to_value(ChainTxExistsResponse { exists: true }).unwrap()),
        );
    }
    if result
        .missed_tx
        .unwrap_or_default()
        .iter()
        .any(|hash| hash.trim().eq_ignore_ascii_case(&normalized))
    {
        return (
            StatusCode::OK,
            Json(serde_json::to_value(ChainTxExistsResponse { exists: false }).unwrap()),
        );
    }

    error!(
        tx_hash = %normalized,
        "ambiguous daemon get_transactions response: no tx entry and no missed marker"
    );
    (
        StatusCode::INTERNAL_SERVER_ERROR,
        Json(error_json(format!(
            "ambiguous daemon get_transactions response for {normalized}"
        ))),
    )
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
        Err(e) => return (StatusCode::BAD_REQUEST, Json(error_json(e))),
    };
    if let Err(e) = sync_sim_account(&state, &account).await {
        error!(error = %e, account = %account.account_id, "failed to sync sim balance");
        return (StatusCode::SERVICE_UNAVAILABLE, Json(error_json(e)));
    }

    let (balance_atomic, unlocked_balance_atomic) = {
        let scanner = state.scanner.lock();
        scanner.get_balance(&account.account_id)
    };

    (
        StatusCode::OK,
        Json(
            serde_json::to_value(SimBalanceResponse {
                address: account.address,
                balance_atomic,
                unlocked_balance_atomic,
            })
            .unwrap(),
        ),
    )
}

pub async fn sim_transfer(
    State(state): State<Arc<AppState>>,
    Json(req): Json<SimTransferRequest>,
) -> impl IntoResponse {
    if let Err(err) = ensure_sim_helper_enabled(&state) {
        return (err.0, Json(err.1));
    }
    if req.to_address.trim().is_empty() {
        return (
            StatusCode::BAD_REQUEST,
            Json(error_json("to_address is required")),
        );
    }
    if req.amount_atomic == 0 {
        return (
            StatusCode::BAD_REQUEST,
            Json(error_json("amount_atomic must be > 0")),
        );
    }

    let account = match sim_account_from_spend_key(&req.spend_key_hex, state.chain_network) {
        Ok(account) => account,
        Err(e) => return (StatusCode::BAD_REQUEST, Json(error_json(e))),
    };
    if let Err(e) = sync_sim_account(&state, &account).await {
        error!(error = %e, account = %account.account_id, "failed to sync sim transfer account");
        return (StatusCode::SERVICE_UNAVAILABLE, Json(error_json(e)));
    }

    let tip_height = match state.daemon.get_height().await {
        Ok(height) => height,
        Err(e) => {
            return (
                StatusCode::SERVICE_UNAVAILABLE,
                Json(error_json(format!("daemon height unavailable: {e}"))),
            )
        }
    };
    let header = match state.daemon.get_block_header_by_height(tip_height).await {
        Ok(header) => header.block_header,
        Err(e) => {
            return (
                StatusCode::SERVICE_UNAVAILABLE,
                Json(error_json(format!("daemon header unavailable: {e}"))),
            )
        }
    };
    let rpc = match state.daemon.oxide_rpc().await {
        Ok(rpc) => rpc,
        Err(e) => {
            return (
                StatusCode::SERVICE_UNAVAILABLE,
                Json(error_json(e.to_string())),
            )
        }
    };
    let fee_rate = match fee::estimate_fee_rate(&rpc).await {
        Ok(rate) => rate,
        Err(e) => {
            return (
                StatusCode::SERVICE_UNAVAILABLE,
                Json(error_json(e.to_string())),
            )
        }
    };
    let available_outputs = state.scanner.lock().get_outputs(&account.account_id);
    let unsigned = match builder::build_unsigned_transaction(
        &rpc,
        req.to_address.trim(),
        req.amount_atomic,
        req.memo.as_deref().unwrap_or_default(),
        fee_rate,
        &format!("sim-helper:{}", Uuid::new_v4()),
        header.major_version,
        tip_height as usize,
        &available_outputs,
        Some(&account.address),
        &HashSet::new(),
        state.chain_network,
    )
    .await
    {
        Ok(unsigned) => unsigned,
        Err(e) => return (StatusCode::BAD_REQUEST, Json(error_json(e.to_string()))),
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
    if let Err(e) = state.store.reserve_outputs(
        &account.account_id,
        &refs,
        &session_id,
        &msg_id,
        chrono::Utc::now().timestamp() + 300,
    ) {
        return (StatusCode::CONFLICT, Json(error_json(e.to_string())));
    }

    let spend_scalar = match xmr::spend_scalar_from_seed_hex(&req.spend_key_hex) {
        Ok(scalar) => scalar,
        Err(e) => {
            let _ = state.store.release_reservations_for_session(
                &account.account_id,
                &session_id,
                &msg_id,
            );
            return (StatusCode::BAD_REQUEST, Json(error_json(e.to_string())));
        }
    };
    let tx = match unsigned.signable.sign(&mut OsRng, &spend_scalar) {
        Ok(tx) => tx,
        Err(e) => {
            let _ = state.store.release_reservations_for_session(
                &account.account_id,
                &session_id,
                &msg_id,
            );
            return (StatusCode::BAD_REQUEST, Json(error_json(e.to_string())));
        }
    };
    let tx_hash = hex::encode(tx.hash());
    let tx_hex = hex::encode(tx.serialize());
    if let Err(e) =
        state
            .store
            .bind_reservation_tx_hash(&account.account_id, &session_id, &msg_id, &tx_hash)
    {
        let _ =
            state
                .store
                .release_reservations_for_session(&account.account_id, &session_id, &msg_id);
        return (
            StatusCode::INTERNAL_SERVER_ERROR,
            Json(error_json(format!(
                "failed to bind sim transfer reservation: {e}"
            ))),
        );
    }

    match state.daemon.send_raw_transaction(&tx_hex).await {
        Ok(resp) if resp.status == "OK" => {
            if let Err(e) =
                state
                    .store
                    .finalize_reserved_for_msg(&account.account_id, &msg_id, &tx_hash)
            {
                error!(error = %e, tx_hash = %tx_hash, "failed to finalize sim transfer reservation");
            }
            state
                .scanner
                .lock()
                .mark_spent_by_refs(&account.account_id, &ref_pairs);
            (
                StatusCode::OK,
                Json(serde_json::to_value(SimTransferResponse { tx_hash }).unwrap()),
            )
        }
        Ok(resp) => {
            let _ = state.store.release_reservations_for_session(
                &account.account_id,
                &session_id,
                &msg_id,
            );
            (
                StatusCode::BAD_REQUEST,
                Json(error_json(resp.reason.unwrap_or(resp.status))),
            )
        }
        Err(e) => {
            let _ = state.store.release_reservations_for_session(
                &account.account_id,
                &session_id,
                &msg_id,
            );
            (
                StatusCode::SERVICE_UNAVAILABLE,
                Json(error_json(format!("failed to broadcast sim transfer: {e}"))),
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
        return (
            StatusCode::BAD_REQUEST,
            Json(error_json("to_address is required")),
        );
    }
    if req.amount_atomic == 0 {
        return (
            StatusCode::BAD_REQUEST,
            Json(error_json("amount_atomic must be > 0")),
        );
    }

    let Some(wallet_rpc_url) = state.sim_helper_wallet_rpc_url.as_deref() else {
        return (
            StatusCode::INTERNAL_SERVER_ERROR,
            Json(error_json("sim helper wallet-rpc URL not configured")),
        );
    };

    if let Err(e) =
        wallet_rpc_call::<_, serde_json::Value>(wallet_rpc_url, "refresh", WalletRefreshParams {})
            .await
    {
        return (
            StatusCode::SERVICE_UNAVAILABLE,
            Json(error_json(format!("wallet-rpc refresh failed: {e}"))),
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
        Err(e) => return (StatusCode::SERVICE_UNAVAILABLE, Json(error_json(e))),
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
        Some(tx_hash) => (
            StatusCode::OK,
            Json(serde_json::to_value(SimTransferResponse { tx_hash }).unwrap()),
        ),
        None => (
            StatusCode::BAD_GATEWAY,
            Json(error_json(
                "wallet-rpc transfer returned success without tx_hash",
            )),
        ),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::Arc;

    use axum::{body::to_bytes, http::Uri, response::IntoResponse};
    use curve25519_dalek::{constants::ED25519_BASEPOINT_TABLE, Scalar};
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
            msg_id: "msg-id".to_string(),
            vault_pub_key: vault_pub_key.to_string(),
            from_address: from.to_string(),
            to_address: to.to_string(),
            amount: 12345,
            fee: 99,
            memo: "OUT:TEST".to_string(),
            planned_height: 77,
            observed_height: Some(77),
            status: OUTGOING_STATUS_MINED.to_string(),
            broadcasted_at: Some(created_at),
            observed_at: Some(created_at),
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

    fn outputs_test_state(
        outputs: Vec<TrackedOutput>,
    ) -> (Arc<AppState>, tempfile::TempDir, String) {
        let data_dir = tempfile::tempdir().expect("failed to create tempdir");
        let store = Arc::new(
            crate::state::Store::open(data_dir.path())
                .expect("failed to open test store"),
        );
        let mut scanner = Scanner::new();
        let vault_pub_key = "thorpub1vaulttest".to_string();
        scanner
            .register_vault(
                &vault_pub_key,
                &test_group_key_hex(),
                crate::config::ChainNetworkMode::Public,
                outputs,
                None,
            )
            .expect("failed to register test vault");
        scanner.set_current_height(200);

        let daemon = DaemonClient::new("http://127.0.0.1:18081", 1024 * 1024);
        let app_state = Arc::new(AppState {
            dkg: DkgCoordinator::new(store.clone(), crate::config::ChainNetworkMode::Public, 4),
            signer: SignCoordinator::new(
                store.clone(),
                daemon.clone(),
                crate::config::ChainNetworkMode::Public,
                4,
            ),
            scanner: parking_lot::Mutex::new(scanner),
            daemon,
            store,
            chain_network: crate::config::ChainNetworkMode::Public,
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
            crate::state::Store::open(data_dir.path())
                .expect("failed to open test store"),
        );
        let daemon = DaemonClient::new(daemon_url, 1024 * 1024);
        let app_state = Arc::new(AppState {
            dkg: DkgCoordinator::new(store.clone(), crate::config::ChainNetworkMode::Public, 4),
            signer: SignCoordinator::new(
                store.clone(),
                daemon.clone(),
                crate::config::ChainNetworkMode::Public,
                4,
            ),
            scanner: parking_lot::Mutex::new(Scanner::new()),
            daemon,
            store,
            chain_network: crate::config::ChainNetworkMode::Public,
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
        record.status = "signed_pending_observation".to_string();
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
        assert_eq!(updated.status, OUTGOING_STATUS_MINED);
        assert_eq!(updated.observed_height, Some(120));
        assert!(updated.observed_at.is_some());
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
            memo: Some("OUT:TEST".to_string()),
            fee_rate: Some(10),
            decoy_context: "ctx".to_string(),
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
        assert_eq!(updated.status, OUTGOING_STATUS_MEMPOOL);
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
        assert_eq!(daemon_record.status, OUTGOING_STATUS_MEMPOOL);
        assert_eq!(caller_record.status, OUTGOING_STATUS_MINED);
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
        assert_eq!(entries[0]["spent"], serde_json::json!(false));
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

        let mut seen = HashMap::new();
        for entry in entries {
            seen.insert(
                entry["state"]
                    .as_str()
                    .expect("state should be a string")
                    .to_string(),
                entry["spent"].as_bool().expect("spent should be a bool"),
            );
        }
        assert_eq!(seen.get("unspent"), Some(&false));
        assert_eq!(seen.get("reserved"), Some(&false));
        assert_eq!(seen.get("spent"), Some(&true));
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
