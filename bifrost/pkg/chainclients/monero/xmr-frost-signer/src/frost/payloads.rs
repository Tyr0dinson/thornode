use std::collections::{HashMap, HashSet};

use anyhow::Result;
use tracing::warn;

pub(super) fn normalize_participant_payloads(
    session_kind: &str,
    payload_kind: Option<&str>,
    participant_count: u16,
    local_participant_index: u16,
    payloads: HashMap<u16, Vec<u8>>,
) -> Result<HashMap<u16, Vec<u8>>> {
    if participant_count == 0 {
        anyhow::bail!("{session_kind} session has invalid participant count 0");
    }
    if local_participant_index == 0 || local_participant_index > participant_count {
        anyhow::bail!(
            "{session_kind} session has invalid local participant index {} for participant count {}",
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

    if let Some(payload_kind) = payload_kind {
        log_dropped_participants(payload_kind, participant_count, &mut dropped);
    }

    Ok(normalized)
}

pub(super) fn normalize_expected_peer_payloads(
    payload_kind: Option<&str>,
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

    if let Some(payload_kind) = payload_kind {
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
    }

    normalized
}

fn log_dropped_participants(payload_kind: &str, participant_count: u16, dropped: &mut Vec<u16>) {
    if dropped.is_empty() {
        return;
    }

    dropped.sort_unstable();
    dropped.dedup();
    warn!(
        payload_kind,
        participant_count,
        dropped_participants = ?dropped,
        "ignoring participant payloads outside signing key set"
    );
}
