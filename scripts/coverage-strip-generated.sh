#!/usr/bin/env bash

# Drop generated code from a Go cover profile, in place, so the coverage total
# (the README badge) measures hand-written code. Generated files are the ones
# carrying Go's standard "// Code generated ... DO NOT EDIT." header: *.pb.go,
# *.pulsar.go, openapi/gen, stringers. The *.pulsar.go and openapi/gen files
# alone hold ~170k never-executed statements, which held the reported total at
# ~21% while the rest of the module was ~90% covered.
#
# usage: scripts/coverage-strip-generated.sh coverage.txt

set -euo pipefail

PROFILE=${1:?usage: $0 <coverprofile>}
MODULE=$(go list -m)

PATTERNS=$(mktemp)
trap 'rm -f "$PATTERNS" "$PROFILE.tmp"' EXIT

# Profile lines look like "<module>/<path>.go:<line>.<col>,...", so match on
# "<module>/<path>.go:" exactly.
git ls-files '*.go' |
  xargs grep -lE '^// Code generated .* DO NOT EDIT\.$' |
  sed "s|^|$MODULE/|; s|\$|:|" >"$PATTERNS"

grep -vFf "$PATTERNS" "$PROFILE" >"$PROFILE.tmp"
mv "$PROFILE.tmp" "$PROFILE"
