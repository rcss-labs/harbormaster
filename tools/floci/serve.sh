#!/usr/bin/env bash
# Runs bin/harbormaster serve against floci in the foreground (used by make floci-serve and the Playwright webServer).
#
# Usage: tools/floci/serve.sh [--help]
#   HM_FLOCI_PORT   port to listen on (default 8482)
# Exit codes: 2 refused (environment not local floci) or bad usage, 1 binary missing.
set -euo pipefail

case "${1:-}" in
  -h | --help) sed -n '2,6p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
  "") ;;
  *) echo "usage: tools/floci/serve.sh [--help]" >&2; exit 2 ;;
esac

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BIN="$HERE/../../bin/harbormaster"
# shellcheck source=server-env.sh
. "$HERE/server-env.sh"
[ -x "$BIN" ] || { echo "serve: $BIN is missing; run make build" >&2; exit 1; }
exec "$BIN" serve
