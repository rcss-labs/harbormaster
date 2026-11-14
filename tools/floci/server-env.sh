#!/usr/bin/env bash
# Environment for the Harbormaster server and CLI against the floci harness. Source it; it first sources env.sh,
# so a non-local endpoint or a profile other than floci is refused (status 2) before anything else is set.
#
# Usage: . tools/floci/server-env.sh
#   HM_FLOCI_PORT   port of the Harbormaster server (default 8482)

_server_env_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=env.sh
. "$_server_env_dir/env.sh" || return $?

HM_FLOCI_PORT="${HM_FLOCI_PORT:-8482}"
export HM_FLOCI_PORT
export HM_LISTEN="127.0.0.1:${HM_FLOCI_PORT}"
export HM_BASE_URL="http://127.0.0.1:${HM_FLOCI_PORT}"
# Test-only Postgres from docker-compose.yml, bound to loopback.
export HM_DATABASE_URL="postgres://postgres:hm@127.0.0.1:55433/postgres?sslmode=disable"
export HM_COOKIE_SECURE=false
export HM_SESSION_KEY=floci
# Fast tiers and no idle slowdown so the checks do not wait on the (slower) defaults.
export HM_POLL_SERVICES=5s
export HM_POLL_RUNNING_TASKS=5s
export HM_POLL_STOPPED_TASKS=15s
export HM_POLL_FULL_TASKS=30s
export HM_POLL_IDLE_AFTER=0
export HM_POLL_SCALING=10s
export HM_POLL_TARGET_HEALTH=10s
export HM_POLL_NETWORK=20s
export HM_POLL_STORAGE=20s
unset HM_FAKE_AWS _server_env_dir
