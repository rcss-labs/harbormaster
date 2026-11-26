#!/usr/bin/env bash
# Runs one CI part of the Go tests. internal/api runs its tests one after another and alone takes most of
# the 30-minute go test limit, so CI splits it into three parts by test name; "rest" runs every other package.
#
# Usage: tools/go-test-part.sh [--list] rest|api-0|api-1|api-2
#   --list  print what the part would run (package paths for rest, test names for api-N) and run nothing
# Exit codes: 0 tests passed (or listed), 1 a test failed, 2 bad usage.
set -euo pipefail

usage() { echo "usage: tools/go-test-part.sh [--list] rest|api-0|api-1|api-2" >&2; exit 2; }

API_SHARDS=3
API=github.com/rcss-labs/harbormaster/internal/api
TIMEOUT=30m

list=0
case "${1:-}" in
  -h | --help) sed -n '2,7p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
  --list) list=1; shift ;;
esac
[ $# -eq 1 ] || usage
cd "$(dirname "${BASH_SOURCE[0]}")/.."

case "$1" in
  rest)
    packages="$(go list ./... | grep -vx "$API")"
    if [ "$list" -eq 1 ]; then echo "$packages"; exit 0; fi
    # shellcheck disable=SC2086 # one package path per word
    go test -timeout "$TIMEOUT" $packages || exit 1
    ;;
  api-[0-9])
    shard="${1#api-}"
    [ "$shard" -lt "$API_SHARDS" ] || usage
    # Every API_SHARDS-th top-level test by sorted name, so each part gets a spread of files.
    tests="$(go test -list '.*' ./internal/api | grep -E '^(Test|Example|Fuzz)' | sort |
      awk -v n="$API_SHARDS" -v i="$shard" '(NR - 1) % n == i')"
    [ -n "$tests" ] || { echo "go-test-part: api-$shard selects no tests" >&2; exit 1; }
    if [ "$list" -eq 1 ]; then echo "$tests"; exit 0; fi
    go test -timeout "$TIMEOUT" -run "^($(paste -sd '|' - <<<"$tests"))\$" ./internal/api || exit 1
    ;;
  *) usage ;;
esac
