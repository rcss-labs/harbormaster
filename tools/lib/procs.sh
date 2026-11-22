#!/usr/bin/env bash
# Shared helpers for tools/run-split.sh and tools/split-smoke.sh: build the binary, prepare a schema, start Harbormaster
# roles as processes, wait for /healthz and stop them. Source it (". tools/lib/procs.sh"); it sets no shell options, the
# caller runs `set -euo pipefail`. Call from the repository root (both scripts cd there first).
#
# State the caller sets before procs_start:
#   PROCS_DB_URL         HM_DATABASE_URL of every process (see procs_prepare_schema)
#   PROCS_KV_URL         HM_KV_URL, may be empty
#   PROCS_WEB_ADDR       host:port of web, used as HM_BASE_URL
#   PROCS_METRICS_TOKEN  HM_METRICS_TOKEN
#   PROCS_SESSION_KEY    HM_SESSION_KEY
# State the library keeps: PROCS_PIDS and PROCS_ROLES (parallel arrays, one entry per started process).
#
# Functions:
#   procs_usage FILE                    print FILE's leading comment block (from line 2 up to its `set -euo` line)
#   procs_check_schema NAME PREFIX      succeed only when NAME matches ^PREFIX[a-z0-9_]*$ (a script that drops the schema
#                                       must never be pointed at one that holds other data)
#   procs_build                         go build -o bin/harbormaster ./cmd/harbormaster
#   procs_prepare_schema SCHEMA         print the database URL of a freshly recreated SCHEMA (tools/e2eschema)
#   procs_start ROLE ADDR LOG [ENV...]  start `harbormaster serve --role=ROLE` listening on ADDR in fake-AWS mode. LOG is a
#                                       file to write output to, or "-" to print it to stdout with a "[ROLE] " prefix.
#                                       ENV items (KEY=VALUE) are added to or override the base environment. The pid is
#                                       appended to PROCS_PIDS
#   procs_healthy ADDR                  succeed when ADDR answers /healthz with the body "ok"
#   procs_wait_healthy ADDR SECONDS     poll procs_healthy every 0.5 s; fail when the time is up
#   procs_stop_all                      SIGTERM every started process, give them 20 s for their graceful shutdown, then
#                                       SIGKILL what is left and reap all of them

PROCS_PIDS=()
PROCS_ROLES=()

procs_usage() {
	sed -n '2,/^set -euo/p' "$1" | sed '$d' | sed 's/^# \{0,1\}//'
}

procs_check_schema() {
	[[ "$1" =~ ^$2[a-z0-9_]*$ ]]
}

procs_build() {
	mkdir -p bin
	go build -o bin/harbormaster ./cmd/harbormaster
}

procs_prepare_schema() {
	HM_E2E_SCHEMA="$1" go run ./tools/e2eschema
}

procs_start() {
	local role="$1" addr="$2" log="$3"
	shift 3
	local base_env=(
		HM_FAKE_AWS=1 HM_COOKIE_SECURE=false HM_SESSION_KEY="$PROCS_SESSION_KEY" HM_METRICS_TOKEN="$PROCS_METRICS_TOKEN"
		HM_DATABASE_URL="$PROCS_DB_URL" HM_KV_URL="$PROCS_KV_URL" HM_BASE_URL="http://$PROCS_WEB_ADDR" HM_LISTEN="$addr"
	)
	if [[ "$log" == "-" ]]; then
		env "${base_env[@]}" ${@+"$@"} bin/harbormaster serve --role="$role" > >(sed -u "s/^/[$role] /") 2>&1 &
	else
		env "${base_env[@]}" ${@+"$@"} bin/harbormaster serve --role="$role" >"$log" 2>&1 &
	fi
	PROCS_PIDS+=("$!")
	PROCS_ROLES+=("$role")
}

procs_healthy() {
	[[ "$(curl -s --max-time 2 "http://$1/healthz" || true)" == "ok" ]]
}

procs_wait_healthy() {
	local addr="$1" seconds="$2" i
	for ((i = 0; i < seconds * 2; i++)); do
		if procs_healthy "$addr"; then return 0; fi
		sleep 0.5
	done
	return 1
}

procs_stop_all() {
	local pid i alive
	for pid in ${PROCS_PIDS[@]+"${PROCS_PIDS[@]}"}; do
		kill -TERM "$pid" 2>/dev/null || true
	done
	# Each process has a 15 s graceful shutdown; wait for it, then force what is left.
	for ((i = 0; i < 40; i++)); do
		alive=0
		for pid in ${PROCS_PIDS[@]+"${PROCS_PIDS[@]}"}; do
			if kill -0 "$pid" 2>/dev/null; then alive=1; fi
		done
		((alive == 0)) && break
		sleep 0.5
	done
	for pid in ${PROCS_PIDS[@]+"${PROCS_PIDS[@]}"}; do
		kill -KILL "$pid" 2>/dev/null || true
	done
	for pid in ${PROCS_PIDS[@]+"${PROCS_PIDS[@]}"}; do
		wait "$pid" 2>/dev/null || true
	done
}
