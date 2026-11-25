#!/usr/bin/env bash
# Runs govulncheck over the module and fails only on advisories that are reached from our code (called) and are not
# listed in tools/vulncheck-accepted.txt. Advisories that are imported but never called are ignored, as govulncheck's
# own text output does.
#
# Usage: tools/vulncheck.sh [--json FILE] [--help]
#   --json FILE  read govulncheck JSON from FILE instead of running govulncheck (used by tools/vulncheck_test.sh)
# Exit codes: 0 no unaccepted called advisories, 1 unaccepted advisories found, 2 usage or govulncheck error.
set -euo pipefail

GOVULNCHECK_VERSION=v1.10.0
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ACCEPTED_FILE="${VULNCHECK_ACCEPTED_FILE:-$HERE/vulncheck-accepted.txt}" # the override is for tools/vulncheck_test.sh

usage() { echo "usage: tools/vulncheck.sh [--json FILE] [--help]" >&2; exit 2; }

json_file=""
while [ $# -gt 0 ]; do
  case "$1" in
    -h | --help) sed -n '2,9p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
    --json) [ $# -ge 2 ] || usage; json_file=$2; shift 2 ;;
    *) usage ;;
  esac
done

command -v jq >/dev/null 2>&1 || { echo "vulncheck: jq is required" >&2; exit 2; }

if [ -n "$json_file" ]; then
  [ -r "$json_file" ] || { echo "vulncheck: cannot read $json_file" >&2; exit 2; }
  report="$(cat "$json_file")"
else
  # In JSON mode govulncheck exits 0 even with findings; a non-zero exit means it could not run.
  if ! report="$(go run "golang.org/x/vuln/cmd/govulncheck@${GOVULNCHECK_VERSION}" -format json ./...)"; then
    echo "vulncheck: govulncheck failed to run" >&2
    exit 2
  fi
fi

# A finding is "called" when the innermost frame of its trace names a function.
called="$(jq -r 'select(.finding) | select(.finding.trace[0].function != null) | .finding.osv' <<<"$report" | sort -u)"
# grep exits 1 when the list holds only comments, which is the normal state once every advisory is fixed.
accepted="$({ grep -v '^\s*#' "$ACCEPTED_FILE" || true; } | awk 'NF {print $1}' | sort -u)"

unaccepted="$(comm -23 <(printf '%s\n' "$called" | sed '/^$/d') <(printf '%s\n' "$accepted" | sed '/^$/d'))"
stale="$(comm -13 <(printf '%s\n' "$called" | sed '/^$/d') <(printf '%s\n' "$accepted" | sed '/^$/d'))"

for id in $(comm -12 <(printf '%s\n' "$called" | sed '/^$/d') <(printf '%s\n' "$accepted" | sed '/^$/d')); do
  echo "accepted: $id ($(awk -v id="$id" '$1 == id {sub(/^[^ ]+ +/, ""); print; exit}' "$ACCEPTED_FILE"))"
done
for id in $stale; do
  echo "note: $id is accepted but no longer reported; remove it from tools/vulncheck-accepted.txt"
done

if [ -n "$unaccepted" ]; then
  echo "vulncheck: called advisories that are not accepted:" >&2
  for id in $unaccepted; do echo "  $id  https://pkg.go.dev/vuln/$id" >&2; done
  exit 1
fi
echo "vulncheck: no unaccepted called advisories"
