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
#                         hm_run_split (HM_RUN_SPLIT_SCHEMA overrides), recreated on every start
#   HM_TEST_KV_URLS       optional; the first URL is used as HM_KV_URL so kicks cross processes. Without it kicks
#                         stay inside each process and the jobs run at their intervals
#
# Sign in at http://127.0.0.1:8480 as sam / harbormaster-e2e-1. Ctrl-C stops all three.
# Exit codes: 0 stopped by Ctrl-C, 2 usage error or missing variable, otherwise the first non-zero status of a process.
set -euo pipefail

usage() {
	sed -n '2,/^set -euo/p' "$0" | sed '$d' | sed 's/^# \{0,1\}//'
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

kv_url=""
if [[ -n "${HM_TEST_KV_URLS:-}" ]]; then
	kv_url="${HM_TEST_KV_URLS%%,*}"
else
	echo "run-split: HM_TEST_KV_URLS is not set (run 'make test-kv'): kicks and live refresh will not cross processes" >&2
fi

web_addr=127.0.0.1:8480
poller_addr=127.0.0.1:8482
coordinator_addr=127.0.0.1:8483
metrics_token=dev-metrics-token

mkdir -p bin
go build -o bin/harbormaster ./cmd/harbormaster
# Fake mode refuses a database that already has users, and a run should start empty: use a dedicated schema,
# recreated on every start (the same helper make e2e uses).
db_url="$(HM_E2E_SCHEMA="${HM_RUN_SPLIT_SCHEMA:-hm_run_split}" go run ./tools/e2eschema)" || {
	echo "run-split: could not prepare the schema" >&2
	exit 1
}

pids=()
roles=()

# start runs one role with its own listen address, prefixing every log line with the role.
start() {
	local role="$1" addr="$2"
	HM_FAKE_AWS=1 HM_COOKIE_SECURE=false HM_SESSION_KEY=dev-session-key HM_METRICS_TOKEN="$metrics_token" \
		HM_DATABASE_URL="$db_url" HM_KV_URL="$kv_url" HM_BASE_URL="http://$web_addr" \
		HM_LISTEN="$addr" bin/harbormaster serve --role="$role" > >(sed -u "s/^/[$role] /") 2>&1 &
	pids+=("$!")
	roles+=("$role")
}

# wait_healthy waits up to 60 s for an address to answer /healthz with 200.
wait_healthy() {
	local addr="$1" i
	for ((i = 0; i < 120; i++)); do
		if [[ "$(curl -s -o /dev/null -w '%{http_code}' "http://$addr/healthz" || true)" == "200" ]]; then
			return 0
		fi
		sleep 0.5
	done
	return 1
}

status=0
stopping=0

stop_all() {
	local pid i
	stopping=1
	for pid in "${pids[@]}"; do
		kill -TERM "$pid" 2>/dev/null || true
	done
	# Give each process its 15 s shutdown, then force what is left.
	for ((i = 0; i < 40; i++)); do
		local alive=0
		for pid in "${pids[@]}"; do
			if kill -0 "$pid" 2>/dev/null; then alive=1; fi
		done
		((alive == 0)) && break
		sleep 0.5
	done
	for pid in "${pids[@]}"; do
		kill -KILL "$pid" 2>/dev/null || true
	done
	for pid in "${pids[@]}"; do
		wait "$pid" 2>/dev/null || true
	done
}

trap 'stop_all; exit "$status"' INT TERM
trap 'stop_all' EXIT

# Web first: it migrates and seeds the demo accounts, which the poller and coordinator then pick up at once.
start web "$web_addr"
if ! wait_healthy "$web_addr"; then
	echo "run-split: web did not become healthy" >&2
	status=1
	exit "$status"
fi
start poller "$poller_addr"
start coordinator "$coordinator_addr"
echo "run-split: web http://$web_addr, poller http://$poller_addr/healthz, coordinator http://$coordinator_addr/healthz" >&2

# Stop everything when any process exits, keeping the first non-zero status.
while ((stopping == 0)); do
	for i in "${!pids[@]}"; do
		if ! kill -0 "${pids[$i]}" 2>/dev/null; then
			rc=0
			wait "${pids[$i]}" || rc=$?
			echo "run-split: ${roles[$i]} exited with status $rc" >&2
			if ((status == 0)); then status=$rc; fi
			exit "$status"
		fi
	done
	sleep 1
done
