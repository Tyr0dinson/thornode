#!/bin/bash
set -euo pipefail

# Make sure token lists are stripped of extraneous information.
find common/tokenlist/ -name "*.json" |
  while read -r F; do
    jq 'del(.. | .logoURI?) | del(.. | .tags?) | del(.keywords?)' <"$F" >"$F".tmp
    if diff -q "$F".tmp "$F" >/dev/null 2>&1; then
      rm -f "$F".tmp
    else
      echo "Updating $F"
      mv "$F".tmp "$F"
    fi
  done

# Run the trunk.io formatting suite against files changed in this branch.
./scripts/trunk fmt
