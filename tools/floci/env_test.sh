#!/usr/bin/env bash
# Self-check for the floci safety guards (make floci-selfcheck). Needs no running containers.
#
# Usage: tools/floci/env_test.sh [--help]
# Exit codes: 0 all guards hold, 1 a guard failed, 2 bad usage.
set -euo pipefail

case "${1:-}" in
  -h | --help) sed -n '2,5p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
  "") ;;
  *) echo "usage: tools/floci/env_test.sh [--help]" >&2; exit 2 ;;
esac

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REAL=https://ecs.eu-west-1.amazonaws.com
failures=0

# expect_status NAME WANT CMD...: runs the command and compares its exit status.
expect_status() {
  local name=$1 want=$2 got=0; shift 2
  "$@" >/dev/null 2>&1 || got=$?
  if [ "$got" -eq "$want" ]; then echo "ok   $name"; else echo "FAIL $name: exit $got, want $want" >&2; failures=$((failures + 1)); fi
}

expect_status "env.sh accepts the default local endpoint" 0 env -u AWS_ENDPOINT_URL -u AWS_PROFILE "$HERE/env.sh"
expect_status "env.sh refuses a real AWS endpoint" 2 env AWS_ENDPOINT_URL=$REAL "$HERE/env.sh"
expect_status "env.sh refuses a LAN endpoint" 2 env AWS_ENDPOINT_URL=http://10.0.0.5:4566 "$HERE/env.sh"
expect_status "env.sh refuses a look-alike host" 2 env AWS_ENDPOINT_URL=http://127.0.0.1.example.com:4566 "$HERE/env.sh"
expect_status "env.sh refuses another profile" 2 env AWS_PROFILE=admin "$HERE/env.sh"
expect_status "seed.sh refuses another profile" 2 env AWS_PROFILE=admin "$HERE/seed.sh"
expect_status "seed.sh refuses a real AWS endpoint" 2 env AWS_ENDPOINT_URL=$REAL "$HERE/seed.sh"
expect_status "connect.sh refuses a real AWS endpoint" 2 env AWS_ENDPOINT_URL=$REAL "$HERE/connect.sh"
expect_status "serve.sh refuses a real AWS endpoint" 2 env AWS_ENDPOINT_URL=$REAL "$HERE/serve.sh"
expect_status "prepare.sh refuses a real AWS endpoint" 2 env AWS_ENDPOINT_URL=$REAL "$HERE/prepare.sh"

# The profile files must point at the local emulator and carry only the dummy keys.
if grep -q '^endpoint_url = http://127.0.0.1:4566$' "$HERE/aws-config"; then echo "ok   aws-config targets 127.0.0.1"; else echo "FAIL aws-config endpoint" >&2; failures=$((failures + 1)); fi
if grep -q '^aws_access_key_id = floci$' "$HERE/aws-credentials"; then echo "ok   aws-credentials are the dummy keys"; else echo "FAIL aws-credentials" >&2; failures=$((failures + 1)); fi

if [ "$failures" -gt 0 ]; then echo "$failures check(s) failed" >&2; exit 1; fi
echo "all floci guards hold"
