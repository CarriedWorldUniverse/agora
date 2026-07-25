#!/usr/bin/env bash
# govulncheck gate with an explicit allowlist of accepted advisories.
#
# Ported from nexus (scripts/govulncheck-gate.sh) deliberately unchanged in
# behaviour, so a finding reads the same in both repos and an operator only
# has to learn one gate.
#
# Runs govulncheck in JSON mode, extracts the OSV ids of SYMBOL-LEVEL findings
# (finding objects whose trace starts with a function frame — the same set
# that fails govulncheck's default text mode), and compares them against
# scripts/govulncheck-allowlist.txt. Allowlisted findings are reported as
# accepted; any non-allowlisted finding fails the gate.
#
# Symbol-level is the point: it means the vulnerable FUNCTION is reachable
# from this module, not merely that the module appears in the graph. That
# distinction is what makes the gate worth failing on.
#
# Allowlist format: one OSV id per line, '#' comments allowed. An entry is a
# decision to ACCEPT a live, reachable vulnerability — it needs a justifying
# comment naming who accepted it and why, not just an id.
set -euo pipefail

# Scan the toolchain the MODULE declares, not whatever the host happens to
# default to. govulncheck reports stdlib advisories against the Go version it
# resolves, and some hosts (croft) set GOTOOLCHAIN=local, which ignores
# go.mod's `toolchain` line — so the same tree scanned clean in CI and dirty
# locally, for a reason that had nothing to do with the code. Honour an
# explicit caller override; otherwise follow go.mod.
export GOTOOLCHAIN="${GOTOOLCHAIN_OVERRIDE:-auto}"

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
allowlist_file="${repo_root}/scripts/govulncheck-allowlist.txt"

scan_json="$(mktemp)"
trap 'rm -f "${scan_json}"' EXIT

echo "govulncheck-gate: running govulncheck -format json ./..." >&2
(cd "${repo_root}" && govulncheck -format json ./...) > "${scan_json}"

findings="$(jq -r '
  select(.finding != null)
  | .finding
  | select(.trace != null and (.trace | length) > 0 and .trace[0].function != null)
  | .osv
' "${scan_json}" | sort -u)"

allowlist=""
if [[ -f "${allowlist_file}" ]]; then
  allowlist="$(sed -e 's/#.*//' -e 's/[[:space:]]//g' "${allowlist_file}" | grep -v '^$' || true)"
fi

rejected=""
while IFS= read -r id; do
  [[ -z "${id}" ]] && continue
  if grep -qxF "${id}" <<<"${allowlist}"; then
    echo "govulncheck-gate: ${id} accepted (allowlisted)"
  else
    rejected="${rejected}${id}"$'\n'
  fi
done <<<"${findings}"

if [[ -n "${rejected}" ]]; then
  echo "govulncheck-gate: FAIL — non-allowlisted findings:" >&2
  printf '%s' "${rejected}" | sed 's/^/  /' >&2
  echo "govulncheck-gate: fix the dependency or add a justified entry to ${allowlist_file}" >&2
  exit 1
fi

echo "govulncheck-gate: PASS (no non-allowlisted findings)"
