#!/usr/bin/env bash
set -euo pipefail

# Resets XMR scanner on all signer nodes and restarts bifrosts to rescan from
# the earliest vault birthday height. This performs a full rescan — the sidecar
# rediscovers all outputs and the bifrost re-observes all transactions to
# thorchain, exactly as if it were a first-time startup.
#
# Steps:
#   1. Fetch XMR asgard vaults from thorchain (pub_key_frost, birthday)
#   2. Call sidecar /v1/monero/scanner/reset for each vault on each signer
#   3. Restart bifrosts with XMR_START_BLOCK_HEIGHT=<earliest_birthday>
#
# The bifrost reads XMR_START_BLOCK_HEIGHT at startup and overrides the
# persisted scan position. On the NEXT restart (without the env var), it
# resumes from wherever the rescan reached.
#
# Auth: derive the sidecar bearer token from SIGNER_SEED_PHRASE.
#
# Usage:
#   bash xmr-reset-scanner.sh                    # docker-compose deployment
#   bash xmr-reset-scanner.sh --dry-run          # print what would happen
#   COMPOSE_FILE=path/to/compose.yml bash xmr-reset-scanner.sh

DRY_RUN=false
if [[ ${1-} == "--dry-run" ]]; then
  DRY_RUN=true
  echo "[DRY RUN] No changes will be made."
  echo ""
fi

API="http://localhost:1317"
EARLIEST_BIRTHDAY=0

SIGNERS=(docker-xmr-frost-signer-1 docker-xmr-frost-signer-cat-1 docker-xmr-frost-signer-fox-1)
BIFROSTS=(docker-bifrost-1 docker-bifrost-cat-1 docker-bifrost-fox-1)

echo "Fetching vaults from thorchain..."
VAULTS=$(curl -s "$API/thorchain/vaults/asgard")
VAULT_COUNT=$(echo "$VAULTS" | jq length)
echo "Found $VAULT_COUNT vaults"

# --- Step 1: Reset sidecar for each XMR vault ---

for i in $(seq 0 $((VAULT_COUNT - 1))); do
  VAULT=$(echo "$VAULTS" | jq ".[$i]")
  CHAINS=$(echo "$VAULT" | jq -r '.chains[]' 2>/dev/null)
  if ! echo "$CHAINS" | grep -q "XMR"; then
    continue
  fi

  VAULT_PK=$(echo "$VAULT" | jq -r '.pub_key_frost // .pub_key_eddsa')
  BIRTHDAY=$(echo "$VAULT" | jq -r '.vault_creation_height_xmr // 0')

  if [ "$BIRTHDAY" -le 0 ] 2>/dev/null; then
    echo "Vault $VAULT_PK: SKIP (no birthday)"
    continue
  fi

  if [ "$EARLIEST_BIRTHDAY" -eq 0 ] || [ "$BIRTHDAY" -lt "$EARLIEST_BIRTHDAY" ]; then
    EARLIEST_BIRTHDAY=$BIRTHDAY
  fi

  echo ""
  echo "Vault: $VAULT_PK"
  echo "Birthday: $BIRTHDAY"

  for signer in "${SIGNERS[@]}"; do
    SEED=$(docker exec "$signer" printenv SIGNER_SEED_PHRASE 2>/dev/null || echo "")
    if [ -z "$SEED" ]; then
      echo "  $signer: SKIP (not found or no seed)"
      continue
    fi
    TRIMMED_SEED=$(echo -n "$SEED" | sed 's/^[[:space:]]*//;s/[[:space:]]*$//')
    TOKEN=$(echo -n "${TRIMMED_SEED}xmr-frost-auth" | sha256sum | cut -d" " -f1)

    if $DRY_RUN; then
      echo "  $signer: would reset to height $BIRTHDAY"
      continue
    fi

    RESULT=$(docker exec "$signer" curl -s -X POST \
      -H "Authorization: Bearer $TOKEN" \
      -H "Content-Type: application/json" \
      -d "{\"vault_pub_key\":\"$VAULT_PK\",\"birthday_height\":$BIRTHDAY}" \
      http://localhost:8283/v1/monero/scanner/reset 2>&1)
    echo "  $signer: $RESULT"
  done
done

if [ "$EARLIEST_BIRTHDAY" -eq 0 ]; then
  echo ""
  echo "No XMR vaults with birthdays found. Nothing to do."
  exit 0
fi

echo ""
echo "Earliest birthday: $EARLIEST_BIRTHDAY"

# --- Step 2: Restart bifrosts with XMR_START_BLOCK_HEIGHT ---

echo ""
echo "Restarting bifrosts with XMR_START_BLOCK_HEIGHT=$EARLIEST_BIRTHDAY ..."

for bifrost in "${BIFROSTS[@]}"; do
  if ! docker inspect "$bifrost" &>/dev/null; then
    echo "  $bifrost: SKIP (not found)"
    continue
  fi

  if $DRY_RUN; then
    echo "  $bifrost: would restart with XMR_START_BLOCK_HEIGHT=$EARLIEST_BIRTHDAY"
    continue
  fi

  docker stop "$bifrost" 2>/dev/null || true
  echo "  $bifrost: stopped"
done

if ! $DRY_RUN; then
  echo ""
  echo "Starting bifrosts..."
  XMR_START_BLOCK_HEIGHT=$EARLIEST_BIRTHDAY docker compose up -d bifrost bifrost-cat bifrost-fox 2>/dev/null || {
    echo ""
    echo "docker compose failed. Start manually:"
    echo "  XMR_START_BLOCK_HEIGHT=$EARLIEST_BIRTHDAY docker compose up -d bifrost bifrost-cat bifrost-fox"
  }
fi

echo ""
echo "Done. Sidecars reset, bifrosts restarting from XMR height $EARLIEST_BIRTHDAY."
echo ""
echo "The bifrost reads XMR_START_BLOCK_HEIGHT once at startup. After the rescan"
echo "completes, restart normally without the env var — the scanner resumes from"
echo "its new position."
