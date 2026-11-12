#!/usr/bin/env bash
set -euo pipefail

die() {
  echo "ERR: $*"
  exit 1
}

# check docs version
version=$(cat version)
if ! grep "^  version: ${version}" openapi/openapi.yaml; then
  die "docs version (openapi/openapi.yaml) does not match version file ${version}"
fi

# format golang
which gofumpt &>/dev/null || go install mvdan.cc/gofumpt@v0.5.0
go_files=()
while IFS= read -r -d '' file; do
  case "$file" in
  docs/* | api/* | openapi/gen/* | *.pb.go | *.pb.gw.go | *_gen.go) continue ;;
  esac
  # Deleted files remain in the index until their removal is staged.
  if [ -f "$file" ]; then
    go_files+=("$file")
  fi
done < <(git ls-files -z '*.go')

if [ "${#go_files[@]}" -gt 0 ]; then
  unformatted=$(printf '%s\0' "${go_files[@]}" | xargs -0 gofumpt -l)
  if [ -n "$unformatted" ]; then
    printf '%s\0' "${go_files[@]}" | xargs -0 gofumpt -w
    die "Go formatting errors"
  fi
fi
go mod verify

./scripts/lint-handlers.bash
./scripts/lint-tokens.bash
./scripts/lint-skill-chain-client.sh

go run tools/analyze/main.go -rand -map_iteration ./common/... ./constants/... ./x/...

go run tools/analyze/main.go -float_comparison ./...

go run tools/lint-whitelist-tokens/main.go

# ensure upgrades in app/upgrades only match the current version
for dir in app/upgrades/*/; do
  upgrade_version=$(basename "${dir}")
  if [ "${upgrade_version}" != "${version}" ] && [ "${upgrade_version}" != "standard" ]; then
    die "upgrade version ${upgrade_version} does not match current version ${version}"
  fi
done
