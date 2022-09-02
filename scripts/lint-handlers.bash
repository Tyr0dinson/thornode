#!/bin/bash
# set -ex

source ./scripts/utils.sh

echo "Linting handlers file"

handlers=$(ls -1 x/thorchain/handler_*)

for f in $handlers; do
  if [[ $f == *"_test"* ]]; then
    continue
  fi
  if [[ $f == *"_archive"* ]]; then
    continue
  fi
  base=$(echo "$f" | awk -F "." '{ print $1 }')
  archive="$base"_archive.go
  files="$f"
  if [[ -f $archive ]]; then
    files+=" $archive"
  fi

  # trunk-ignore(shellcheck/SC2086): word split is desired behavior
  validate_init=$(grep " validateV" $files | awk '{ print $4 }' | awk -F "(" '{ print $1 }')
  validate_call=$(grep "h.validateV" "$f" | awk '{ print $2 }' | awk -F "(" '{ print $1 }' | awk -F "." '{ print $2 }')

  # trunk-ignore(shellcheck/SC2086): word split is desired behavior
  handler_init=$(grep " handleV" $files | awk '{ print $4 }' | awk -F "(" '{ print $1 }')
  handler_call=$(grep "h.handleV" "$f" | awk '{ print $2 }' | awk -F "(" '{ print $1 }' | awk -F "." '{ print $2 }')

  missing=$(echo -e "$validate_init\n$validate_call\n$handler_init\n$handler_call" | sort -n | uniq -u)

  if [[ -n $missing ]]; then
    echo "Handler: $f... Not OK"
    echo "$missing"
    exit 1
  fi
  echo "Handler: $f... OK"
done

# Check that any new code does not add a handler that is
# less than or equal to the current repository version.
REPO_VERSION=$(version "$(cat version)")
for V in $(git diff origin/develop | sed -nE 's/^\+.*\.GTE\(.*\.MustParse\("([0-9.]+)".*/\1/p'); do
  HANDLER_VERSION=$(version "$V")
  if [ "$HANDLER_VERSION" -le "$REPO_VERSION" ]; then
    cat <<EOF
ERROR: Bad version introduced. $V is less than or equal to current repo version ($(cat version))
EOF
    exit 1
  fi
done
