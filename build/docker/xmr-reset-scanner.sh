#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

if command -v xmr-reset-scanner >/dev/null 2>&1; then
  exec xmr-reset-scanner "$@"
fi

extract_from_local_image() {
  command -v docker >/dev/null 2>&1 || return 1

  local chain_network=""
  local expect_value=false
  local arg
  for arg in "$@"; do
    if $expect_value; then
      chain_network="$arg"
      expect_value=false
      continue
    fi
    case "$arg" in
      --chain-network)
        expect_value=true
        ;;
      --chain-network=*)
        chain_network="${arg#--chain-network=}"
        ;;
    esac
  done

  local -a images=()
  if [[ -n "$chain_network" ]]; then
    images+=("thornode:xmr-${chain_network}")
  fi
  images+=("thornode:xmr-chainnet" "thornode:xmr-stagenet")

  local image
  for image in "${images[@]}"; do
    if docker image inspect "$image" >/dev/null 2>&1; then
      local bin_dir="$SCRIPT_DIR/.bin"
      "$SCRIPT_DIR/xmr-install-host-tools.sh" --image "$image" --bin-dir "$bin_dir" xmr-reset-scanner >/dev/null
      exec "$bin_dir/xmr-reset-scanner" "$@"
    fi
  done

  return 1
}

extract_from_local_image "$@" || true

exec go run "$REPO_ROOT/tools/xmr-reset-scanner" "$@"
