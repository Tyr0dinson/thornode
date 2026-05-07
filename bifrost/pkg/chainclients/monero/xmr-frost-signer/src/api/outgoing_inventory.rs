use std::collections::{HashMap, HashSet};
use std::sync::Arc;

use axum::http::StatusCode;
use tracing::{error, info, warn};

use super::routes::{
    error_json, hash_matches, is_valid_chain_tx_hash, normalize_tx_hash, AppState,
};
use super::types::{RegisterTxKeyEntry, ValidateOutboundRequest, ValidateOutboundResponse};
use crate::state::store::{OutgoingTransferRecord, OutputState, TrackedOutput};
use crate::state::{
    OutgoingLifecycle, OutgoingObservation, OutputLifecycle, RecordValidatedParams,
};
use crate::tx::validation;

type ApiError = (StatusCode, serde_json::Value);

fn api_error(status: StatusCode, msg: impl Into<String>) -> ApiError {
    (status, error_json(msg))
}

fn bad_request(msg: impl Into<String>) -> ApiError {
    api_error(StatusCode::BAD_REQUEST, msg)
}

fn normalized_tx_keys(keys: &[String]) -> Vec<String> {
    let mut normalized = keys
        .iter()
        .map(|key| key.trim().to_ascii_lowercase())
        .filter(|key| !key.is_empty())
        .collect::<Vec<_>>();
    normalized.sort();
    normalized
}

fn outgoing_record_matches_registered_tx_key(
    record: &OutgoingTransferRecord,
    entry: &RegisterTxKeyEntry,
) -> bool {
    if !record.status.is_observed_mined() {
        return false;
    }
    if !record.registered_inventory {
        return false;
    }
    if record.has_owned_change == Some(true) && record.local_change_materialized != Some(true) {
        return false;
    }
    if normalized_tx_keys(&record.tx_keys) != normalized_tx_keys(&entry.tx_keys) {
        return false;
    }
    if !record
        .to_address
        .eq_ignore_ascii_case(entry.to_address.trim())
    {
        return false;
    }
    if record.amount != entry.amount {
        return false;
    }

    let entry_from = entry.from_address.as_deref().unwrap_or_default().trim();
    if !entry_from.is_empty() && !record.from_address.eq_ignore_ascii_case(entry_from) {
        return false;
    }

    let entry_memo = entry.memo.as_deref().unwrap_or_default().trim();
    if !entry_memo.is_empty() && record.memo.trim() != entry_memo {
        return false;
    }

    true
}

fn is_stale_inventory_prune_candidate(record: &OutgoingTransferRecord) -> bool {
    record.registered_inventory
        && !record.tx_keys.is_empty()
        && record.msg_id.trim().starts_with("attestation:")
}

fn register_tx_key_entry_as_validate_request(
    vault_pub_key: &str,
    entry: RegisterTxKeyEntry,
) -> ValidateOutboundRequest {
    ValidateOutboundRequest {
        vault_pub_key: vault_pub_key.to_string(),
        tx_hash: entry.tx_hash,
        tx_keys: entry.tx_keys,
        registered_inventory: true,
        from_address: entry.from_address,
        to_address: entry.to_address,
        amount: entry.amount,
        fee: entry.fee,
        memo: entry.memo,
        reconcile: true,
    }
}

async fn materialize_owned_change_outputs(
    state: &Arc<AppState>,
    vault_pub_key: &str,
    tx_hash: &str,
    tx_hex: &str,
    tx_keys: &[String],
    height: Option<u64>,
    output_indices: Option<&[u64]>,
) -> Result<usize, ApiError> {
    let Some(height) = height else {
        return Ok(0);
    };
    let first_ringct_output = output_indices
        .and_then(|indices| indices.first())
        .copied()
        .ok_or_else(|| {
            api_error(
                StatusCode::BAD_GATEWAY,
                format!("daemon omitted output_indices for mined transaction {tx_hash}"),
            )
        })?;
    let address_entries = {
        let scanner = state.scanner.lock();
        scanner
            .get_address_entries(vault_pub_key)
            .into_iter()
            .map(|entry| validation::MaterializeOwnedAddress {
                address: entry.address,
                subaddress_major: entry.subaddress_major,
                subaddress_minor: entry.subaddress_minor,
            })
            .collect::<Vec<_>>()
    };
    if address_entries.is_empty() {
        return Ok(0);
    }
    let materialized = validation::materialize_owned_outputs_from_tx_keys(
        tx_hash,
        tx_hex,
        tx_keys,
        &address_entries,
        first_ringct_output,
    )
    .map_err(|e| {
        api_error(
            StatusCode::BAD_REQUEST,
            format!("failed to materialize owned outbound outputs from tx keys: {e}"),
        )
    })?;
    if materialized.is_empty() {
        return Ok(0);
    }

    let outputs = materialized
        .into_iter()
        .map(|output| TrackedOutput {
            tx_hash: tx_hash.to_string(),
            output_index: output.output_index,
            amount: output.amount,
            height,
            key_image: None,
            spent_by_key_image_height: None,
            state: OutputState::Unspent,
            reservation: None,
            subaddress_major: output.subaddress_major,
            subaddress_minor: output.subaddress_minor,
            wallet_output_hex: Some(output.wallet_output_hex),
            address: Some(output.address),
            note: None,
        })
        .collect::<Vec<_>>();
    let (added, outputs_after_upsert) = {
        let mut scanner = state.scanner.lock();
        let added = scanner.upsert_outputs(vault_pub_key, outputs);
        let outputs_after_upsert = scanner.get_all_outputs(vault_pub_key);
        (added, outputs_after_upsert)
    };
    if added > 0 {
        OutputLifecycle::new(state.store.as_ref())
            .merge_scanned_outputs(vault_pub_key, &outputs_after_upsert)
            .map_err(|e| {
                api_error(
                    StatusCode::INTERNAL_SERVER_ERROR,
                    format!("failed to persist tx-key materialized outputs: {e}"),
                )
            })?;
    }
    Ok(added)
}

pub(crate) async fn validate_outbound_request(
    state: &Arc<AppState>,
    req: ValidateOutboundRequest,
) -> Result<ValidateOutboundResponse, ApiError> {
    let vault_pub_key = req.vault_pub_key.trim().to_string();
    if vault_pub_key.is_empty() {
        return Err(bad_request("vault_pub_key is required"));
    }

    let tx_hash = normalize_tx_hash(&req.tx_hash);
    if !is_valid_chain_tx_hash(&tx_hash) {
        return Err(bad_request("tx_hash must be a 64-character hex string"));
    }
    if req.tx_keys.is_empty() {
        return Err(bad_request("tx_keys must not be empty"));
    }

    let to_address = req.to_address.trim().to_string();
    if to_address.is_empty() {
        return Err(bad_request("to_address is required"));
    }

    let owned_addresses = {
        let scanner = state.scanner.lock();
        scanner.get_addresses(&vault_pub_key)
    };
    if owned_addresses.is_empty() {
        return Err(api_error(
            StatusCode::NOT_FOUND,
            format!("no registered addresses found for vault {vault_pub_key}"),
        ));
    }

    let from_address = req
        .from_address
        .as_deref()
        .unwrap_or_default()
        .trim()
        .to_string();
    if !from_address.is_empty()
        && !owned_addresses
            .iter()
            .any(|address| address.eq_ignore_ascii_case(&from_address))
    {
        return Err(bad_request(
            "from_address does not belong to the provided vault",
        ));
    }

    let daemon_result = match state.daemon.get_transactions(&[tx_hash.clone()]).await {
        Ok(result) => result,
        Err(e) => {
            error!(error = %e, tx_hash, "failed to query daemon transaction");
            return Err(api_error(
                StatusCode::SERVICE_UNAVAILABLE,
                format!("daemon transaction query unavailable for {tx_hash}: {e}"),
            ));
        }
    };

    let tx_entry = match daemon_result
        .txs
        .unwrap_or_default()
        .into_iter()
        .find(|entry| hash_matches(&entry.tx_hash, &tx_hash))
    {
        Some(entry) => entry,
        None => {
            if daemon_result
                .missed_tx
                .unwrap_or_default()
                .iter()
                .any(|hash| hash_matches(hash, &tx_hash))
            {
                return Err(api_error(
                    StatusCode::NOT_FOUND,
                    format!("transaction {tx_hash} not found"),
                ));
            }
            return Err(api_error(
                StatusCode::INTERNAL_SERVER_ERROR,
                format!("ambiguous daemon get_transactions response for {tx_hash}"),
            ));
        }
    };

    let tx_hex = tx_entry.as_hex.trim().to_string();
    if tx_hex.is_empty() {
        return Err(api_error(
            StatusCode::BAD_GATEWAY,
            format!("daemon returned empty tx hex for transaction {tx_hash}"),
        ));
    }

    let validated = match if req.registered_inventory {
        validation::validate_registered_outbound_transaction(
            &tx_hex,
            &req.tx_keys,
            &to_address,
            Some(req.fee),
            &owned_addresses,
        )
    } else {
        validation::validate_outbound_transaction(
            &tx_hex,
            &req.tx_keys,
            &to_address,
            req.amount,
            Some(req.fee),
            &owned_addresses,
        )
    } {
        Ok(validated) => validated,
        Err(e) => {
            warn!(
                error = %e,
                vault = vault_pub_key,
                tx_hash,
                to_address,
                amount = req.amount,
                registered_inventory = req.registered_inventory,
                fee = req.fee,
                "outbound tx-key validation failed"
            );
            return Err(bad_request(format!(
                "failed to validate outbound transaction: {e}"
            )));
        }
    };

    let record_amount = if req.registered_inventory {
        validated.destination_amount
    } else {
        req.amount
    };

    let current_height = state.scanner.lock().current_height();
    let observation = if let Some(height) = tx_entry.block_height {
        OutgoingObservation::ObservedMined(height)
    } else if tx_entry.in_pool.unwrap_or(false) {
        OutgoingObservation::ObservedInMempool
    } else {
        OutgoingObservation::AttestedPending
    };
    let observed_at = observation
        .observed_height()
        .map(|_| chrono::Utc::now().timestamp());
    if let Err(e) =
        OutgoingLifecycle::new(state.store.as_ref()).record_validated(RecordValidatedParams {
            tx_hash: tx_hash.clone(),
            tx_hex: tx_hex.clone(),
            tx_keys: req.tx_keys.clone(),
            registered_inventory: req.registered_inventory,
            has_owned_change: validated.tracked_owned_output,
            vault_pub_key: vault_pub_key.clone(),
            from_address,
            to_address,
            amount: record_amount,
            fee: validated.actual_fee,
            memo: req.memo.unwrap_or_default(),
            planned_height: observation.observed_height().unwrap_or(current_height),
            observation,
            observed_at,
            created_at: chrono::Utc::now().timestamp(),
        })
    {
        error!(error = %e, vault = vault_pub_key, tx_hash, "failed to persist tx-key validated outgoing transfer");
        return Err(api_error(
            StatusCode::INTERNAL_SERVER_ERROR,
            format!("failed to persist outgoing transfer: {e}"),
        ));
    }

    let marked = if validated.tracked_owned_output {
        materialize_owned_change_outputs(
            state,
            &vault_pub_key,
            &tx_hash,
            &tx_hex,
            &req.tx_keys,
            tx_entry.block_height,
            tx_entry.output_indices.as_deref(),
        )
        .await?
    } else {
        0
    };

    info!(
        vault = vault_pub_key,
        tx_hash,
        tracked = validated.tracked_owned_output,
        marked,
        registered_inventory = req.registered_inventory,
        reconcile = req.reconcile,
        "validated outbound via tx keys"
    );

    Ok(ValidateOutboundResponse {
        tracked: validated.tracked_owned_output,
        marked,
        not_found: 0,
        actual_fee: validated.actual_fee,
    })
}

pub(crate) async fn reconcile_registered_tx_key_inventory(
    state: &Arc<AppState>,
    vault_pub_key: &str,
    tx_key_entries: Vec<RegisterTxKeyEntry>,
) -> Result<(), ApiError> {
    let existing = state
        .store
        .load_outgoing_transfers(vault_pub_key)
        .map_err(|e| {
            api_error(
                StatusCode::INTERNAL_SERVER_ERROR,
                format!("failed to load outgoing transfers: {e}"),
            )
        })?;
    let existing_by_hash: HashMap<String, OutgoingTransferRecord> = existing
        .into_iter()
        .filter(|record| !record.tx_hash.trim().is_empty())
        .map(|record| (normalize_tx_hash(&record.tx_hash), record))
        .collect();

    let mut retained_hashes = HashSet::new();
    for entry in tx_key_entries {
        let normalized_hash = normalize_tx_hash(&entry.tx_hash);
        if normalized_hash.is_empty() {
            continue;
        }
        retained_hashes.insert(normalized_hash.clone());
        if existing_by_hash
            .get(&normalized_hash)
            .is_some_and(|record| outgoing_record_matches_registered_tx_key(record, &entry))
        {
            continue;
        }
        validate_outbound_request(
            state,
            register_tx_key_entry_as_validate_request(vault_pub_key, entry),
        )
        .await?;
    }

    let stale_hashes: HashSet<String> = existing_by_hash
        .iter()
        .filter_map(|(tx_hash, record)| {
            if !is_stale_inventory_prune_candidate(record) || retained_hashes.contains(tx_hash) {
                return None;
            }
            Some(tx_hash.clone())
        })
        .collect();
    if !stale_hashes.is_empty() {
        OutgoingLifecycle::new(state.store.as_ref())
            .prune_stale(vault_pub_key, &stale_hashes, false, true)
            .map_err(|e| {
                api_error(
                    StatusCode::INTERNAL_SERVER_ERROR,
                    format!("failed to prune stale tx-key outbounds: {e}"),
                )
            })?;
    }

    Ok(())
}
