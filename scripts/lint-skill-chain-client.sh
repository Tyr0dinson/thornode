#!/usr/bin/env bash
#
# Validates factual claims in .claude/skills/chain-client/ and the public
# chain-client guides against the live codebase. Catches drift in interface
# method counts, copied method lists, and referenced file paths.
# Runs as part of `make lint`.
#
# The checks here are intentionally narrow: we verify only load-bearing
# claims that, if stale, would cause the skill to generate wrong code.

set -euo pipefail

SKILL_DIR=".claude/skills/chain-client"
SKILL_MD="${SKILL_DIR}/SKILL.md"
EVALS_JSON="${SKILL_DIR}/evals/evals.json"
INTERFACE_MD="${SKILL_DIR}/references/interface.md"
GUIDES=("docs/chain-clients/README.md" "docs/new-chains/implementation-guide.md")

if [[ ! -f ${SKILL_MD} ]]; then
  # Skill not present on this branch — nothing to validate.
  exit 0
fi

fail() {
  echo "ERR: chain-client skill lint: $*" >&2
  exit 1
}

# List methods inside a named Go interface (first occurrence by default).
# The optional occurrence selects between same-named examples in references.
# A declaration is identified by a line whose first non-whitespace token is an identifier
# followed by `(` — this correctly handles multi-line signatures (the
# continuation lines start with a different token) and skips comments and
# blank lines.
interface_methods() {
  local file="$1" name="$2" occurrence="${3:-1}"
  awk -v name="${name}" -v occurrence="${occurrence}" '
    $0 ~ "^[ \t]*type[ \t]+" name "[ \t]+interface[ \t]*{" {
      matches++
      inside = (matches == occurrence)
      next
    }
    inside && /^[[:space:]]*}/ { inside = 0; exit }
    inside && /^[[:space:]]*[A-Za-z_][A-Za-z0-9_]*[[:space:]]*\(/ {
      sub(/^[[:space:]]*/, "")
      sub(/[[:space:]]*\(.*/, "")
      print
    }
  ' "${file}"
}

count_interface_methods() {
  interface_methods "$1" "$2" | awk 'END { print NR }'
}

check_method_count() {
  local label="$1" file="$2" iface="$3" claim_pattern="$4"

  [[ -f ${file} ]] || fail "${label}: file not found: ${file}"

  local actual
  actual=$(count_interface_methods "${file}" "${iface}")
  [[ ${actual} -gt 0 ]] || fail "${label}: could not find interface ${iface} in ${file}"

  local claims documented
  claims=$(grep -oE "${claim_pattern//N/[0-9]+}" "${SKILL_MD}" || true)
  [[ -n ${claims} ]] || fail "${label}: method count not found in SKILL.md"
  while IFS= read -r documented; do
    if ! grep -qE "${claim_pattern//N/${actual}}" <<<"${documented}"; then
      fail "${label}: SKILL.md claims '${documented}' but ${iface} in ${file} has ${actual} methods"
    fi
  done <<<"${claims}"
  echo "OK: ${label} — method count ${actual} matches SKILL.md"
}

# Backticks delimit the documented name so ChainClient cannot match reporter.ChainClient.
# shellcheck disable=SC2016
check_method_count "ChainClient" \
  "bifrost/pkg/chainclients/shared/types/types.go" \
  "ChainClient" \
  '`ChainClient`.{0,10}\(N methods\)'

# shellcheck disable=SC2016
check_method_count "BlockScannerFetcher" \
  "bifrost/blockscanner/blockscanner.go" \
  "BlockScannerFetcher" \
  '`BlockScannerFetcher`.{0,10}\(N methods\)'

# shellcheck disable=SC2016
check_method_count "reporter.ChainClient" \
  "bifrost/reporter/types.go" \
  "ChainClient" \
  '`reporter\.ChainClient`.{0,10}\(N methods\)'

# shellcheck disable=SC2016
check_method_count "reporter.Dispatcher" \
  "bifrost/reporter/types.go" \
  "Dispatcher" \
  '`reporter\.Dispatcher`.{0,10}\(N methods?\)'

# Check the method lists in copied interface definitions as well as the headline
# counts. Compare sets so documentation may reorder methods without failing.
check_reference_methods() {
  local file="$1" iface="$2" reference="$3" occurrence="${4:-1}" actual documented
  [[ -f ${reference} ]] || fail "reference not found: ${reference}"
  actual=$(interface_methods "${file}" "${iface}" | sort)
  documented=$(interface_methods "${reference}" "${iface}" "${occurrence}" | sort)
  [[ -n ${actual} ]] || fail "could not find interface ${iface} in ${file}"
  [[ ${documented} == "${actual}" ]] || fail "${reference}: ${iface} occurrence ${occurrence} methods differ from ${file}"
  echo "OK: ${reference} — ${iface} occurrence ${occurrence} methods match source"
}

for reference in "${INTERFACE_MD}" "${GUIDES[0]}"; do
  check_reference_methods "bifrost/pkg/chainclients/shared/types/types.go" "ChainClient" "${reference}"
  check_reference_methods "bifrost/blockscanner/blockscanner.go" "BlockScannerFetcher" "${reference}"
  check_reference_methods "bifrost/reporter/types.go" "Dispatcher" "${reference}"
done

# The reference shows the full ChainClient first and reporter.ChainClient second.
check_reference_methods "bifrost/reporter/types.go" "ChainClient" "${INTERFACE_MD}" 2

# Validate evals.json mentions the same method count. The evals file is a
# secondary source of truth for Claude's expected output and must stay aligned
# with SKILL.md.
if [[ -f ${EVALS_JSON} ]]; then
  chain_client_methods=$(count_interface_methods \
    "bifrost/pkg/chainclients/shared/types/types.go" "ChainClient")
  claims=$(grep -oE "[0-9]+ ChainClient methods" "${EVALS_JSON}" || true)
  while IFS= read -r documented; do
    [[ -z ${documented} ]] && continue
    if [[ ${documented} != "${chain_client_methods} ChainClient methods" ]]; then
      fail "evals.json claims '${documented}' but ChainClient has ${chain_client_methods} methods"
    fi
  done <<<"${claims}"
  echo "OK: evals.json ChainClient method counts match interface"
fi

# Validate that every source file path referenced by the skill and guides exists
# -- these are the paths Claude will open when scaffolding a chain. Extract
# every backtick-quoted, directory-qualified .go path and assert it resolves,
# either from the repo root or relative to bifrost/pkg/chainclients/ (the skill
# writes chain-client paths in short form, e.g. `xrp/client.go`).
#
# Extracting from the docs beats a hand-maintained list: a stale entry in such
# a list used to be skipped because the check only ran for paths the docs
# happened to mention.
#
# ponytail: .go files only, and only paths containing a `/` -- bare basenames
# like `client.go` are ambiguous and suffix conventions like `_mocknet.go`
# aren't paths at all. Placeholders are written `<chain>.go` so they fall
# outside the character class and are never extracted.
missing=()
for guide in "${GUIDES[@]}"; do
  [[ -f ${guide} ]] || fail "guide not found: ${guide}"
done
# shellcheck disable=SC2016  # backticks here are literal markdown, not expansion
while read -r path; do
  [[ -z ${path} ]] && continue
  [[ -f ${path} || -f bifrost/pkg/chainclients/${path} ]] || missing+=("${path}")
done < <(grep -rhoE '`[A-Za-z0-9_/.-]+/[A-Za-z0-9_.-]+\.go`' \
  "${SKILL_MD}" "${SKILL_DIR}/references/" "${GUIDES[@]}" | tr -d '`' | sort -u)

if [[ ${#missing[@]} -gt 0 ]]; then
  echo "ERR: chain-client skill references non-existent paths:" >&2
  printf "  - %s\n" "${missing[@]}" >&2
  exit 1
fi

echo "OK: all referenced chain client paths exist"
echo "chain-client skill lint passed"
