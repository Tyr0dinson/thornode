#!/usr/bin/env bash

# This script wraps execution of trunk when run in CI.

set -euo pipefail

SCRIPT_DIR="$(dirname "$0")"
BASE_BRANCH="origin/develop"
FLAGS=(-j8 --ci)
CHECK_EXISTING=false

if [ -n "${CI_MERGE_REQUEST_ID-}" ]; then
  # if go modules or trunk settings changed, also run with --all on merge requests
  if ! git diff --exit-code "$BASE_BRANCH" -- go.mod go.sum .trunk >/dev/null; then
    FLAGS+=(--all)
  # if there is a trunk-ignore comment change, also run with --all on merge requests
  elif git diff --unified=0 --no-prefix "$BASE_BRANCH" | sed '/^@@/d' | grep -q 'trunk-ignore'; then
    FLAGS+=(--all)
  # if this is a merge train, run with --all
  elif [ "${CI_MERGE_REQUEST_EVENT_TYPE-}" = "merge_train" ]; then
    FLAGS+=(--all)
  else
    FLAGS+=(--upstream "$BASE_BRANCH" --show-existing)
    CHECK_EXISTING=true
  fi
else
  FLAGS+=(--all)
fi

# run trunk, retrying once if it crashed with an internal error. That banner
# means trunk died before reporting (2026-09-14..28: 21 lint failures, none with
# findings), so a retry cannot hide a real finding.
run_trunk() {
  echo "Running: $SCRIPT_DIR/trunk check ${FLAGS[*]}"
  exec 3>&1
  OUT=$("$SCRIPT_DIR"/trunk check "${FLAGS[@]}" | tee /dev/fd/3)
  STATUS=$?
  exec 3>&-
}
set +e
run_trunk
if [ "$STATUS" -ne 0 ] && echo "$OUT" | grep -q "Trunk Internal Error"; then
  echo
  echo "Trunk crashed with an internal error; retrying once."
  sleep 10
  run_trunk
fi
set -e
if [ "$STATUS" -ne 0 ]; then
  exit "$STATUS"
fi

# confirm that we did not introduce lint errors outside our changes on merge requests
if $CHECK_EXISTING; then
  if echo "$OUT" | grep -q "ISSUES"; then
    echo
    echo "Changes introduce external lint errors."
    exit 1
  fi
fi
