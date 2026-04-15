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

3. Recreate thornodes and bifrosts.

```bash
docker compose --env-file .env -f build/docker/docker-compose.xmr-chainnet.yml up -d --force-recreate \
  thornode thornode-cat thornode-fox thornode-goat thornode-whale thornode-wolf thornode-eagle \
  bifrost bifrost-cat bifrost-fox bifrost-goat bifrost-whale bifrost-wolf bifrost-eagle
```

4. If Rust signer code changed, rebuild and recreate the signer sidecars too.

```bash
docker compose --env-file .env -f build/docker/docker-compose.xmr-chainnet.yml build xmr-frost-signer
docker compose --env-file .env -f build/docker/docker-compose.xmr-chainnet.yml up -d --force-recreate \
  xmr-frost-signer xmr-frost-signer-cat xmr-frost-signer-fox xmr-frost-signer-goat \
  xmr-frost-signer-whale xmr-frost-signer-wolf xmr-frost-signer-eagle
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
