# XMR Chainnet Runbook

This runbook covers the private 7-node XMR chainnet cluster defined in
[build/docker/docker-compose.xmr-chainnet.yml](/Users/dev/dev/thorchain/thornode/build/docker/docker-compose.xmr-chainnet.yml)
and bootstrapped by
[build/docker/xmr-chainnet-post-deploy.sh](/Users/dev/dev/thorchain/thornode/build/docker/xmr-chainnet-post-deploy.sh).

## Topology

- 7 validators: `dog`, `cat`, `fox`, `goat`, `whale`, `wolf`, `eagle`
- `dog` is the genesis validator and maps to the unsuffixed compose services:
  `thornode`, `bifrost`, `xmr-frost-signer`
- steady state: `6 Active`, `1 Standby`
- XMR target shape after fresh bootstrap: `2` active Asgard vaults, each with `3` members
- FROST threshold in that shape: `2-of-3`
- bifrost uses the same Monero RPC URL as the signer sidecars in this compose file

## Files That Matter

- cluster definition:
  [build/docker/docker-compose.xmr-chainnet.yml](/Users/dev/dev/thorchain/thornode/build/docker/docker-compose.xmr-chainnet.yml)
- fresh bootstrap:
  [build/docker/xmr-chainnet-post-deploy.sh](/Users/dev/dev/thorchain/thornode/build/docker/xmr-chainnet-post-deploy.sh)

## Prerequisites

Run all commands from the repo root on the cluster host, usually `/root/thornode`.

Required env in the repo `.env`:

```bash
XMR_DAEMON_URL=http://<monerod-rpc>:18081
```

Current chainnet value on `91.99.89.216`:

```bash
XMR_DAEMON_URL=http://188.245.203.167:18081
```

The post-deploy script also reads `XMR_DAEMON_URL` from the shell environment and does a host-side reachability check, so export it before running the script even if it already exists in `.env`.

## Fresh Bring-Up

1. Update code to the desired branch or commit.

```bash
git fetch origin
git checkout <branch>
git pull --ff-only
```

2. Verify Monero RPC is set.

```bash
grep XMR_DAEMON_URL .env
export XMR_DAEMON_URL="$(grep '^XMR_DAEMON_URL=' .env | cut -d= -f2-)"
```

3. Build the shared image from scratch.

```bash
docker compose --env-file .env -f build/docker/docker-compose.xmr-chainnet.yml build --no-cache
```

4. Start the stack.

```bash
docker compose --env-file .env -f build/docker/docker-compose.xmr-chainnet.yml up -d
```

5. Run the bootstrap script.

```bash
bash build/docker/xmr-chainnet-post-deploy.sh
```

What the bootstrap script does:

- waits for dog thornode health
- sets initial mimirs:
  - `HaltChurning=1`
  - `P2PGateDisabled=1`
  - `DesiredValidatorSet=6`
  - `AsgardSize=3`
  - `NumberOfNewNodesPerChurn=5`
- waits for XMR fee and chain tip to appear
- sets node keys / IP / version for the 6 joiners
- bonds all 7 nodes
- restarts bifrost in bootstrap order: dog first, then joiners
- lifts `HaltChurning`
- waits for the first churn/keygen
- once vault shape is `3,3`, votes:
  - `HALTCHURNING=1`
  - `NUMBEROFNEWNODESPERCHURN=1`

## Expected Fresh-Bootstrap End State

Check these:

```bash
curl -s localhost:1317/thorchain/mimir | jq
curl -s localhost:1317/thorchain/nodes | jq -r '.[].status' | sort | uniq -c
curl -s localhost:1317/thorchain/vaults/asgard | jq '[.[] | select(.status=="ActiveVault") | (.membership | length)] | sort'
curl -s localhost:1317/thorchain/inbound_addresses | jq '.[] | select(.chain=="XMR")'
curl -s localhost:1317/thorchain/lastblock | jq '.[] | select(.chain=="XMR")'
```

Expected values:

- node statuses: `6 Active`, `1 Standby`
- active vault shape: `[3,3]`
- `HALTCHURNING = 1`
- `DESIREDVALIDATORSET = 6`
- `ASGARDSIZE = 3`
- `NUMBEROFNEWNODESPERCHURN = 1`
- XMR inbound address exists
- `observed_fee_rate` is nonzero
- XMR `last_observed_in` is nonzero and advancing

## Normal Code Rollout

Use this for ordinary code deploys. Do not re-run the bootstrap script unless this is a fresh cluster.

1. Update code.

```bash
git fetch origin
git checkout <branch>
git pull --ff-only
```

2. Rebuild the shared image.

```bash
docker compose --env-file .env -f build/docker/docker-compose.xmr-chainnet.yml build thornode
```

3. Recreate thornodes only.

```bash
docker compose --env-file .env -f build/docker/docker-compose.xmr-chainnet.yml up -d --force-recreate \
  thornode thornode-cat thornode-fox thornode-goat thornode-whale thornode-wolf thornode-eagle
```

4. If Rust signer code changed, rebuild and recreate the signer sidecars too.

```bash
docker compose --env-file .env -f build/docker/docker-compose.xmr-chainnet.yml build xmr-frost-signer
docker compose --env-file .env -f build/docker/docker-compose.xmr-chainnet.yml up -d --force-recreate \
  xmr-frost-signer xmr-frost-signer-cat xmr-frost-signer-fox xmr-frost-signer-goat \
  xmr-frost-signer-whale xmr-frost-signer-wolf xmr-frost-signer-eagle
```

5. Recreate bifrosts in bootstrap order.

Do not recreate all bifrosts in one `docker compose up` call. That reintroduces
the startup race where dog has no peers yet and joiners hit `protocol not supported`
while the leader is still finishing TSS pre-parameter setup.

```bash
bash build/docker/xmr-chainnet-restart-bifrosts.sh recreate
```

## Smoke Checks After Deploy

```bash
docker compose --env-file .env -f build/docker/docker-compose.xmr-chainnet.yml ps
curl -s localhost:1317/thorchain/lastblock | jq '.[] | select(.chain=="XMR")'
curl -s localhost:1317/thorchain/queue/outbound | jq
docker logs --since 5m docker-bifrost-1 2>&1 | tail -200
docker logs --since 5m docker-xmr-frost-signer-1 2>&1 | tail -200
```

Things to watch for:

- `last_observed_in` advancing
- `last_signed_out` advancing when there are XMR outbounds
- no fresh `scanner unhealthy`
- no fresh `failed to derive outbound tx keys`
- no fresh `leader not reachable` storms during normal keysign

## Operator Notes

### Service naming on the 7-node cluster

- chainnet host: `root@91.99.89.216`
- run commands from `/root/thornode`
- `dog` uses the unsuffixed service names: `thornode`, `bifrost`, `xmr-frost-signer`
- the other 6 validators use suffixed names, for example `docker-bifrost-cat-1`
- when checking "all nodes", include both unsuffixed `dog` and the 6 suffixed services

Keep canonical container lists around so loops do not silently skip `dog`:

```bash
THORNODES=(docker-thornode-1 docker-thornode-cat-1 docker-thornode-fox-1 docker-thornode-goat-1 docker-thornode-whale-1 docker-thornode-wolf-1 docker-thornode-eagle-1)
BIFROSTS=(docker-bifrost-1 docker-bifrost-cat-1 docker-bifrost-fox-1 docker-bifrost-goat-1 docker-bifrost-whale-1 docker-bifrost-wolf-1 docker-bifrost-eagle-1)
SIGNERS=(docker-xmr-frost-signer-1 docker-xmr-frost-signer-cat-1 docker-xmr-frost-signer-fox-1 docker-xmr-frost-signer-goat-1 docker-xmr-frost-signer-whale-1 docker-xmr-frost-signer-wolf-1 docker-xmr-frost-signer-eagle-1)
```

### SSH and shell escaping

The cluster is much easier to operate if you avoid stacking local shell expansion,
remote shell expansion, and inner `docker exec ... sh -lc` expansion in the same line.

Preferred patterns:

- for exploratory work, `ssh root@91.99.89.216`, `cd /root/thornode`, then run the
  runbook commands locally on the host
- for complex remote command blocks, use a quoted heredoc so the local shell does not
  pre-expand `$vars`, backticks, or globs
- for signer API calls, derive the seed on the host and pass it into `docker exec -e`;
  do not try to inline a seed phrase directly through nested `ssh "...$seed..."` quoting

Safe multiline pattern:

```bash
ssh root@91.99.89.216 'bash -s' <<'EOF'
set -euo pipefail
cd /root/thornode
docker compose --env-file .env -f build/docker/docker-compose.xmr-chainnet.yml ps
curl -s localhost:1317/thorchain/lastblock | jq '.[] | select(.chain=="XMR")'
EOF
```

Redirection rule:

- `ssh root@91.99.89.216 'docker cp ... | gzip' > local.tar.gz` writes `local.tar.gz` on your local machine
- `ssh root@91.99.89.216 'docker cp ... | gzip > /tmp/remote.tar.gz'` writes `/tmp/remote.tar.gz` on the remote host

Avoid this pattern for seed phrases and raw auth tokens:

```bash
ssh root@91.99.89.216 "seed=...words with spaces...; docker exec -e AUTH=\"$seed\" ..."
```

Use this pattern instead:

```bash
ssh root@91.99.89.216 'bash -s' <<'EOF'
set -euo pipefail
seed=$(docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' docker-xmr-frost-signer-1 | awk -F= '/^SIGNER_SEED_PHRASE=/{sub(/^SIGNER_SEED_PHRASE=/,""); print}')
docker exec -e AUTH="$seed" docker-xmr-frost-signer-1 sh -lc \
  'curl -sf -H "Authorization: $AUTH" http://127.0.0.1:8283/v1/monero/chain/height'
EOF
```

### Signer API auth

The XMR signer API uses the raw `SIGNER_SEED_PHRASE` as the `Authorization`
header value unless
`XMR_FROST_AUTH_BEARER` is explicitly set.

Despite the env name, `XMR_FROST_AUTH_BEARER` is also sent as the raw
`Authorization` header value, not as `Authorization: Bearer ...`.

In this chainnet, each signer has its own auth token because each signer has its
own seed phrase. Do not reuse a token from one node against another node.

Example for one signer:

```bash
seed=$(docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' docker-xmr-frost-signer-1 | awk -F= '/^SIGNER_SEED_PHRASE=/{sub(/^SIGNER_SEED_PHRASE=/,""); print}')
docker exec -e AUTH="$seed" docker-xmr-frost-signer-1 sh -lc \
  'curl -sf -H "Authorization: $AUTH" "http://127.0.0.1:8283/v1/monero/outputs/<vault_pub_key>?view=all"'
```

Quick auth smoke test across all signers:

```bash
SIGNERS=(docker-xmr-frost-signer-1 docker-xmr-frost-signer-cat-1 docker-xmr-frost-signer-fox-1 docker-xmr-frost-signer-goat-1 docker-xmr-frost-signer-whale-1 docker-xmr-frost-signer-wolf-1 docker-xmr-frost-signer-eagle-1)
for c in "${SIGNERS[@]}"; do
  seed=$(docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' "$c" | awk -F= '/^SIGNER_SEED_PHRASE=/{sub(/^SIGNER_SEED_PHRASE=/,""); print}')
  printf '%s ' "$c"
  docker exec -e AUTH="$seed" "$c" sh -lc \
    'curl -sf -H "Authorization: $AUTH" http://127.0.0.1:8283/v1/monero/chain/height >/dev/null && echo OK'
done
```

### Fresh outbound visibility on signers

For a newly signed XMR outbound, use the signer mempool view first:

```bash
TX=<lowercase_xmr_tx_hash>
SIGNERS=(docker-xmr-frost-signer-1 docker-xmr-frost-signer-cat-1 docker-xmr-frost-signer-fox-1 docker-xmr-frost-signer-goat-1 docker-xmr-frost-signer-whale-1 docker-xmr-frost-signer-wolf-1 docker-xmr-frost-signer-eagle-1)
for c in "${SIGNERS[@]}"; do
  seed=$(docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' "$c" | awk -F= '/^SIGNER_SEED_PHRASE=/{sub(/^SIGNER_SEED_PHRASE=/,""); print}')
  resp=$(docker exec -e AUTH="$seed" "$c" sh -lc 'curl -sf -H "Authorization: $AUTH" "http://127.0.0.1:8283/v1/monero/transfers?mempool=true"')
  printf '%s ' "$c"
  printf '%s' "$resp" | jq -c --arg tx "$TX" 'first(.outgoing[]? | select((.txid // "") | ascii_downcase == $tx) | {txid,from_address,address,vault_pub_key,amount,fee,height,tx_keys,note}) // "MISSING"'
done
```

Endpoint semantics:

- `GET /v1/monero/transfers?mempool=true` is the correct view for a fresh outbound and tx-key propagation
- `GET /v1/monero/transfers?height=0` only returns mined outbound records
- if keysign just succeeded, absence from the height-based view does not mean the signer lost the tx

### Fast path to verify an XMR tx end to end

1. Check thornode has the tx:

```bash
TX=<lowercase_or_uppercase_xmr_tx_hash>
curl -s "http://localhost:1317/thorchain/tx/${TX^^}" | jq
```

2. Get the canonical active XMR vaults and the key-images stored on chain:

```bash
curl -s http://localhost:1317/thorchain/vaults/asgard | jq '[.[] | {status, pub_key_frost, key_images_xmr, tx_keys_xmr}]'
```

3. For a fresh outbound, confirm the signer-side outgoing record includes the tx keys:

```bash
TX=<tx_hash_lowercase>
SIGNERS=(docker-xmr-frost-signer-1 docker-xmr-frost-signer-cat-1 docker-xmr-frost-signer-fox-1 docker-xmr-frost-signer-goat-1 docker-xmr-frost-signer-whale-1 docker-xmr-frost-signer-wolf-1 docker-xmr-frost-signer-eagle-1)
for c in "${SIGNERS[@]}"; do
  seed=$(docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' "$c" | awk -F= '/^SIGNER_SEED_PHRASE=/{sub(/^SIGNER_SEED_PHRASE=/,""); print}')
  resp=$(docker exec -e AUTH="$seed" "$c" sh -lc 'curl -sf -H "Authorization: $AUTH" "http://127.0.0.1:8283/v1/monero/transfers?mempool=true"')
  printf '%s ' "$c"
  printf '%s' "$resp" | jq -c --arg tx "$TX" 'first(.outgoing[]? | select((.txid // "") | ascii_downcase == $tx) | {txid,from_address,address,vault_pub_key,amount,fee,tx_keys,note}) // "MISSING"'
done
```

Interpretation:

- signing nodes should show the outgoing row immediately after keysign
- non-signing nodes can miss the local row briefly, but must catch up from thornode attestation
- once the tx is mined, the same record should become visible in `GET /v1/monero/transfers?height=0`

4. Check all signers hold the same tracked output state for that tx:

```bash
TX=<tx_hash_lowercase>
SIGNERS=(docker-xmr-frost-signer-1 docker-xmr-frost-signer-cat-1 docker-xmr-frost-signer-fox-1 docker-xmr-frost-signer-goat-1 docker-xmr-frost-signer-whale-1 docker-xmr-frost-signer-wolf-1 docker-xmr-frost-signer-eagle-1)
VAULT=<cthorpub1...frost_vault>
for c in "${SIGNERS[@]}"; do
  seed=$(docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' "$c" | awk -F= '/^SIGNER_SEED_PHRASE=/{sub(/^SIGNER_SEED_PHRASE=/,""); print}')
  resp=$(docker exec -e AUTH="$seed" "$c" sh -lc 'curl -sf -H "Authorization: $AUTH" "http://127.0.0.1:8283/v1/monero/outputs/'"$VAULT"'?view=all"')
  printf '%s ' "$c"
  printf '%s' "$resp" | jq -c --arg tx "$TX" 'first(.outputs[]? | select((.tx_hash // "") | ascii_downcase == $tx) | {tx_hash,state,key_image,height,amount,output_index}) // "MISSING"'
done
```

Interpretation:

- tx visible in thornode and on all signers with `key_image=null`:
  first observation completed, finalized key-image round has not landed yet
- tx visible in `key_images_xmr` on chain and on all signers with the same non-null `key_image`:
  key-image generation, validation, attestation, and local apply all completed

5. Check the recent bifrost path for that tx:

```bash
TX=<tx_hash_lowercase>
for c in docker-bifrost-1 docker-bifrost-cat-1 docker-bifrost-fox-1 docker-bifrost-goat-1 docker-bifrost-whale-1 docker-bifrost-wolf-1 docker-bifrost-eagle-1; do
  hit=$(docker logs "$c" --since 30m 2>&1 | grep -i "$TX" || true)
  if [ -n "$hit" ]; then
    echo "=== $c ==="
    printf '%s\n' "$hit" | tail -n 12
  fi
done
```

Look for these stages:

- `queueing XMR key-image generation`
- `starting frost key-image ceremony`
- `completed frost key-image ceremony`
- `attached XMR key-images to finalized observation`
- `validating finalized XMR key-images against local tracked outputs`
- `validated and applied finalized XMR key-images`
- `sent quorum tx to thornode ... final: true`

### Transient key-image leader failures

A single `leader not reachable` during XMR key-image generation is not automatically
fatal. Another bifrost can win the next attempt, complete the ceremony, and the cluster
can still converge correctly. Treat repeated failures across multiple nodes as the
real problem, not a single failed attempt.

## Operator query scripts

Use the scripts below instead of rebuilding one-off shell snippets each time. They all
support:

- `--host root@91.99.89.216` to query the chainnet cluster from your local machine
- `--node dog` for one node
- `--node all` for the full cluster
- repeated `--node` flags to scope to a subset
- automatic node discovery from the current docker cluster; set `XMR_CHAINNET_NODES` only if you need to override it

Check signer state, outputs, key-images, tx-keys, and outbound inventory:

```bash
bash build/docker/xmr-chainnet-check-signer-state.sh
bash build/docker/xmr-chainnet-check-signer-state.sh --host root@91.99.89.216
bash build/docker/xmr-chainnet-check-signer-state.sh --node dog
bash build/docker/xmr-chainnet-check-signer-state.sh --node cat --node fox
bash build/docker/xmr-chainnet-check-signer-state.sh --node all --vault <cthorpub1...>
```

Use this first when you need to answer:

- whether all 7 signers have the new active vault registered
- which nodes are missing an active XMR vault
- whether local signer inventory has diverged from on-chain XMR vault state

The script now prints an `Active vault registration coverage` section before the
full per-vault signer table, so there should be no need to rebuild one-off curl
or jq commands for that question.

Check a transaction across thornode, signers, outputs, outbounds, and bifrost logs:

```bash
bash build/docker/xmr-chainnet-check-tx.sh --tx 031ccfb458800fecc7de0b3ba57a4ea9681d2af4bb95a0e63e0ea1ef86580e7d
bash build/docker/xmr-chainnet-check-tx.sh --host root@91.99.89.216 --tx 031ccf...
bash build/docker/xmr-chainnet-check-tx.sh --tx 031ccf... --node dog
bash build/docker/xmr-chainnet-check-tx.sh --tx 031ccf... --node all --logs-since 2h
```

Check pools, vaults, and per-node XMR summary:

```bash
bash build/docker/xmr-chainnet-check-pools-vaults.sh
bash build/docker/xmr-chainnet-check-pools-vaults.sh --host root@91.99.89.216
bash build/docker/xmr-chainnet-check-pools-vaults.sh --node dog
bash build/docker/xmr-chainnet-check-pools-vaults.sh --node all
```

### Driving a birthday reset scanner

Use the reset tool when a signer needs to forget local Monero scan state and replay from
the vault birthday.

Wrapper:

```bash
build/docker/xmr-reset-scanner.sh
```

The wrapper runs an installed `xmr-reset-scanner` binary if one exists. Otherwise it
extracts `xmr-reset-scanner` from the locally built `thornode:xmr-chainnet` /
`thornode:xmr-stagenet` image into `build/docker/.bin` if possible, then falls
back to `go run tools/xmr-reset-scanner`. The chainnet post-deploy script also
installs the host copy to `/usr/local/bin/xmr-reset-scanner`.

Single vault on one signer:

```bash
build/docker/xmr-reset-scanner.sh \
  --chain-network chainnet \
  --signer-container docker-xmr-frost-signer-1 \
  --vault-pubkey <cthorpub1...frost_vault> \
  --height <vault_creation_height_xmr>
```

All local vaults on one signer, using birthdays from thornode:

```bash
build/docker/xmr-reset-scanner.sh \
  --chain-network chainnet \
  --signer-container docker-xmr-frost-signer-1 \
  --all-vaults
```

Important scope note:

- `--all-vaults` resets every XMR vault the sidecar has registered, regardless of whether this node holds FROST keyshares for that vault. Non-signing nodes must rescan member-less vaults too so they can validate and co-attest to key-image observations produced by the signing subset.
- it does not reset every historical XMR vault record in thornode — only vaults the sidecar already knows about
- for a cluster-wide sidecar rebuild, run `--all-vaults` against each signer container; alternatively run `--vault-pubkey` per target

Recommended full reset flow:

1. Run `--all-vaults` on every signer container to rescan every registered XMR vault.
2. Optionally use `--vault-pubkey` to scope the reset to a specific vault when only one is suspect.

Cluster-wide reset for two tracked vaults on all 7 signers:

```bash
SIGNERS=(docker-xmr-frost-signer-1 docker-xmr-frost-signer-cat-1 docker-xmr-frost-signer-fox-1 docker-xmr-frost-signer-goat-1 docker-xmr-frost-signer-whale-1 docker-xmr-frost-signer-wolf-1 docker-xmr-frost-signer-eagle-1)
VAULTS=(<cthorpub1_vault_a> <cthorpub1_vault_b>)
for signer in "${SIGNERS[@]}"; do
  for vault in "${VAULTS[@]}"; do
    build/docker/xmr-reset-scanner.sh \
      --chain-network chainnet \
      --signer-container "$signer" \
      --vault-pubkey "$vault"
  done
done
```

Example: pull the canonical XMR vault pubkeys from thornode first, then run the full reset:

```bash
SIGNERS=(docker-xmr-frost-signer-1 docker-xmr-frost-signer-cat-1 docker-xmr-frost-signer-fox-1 docker-xmr-frost-signer-goat-1 docker-xmr-frost-signer-whale-1 docker-xmr-frost-signer-wolf-1 docker-xmr-frost-signer-eagle-1)
mapfile -t VAULTS < <(
  curl -s http://localhost:1317/thorchain/vaults/asgard |
    jq -r '.[] | select(.chains[]? == "XMR") | select((.vault_creation_height_xmr // "0") != "0") | (.pub_key_frost // .pub_key_eddsa // .pub_key)' |
    sort -u
)
for signer in "${SIGNERS[@]}"; do
  for vault in "${VAULTS[@]}"; do
    build/docker/xmr-reset-scanner.sh \
      --chain-network chainnet \
      --signer-container "$signer" \
      --vault-pubkey "$vault"
  done
done
```

`--vault-pubkey` now canonicalizes through thornode as well, but the query above keeps the operator-visible vault list aligned with what the sidecars actually track.

Dry run first if needed:

```bash
build/docker/xmr-reset-scanner.sh \
  --chain-network chainnet \
  --signer-container docker-xmr-frost-signer-1 \
  --all-vaults \
  --dry-run
```

Important behavior:

- the reset is local to the signer you target
- repeat the command for each signer container that needs replay
- if `--height` is omitted for `--vault-pubkey`, the tool looks up `vault_creation_height_xmr` from thornode
- the signer reset clears local outputs, clears outgoing transfer records, rewinds scan state, and re-registers the vault at its birthday
- bifrost then rebuilds state during normal `scanner_sync`, including replaying key-images and tx-key-backed outbound audit records from thornode
- immediately after reset, nodes can disagree for a short period while scan replay and catchup are still running

What to expect after a reset:

- signer outputs for that vault can temporarily drop to zero
- signer outgoing transfer lists for that vault can temporarily be empty
- signer logs should show `scanner reset complete — rescan enabled from birthday`
- later logs should show the scanner walking forward from the birthday
- bifrost can briefly log both XMR key-image catchup and XMR tx-key catchup while state is being rediscovered
- final state should converge back to the same tracked outputs, key-images, and outbound audit records as before the reset

Quick verification loop:

```bash
docker logs --since 5m docker-xmr-frost-signer-1 2>&1 | egrep 'scanner reset complete|rescan|scanning blocks'
docker logs --since 5m docker-bifrost-1 2>&1 | egrep 'xmr key-image catchup|xmr tx-key catchup' || true
```

## Fresh Teardown

Do not remove the Monero volume unless you intend to fully rebuild Monero state.

1. Stop and remove cluster services, but leave `monero-daemon` alone.

```bash
docker compose --env-file .env -f build/docker/docker-compose.xmr-chainnet.yml stop \
  thornode thornode-cat thornode-fox thornode-goat thornode-whale thornode-wolf thornode-eagle \
  bifrost bifrost-cat bifrost-fox bifrost-goat bifrost-whale bifrost-wolf bifrost-eagle \
  xmr-frost-signer xmr-frost-signer-cat xmr-frost-signer-fox xmr-frost-signer-goat \
  xmr-frost-signer-whale xmr-frost-signer-wolf xmr-frost-signer-eagle

docker compose --env-file .env -f build/docker/docker-compose.xmr-chainnet.yml rm -f \
  thornode thornode-cat thornode-fox thornode-goat thornode-whale thornode-wolf thornode-eagle \
  bifrost bifrost-cat bifrost-fox bifrost-goat bifrost-whale bifrost-wolf bifrost-eagle \
  xmr-frost-signer xmr-frost-signer-cat xmr-frost-signer-fox xmr-frost-signer-goat \
  xmr-frost-signer-whale xmr-frost-signer-wolf xmr-frost-signer-eagle
```

2. Remove cluster state volumes.

```bash
docker volume rm \
  docker_thornode docker_thornode-cat docker_thornode-fox docker_thornode-goat docker_thornode-whale docker_thornode-wolf docker_thornode-eagle \
  docker_bifrost docker_bifrost-cat docker_bifrost-fox docker_bifrost-goat docker_bifrost-whale docker_bifrost-wolf docker_bifrost-eagle \
  docker_xmr-frost-data docker_xmr-frost-data-cat docker_xmr-frost-data-fox docker_xmr-frost-data-goat docker_xmr-frost-data-whale docker_xmr-frost-data-wolf docker_xmr-frost-data-eagle
```

## Common Failure Modes

### No XMR fee or tip after startup

Fresh XMR bring-up can sit at `observed_fee_rate=0` and `last_observed_in=0` for a few minutes. This can still be healthy. Wait for the first Monero block after bifrost startup to be observed.

### Bifrost peer discovery deadlock

If dog bifrost is stuck dialing other nodes and TSS cannot form parties:

```bash
docker exec docker-thornode-1 rm -f /root/.thornode/address_book.seed
docker exec docker-thornode-cat-1 rm -f /root/.thornode/address_book.seed
docker exec docker-thornode-fox-1 rm -f /root/.thornode/address_book.seed
docker exec docker-thornode-goat-1 rm -f /root/.thornode/address_book.seed
docker exec docker-thornode-whale-1 rm -f /root/.thornode/address_book.seed
docker exec docker-thornode-wolf-1 rm -f /root/.thornode/address_book.seed
docker exec docker-thornode-eagle-1 rm -f /root/.thornode/address_book.seed
```

Then restart bifrosts in order:

```bash
docker compose --env-file .env -f build/docker/docker-compose.xmr-chainnet.yml restart bifrost
docker compose --env-file .env -f build/docker/docker-compose.xmr-chainnet.yml restart \
  bifrost-cat bifrost-fox bifrost-goat bifrost-whale bifrost-wolf bifrost-eagle
```

### Fresh outbound missing from `transfers?height=0`

Newly signed XMR outbounds appear first in `GET /v1/monero/transfers?mempool=true`.
`GET /v1/monero/transfers?height=0` only returns `observed_mined` rows, so a valid
fresh outbound can be absent there until it lands on chain and is observed mined.

### `xmr-reset-scanner.sh` fails with `go: command not found`

This now usually means neither of these recovery paths was available:

- `/usr/local/bin/xmr-reset-scanner` was not installed by post-deploy
- no local `thornode:xmr-chainnet` image was available for wrapper extraction

Manual recovery:

```bash
bash build/docker/xmr-install-host-tools.sh --image thornode:xmr-chainnet xmr-reset-scanner
```

### Go/Rust auth mismatch

If bifrost and the signer sidecars are built from different commits, bearer auth can mismatch and XMR scanning can silently fail. Rebuild both images from the same commit.

## Keyshare Backup

After a successful fresh bootstrap, export keyshares:

```bash
mkdir -p ~/frost-keyshares/xmr-chainnet/$(date +%Y%m%d)
ssh root@91.99.89.216 'docker cp docker-xmr-frost-signer-1:/var/lib/xmr-frost - | gzip' > ~/frost-keyshares/xmr-chainnet/$(date +%Y%m%d)/dog.tar.gz
ssh root@91.99.89.216 'docker cp docker-xmr-frost-signer-cat-1:/var/lib/xmr-frost - | gzip' > ~/frost-keyshares/xmr-chainnet/$(date +%Y%m%d)/cat.tar.gz
ssh root@91.99.89.216 'docker cp docker-xmr-frost-signer-fox-1:/var/lib/xmr-frost - | gzip' > ~/frost-keyshares/xmr-chainnet/$(date +%Y%m%d)/fox.tar.gz
ssh root@91.99.89.216 'docker cp docker-xmr-frost-signer-goat-1:/var/lib/xmr-frost - | gzip' > ~/frost-keyshares/xmr-chainnet/$(date +%Y%m%d)/goat.tar.gz
ssh root@91.99.89.216 'docker cp docker-xmr-frost-signer-whale-1:/var/lib/xmr-frost - | gzip' > ~/frost-keyshares/xmr-chainnet/$(date +%Y%m%d)/whale.tar.gz
ssh root@91.99.89.216 'docker cp docker-xmr-frost-signer-wolf-1:/var/lib/xmr-frost - | gzip' > ~/frost-keyshares/xmr-chainnet/$(date +%Y%m%d)/wolf.tar.gz
ssh root@91.99.89.216 'docker cp docker-xmr-frost-signer-eagle-1:/var/lib/xmr-frost - | gzip' > ~/frost-keyshares/xmr-chainnet/$(date +%Y%m%d)/eagle.tar.gz
```
