#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: build/docker/xmr-install-host-tools.sh --image <image> [--bin-dir <dir>] [tool...]

Examples:
  build/docker/xmr-install-host-tools.sh --image thornode:xmr-chainnet
  build/docker/xmr-install-host-tools.sh --image thornode:xmr-chainnet --bin-dir /usr/local/bin xmr-reset-scanner
  build/docker/xmr-install-host-tools.sh --image thornode:xmr-stagenet --bin-dir build/docker/.bin xmr-reset-scanner
EOF
}

command -v docker >/dev/null 2>&1 || {
  echo "docker is required" >&2
  exit 1
}

image=""
bin_dir="${HOST_TOOL_BIN_DIR:-/usr/local/bin}"
tools=()
while (($# > 0)); do
  case "$1" in
    --image)
      [[ $# -ge 2 ]] || { echo "--image requires a value" >&2; exit 1; }
      image="$2"
      shift 2
      ;;
    --bin-dir)
      [[ $# -ge 2 ]] || { echo "--bin-dir requires a value" >&2; exit 1; }
      bin_dir="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      tools+=("$1")
      shift
      ;;
  esac
done

[[ -n "$image" ]] || {
  echo "--image is required" >&2
  usage >&2
  exit 1
}

if ((${#tools[@]} == 0)); then
  tools=("xmr-reset-scanner")
fi

docker image inspect "$image" >/dev/null 2>&1 || {
  echo "docker image not found: $image" >&2
  exit 1
}

mkdir -p "$bin_dir"

container_id="$(docker create "$image")"
cleanup() {
  docker rm -f "$container_id" >/dev/null 2>&1 || true
}
trap cleanup EXIT

for tool in "${tools[@]}"; do
  src="/usr/bin/$tool"
  tmp_target="$bin_dir/.${tool}.tmp.$$"
  docker cp "${container_id}:${src}" "$tmp_target"
  chmod +x "$tmp_target"
  mv "$tmp_target" "$bin_dir/$tool"
  echo "installed $tool -> $bin_dir/$tool"
done
