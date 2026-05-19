#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
NODE_COUNT="${1:-${DKLS_NODE_COUNT:-7}}"
ACTIVE_COUNT=$((NODE_COUNT - 1))
NEW_NODES_PER_CHURN=$((ACTIVE_COUNT - 1))
OUT_DIR="${DKLS_CHAINNET_GENERATED_DIR:-$SCRIPT_DIR/dkls-chainnet-generated}"
NODE_META="$OUT_DIR/nodes-${NODE_COUNT}.tsv"
COMPOSE_OUT="${DKLS_CHAINNET_COMPOSE_OUT:-$SCRIPT_DIR/docker-compose.dkls-chainnet-timing.yml}"
IMAGE="${DKLS_THORNODE_IMAGE:-thornode:dkls-chainnet}"
PASSWORD="${PASSWORD:-password}"

case "$NODE_COUNT" in
7 | 14 | 21 | 28) ;;
*)
  echo "ERROR: node count must be one of 7,14,21,28"
  exit 1
  ;;
esac

mkdir -p "$OUT_DIR"

if [ "${DKLS_FORCE_BUILD:-0}" = "1" ] || ! docker image inspect "$IMAGE" >/dev/null 2>&1; then
  docker build -t "$IMAGE" --build-arg TAG=chainnet -f "$SCRIPT_DIR/Dockerfile" "$REPO_ROOT"
fi

address_for_seed() {
  local seed="$1"
  local tmp
  tmp=$(mktemp -d)
  printf "%s\n%s\n%s\n" "$seed" "$PASSWORD" "$PASSWORD" | docker run --rm -i \
    -e NET=chainnet \
    -v "$tmp:/root/.thornode" \
    --entrypoint thornode \
    "$IMAGE" keys --keyring-backend file add thorchain --recover >/dev/null
  docker run --rm -i \
    -e NET=chainnet \
    -v "$tmp:/root/.thornode" \
    --entrypoint sh \
    "$IMAGE" -lc "printf '%s\n' '$PASSWORD' | thornode keys show thorchain -a --keyring-backend file"
  rm -rf "$tmp"
}

if [ ! -f "$NODE_META" ]; then
  {
    printf "index\tname\tseed\taddress\n"
    printf "1\tdog\tdog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog fossil\tcthor1zf3gsk7edzwl9syyefvfhle37cjtql35layy8z\n"
  } >"$NODE_META"

  for i in $(seq 2 "$NODE_COUNT"); do
    name=$(printf "node%02d" "$i")
    seed=$(docker run --rm --entrypoint thornode "$IMAGE" keys mnemonic | tail -1)
    address=$(address_for_seed "$seed")
    printf "%s\t%s\t%s\t%s\n" "$i" "$name" "$seed" "$address" >>"$NODE_META"
  done
fi

python3 - "$NODE_META" "$COMPOSE_OUT" "$ACTIVE_COUNT" "$NEW_NODES_PER_CHURN" <<'PY'
import sys
from pathlib import Path

meta_path, out_path, active_count, new_nodes = sys.argv[1:]
active_count = int(active_count)
new_nodes = int(new_nodes)

rows = []
for line in Path(meta_path).read_text().splitlines()[1:]:
    index, name, seed, address = line.split("\t")
    rows.append({"index": int(index), "name": name, "seed": seed, "address": address})

def service_suffix(row):
    return "" if row["name"] == "dog" else f"-{row['name']}"

def thornode_service(row):
    return "thornode" if row["name"] == "dog" else f"thornode-{row['name']}"

def bifrost_service(row):
    return "bifrost" if row["name"] == "dog" else f"bifrost-{row['name']}"

def volume_name(prefix, row):
    return prefix if row["name"] == "dog" else f"{prefix}-{row['name']}"

def yaml_scalar(value):
    return '"' + str(value).replace("\\", "\\\\").replace('"', '\\"') + '"'

disabled = {
    "BIFROST_CHAINS_BTC_DISABLED": "true",
    "BIFROST_CHAINS_BCH_DISABLED": "true",
    "BIFROST_CHAINS_LTC_DISABLED": "true",
    "BIFROST_CHAINS_DOGE_DISABLED": "false",
    "BIFROST_CHAINS_ETH_DISABLED": "true",
    "BIFROST_CHAINS_AVAX_DISABLED": "true",
    "BIFROST_CHAINS_BSC_DISABLED": "true",
    "BIFROST_CHAINS_BASE_DISABLED": "true",
    "BIFROST_CHAINS_GAIA_DISABLED": "true",
    "BIFROST_CHAINS_NOBLE_DISABLED": "true",
    "BIFROST_CHAINS_SOL_DISABLED": "true",
    "BIFROST_CHAINS_XRP_DISABLED": "true",
    "BIFROST_CHAINS_TRON_DISABLED": "true",
    "BIFROST_CHAINS_POL_DISABLED": "true",
    "BIFROST_CHAINS_ZEC_DISABLED": "true",
    "BIFROST_CHAINS_SUI_DISABLED": "true",
    "BIFROST_CHAINS_ADA_DISABLED": "true",
}

thor_env = {
    "NET": "chainnet",
    "CHAIN_ID": "thorchain",
    "SIGNER_NAME": "thorchain",
    "SIGNER_PASSWD": "password",
    "THORNODE_GRPC_ENABLE": "true",
    "THOR_COSMOS_EBIFROST_ADDRESS": "0.0.0.0:50051",
    "THOR_BLOCK_TIME": "6s",
    "PORT_RPC": "27147",
    "PORT_API": "1317",
    "THOR_SEED_NODES_ENDPOINT": "",
    "THOR_CHAINNET_DESIRED_VALIDATOR_SET": active_count,
    "THOR_CHAINNET_ASGARD_SIZE": active_count,
    "THOR_CHAINNET_NUMBER_OF_NEW_NODES_PER_CHURN": new_nodes,
    "THOR_CHAINNET_CHURN_RETRY_INTERVAL": 10,
    "THOR_CHAINNET_KEYGEN_RETRY_INTERVAL": 10,
    "THOR_CHAINNET_JAIL_TIME_KEYGEN": 11,
    "THOR_CHAINNET_JAIL_TIME_KEYSIGN": 11,
    "EXTRA_FUND_ACCOUNT_COUNT": len(rows) - 1,
    "FAUCET": rows[0]["address"],
    **{f"EXTRA_FUND_ACCOUNT_{i}": row["address"] for i, row in enumerate(rows[1:], start=1)},
    **disabled,
}

bifrost_env = {
    "NET": "chainnet",
    "CHAIN_ID": "thorchain",
    "SIGNER_NAME": "thorchain",
    "SIGNER_PASSWD": "password",
    "THOR_SEED_NODES_ENDPOINT": "",
    "THOR_BLOCK_TIME": "6s",
    "BLOCK_SCANNER_BACKOFF": "5s",
    "BIFROST_METRICS_PPROF_ENABLED": "true",
    "BIFROST_SIGNER_BACKUP_KEYSHARES": "true",
    "BIFROST_SIGNER_RESCHEDULE_BUFFER_BLOCKS": "20",
    "BIFROST_SIGNER_AUTO_OBSERVE": "false",
    "BIFROST_SIGNER_KEYGEN_TIMEOUT": "60s",
    "BIFROST_SIGNER_KEYSIGN_TIMEOUT": "60s",
    "BIFROST_SIGNER_PARTY_TIMEOUT": "55s",
    "LD_LIBRARY_PATH": "/usr/lib:/usr/local/lib",
    "THOR_CHAINNET_CHURN_RETRY_INTERVAL": 10,
    "THOR_CHAINNET_KEYGEN_RETRY_INTERVAL": 10,
    "THOR_CHAINNET_JAIL_TIME_KEYGEN": 11,
    "THOR_CHAINNET_JAIL_TIME_KEYSIGN": 11,
    "DOGE_HOST": "${DOGE_HOST:-dogecoin:18332}",
    "BIFROST_CHAINS_DOGE_CHAIN_NETWORK": "mocknet",
    "BIFROST_CHAINS_DOGE_BLOCK_SCANNER_START_BLOCK_HEIGHT": "0",
    **disabled,
}

def emit_env(lines, env, indent):
    pad = " " * indent
    lines.append(f"{pad}environment:")
    for key, value in env.items():
        lines.append(f"{pad}  {key}: {yaml_scalar(value)}")

lines = [
    "########################################################################################",
    f"# Generated DKLS timing chainnet: {len(rows)} nodes, {active_count} active + 1 standby",
    "# Regenerate with: bash dkls-chainnet-generate-compose.sh <7|14|21|28>",
    "########################################################################################",
    "",
    "volumes:",
]
for row in rows:
    lines.append(f"  {volume_name('thornode', row)}:")
for row in rows:
    lines.append(f"  {volume_name('bifrost', row)}:")

lines += [
    "",
    "x-logging: &logging",
    '  driver: "json-file"',
    "  options:",
    '    max-size: "512m"',
    '    max-file: "4"',
    "",
    "services:",
    "  dogecoin:",
    "    profiles:",
    "      - local-dogecoin",
    "    user: root",
    "    image: registry.gitlab.com/thorchain/devops/node-launcher:dogecoin-daemon-1.14.9",
    "    restart: unless-stopped",
    "    environment:",
    '      BLOCK_TIME: "1"',
    '      DOGE_MASTER_ADDR: "mnaioCtEGdw6bd6rWJ13Mbre1kN5rPa2Mo"',
    "    ports:",
    '      - "18332:18332"',
    "    logging: *logging",
    "",
]

for row in rows:
    svc = thornode_service(row)
    vol = volume_name("thornode", row)
    lines.append(f"  {svc}:")
    if row["name"] == "dog":
        lines += [
            "    build:",
            "      context: ../../",
            "      dockerfile: ./build/docker/Dockerfile",
            "      args:",
            "        TAG: chainnet",
        ]
    else:
        lines += [
            "    image: thornode:dkls-chainnet",
            "    depends_on:",
            "      thornode:",
            "        condition: service_healthy",
        ]
    if row["name"] == "dog":
        lines.append("    image: thornode:dkls-chainnet")
    lines += [
        "    restart: unless-stopped",
        f"    hostname: {svc}",
    ]
    env = dict(thor_env)
    env["SEED"] = svc
    env["SIGNER_SEED_PHRASE"] = row["seed"]
    if row["name"] != "dog":
        env["PEER"] = "thornode"
        env["AUTO_BOND"] = "true"
        seed_services = ["thornode"] + [thornode_service(r) for r in rows[1:row["index"]-1]][-3:]
        env["SEEDS"] = ",".join(seed_services)
    emit_env(lines, env, 4)
    if row["name"] == "dog":
        lines += [
            "    ports:",
            '      - "27147:27147"',
            '      - "1317:1317"',
            '      - "50051:50051"',
            '      - "9090:9090"',
        ]
    lines += [
        "    volumes:",
        '      - "../scripts:/docker/scripts"',
        f"      - {vol}:/root/.thornode",
    ]
    if row["name"] == "dog":
        lines += [
            "    networks:",
            "      default:",
            "        aliases:",
            "          - thornode-dog",
            "    entrypoint: /docker/scripts/genesis.sh",
        ]
    else:
        lines.append("    entrypoint: /docker/scripts/validator.sh")
    lines += [
        '    command: ["thornode", "start"]',
        "    healthcheck:",
        '      test: ["CMD", "curl", "-f", "http://localhost:27147/health"]',
        "      interval: 10s",
        "      timeout: 10s",
        "      retries: 10",
        "      start_period: 60s",
        "    logging: *logging",
        "",
    ]

for row in rows:
    svc = bifrost_service(row)
    thor_svc = thornode_service(row)
    vol_b = volume_name("bifrost", row)
    vol_t = volume_name("thornode", row)
    lines += [
        f"  {svc}:",
        "    image: thornode:dkls-chainnet",
        "    restart: unless-stopped",
        "    depends_on:",
        f"      {thor_svc}:",
        "        condition: service_healthy",
    ]
    env = dict(bifrost_env)
    env["SIGNER_SEED_PHRASE"] = row["seed"]
    env["CHAIN_API"] = f"{thor_svc}:1317"
    env["CHAIN_RPC"] = f"{thor_svc}:27147"
    env["BIFROST_THORCHAIN_CHAIN_EBIFROST"] = f"{thor_svc}:50051"
    if row["name"] != "dog":
        env["PEER"] = "bifrost"
    emit_env(lines, env, 4)
    lines += [
        "    volumes:",
        '      - "../scripts:/docker/scripts"',
        '      - "../../dkls/go-wrappers/includes/linux/${DKLS_LIB_ARCH:-arm64}/libgodkls.so:/usr/lib/libgodkls.so:ro"',
        f"      - {vol_b}:/var/data/bifrost",
        f"      - {vol_t}:/root/.thornode",
    ]
    if row["name"] == "dog":
        lines += [
            "    networks:",
            "      default:",
            "        aliases:",
            "          - bifrost-dog",
        ]
    lines += [
        "    entrypoint: /docker/scripts/bifrost.sh",
        '    command: ["bifrost", "-p"]',
    ]
    lines += [
        "    logging: *logging",
        "",
    ]

Path(out_path).write_text("\n".join(lines) + "\n")
PY

echo "Generated node metadata: $NODE_META"
echo "Generated compose:       $COMPOSE_OUT"
echo "Node count:              $NODE_COUNT"
echo "Active target:           $ACTIVE_COUNT"
echo "New nodes per churn:     $NEW_NODES_PER_CHURN"
