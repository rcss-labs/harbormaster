#!/usr/bin/env bash
# Runs bin/harbormaster serve against floci with access tokens and OAuth switched on (used by make floci-mcp-check and
# the Playwright floci-tokens config). Same as serve.sh plus HM_API_TOKENS=true and HM_OAUTH=true.
#
# Usage: tools/floci/serve-tokens.sh [--help]
#   HM_FLOCI_PORT   port to listen on (default 8491, so it never collides with make floci-serve on 8490)
# Both servers share the one floci Postgres, so run only one of them at a time.
# Exit codes: 2 refused (environment not local floci) or bad usage, 1 binary missing.
set -euo pipefail

case "${1:-}" in
  -h | --help) sed -n '2,9p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
  "") ;;
  *) echo "usage: tools/floci/serve-tokens.sh [--help]" >&2; exit 2 ;;
esac

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BIN="$HERE/../../bin/harbormaster"
export HM_FLOCI_PORT="${HM_FLOCI_PORT:-8491}"
# shellcheck source=server-env.sh
. "$HERE/server-env.sh"
[ -x "$BIN" ] || { echo "serve-tokens: $BIN is missing; run make build" >&2; exit 1; }

export HM_API_TOKENS=true
export HM_OAUTH=true
# Harbormaster insists on an https issuer URL; nothing connects to it (the gateway run by mcp-e2e.sh has its own
# loopback public URL). It only shows in the UI's connect instructions.
export HM_MCP_PUBLIC_URL="${HM_FLOCI_MCP_ISSUER:-https://mcp.floci.invalid}"
exec "$BIN" serve
