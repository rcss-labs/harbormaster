#!/usr/bin/env bash
# Runs Harbormaster as three processes on one machine: web on 127.0.0.1:8480, poller on 127.0.0.1:8482 and
# coordinator on 127.0.0.1:8483, all against fake AWS data and the test database, so leases, kicks and health can be
# tried without ECS. Each process has its own in-memory fake estate, so a deploy made through web is not seen by the
# poller's fake; write flows are exercised in role=all (make run).
#
# Usage: tools/run-split.sh [-h|--help]
#
# Environment:
#   HM_TEST_DATABASE_URL  required; the Postgres URL that make test-db prints. The run uses its own schema in it,
#                         hm_run_split, recreated (dropped) on every start. HM_RUN_SPLIT_SCHEMA may change it, but the
#                         name must start with hm_run_split: anything else is refused, so a schema that holds other
#                         data is never dropped
#   HM_TEST_KV_URLS       required; the first URL is used as HM_KV_URL (split roles refuse to start without a store,
#                         because focus heartbeats and kicks must cross processes)
#
# Sign in at http://127.0.0.1:8480 as sam / harbormaster-e2e-1. Ctrl-C stops all three.
# Exit codes: 0 stopped by Ctrl-C or SIGTERM, 1 the build or schema preparation failed or a process exited on its own
# (a clean exit counts as a failure: none of them should stop), 2 usage error, missing variable or a refused schema
# name, otherwise the first non-zero status of a process.
set -euo pipefail

# shellcheck source=lib/procs.sh
. "$(dirname "$0")/lib/procs.sh"

usage() {
	procs_usage "$0"
}

case "${1:-}" in
-h | --help)
	usage
	exit 0
	;;
"") ;;
*)
	echo "run-split: unknown argument: $1" >&2
	usage >&2
	exit 2
	;;
esac

if [[ -z "${HM_TEST_DATABASE_URL:-}" ]]; then
	echo "run-split: HM_TEST_DATABASE_URL is not set; run 'make test-db' and export the URL it prints" >&2
	exit 2
fi

cd "$(dirname "$0")/.."

if [[ -z "${HM_TEST_KV_URLS:-}" ]]; then
	echo "run-split: HM_TEST_KV_URLS is not set; run 'make test-kv' and export the URLs it prints (split roles need HM_KV_URL)" >&2
	exit 2
fi
kv_url="${HM_TEST_KV_URLS%%,*}"

web_addr=127.0.0.1:8480
poller_addr=127.0.0.1:8482
coordinator_addr=127.0.0.1:8483
metrics_token=dev-metrics-token

schema="${HM_RUN_SPLIT_SCHEMA:-hm_run_split}"
if ! procs_check_schema "$schema" hm_run_split; then
	echo "run-split: HM_RUN_SPLIT_SCHEMA must start with hm_run_split (this script drops the schema); got: $schema" >&2
	exit 2
fi

procs_build
# Fake mode refuses a database that already has users, and a run should start empty: use a dedicated schema,
# recreated on every start (the same helper make e2e uses).
PROCS_DB_URL="$(procs_prepare_schema "$schema")" || {
	echo "run-split: could not prepare the schema" >&2
	exit 1
}
PROCS_KV_URL="$kv_url"
PROCS_WEB_ADDR="$web_addr"
PROCS_METRICS_TOKEN="$metrics_token"
PROCS_SESSION_KEY=dev-session-key

status=0
stopping=0

stop_all() {
	stopping=1
	procs_stop_all
}

trap 'stop_all; exit "$status"' INT TERM
trap 'stop_all' EXIT

# Web first: it migrates and seeds the demo accounts, which the poller and coordinator then pick up at once.
# HM_AGENT=1 switches the chat on for the web process; start the agent with make run-agent.
agent_env=()
if [[ "${HM_AGENT:-}" == 1 ]]; then agent_env=(HM_AGENT_URL=http://127.0.0.1:8484); fi
procs_start web "$web_addr" - ${agent_env[@]+"${agent_env[@]}"}
if ! procs_wait_healthy "$web_addr" 60; then
	echo "run-split: web did not become healthy" >&2
	status=1
	exit "$status"
fi
procs_start poller "$poller_addr" -
procs_start coordinator "$coordinator_addr" -
echo "run-split: web http://$web_addr, poller http://$poller_addr/healthz, coordinator http://$coordinator_addr/healthz" >&2

# Stop everything when any process exits, keeping the first non-zero status.
while ((stopping == 0)); do
	for i in "${!PROCS_PIDS[@]}"; do
		if ! kill -0 "${PROCS_PIDS[$i]}" 2>/dev/null; then
			rc=0
			wait "${PROCS_PIDS[$i]}" || rc=$?
			echo "run-split: ${PROCS_ROLES[$i]} exited with status $rc" >&2
			# None of the three should stop on its own, so even a clean exit is a failure.
			if ((status == 0)); then status=$rc; fi
			if ((status == 0)); then status=1; fi
			exit "$status"
		fi
	done
	sleep 1
done
