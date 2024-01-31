#!/bin/bash

set -euo pipefail

# This script checks to ensure only current managers are used in unversioned/current tests
echo -n "Checking unversioned tests for versioned managers... "

VERSION=$(awk -F. '{ print $2 }' version)
CI_MERGE_REQUEST_TITLE=${CI_MERGE_REQUEST_TITLE-}

# skip for develop
if git merge-base --is-ancestor "$(git rev-parse HEAD)" "$(git rev-parse FETCH_HEAD)"; then
  echo "Skipping lint for commit in develop ($(git rev-parse FETCH_HEAD))."
  exit 0
fi

go run tools/test-current-versions/main.go --managers >/tmp/testing-old-versions
if [ -s /tmp/testing-old-versions ]; then
  echo "Detected use of versioned manager in unversioned test."
  if [[ $CI_MERGE_REQUEST_TITLE == *"#check-lint-warning"* ]]; then
    echo "Merge request is marked unsafe."
  else
    echo 'In the following locations, switch to the current manager, or add "#check-lint-warning" to the PR description.'
    cat /tmp/testing-old-versions
    exit 1
  fi
fi

echo "OK"