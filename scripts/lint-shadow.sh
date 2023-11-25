#!/bin/bash
set -euo pipefail

which shadow &>/dev/null || go install golang.org/x/tools/go/analysis/passes/shadow/cmd/shadow@v0.15.0

git checkout develop
go vet -vettool="$(which shadow)" ./... |& sort >/tmp/shadow.orig || true
git checkout -
go vet -vettool="$(which shadow)" ./... |& sort >/tmp/shadow.new || true

DIFF=$(git diff /tmp/shadow.orig /tmp/shadow.new | grep -E '^\+[^#]' | grep -v 'declaration of "err"' || true)
if [ -n "$DIFF" ]; then
  cat <<EOF
ERROR - New shadowed variables detected in this diff:

$DIFF

EOF
  exit 1
fi
