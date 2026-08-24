#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
COMPOSE_DIR="$REPO_ROOT/build/docker"
BEARER_SENTINEL="xmr-compose-contract-test-bearer"
SEED_TARGET="/run/thornode/seed_phrase"
EXPECTED_SIGNER_IMAGE="registry.gitlab.com/thorchain/devops/serai/xmr-frost-signer:mr70-6844c817-20260824-post15@sha256:ba4d243d6f129dda8ca127f1f95e71344b97c6dca02efb3ca9e453ecf1572978"
# shellcheck disable=SC2016
REQUIRED_BEARER_EXPANSION='${XMR_FROST_AUTH_BEARER:?set XMR_FROST_AUTH_BEARER to a strong random token}'

fail() {
  echo "XMR Compose contract failed: $*" >&2
  exit 1
}

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || fail "required command not found: $1"
}

assert_env() {
  local rendered="$1"
  local service="$2"
  local key="$3"
  local expected="$4"
  local actual

  actual="$(jq -er --arg service "$service" --arg key "$key" \
    '.services[$service].environment[$key]' "$rendered")" ||
    fail "$service is missing $key"
  [ "$actual" = "$expected" ] ||
    fail "$service $key is $actual, expected $expected"
}

assert_env_absent() {
  local rendered="$1"
  local service="$2"
  local key="$3"

  jq -e --arg service "$service" --arg key "$key" \
    '(.services[$service].environment // {}) | has($key) | not' \
    "$rendered" >/dev/null || fail "$service must not set obsolete or disallowed $key"
}

assert_command_has() {
  local rendered="$1"
  local service="$2"
  local argument="$3"

  jq -e --arg service "$service" --arg argument "$argument" \
    '.services[$service].command | index($argument) != null' \
    "$rendered" >/dev/null || fail "$service command must contain $argument"
}

assert_command_lacks_prefix() {
  local rendered="$1"
  local service="$2"
  local prefix="$3"

  jq -e --arg service "$service" --arg prefix "$prefix" \
    '[.services[$service].command[] | startswith($prefix)] | any | not' \
    "$rendered" >/dev/null || fail "$service command must not contain an argument starting with $prefix"
}

assert_source_bearer_contract() {
  local compose_file="$1"
  local count

  for assignment in \
    "BIFROST_CHAINS_XMR_SIGNER_AUTHORIZATION_BEARER: \"$REQUIRED_BEARER_EXPANSION\"" \
    "XMR_FROST_AUTH_BEARER: \"$REQUIRED_BEARER_EXPANSION\""; do
    grep -Fq "$assignment" "$compose_file" ||
      fail "$compose_file must contain $assignment"
  done
  count="$(grep -Fo "$REQUIRED_BEARER_EXPANSION" "$compose_file" | wc -l | tr -d ' ')"
  [ "$count" = 2 ] || fail "$compose_file must require the shared bearer exactly twice"
  # This is a literal Compose interpolation prefix.
  # shellcheck disable=SC2016
  if grep -Fq '${XMR_FROST_AUTH_BEARER:-' "$compose_file"; then
    fail "$compose_file must not provide a fallback bearer"
  fi
  if grep -Fq BIFROST_CHAINS_XMR_AUTHORIZATION_BEARER "$compose_file"; then
    fail "$compose_file contains the obsolete BIFROST_CHAINS_XMR_AUTHORIZATION_BEARER"
  fi
}

assert_default_network() {
  local rendered="$1"
  local service="$2"

  jq -e --arg service "$service" \
    '(.services[$service].networks | keys) == ["default"]' \
    "$rendered" >/dev/null || fail "$service must use only the private default Compose network"
}

assert_healthy_dependency() {
  local rendered="$1"
  local service="$2"
  local dependency="$3"

  jq -e --arg service "$service" --arg dependency "$dependency" \
    '.services[$service].depends_on[$dependency].condition == "service_healthy"' \
    "$rendered" >/dev/null || fail "$service must wait for healthy $dependency"
}

assert_seed_config() {
  local rendered="$1"
  local service="$2"
  local source="$3"

  assert_env "$rendered" "$service" SIGNER_SEED_PHRASE_FILE "$SEED_TARGET"
  assert_env_absent "$rendered" "$service" SIGNER_SEED_PHRASE
  jq -e --arg service "$service" --arg source "$source" --arg target "$SEED_TARGET" \
    '.services[$service].configs as $configs |
      ($configs | length) == 1 and $configs[0].source == $source and $configs[0].target == $target' \
    "$rendered" >/dev/null || fail "$service must mount $source at $SEED_TARGET"
}

assert_signer_image() {
  local rendered="$1"
  local service="$2"
  local image

  image="$(jq -er --arg service "$service" '.services[$service].image' "$rendered")" ||
    fail "$service has no image"
  [[ $image =~ ^registry\.gitlab\.com/thorchain/devops/serai/xmr-frost-signer:[^@[:space:]]+@sha256:[0-9a-f]{64}$ ]] ||
    fail "$service image must be an immutable registry tag@sha256 reference"
  [[ $image != *":latest@"* ]] || fail "$service image must not use a latest tag"
  [ "$image" = "$EXPECTED_SIGNER_IMAGE" ] ||
    fail "$service image is $image, expected $EXPECTED_SIGNER_IMAGE"
}

assert_loopback_publication() {
  local rendered="$1"
  local service="$2"
  local published="$3"

  if [ "$published" = true ]; then
    jq -e --arg service "$service" \
      '.services[$service].ports as $ports |
        ($ports | length) == 1 and $ports[0].host_ip == "127.0.0.1" and
        $ports[0].target == 8283 and $ports[0].published == "8283" and
        $ports[0].protocol == "tcp" and $ports[0].mode == "ingress"' \
      "$rendered" >/dev/null || fail "$service must publish only 127.0.0.1:8283:8283"
  else
    jq -e --arg service "$service" \
      '(.services[$service].ports // [] | length) == 0' \
      "$rendered" >/dev/null || fail "$service must not publish signer ports"
  fi
}

assert_single_target_loopback_publication() {
  local rendered="$1"
  local service="$2"
  local target="$3"

  jq -e --arg service "$service" --argjson target "$target" \
    '.services[$service].ports as $ports |
      [$ports[] | select(.target == $target)] as $matching |
      ($matching | length) == 1 and $matching[0].host_ip == "127.0.0.1"' \
    "$rendered" >/dev/null || fail "$service must publish target port $target only on 127.0.0.1"
}

assert_delayed_bearer_healthcheck() {
  local rendered="$1"
  local service="$2"
  local healthcheck
  local normalized
  # The rendered healthcheck must retain this literal for container-time expansion.
  # shellcheck disable=SC2016
  local bearer_reference='${XMR_FROST_AUTH_BEARER}'

  healthcheck="$(jq -er --arg service "$service" \
    '.services[$service].healthcheck.test | join(" ")' "$rendered")" ||
    fail "$service has no healthcheck"
  normalized="${healthcheck//\$\$/\$}"
  [[ $normalized == *"Authorization: Bearer $bearer_reference"* ]] ||
    fail "$service healthcheck must use its container-time bearer in the Authorization header"
  [[ $normalized == *"http://127.0.0.1:8283/v1/monero/chain/height"* ]] ||
    fail "$service healthcheck must use the signer loopback endpoint"
  [[ $healthcheck != *"$BEARER_SENTINEL"* ]] ||
    fail "$service healthcheck bearer was expanded while rendering Compose"
}

assert_required_bearer() {
  local compose_file="$1"
  local output

  assert_source_bearer_contract "$compose_file"

  if output="$(env -u XMR_FROST_AUTH_BEARER -u XMR_FROST_SIGNER_IMAGE \
    docker compose --env-file /dev/null -f "$compose_file" config 2>&1)"; then
    fail "$compose_file rendered without XMR_FROST_AUTH_BEARER"
  fi
  [[ $output == *"set XMR_FROST_AUTH_BEARER to a strong random token"* ]] ||
    fail "$compose_file did not report the required bearer error when it was unset"

  if output="$(env -u XMR_FROST_SIGNER_IMAGE XMR_FROST_AUTH_BEARER= \
    docker compose --env-file /dev/null -f "$compose_file" config 2>&1)"; then
    fail "$compose_file rendered with an empty XMR_FROST_AUTH_BEARER"
  fi
  [[ $output == *"set XMR_FROST_AUTH_BEARER to a strong random token"* ]] ||
    fail "$compose_file did not report the required bearer error when it was empty"
}

render_compose() {
  local compose_file="$1"
  local rendered="$2"

  env -u XMR_FROST_SIGNER_IMAGE XMR_FROST_AUTH_BEARER="$BEARER_SENTINEL" \
    docker compose --env-file /dev/null -f "$compose_file" config --format json >"$rendered"
}

render_mocknet_compose() {
  local compose_file="$1"
  local rendered="$2"

  env -u XMR_FROST_SIGNER_IMAGE \
    XMR_FROST_AUTH_BEARER="$BEARER_SENTINEL" \
    XMR_SIM_HELPER_AUTH_BEARER="$BEARER_SENTINEL" \
    docker compose --env-file /dev/null --profile mocknet-cluster \
    -f "$compose_file" config --format json >"$rendered"
}

validate_chainnet_seed_files() {
  local rendered="$1"
  local labels=(dog cat fox)
  local suffixes=("" -cat -fox)
  local index label suffix source config_file service

  jq -e '(.configs | keys) == ["seed-cat", "seed-dog", "seed-fox"]' \
    "$rendered" >/dev/null || fail "chainnet must define exactly the dog, cat, and fox seed configs"

  for index in "${!labels[@]}"; do
    label="${labels[$index]}"
    suffix="${suffixes[$index]}"
    source="seed-$label"
    for service in "thornode$suffix" "bifrost$suffix" "xmr-frost-signer$suffix"; do
      assert_seed_config "$rendered" "$service" "$source"
    done
    config_file="$(jq -er --arg source "$source" '.configs[$source].file' "$rendered")" ||
      fail "$source has no backing file"
    [ -f "$config_file" ] || fail "$source backing file does not exist: $config_file"
    [ "$config_file" = "$COMPOSE_DIR/chainnet-seeds/$label.txt" ] ||
      fail "$source points to the wrong fixture: $config_file"
  done
}

validate_mocknet_seed_consistency() {
  local rendered="$1"
  local suffixes=("" -cat -fox)
  local node_seeds=()
  local suffix service seed expected

  jq -e '(.configs // {} | length) == 0' "$rendered" >/dev/null ||
    fail "the private mocknet fixture must not declare seed-file configs"

  for suffix in "${suffixes[@]}"; do
    expected=""
    for service in "thornode$suffix" "bifrost$suffix" "xmr-frost-signer$suffix"; do
      seed="$(jq -er --arg service "$service" \
        '.services[$service].environment.SIGNER_SEED_PHRASE' "$rendered")" ||
        fail "$service is missing its mocknet seed fixture"
      assert_env_absent "$rendered" "$service" SIGNER_SEED_PHRASE_FILE
      [ -n "$seed" ] || fail "$service has an empty mocknet seed fixture"
      if [ -z "$expected" ]; then
        expected="$seed"
      elif [ "$seed" != "$expected" ]; then
        fail "mocknet seed fixture differs across node$suffix services"
      fi
    done
    node_seeds+=("$expected")
  done

  if [ "${node_seeds[0]}" = "${node_seeds[1]}" ] ||
    [ "${node_seeds[0]}" = "${node_seeds[2]}" ] ||
    [ "${node_seeds[1]}" = "${node_seeds[2]}" ]; then
    fail "dog, cat, and fox must use distinct mocknet seed fixtures"
  fi
}

validate_rendered_contract() {
  local rendered="$1"
  local node_network="$2"
  local signer_network="$3"
  local monero_network="$4"
  local seed_mode="$5"
  local signer_publication_mode="$6"
  local suffixes=("" -cat -fox)
  local suffix bifrost signer endpoint

  [ "$signer_network" != mainnet ] ||
    fail "plaintext signer topology must never use a mainnet signer identity"

  jq -e \
    '[.services[] | (.environment // {}) | has("BIFROST_CHAINS_XMR_AUTHORIZATION_BEARER")] | any | not' \
    "$rendered" >/dev/null || fail "obsolete BIFROST_CHAINS_XMR_AUTHORIZATION_BEARER is present"
  jq -e '(.networks.default.external // false) == false' "$rendered" >/dev/null ||
    fail "the default signer network must not be external"
  assert_default_network "$rendered" monero-daemon

  for suffix in "${suffixes[@]}"; do
    bifrost="bifrost$suffix"
    signer="xmr-frost-signer$suffix"
    endpoint="http://$signer:8283"

    if [ "$signer_publication_mode" != all-loopback ]; then
      assert_env "$rendered" "thornode$suffix" NET "$node_network"
      assert_env "$rendered" "$bifrost" NET "$node_network"
    fi
    assert_env "$rendered" "$bifrost" BIFROST_CHAINS_XMR_CHAIN_NETWORK "$monero_network"
    assert_env "$rendered" "$bifrost" BIFROST_CHAINS_XMR_RPC_HOST http://monero-daemon:18081
    assert_env "$rendered" "$bifrost" BIFROST_CHAINS_XMR_SIGNER_ENDPOINT "$endpoint"
    assert_env "$rendered" "$bifrost" BIFROST_CHAINS_XMR_SIGNER_EXPECTED_NETWORK "$signer_network"
    assert_env "$rendered" "$bifrost" BIFROST_CHAINS_XMR_SIGNER_ALLOW_INSECURE_HTTP true
    assert_env "$rendered" "$bifrost" BIFROST_CHAINS_XMR_SIGNER_AUTHORIZATION_BEARER "$BEARER_SENTINEL"
    assert_env_absent "$rendered" "$bifrost" XMR_FROST_AUTH_BEARER
    assert_env_absent "$rendered" "$bifrost" BIFROST_CHAINS_XMR_SIGNER_USERNAME
    assert_env_absent "$rendered" "$bifrost" BIFROST_CHAINS_XMR_SIGNER_PASSWORD

    assert_env "$rendered" "$signer" XMR_FROST_CHAIN_NETWORK "$signer_network"
    assert_env "$rendered" "$signer" XMR_FROST_DAEMON_URL http://monero-daemon:18081
    assert_env "$rendered" "$signer" XMR_FROST_LISTEN_ADDR 0.0.0.0:8283
    assert_env "$rendered" "$signer" XMR_FROST_AUTH_BEARER "$BEARER_SENTINEL"
    assert_env_absent "$rendered" "$signer" XMR_FROST_AUTH_USERNAME
    assert_env_absent "$rendered" "$signer" XMR_FROST_AUTH_PASSWORD

    assert_default_network "$rendered" "thornode$suffix"
    assert_default_network "$rendered" "$bifrost"
    assert_default_network "$rendered" "$signer"
    if [ "$signer_publication_mode" != all-loopback ]; then
      assert_healthy_dependency "$rendered" "$bifrost" "$signer"
    fi
    if [ "$signer_publication_mode" = all-loopback ]; then
      assert_single_target_loopback_publication "$rendered" "$signer" 8283
    elif [ -z "$suffix" ]; then
      assert_loopback_publication "$rendered" "$signer" true
    else
      assert_loopback_publication "$rendered" "$signer" false
    fi
    assert_delayed_bearer_healthcheck "$rendered" "$signer"
    assert_signer_image "$rendered" "$signer"
  done

  case "$monero_network" in
  mainnet)
    assert_command_lacks_prefix "$rendered" monero-daemon --stagenet
    ;;
  stagenet)
    assert_command_has "$rendered" monero-daemon --stagenet
    assert_command_lacks_prefix "$rendered" monero-daemon --bootstrap-daemon-address=
    ;;
  mocknet) ;;
  *)
    fail "unsupported Monero network in production-like Compose contract: $monero_network"
    ;;
  esac

  case "$seed_mode" in
  file)
    validate_chainnet_seed_files "$rendered"
    ;;
  mocknet-env)
    validate_mocknet_seed_consistency "$rendered"
    ;;
  existing-mocknet) ;;
  *)
    fail "unknown seed mode: $seed_mode"
    ;;
  esac
}

require_cmd docker
require_cmd grep
require_cmd jq
# Rendering does not need a daemon. Ignore the repository's CI-wide dind endpoint,
# which has no service or client certificates in this short contract job.
unset DOCKER_HOST DOCKER_TLS_CERTDIR DOCKER_TLS_VERIFY DOCKER_CERT_PATH
docker compose version >/dev/null 2>&1 || fail "Docker Compose v2 is required"

tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT

chainnet_compose="$COMPOSE_DIR/docker-compose.xmr-chainnet.yml"
stagenet_compose="$COMPOSE_DIR/docker-compose.xmr-stagenet.yml"
mocknet_compose="$COMPOSE_DIR/docker-compose.yml"

assert_required_bearer "$chainnet_compose"
assert_required_bearer "$stagenet_compose"
assert_source_bearer_contract "$mocknet_compose"
# These literal checks ensure the mocknet helper also has no committed secret.
# shellcheck disable=SC2016
grep -Fq '${XMR_SIM_HELPER_AUTH_BEARER:?set XMR_SIM_HELPER_AUTH_BEARER to a random mocknet token}' \
  "$mocknet_compose" || fail "$mocknet_compose must require XMR_SIM_HELPER_AUTH_BEARER"
# shellcheck disable=SC2016
if grep -Eq '\$\{XMR_(FROST|SIM_HELPER)_AUTH_BEARER:-' "$mocknet_compose"; then
  fail "$mocknet_compose must not provide a fallback XMR bearer"
fi

render_compose "$chainnet_compose" "$tmp_dir/chainnet.json"
render_compose "$stagenet_compose" "$tmp_dir/stagenet.json"
render_mocknet_compose "$mocknet_compose" "$tmp_dir/mocknet.json"

validate_rendered_contract "$tmp_dir/chainnet.json" chainnet chainnet mainnet file primary-only
validate_rendered_contract "$tmp_dir/stagenet.json" mocknet mocknet stagenet mocknet-env primary-only
validate_rendered_contract "$tmp_dir/mocknet.json" mocknet mocknet mocknet existing-mocknet all-loopback

for rendered in "$tmp_dir/chainnet.json" "$tmp_dir/stagenet.json" "$tmp_dir/mocknet.json"; do
  assert_single_target_loopback_publication "$rendered" monero-daemon 18081
  assert_single_target_loopback_publication "$rendered" thornode 50051
done
assert_single_target_loopback_publication "$tmp_dir/mocknet.json" xmr-frost-signer-pig 8283
assert_single_target_loopback_publication "$tmp_dir/mocknet.json" xmr-sim-helper 8284

echo "XMR Compose security contract passed"
