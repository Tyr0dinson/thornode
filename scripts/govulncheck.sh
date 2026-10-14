#!/bin/bash
# Runs govulncheck and fails only on reachable (symbol-level) vulnerabilities whose
# OSV ID is not listed in .govulncheck-ignore. govulncheck has no native ignore flag,
# so this wraps its JSON output. Allowlisted IDs that no longer appear are reported so
# the list can be pruned.
#
# With GOVULNCHECK_BASE_REF set (MR pipelines), unaccepted findings are compared
# against a scan of that commit. Findings the base also has were not introduced by
# the change under test, typically a newly published advisory, and exit 3 instead
# of 1 so CI can report them without blocking every MR. The scan without a base
# (develop, tags, schedules) still blocks.
#
# Exit status: 0 no unaccepted findings, 1 unaccepted findings (introduced by the
# change when a base is set), 2 scanner, parser or allowlist error, 3 unaccepted
# findings that the base already has. Every error path fails closed.
#
# Usage: scripts/govulncheck.sh [packages...]   (default: ./...)
# Requires: go, jq
# Tests: build/scripts/tests/test_govulncheck.py
set -euo pipefail
export LC_ALL=C

GOVULNCHECK_VERSION="${GOVULNCHECK_VERSION:-v1.1.4}"
IGNORE_FILE="${IGNORE_FILE:-.govulncheck-ignore}"
# traces printed per unaccepted ID; the rest are counted
MAX_TRACES="${MAX_TRACES:-5}"

cd "$(dirname "$0")/.."

die() {
  echo "govulncheck: ERROR: $*" >&2
  exit 2
}

if [ $# -eq 0 ]; then
  set -- ./...
fi

command -v jq >/dev/null || die "jq is required"
[ -r "$IGNORE_FILE" ] || die "cannot read allowlist $IGNORE_FILE"

OUT=$(mktemp)
BASE_OUT=$(mktemp)
BASE_DIR=
# shellcheck disable=SC2317  # invoked by the EXIT trap
cleanup() {
  rm -f "$OUT" "$BASE_OUT"
  if [ -n "$BASE_DIR" ]; then
    git worktree remove --force "$BASE_DIR" >/dev/null 2>&1 || rm -rf "$BASE_DIR"
  fi
}
trap cleanup EXIT

# a finding is reachable when its innermost trace frame names a function
REACHABLE_FILTER='select(.finding) | .finding | select(.trace[0].function != null)'

# scan <output file> [packages...]: scan the current directory
scan() {
  local out=$1
  shift
  # -format json always exits 0 on findings; a non-zero exit is a real error
  # (build failure, DB fetch failure) and must fail the job.
  go run "golang.org/x/vuln/cmd/govulncheck@${GOVULNCHECK_VERSION}" \
    -tags mocknet -format json "$@" >"$out" || return 1
  # Parse everything up front with checked exit status. Process substitution would
  # hide a jq failure and turn malformed output into a silent pass. A real run
  # always starts with a config message, so its absence means empty or foreign
  # output.
  jq -e -n '[inputs] | any(.config.scanner_name == "govulncheck")' "$out" >/dev/null 2>&1 || return 2
}

# reachable_ids <output file>: reachable OSV IDs, one per line, unsorted
reachable_ids() {
  local malformed
  jq -r "$REACHABLE_FILTER | .osv | strings" "$1" || return 1
  malformed=$(jq -n "[inputs | $REACHABLE_FILTER | select(.osv | type != \"string\")] | length" "$1") ||
    return 1
  [ "$malformed" -eq 0 ] || return 2
}

rc=0
scan "$OUT" "$@" || rc=$?
[ "$rc" -ne 1 ] || die "scanner failed"
[ "$rc" -eq 0 ] || die "scanner output is not valid govulncheck JSON"

rc=0
found_ids=$(reachable_ids "$OUT") || rc=$?
[ "$rc" -ne 1 ] || die "failed to parse findings"
[ "$rc" -eq 0 ] || die "reachable findings have no OSV ID"

ignored_ids=$(sed -e 's/#.*//' "$IGNORE_FILE" | awk 'NF {print $1}')
bad=$(grep -Ev '^GO-[0-9]{4}-[0-9]+$' <<<"$ignored_ids" | grep -v '^$' || true)
[ -z "$bad" ] || die "malformed entries in $IGNORE_FILE: $(echo "$bad" | tr '\n' ' ')"

mapfile -t found < <(grep -v '^$' <<<"$found_ids" | sort -u)
mapfile -t ignored < <(grep -v '^$' <<<"$ignored_ids" | sort -u)
mapfile -t new < <(comm -23 <(printf '%s\n' "${found[@]}" | grep -v '^$') <(printf '%s\n' "${ignored[@]}" | grep -v '^$'))
mapfile -t stale < <(comm -13 <(printf '%s\n' "${found[@]}" | grep -v '^$') <(printf '%s\n' "${ignored[@]}" | grep -v '^$'))

echo "govulncheck: ${#found[@]} reachable vulnerabilities, ${#ignored[@]} allowlisted in $IGNORE_FILE"

for id in "${stale[@]}"; do
  echo "WARNING: $id is allowlisted in $IGNORE_FILE but no longer reported; remove it"
done

if [ ${#new[@]} -eq 0 ]; then
  echo "govulncheck: no reachable vulnerabilities outside the allowlist"
  exit 0
fi

echo
echo "govulncheck: ${#new[@]} reachable vulnerabilities not in $IGNORE_FILE:"
for id in "${new[@]}"; do
  # Limit output inside jq: piping to head would SIGPIPE jq and, under pipefail,
  # abort before the remaining IDs are reported.
  summary=$(jq -r -n --arg id "$id" 'first(inputs | select(.osv.id? == $id) | .osv.summary) // "(no summary)"' "$OUT")
  echo
  echo "$id: $summary"
  echo "  https://pkg.go.dev/vuln/$id"
  jq -r -n --arg id "$id" --argjson max "$MAX_TRACES" "[inputs | $REACHABLE_FILTER | select(.osv == \$id)]"'
    | (.[:$max][]
      | "  \(.trace[0].module)@\(.trace[0].version) (fixed in \(.fixed_version // "none"))\n    " +
        ([.trace[] | "\(.package).\(if .receiver then .receiver + "." else "" end)\(.function)"] | reverse | join("\n      -> "))),
      (select(length > $max) | "  ... and \(length - $max) more traces")' \
    "$OUT"
done
echo
echo "Upgrade the affected dependency, or add the ID to $IGNORE_FILE with a reason."

[ -n "${GOVULNCHECK_BASE_REF:-}" ] || exit 1

# Scan the base in its own checkout. Any failure here keeps the findings blocking.
echo
echo "govulncheck: scanning base $GOVULNCHECK_BASE_REF to see which findings this change introduced"
base_failed() {
  echo "govulncheck: base scan failed ($*); treating every finding above as introduced"
  exit 1
}
BASE_DIR=$(mktemp -d -t govulncheck-base.XXXXXX)
git worktree add --quiet --detach "$BASE_DIR" "$GOVULNCHECK_BASE_REF" || base_failed "cannot check out base"
rc=0
(cd "$BASE_DIR" && scan "$BASE_OUT" "$@") || rc=$?
[ "$rc" -eq 0 ] || base_failed "scanner exit $rc"
base_ids=$(reachable_ids "$BASE_OUT") || base_failed "cannot parse base findings"

mapfile -t introduced < <(comm -23 <(printf '%s\n' "${new[@]}") <(grep -v '^$' <<<"$base_ids" | sort -u))
if [ ${#introduced[@]} -eq 0 ]; then
  echo "govulncheck: base $GOVULNCHECK_BASE_REF has all ${#new[@]} of these findings, so this change did not introduce them"
  echo "Fix them on the target branch; this does not block the change."
  exit 3
fi
echo "govulncheck: introduced by this change (not reported on base $GOVULNCHECK_BASE_REF): ${introduced[*]}"
exit 1
