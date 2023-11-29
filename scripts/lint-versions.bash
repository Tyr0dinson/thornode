#!/bin/bash

set -euo pipefail

# This script compares versioned functions to the develop branch to ensure no logic
# changes in historical versions that would cause consensus failure.

VERSION=$(awk -F. '{ print $2 }' version)
CI_MERGE_REQUEST_TITLE=${CI_MERGE_REQUEST_TITLE-}

# if there the branch is mr-<x> (fork repo ci), extract title from api
if [[ -z $CI_MERGE_REQUEST_TITLE ]] && [[ $CI_COMMIT_REF_NAME =~ ^mr-[0-9]+$ ]]; then
  # extract merge request id from branch name
  MR_ID="${CI_COMMIT_REF_NAME//mr-/}"
  CI_MERGE_REQUEST_TITLE=$(curl "https://gitlab.com/api/v4/projects/$CI_PROJECT_ID/merge_requests?iids=$MR_ID" | jq -r ".[]|select(.sha==\"$CI_COMMIT_SHA\")|.title")
fi

git fetch https://gitlab.com/thorchain/thornode.git develop

# skip for develop
if git merge-base --is-ancestor "$(git rev-parse HEAD)" "$(git rev-parse FETCH_HEAD)"; then
  echo "Skipping lint for commit in develop ($(git rev-parse FETCH_HEAD))."
  exit 0
fi

go run tools/versioned-functions/main.go --version="$VERSION" >/tmp/versioned-fns-current
git checkout FETCH_HEAD
git checkout - -- tools scripts
go run tools/versioned-functions/main.go --version="$VERSION" >/tmp/versioned-fns-develop
git checkout -

gofumpt -w /tmp/versioned-fns-develop /tmp/versioned-fns-current

if ! diff -u -F '^func' -I '^//' --color=always /tmp/versioned-fns-develop /tmp/versioned-fns-current; then
  echo "Detected change in versioned function."
  if [[ $CI_MERGE_REQUEST_TITLE == *"#check-lint-warning"* ]]; then
    echo "Merge request is marked unsafe."
  else
    echo 'Correct the change, add a new versioned function, or add "#check-lint-warning" to the PR description.'
    exit 1
  fi
fi
