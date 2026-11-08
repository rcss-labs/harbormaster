#!/usr/bin/env bash
# Prepares the floci database: applies migrations and creates the harness administrator. Starts nothing.
#
# Usage: tools/floci/prepare.sh [--help]
#
# The admin password is generated here, once, into tools/floci/.admin-password (git-ignored, mode 600). It is never
# printed. connect.sh replaces it after the forced first-sign-in password change and keeps the file current.
# Exit codes: 0 ready, 2 refused or bad usage, 1 failure (including an existing admin whose password file is gone).
set -euo pipefail

case "${1:-}" in
  -h | --help) sed -n '2,8p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
  "") ;;
  *) echo "usage: tools/floci/prepare.sh [--help]" >&2; exit 2 ;;
esac

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BIN="$HERE/../../bin/harbormaster"
PASSWORD_FILE="$HERE/.admin-password"
# shellcheck source=server-env.sh
. "$HERE/server-env.sh"
[ -x "$BIN" ] || { echo "prepare: $BIN is missing; run make build" >&2; exit 1; }

"$BIN" migrate

if [ -s "$PASSWORD_FILE" ]; then
  echo "prepare: admin password file exists; keeping it"
else
  umask 077
  openssl rand -hex 16 >"$PASSWORD_FILE"
  chmod 600 "$PASSWORD_FILE"
fi

set +e
"$BIN" create-admin --username admin --password-stdin <"$PASSWORD_FILE" >/dev/null 2>"$HERE/.create-admin.err"
status=$?
set -e
case $status in
  0) echo "prepare: created admin" ;;
  3) echo "prepare: admin already exists" ;;
  *) cat "$HERE/.create-admin.err" >&2; rm -f "$HERE/.create-admin.err"; exit 1 ;;
esac
rm -f "$HERE/.create-admin.err"
