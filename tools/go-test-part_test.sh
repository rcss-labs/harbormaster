#!/usr/bin/env bash
# Self-check for tools/go-test-part.sh: the CI parts together run every Go test exactly once.
#
# Usage: tools/go-test-part_test.sh [--help]
# Exit codes: 0 all cases hold, 1 a case failed, 2 bad usage.
set -euo pipefail

case "${1:-}" in
  -h | --help) sed -n '2,5p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
  "") ;;
  *) echo "usage: tools/go-test-part_test.sh [--help]" >&2; exit 2 ;;
esac

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PART="$HERE/go-test-part.sh"
API=github.com/rcss-labs/harbormaster/internal/api
failures=0
fail() { echo "FAIL $1" >&2; failures=$((failures + 1)); }

all_api="$(go test -list '.*' ./internal/api | grep -E '^(Test|Example|Fuzz)' | sort)"
sharded=""
for i in 0 1 2; do
  part="$("$PART" --list "api-$i")"
  [ -n "$part" ] || fail "api-$i selects no tests"
  sharded+="$part"$'\n'
done
sharded="$(grep -v '^$' <<<"$sharded" | sort)"
if [ "$(uniq -d <<<"$sharded")" != "" ]; then fail "a test is in more than one api part"; fi
if [ "$sharded" != "$all_api" ]; then fail "the api parts do not cover exactly the package's tests"; else echo "ok   the api parts cover every api test exactly once"; fi

rest="$("$PART" --list rest | sort)"
if grep -qx "$API" <<<"$rest"; then fail "the rest part also runs internal/api"; fi
if [ "$rest" != "$(go list ./... | grep -vx "$API" | sort)" ]; then fail "the rest part does not run every other package"; else echo "ok   the rest part runs every package but internal/api"; fi

got=0; "$PART" --list api-3 >/dev/null 2>&1 || got=$?
if [ "$got" -ne 2 ]; then fail "an unknown part exits $got, want 2"; else echo "ok   an unknown part is a usage error"; fi

if [ "$failures" -gt 0 ]; then echo "$failures case(s) failed" >&2; exit 1; fi
