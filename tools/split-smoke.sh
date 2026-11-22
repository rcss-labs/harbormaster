#!/usr/bin/env bash
# Smoke-tests the three roles as real processes: web, poller (and a second poller) and coordinator, on fake AWS data,
# against Postgres and a real Valkey or Redis. It checks health, leases, kicks across processes, live focus, the snapshot
# cache, a poller taking over, and a store outage.
#
# Usage: tools/split-smoke.sh [--engine valkey|redis] [-h|--help]
#
# Options:
#   --engine valkey|redis  the store to run against (default valkey)
#
# Environment:
#   HM_TEST_DATABASE_URL    required; the Postgres URL that make test-db prints. The run uses its own schema there,
#                           hm_split_smoke_<engine>, recreated at the start. HM_SPLIT_SMOKE_SCHEMA may rename it, but the
#                           name must start with hm_split_smoke: anything else is refused, so a schema that holds other
#                           data is never dropped
#   HM_SPLIT_SMOKE_PORT     first of four consecutive local ports (default 8580): web, poller, second poller, coordinator
#
# Needs curl, docker, jq and python3. The script starts its own store container (hm-split-smoke-<engine>-<pid>, the images
# make test-kv pins, on a random local port), stops and starts it for the outage check, and removes it on exit. It never
# touches the containers make test-kv and make test-db created, so a run in progress elsewhere is not disturbed. No
# secret is printed.
#
# Exit codes: 0 every check passed, 1 a check failed (named on the last line), 2 usage error, missing prerequisite or a
# refused schema name.
set -euo pipefail

usage() {
	sed -n '2,/^set -euo/p' "$0" | sed '$d' | sed 's/^# \{0,1\}//'
}

die_usage() {
	echo "split-smoke: $1" >&2
	exit 2
}

engine=valkey
while (($# > 0)); do
	case "$1" in
	-h | --help)
		usage
		exit 0
		;;
	--engine)
		(($# >= 2)) || die_usage "--engine needs valkey or redis"
		engine="$2"
		shift 2
		;;
	--engine=*)
		engine="${1#--engine=}"
		shift
		;;
	*)
		echo "split-smoke: unknown argument: $1" >&2
		usage >&2
		exit 2
		;;
	esac
done

# Keep in step with the floor versions make test-kv pins.
case "$engine" in
valkey) image="valkey/valkey:7.2.5-alpine" ;;
redis) image="redis:7.0.15-alpine" ;;
*) die_usage "--engine must be valkey or redis, got: $engine" ;;
esac

for tool in curl docker jq go python3; do
	command -v "$tool" >/dev/null 2>&1 || die_usage "$tool is required but not installed"
done
[[ -n "${HM_TEST_DATABASE_URL:-}" ]] || die_usage "HM_TEST_DATABASE_URL is not set; run 'make test-db' and export the URL it prints"
docker info >/dev/null 2>&1 || die_usage "docker is not running"

schema="${HM_SPLIT_SMOKE_SCHEMA:-hm_split_smoke_$engine}"
[[ "$schema" =~ ^hm_split_smoke[a-z0-9_]*$ ]] || die_usage "HM_SPLIT_SMOKE_SCHEMA must start with hm_split_smoke (this script drops the schema); got: $schema"

base_port="${HM_SPLIT_SMOKE_PORT:-8580}"
[[ "$base_port" =~ ^[0-9]+$ ]] || die_usage "HM_SPLIT_SMOKE_PORT must be a number"
web_addr="127.0.0.1:$base_port"
poller_addr="127.0.0.1:$((base_port + 1))"
poller2_addr="127.0.0.1:$((base_port + 2))"
coordinator_addr="127.0.0.1:$((base_port + 3))"
for addr in "$web_addr" "$poller_addr" "$poller2_addr" "$coordinator_addr"; do
	if curl -s -o /dev/null --max-time 1 "http://$addr/healthz"; then
		die_usage "$addr is already in use; stop what listens there or set HM_SPLIT_SMOKE_PORT"
	fi
done

cd "$(dirname "$0")/.."

metrics_token="smoke-metrics-token"
password="harbormaster-e2e-1" # the seeded users' password in fake mode
workdir="$(mktemp -d)"
container="hm-split-smoke-$engine-$$"
kv_prefix="smoke$$"

pids=()
roles=()
current_check="setup"
checks_passed=0

# ---------------------------------------------------------------------------------------------------------------------
# Cleanup: stop every process, remove the private store container, delete the temporary files.

stop_pid() {
	local pid="$1" i
	kill -TERM "$pid" 2>/dev/null || return 0
	for ((i = 0; i < 40; i++)); do
		kill -0 "$pid" 2>/dev/null || return 0
		sleep 0.5
	done
	kill -KILL "$pid" 2>/dev/null || true
}

cleanup() {
	local status=$?
	trap - EXIT INT TERM
	local pid
	for pid in ${pids[@]+"${pids[@]}"}; do
		stop_pid "$pid"
	done
	for pid in ${pids[@]+"${pids[@]}"}; do
		wait "$pid" 2>/dev/null || true
	done
	docker rm -f "$container" >/dev/null 2>&1 || true
	if ((status != 0)) && [[ -d "$workdir" ]]; then
		local f
		for f in "$workdir"/*.log; do
			[[ -f "$f" ]] || continue
			echo "---- last lines of $(basename "$f") ----" >&2
			tail -n 15 "$f" >&2 || true
		done
	fi
	rm -rf "$workdir"
	exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

fail() {
	echo "FAIL $current_check: $1" >&2
	exit 1
}

pass() {
	checks_passed=$((checks_passed + 1))
	echo "ok   $current_check${1:+: $1}"
}

# ---------------------------------------------------------------------------------------------------------------------
# Helpers.

# metrics ADDR prints the /metrics body of a process.
metrics() {
	curl -s --max-time 5 -H "Authorization: Bearer $metrics_token" "http://$1/metrics" || true
}

# metric_sum ADDR NAME [FILTER...] prints the sum of the samples of a series whose line contains every filter.
metric_sum() {
	local addr="$1" name="$2"
	shift 2
	metrics "$addr" | awk -v name="$name" -v filters="${*:-}" '
		BEGIN { n = split(filters, f, " ") }
		index($0, name "{") == 1 || index($0, name " ") == 1 {
			for (i = 1; i <= n; i++) if (index($0, f[i]) == 0) next
			sum += $NF; seen = 1
		}
		END { printf "%s\n", seen ? sum : "none" }'
}

# wait_until SECONDS DESCRIPTION COMMAND... runs the command every 0.5 s until it succeeds or the time is up.
wait_until() {
	local seconds="$1" what="$2" i
	shift 2
	for ((i = 0; i < seconds * 2; i++)); do
		if "$@" >/dev/null 2>&1; then return 0; fi
		sleep 0.5
	done
	fail "timed out after ${seconds}s waiting for $what"
}

# start_role ROLE ADDR LOGNAME [EXTRA_ENV...] starts one process; its pid is the last entry of pids.
start_role() {
	local role="$1" addr="$2" log="$3"
	shift 3
	env HM_FAKE_AWS=1 HM_COOKIE_SECURE=false HM_SESSION_KEY=smoke-session-key HM_METRICS_TOKEN="$metrics_token" \
		HM_DATABASE_URL="$db_url" HM_KV_URL="$kv_url" HM_KV_PREFIX="$kv_prefix" HM_BASE_URL="http://$web_addr" \
		HM_LISTEN="$addr" HM_POLL_JITTER=1ms HM_POLL_IDLE_AFTER=0 HM_FAKE_OFFHOURS_DEMO=false HM_FAKE_NOTIFY_DEMO=false \
		HM_POLL_SERVICES=2s HM_POLL_RUNNING_TASKS=2s HM_LOG_LEVEL=info "$@" \
		bin/harbormaster serve --role="$role" >"$workdir/$log.log" 2>&1 &
	pids+=("$!")
	roles+=("$role")
}

healthz_ok() { [[ "$(curl -s --max-time 2 "http://$1/healthz" || true)" == "ok" ]]; }

# api METHOD PATH [JSON] prints the body; the status goes to $workdir/status. It uses the signed-in cookie jar.
api() {
	local method="$1" path="$2" body="${3:-}"
	local args=(-s --max-time 10 -o "$workdir/body" -w '%{http_code}' -X "$method" -b "$workdir/jar" -c "$workdir/jar")
	[[ -n "${csrf:-}" ]] && args+=(-H "X-CSRF-Token: $csrf")
	[[ -n "$body" ]] && args+=(-H 'Content-Type: application/json' -d "$body")
	curl "${args[@]}" "http://$web_addr$path" >"$workdir/status" || echo 000 >"$workdir/status"
	cat "$workdir/body"
}

status_is() { [[ "$(cat "$workdir/status")" == "$1" ]]; }

# focus_answer [open] posts one heartbeat for the focused service and prints the JSON answer.
focus_answer() {
	local open=false
	[[ "${1:-}" == "open" ]] && open=true
	api POST /api/focus "{\"kind\":\"service\",\"id\":\"$service_id\",\"open\":$open}"
}

# now_ms prints the current time in epoch milliseconds.
now_ms() { python3 -c 'import time; print(int(time.time() * 1000))'; }

# iso_ms converts an RFC 3339 time to epoch milliseconds (0 when it cannot be read).
iso_ms() {
	python3 -c 'import sys, datetime
try:
    t = datetime.datetime.fromisoformat(sys.argv[1].replace("Z", "+00:00"))
    print(int(t.timestamp() * 1000))
except ValueError:
    print(0)' "$1"
}

# focus_live_since T succeeds when a heartbeat answers live with a read time strictly after T (epoch milliseconds, taken
# after the event that is being waited out), so a read made before the kill or the outage never counts.
focus_live_since() {
	local since="$1" answer state at
	answer="$(focus_answer)"
	state="$(jq -r '.state // empty' <<<"$answer" 2>/dev/null || true)"
	at="$(jq -r '.refreshed_at // empty' <<<"$answer" 2>/dev/null || true)"
	[[ "$state" == "live" && -n "$at" ]] || return 1
	(($(iso_ms "$at") > since))
}

focus_unavailable() { [[ "$(jq -r '.state // empty' <<<"$(focus_answer)" 2>/dev/null || true)" == "unavailable" ]]; }

kv_up_is() { [[ "$(metric_sum "$1" harbormaster_kv_up)" == "$2" ]]; }

# ---------------------------------------------------------------------------------------------------------------------
# Setup: the binary, the schema, the store container.

current_check="setup"
mkdir -p bin
go build -o bin/harbormaster ./cmd/harbormaster || fail "go build failed"
db_url="$(HM_E2E_SCHEMA="$schema" go run ./tools/e2eschema)" || fail "could not prepare the schema $schema"

# A fixed local port: a container that is stopped and started again keeps its published port only when it was fixed.
host_port="$(python3 -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])')"
[[ "$host_port" =~ ^[0-9]+$ ]] || die_usage "could not find a free local port (python3 is needed)"
docker run -d --name "$container" -p "127.0.0.1:$host_port:6379" "$image" >/dev/null || die_usage "could not start $image (is the image available?)"
kv_url="redis://127.0.0.1:$host_port"
kv_ready() { [[ "$(docker exec "$container" sh -c 'redis-cli ping 2>/dev/null || valkey-cli ping 2>/dev/null' || true)" == "PONG" ]]; }
wait_until 30 "the $engine container to answer PING" kv_ready

# Web first: it migrates and seeds the demo accounts, which the poller and coordinator then pick up at once. The
# coordinator reconciles off-hours schedules only every 300 s, so a change inside the first minutes can only come from a
# kick.
start_role web "$web_addr" web
wait_until 90 "web /healthz" healthz_ok "$web_addr"
start_role poller "$poller_addr" poller
poller1_pid="${pids[${#pids[@]} - 1]}"
start_role coordinator "$coordinator_addr" coordinator HM_OFFHOURS_INTERVAL=300s
wait_until 60 "poller /healthz" healthz_ok "$poller_addr"
wait_until 60 "coordinator /healthz" healthz_ok "$coordinator_addr"

# ---------------------------------------------------------------------------------------------------------------------
# 1. Health.

current_check="1 health"
status_poller_me="$(curl -s -o /dev/null -w '%{http_code}' --max-time 3 "http://$poller_addr/api/me" || true)"
[[ "$status_poller_me" == "404" ]] || fail "poller /api/me = $status_poller_me, want 404"
healthz_ok "$web_addr" && healthz_ok "$poller_addr" && healthz_ok "$coordinator_addr" || fail "/healthz is not ok on all three"
pass "/healthz ok on web, poller and coordinator; poller /api/me is 404"

# Sign in as the seeded user and pick the service the focus checks use.
: >"$workdir/jar"
csrf=""
login="$(api POST /api/auth/login "{\"username\":\"sam\",\"password\":\"$password\"}")"
status_is 200 || fail "sign-in answered $(cat "$workdir/status")"
csrf="$(jq -r '.user.csrf_token // empty' <<<"$login")"
[[ -n "$csrf" ]] || fail "sign-in answered without a CSRF token"
services="$(api GET /api/services)"
service_id="$(jq -r '[.items[] | select(.name == "api" and .cluster.name == "staging-web")][0].id // empty' <<<"$services")"
# The off-hours check uses a service the seeded schedule does not select (the staging services).
sched_service_id="$(jq -r '[.items[] | select(.name == "artifact-proxy")][0].id // empty' <<<"$services")"
service_tag_id="$(jq -r '[.items[] | select(.name == "artifact-proxy")][0].tag.id // empty' <<<"$services")"
cluster_id="$(jq -r '[.items[] | select(.name == "api" and .cluster.name == "staging-web")][0].cluster.id // empty' <<<"$services")"
[[ -n "$service_id" && -n "$cluster_id" && -n "$service_tag_id" && -n "$sched_service_id" ]] || fail "the seeded services are not in /api/services"
accounts="$(api GET /api/accounts)"
slice_count="$(jq '[.items[] | select(.enabled) | .regions | length] | add' <<<"$accounts")"
[[ "$slice_count" =~ ^[0-9]+$ && "$slice_count" -gt 0 ]] || fail "could not count the enabled account x region pairs"

# ---------------------------------------------------------------------------------------------------------------------
# 2. Ownership.

current_check="2 leases"
owned_all() { [[ "$(metric_sum "$poller_addr" harbormaster_lease_slices_owned)" == "$slice_count" ]]; }
wait_until 30 "the poller to own all $slice_count slices" owned_all
coordinator_active() { [[ "$(metric_sum "$coordinator_addr" harbormaster_coordinator_active)" == "1" ]]; }
wait_until 30 "harbormaster_coordinator_active 1 on the coordinator" coordinator_active
web_owned="$(metric_sum "$web_addr" harbormaster_lease_slices_owned)"
[[ "$web_owned" == "none" || "$web_owned" == "0" ]] || fail "web owns $web_owned slices"
pass "poller owns $slice_count of $slice_count slices; coordinator active; web owns none"

# ---------------------------------------------------------------------------------------------------------------------
# 3. The store.

current_check="3 store"
for addr in "$web_addr" "$poller_addr" "$coordinator_addr"; do
	wait_until 20 "harbormaster_kv_up 1 on $addr" kv_up_is "$addr" 1
done
pass "harbormaster_kv_up 1 on all three ($engine)"

# ---------------------------------------------------------------------------------------------------------------------
# 4. Focus: the open on web reaches the poller through the store, and the poller's read comes back as live.

current_check="4 focus"
opened_at="$(now_ms)"
focus_answer open >/dev/null
status_is 200 || fail "POST /api/focus answered $(cat "$workdir/status")"
wait_until 15 "a live heartbeat with a read time" focus_live_since "$opened_at"
handled() { [[ "$(metric_sum "$poller_addr" harbormaster_pollnow_signals_total 'result="handled"')" =~ ^[1-9] ]]; }
wait_until 10 "the poller to count a handled poll-now signal" handled
pass "focus is live; the poller handled the poll-now signal"

# ---------------------------------------------------------------------------------------------------------------------
# 5. Snapshot cache.

current_check="5 snapshot cache"
cache_hit() {
	api GET "/api/services/$service_id" >/dev/null
	api GET "/api/services/$service_id" >/dev/null
	[[ "$(metric_sum "$web_addr" harbormaster_snapshot_cache_requests_total 'model="service"' 'result="hit"')" =~ ^[1-9] ]]
}
wait_until 15 "a snapshot cache hit for the service page on web" cache_hit
pass "web counted a cache hit for the service page"

# ---------------------------------------------------------------------------------------------------------------------
# 6. A kick crosses processes: saving a schedule on web makes the coordinator's reconciler enrol the service at once.

current_check="6 kick"
schedule="$(jq -n --argjson tag "$service_tag_id" '{
	name: "smoke schedule", enabled: true, timezone: "UTC",
	awake: [range(7) | [range(24) | true]],
	tags: [{kind: "harbormaster", key: "", value: "", tag_id: $tag}], excluded_service_ids: []}')"
api POST /api/offhours/schedules "$schedule" >/dev/null
status_is 200 || status_is 201 || fail "saving the schedule answered $(cat "$workdir/status"): $(cat "$workdir/body")"
enrolled() {
	[[ "$(jq -r '.enrolled // false' <<<"$(api GET "/api/services/$sched_service_id/offhours")")" == "true" ]]
}
wait_until 5 "the coordinator's reconciler to enrol the service within 5 s of the save" enrolled
pass "the schedule saved on web was reconciled by the coordinator within 5 s"

# ---------------------------------------------------------------------------------------------------------------------
# 7. A poller takes over: start a second one, SIGTERM the first.

current_check="7 takeover"
start_role poller "$poller2_addr" poller2
wait_until 60 "second poller /healthz" healthz_ok "$poller2_addr"
kill -TERM "$poller1_pid"
killed_at="$(now_ms)"
poller2_owns_all() { [[ "$(metric_sum "$poller2_addr" harbormaster_lease_slices_owned)" == "$slice_count" ]]; }
wait_until 10 "the second poller to own all $slice_count slices" poller2_owns_all
wait_until 15 "the focused service to answer live again" focus_live_since "$killed_at"
pass "the second poller owns all $slice_count slices; the focused service is live again"

# ---------------------------------------------------------------------------------------------------------------------
# 8. The store goes away and comes back.

current_check="8 store outage"
docker stop "$container" >/dev/null || fail "could not stop the store container"
wait_until 15 "harbormaster_kv_up 0 on web" kv_up_is "$web_addr" 0
api GET "/api/clusters/$cluster_id" >/dev/null
status_is 200 || fail "GET /api/clusters/{id} answered $(cat "$workdir/status") with the store down"
wait_until 10 "a focus heartbeat to answer unavailable" focus_unavailable
loop_runs() { metric_sum "$poller2_addr" harbormaster_poll_loop_runs_total 'tier="services"'; }
runs_before="$(loop_runs)"
cycled() { [[ "$(loop_runs)" != "$runs_before" ]]; }
wait_until 15 "the poller's services tier to keep cycling" cycled
docker start "$container" >/dev/null || fail "could not start the store container"
restored_at="$(now_ms)"
wait_until 20 "harbormaster_kv_up 1 on web" kv_up_is "$web_addr" 1
wait_until 20 "the focused service to answer live again" focus_live_since "$restored_at"
pass "pages stayed up, focus said unavailable, polling continued; everything came back with the store"

echo "split-smoke: $checks_passed of 8 checks passed ($engine)"
