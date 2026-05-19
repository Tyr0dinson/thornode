#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

export DKLS_CHAINNET_LIB_ONLY=1
source "$SCRIPT_DIR/dkls-chainnet-singlehost-lib.sh"

assert_contains() {
  local haystack="$1"
  local needle="$2"
  if ! grep -F "$needle" <<<"$haystack" >/dev/null; then
    echo "expected output to contain: $needle" >&2
    echo "actual output:" >&2
    echo "$haystack" >&2
    exit 1
  fi
}

run_with_logs() {
  local name="$1"
  local body="$2"
  local logfile="$TMP_DIR/$name.log"
  cat >"$logfile"
  DKLS_TIMING_LOG_FILE="$logfile" bash -c "$body"
}

gg20_logs="$TMP_DIR/gg20.log"
cat >"$gg20_logs" <<'LOG'
2026-05-17T05:30:50.000000000Z docker-bifrost-cat-1 INF msg_id=oldkeygen leader_peer_id=16bar participants=5 block_height=55 GG20 keygen ceremony leader started
2026-05-17T05:30:51.000000000Z docker-bifrost-cat-1 INF msg_id=oldkeygen local_pubkey=cthorpub-local pubkey=cthorpub-old GG20 keygen ceremony party produced
2026-05-17T05:31:00.501949127Z docker-bifrost-whale-1 INF msg_id=abc123ecdsa leader_peer_id=16foo participants=6 block_height=60 GG20 keygen ceremony leader started
2026-05-17T05:31:01.001568765Z docker-bifrost-fox-1 INF msg_id=abc123ecdsa local_pubkey=cthorpub-other pubkey=cthorpub-other GG20 keygen ceremony party produced
2026-05-17T05:31:03.432306503Z docker-bifrost-wolf-1 INF msg_id=abc123ecdsa local_pubkey=cthorpub-local pubkey=cthorpub-target GG20 keygen ceremony party produced
LOG
out=$(DKLS_TIMING_LOG_FILE="$gg20_logs" print_keygen_ceremony_timing "GG20" "gg20" "cthorpub-target" "2026-05-17T05:30:00Z")
assert_contains "$out" "GG20 keygen TSS leader-start-to-first-party-produced=2931ms msg_id=abc123ecdsa"
assert_contains "$out" "start: 2026-05-17T05:31:00.501949127Z docker-bifrost-whale-1"
assert_contains "$out" "stop:  2026-05-17T05:31:03.432306503Z docker-bifrost-wolf-1"

dkls_logs="$TMP_DIR/dkls.log"
cat >"$dkls_logs" <<'LOG'
2026-05-17T05:08:20.902527865Z docker-bifrost-1 {"level":"info","service":"bifrost","msg_id":"dkls-keygen-1:dkls","leader_pubkey":"cthorpub-leader","participants":6,"threshold":4,"message":"DKLS keygen ceremony leader started"}
2026-05-17T05:08:22.303843886Z docker-bifrost-cat-1 {"level":"info","service":"bifrost","msg_id":"dkls-keygen-1:dkls","local_pubkey":"cthorpub-cat","message":"DKLS keygen ceremony party produced"}
2026-05-17T05:08:23.003843886Z docker-bifrost-dog-1 {"level":"info","service":"bifrost","msg_id":"dkls-keygen-1:dkls","local_pubkey":"cthorpub-dog","message":"DKLS keygen ceremony party produced"}
LOG
out=$(DKLS_TIMING_LOG_FILE="$dkls_logs" print_keygen_ceremony_timing "DKLS" "dkls" "unused" "2026-05-17T05:08:00Z")
assert_contains "$out" "DKLS keygen TSS leader-start-to-first-party-produced=1401ms msg_id=dkls-keygen-1:dkls"
assert_contains "$out" "start: 2026-05-17T05:08:20.902527865Z docker-bifrost-1"
assert_contains "$out" "stop:  2026-05-17T05:08:22.303843886Z docker-bifrost-cat-1"

keysign_logs="$TMP_DIR/keysign.log"
cat >"$keysign_logs" <<'LOG'
2026-05-17T05:31:05.445453229Z docker-bifrost-1 INF msg_id=sign123 pool_pub_key=cthorpub-old leader_peer_id=16foo participants=4 block_height=70 GG20 keysign ceremony leader started
2026-05-17T05:31:06.445453229Z docker-bifrost-cat-1 INF msg_id=sign123 pool_pub_key=cthorpub-other local_pubkey=cthorpub-cat GG20 keysign ceremony party produced
2026-05-17T05:31:07.945453229Z docker-bifrost-eagle-1 INF msg_id=sign123 pool_pub_key=cthorpub-old local_pubkey=cthorpub-eagle GG20 keysign ceremony party produced
LOG
out=$(DKLS_TIMING_LOG_FILE="$keysign_logs" print_keysign_ceremony_timing "GG20 migration" "gg20" "cthorpub-old" "2026-05-17T05:31:00Z")
assert_contains "$out" "GG20 migration keysign TSS leader-start-to-first-party-produced=2500ms msg_id=sign123"
assert_contains "$out" "stop:  2026-05-17T05:31:07.945453229Z docker-bifrost-eagle-1"

dkls_keysign_logs="$TMP_DIR/dkls-keysign.log"
cat >"$dkls_keysign_logs" <<'LOG'
2026-05-17T06:35:29.438253585Z docker-bifrost-1 INF msg_id=checksign:dkls pool_pub_key=cthorpub-new leader_pubkey=cthorpub-leader message_index=0 participants=4 block_height=220 DKLS keysign ceremony leader started
2026-05-17T06:35:30.019555701Z docker-bifrost-1 INF msg_id=checksign:dkls pool_pub_key=cthorpub-new local_pubkey=cthorpub-leader message_index=0 DKLS keysign ceremony party produced
2026-05-17T06:35:39.261620805Z docker-bifrost-goat-1 {"level":"info","service":"bifrost","module":"tss","msg_id":"migration:dkls","pool_pub_key":"cthorpub-old","leader_pubkey":"cthorpub-goat","message_index":0,"participants":4,"block_height":220,"message":"DKLS keysign ceremony leader started"}
2026-05-17T06:35:39.881870181Z docker-bifrost-eagle-1 {"level":"info","service":"bifrost","module":"tss","msg_id":"migration:dkls","pool_pub_key":"cthorpub-old","local_pubkey":"cthorpub-eagle","message_index":0,"message":"DKLS keysign ceremony party produced"}
LOG
out=$(DKLS_TIMING_LOG_FILE="$dkls_keysign_logs" print_keysign_ceremony_timing "DKLS migration" "dkls" "cthorpub-old" "2026-05-17T06:35:00Z")
assert_contains "$out" "DKLS migration keysign TSS leader-start-to-first-party-produced=620ms msg_id=migration:dkls"
assert_contains "$out" "start: 2026-05-17T06:35:39.261620805Z docker-bifrost-goat-1"
assert_contains "$out" "stop:  2026-05-17T06:35:39.881870181Z docker-bifrost-eagle-1"

out_file="$TMP_DIR/dkls-keysign.out"
DKLS_TIMING_LOG_FILE="$dkls_keysign_logs" print_keysign_ceremony_timing "DKLS migration" "dkls" "cthorpub-old" "2026-05-17T06:35:00Z" >"$out_file"

RESULTS_DIR="$TMP_DIR/results"
RESULTS_CSV="$RESULTS_DIR/tss-timings.csv"
RESULTS_JSONL="$RESULTS_DIR/tss-timings.jsonl"
export RUN_ID="test-run"
init_results >/dev/null
append_timing_result "keysign" "dkls" "DKLS migration" "cthorpub-old" "396" "228" "txid123" "unit test" >/dev/null
assert_contains "$(cat "$RESULTS_CSV")" "test-run"
assert_contains "$(cat "$RESULTS_CSV")" "migration:dkls"
assert_contains "$(cat "$RESULTS_CSV")" "620"
assert_contains "$(cat "$RESULTS_JSONL")" '"txid":"txid123"'

missing_logs="$TMP_DIR/missing.log"
: >"$missing_logs"
out=$(DKLS_TIMING_LOG_FILE="$missing_logs" print_keygen_ceremony_timing "DKLS" "dkls" "unused" "2026-05-17T05:08:00Z")
assert_contains "$out" "WARN: DKLS keygen TSS ceremony start log not found"

echo "dkls-chainnet timing parser tests passed"
