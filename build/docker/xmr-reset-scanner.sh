#!/usr/bin/env bash
set -euo pipefail

# Resets XMR scanner on all signer nodes, rescanning from vault birthday heights.
# Run from server: bash xmr-reset-scanner.sh
#
# This fetches vault data from thorchain, then calls the sidecar reset endpoint
# on each signer. After reset, the bifrost's scanner_sync will rescan from
# birthday heights, rediscovering all outputs.

API="http://localhost:1317"

echo "Fetching vaults from thorchain..."
VAULTS=$(curl -s "$API/thorchain/vaults/asgard")
VAULT_COUNT=$(echo "$VAULTS" | jq length)
echo "Found $VAULT_COUNT vaults"

for i in $(seq 0 $((VAULT_COUNT - 1))); do
  VAULT=$(echo "$VAULTS" | jq ".[$i]")
  CHAINS=$(echo "$VAULT" | jq -r '.chains[]' 2>/dev/null)
  if ! echo "$CHAINS" | grep -q "XMR"; then
    continue
  fi

  VAULT_PK=$(echo "$VAULT" | jq -r '.pub_key_eddsa')
  BIRTHDAY=$(echo "$VAULT" | jq -r '.vault_creation_height_xmr // 0')
  echo ""
  echo "Vault: $VAULT_PK"
  echo "Birthday: $BIRTHDAY"

  for signer in docker-xmr-frost-signer-1 docker-xmr-frost-signer-cat-1 docker-xmr-frost-signer-fox-1; do
    # Derive auth token from the signer's seed phrase
    SEED=$(docker exec "$signer" printenv SIGNER_SEED_PHRASE 2>/dev/null || echo "")
    if [ -z "$SEED" ]; then
      echo "  $signer: SKIP (no seed)"
      continue
    fi
    TOKEN=$(echo -n "${SEED}xmr-frost-auth" | sha256sum | cut -d" " -f1)

    # Find the signer's port (only dog exposes 8283 on host)
    # Use docker exec to call from within the container
    RESULT=$(docker exec "$signer" curl -s -X POST \
      -H "Authorization: Bearer $TOKEN" \
      -H "Content-Type: application/json" \
      -d "{\"vault_pub_key\":\"$VAULT_PK\",\"birthday_height\":$BIRTHDAY}" \
      http://localhost:8283/v1/monero/scanner/reset 2>&1)
    echo "  $signer: $RESULT"
  done
done

echo ""
echo "Reset complete. Bifrosts will rescan from birthday heights on next scanner_sync cycle."
echo "Monitor output counts: check /v1/monero/outputs/<vault_pub_key> on each signer."
