#!/bin/bash

set -euo pipefail

# This script compares versioned functions to the develop branch to ensure no logic
# changes in historical versions that would cause consensus failure.

VERSION=$(awk -F. '{ print $2 }' version)
CI_MERGE_REQUEST_TITLE=${CI_MERGE_REQUEST_TITLE-}

git fetch https://gitlab.com/thorchain/thornode.git develop

# skip for develop
if git merge-base --is-ancestor "$(git rev-parse HEAD)" "$(git rev-parse FETCH_HEAD)"; then
  echo "Skipping lint for commit in develop ($(git rev-parse FETCH_HEAD))."
  exit 0
fi

git checkout FETCH_HEAD
git checkout - -- tools scripts
go run tools/test-current-versions/main.go --managers >/tmp/testing-old-versions
RETCODE=$?
git checkout -

if [ $RETCODE -eq 1 ]; then
  echo "Detected use of non-current manager in unversioned test."
  if [[ $CI_MERGE_REQUEST_TITLE == *"#check-lint-warning"* ]]; then
    echo "Merge request is marked unsafe."
  else
    echo 'In the following locations, use the current manager, or add "#check-lint-warning" to the PR description.'
    cat /tmp/testing-old-versions
    exit 1
  fi
fi
