#!/usr/bin/env bash
# Self-check for tools/vulncheck.sh against recorded govulncheck JSON. Needs no network.
#
# Usage: tools/vulncheck_test.sh [--help]
# Exit codes: 0 all cases hold, 1 a case failed, 2 bad usage.
set -euo pipefail

case "${1:-}" in
  -h | --help) sed -n '2,5p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
  "") ;;
  *) echo "usage: tools/vulncheck_test.sh [--help]" >&2; exit 2 ;;
esac

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DATA="$HERE/testdata/vulncheck"
failures=0

# expect NAME WANT_STATUS WANT_TEXT CMD...: runs the command, compares its exit status, and checks its output
# contains WANT_TEXT (empty means any output).
expect() {
  local name=$1 want=$2 text=$3 got=0 out; shift 3
  out="$("$@" 2>&1)" || got=$?
  if [ "$got" -ne "$want" ]; then
    echo "FAIL $name: exit $got, want $want" >&2; echo "$out" | sed 's/^/     /' >&2; failures=$((failures + 1)); return
  fi
  if [ -n "$text" ] && ! grep -qF -- "$text" <<<"$out"; then
    echo "FAIL $name: output lacks \"$text\"" >&2; echo "$out" | sed 's/^/     /' >&2; failures=$((failures + 1)); return
  fi
  echo "ok   $name"
}

expect "an accepted called advisory passes" 0 "accepted: GO-2026-6909" "$HERE/vulncheck.sh" --json "$DATA/accepted-only.json"
expect "a new called advisory fails and is named" 1 "GO-2099-0001" "$HERE/vulncheck.sh" --json "$DATA/new-called.json"
expect "an imported but never called advisory passes" 0 "" "$HERE/vulncheck.sh" --json "$DATA/imported-not-called.json"
expect "a missing JSON file is a usage error" 2 "" "$HERE/vulncheck.sh" --json "$DATA/does-not-exist.json"
expect "an unknown flag is a usage error" 2 "usage:" "$HERE/vulncheck.sh" --bogus

[ "$failures" -eq 0 ] || exit 1
