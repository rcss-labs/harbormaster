#!/usr/bin/env bash
# End-to-end check of the MCP gateway and access tokens against the floci estate.
#
# Usage: tools/floci/mcp-e2e.sh [--help]
#   HM_FLOCI_PORT       port of the token-enabled Harbormaster (default 8491, see serve-tokens.sh)
#   HM_FLOCI_MCP_PORT   port the script starts bin/harbormaster-mcp on (default 8495)
#
# Needs bin/harbormaster and bin/harbormaster-mcp (make build), the floci containers (make floci-up floci-seed) and
# make prepare's database (tools/floci/prepare.sh). When nothing answers on the Harbormaster port the script starts
# tools/floci/serve-tokens.sh itself (HM_API_TOKENS and HM_OAUTH on) and stops it on exit; a server that is already
# up must have both switches on. It runs connect.sh, starts the gateway, creates tokens for admin and omer, checks
# the gateway, the read-only gate, per-user visibility, OAuth discovery and revocation, then stops what it started.
# Secrets (admin password, omer password, tokens) are kept in mode-600 temp files and never printed.
# Prints "ok   <check>", "skip <check>: <reason>" or "FAIL <check>: <reason>" per check.
# Exit codes: 0 all checks passed, 1 a check failed or setup failed, 2 refused (non-local environment) or bad usage.
set -euo pipefail
umask 077

case "${1:-}" in
  -h | --help) sed -n '2,17p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
  "") ;;
  *) echo "usage: tools/floci/mcp-e2e.sh [--help]" >&2; exit 2 ;;
esac

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
export HM_FLOCI_PORT="${HM_FLOCI_PORT:-8491}"
# shellcheck source=server-env.sh
. "$HERE/server-env.sh" || exit 2

HM_BIN="$ROOT/bin/harbormaster"
MCP_BIN="$ROOT/bin/harbormaster-mcp"
PASSWORD_FILE="$HERE/.admin-password"
BASE="$HM_BASE_URL"
MCP_PORT="${HM_FLOCI_MCP_PORT:-8495}"
MCP_BASE="http://127.0.0.1:${MCP_PORT}"

for bin in "$HM_BIN" "$MCP_BIN"; do
  [ -x "$bin" ] || { echo "mcp-e2e: $bin is missing; run make build" >&2; exit 1; }
done
for tool in curl jq openssl; do
  command -v "$tool" >/dev/null || { echo "mcp-e2e: $tool is required" >&2; exit 1; }
done
[ -s "$PASSWORD_FILE" ] || { echo "mcp-e2e: no admin password file; run tools/floci/prepare.sh first" >&2; exit 1; }

WORK="$(mktemp -d)"
SERVER_PID=""
GATEWAY_PID=""
cleanup() {
  local pid
  for pid in "$GATEWAY_PID" "$SERVER_PID"; do
    [ -n "$pid" ] || continue
    kill "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
  done
  rm -rf "$WORK"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

PASS=0 FAILED=0 SKIPPED=0
ok() { PASS=$((PASS + 1)); echo "ok   $1"; }
fail() { FAILED=$((FAILED + 1)); echo "FAIL $1: $2"; }
skip() { SKIPPED=$((SKIPPED + 1)); echo "skip $1: $2"; }
die() { echo "mcp-e2e: $*" >&2; exit 1; }

STATUS=""
# check LABEL CONDITION-RESULT(0|1) REASON: ok when the condition command succeeded.
check() { if [ "$2" = 0 ]; then ok "$1"; else fail "$1" "$3"; fi; }
expect_status() { # label expected [reason-suffix]
  if [ "$STATUS" = "$2" ]; then ok "$1"; else fail "$1" "expected HTTP $2, got $STATUS ${3:-}"; fi
}

# ---- session (cookie + CSRF) helpers -------------------------------------------------------------------------------

# sreq JAR METHOD PATH [JSON-BODY-FILE]: sets STATUS, body in $WORK/body.
sreq() {
  local jar=$1 method=$2 path=$3 body=${4:-}
  local args=(-sS -o "$WORK/body" -w '%{http_code}' -X "$method" -b "$jar" -c "$jar")
  [ -f "$jar.csrf" ] && args+=(-H "X-CSRF-Token: $(cat "$jar.csrf")")
  [ -n "$body" ] && args+=(-H 'Content-Type: application/json' --data-binary "@$body")
  STATUS=$(curl "${args[@]}" "$BASE$path") || STATUS=000
}
jfile() { local f; f=$(mktemp "$WORK/j.XXXXXX"); printf '%s' "$1" >"$f"; echo "$f"; }

# login_user USER PASSWORD-FILE JAR: signs in and stores the CSRF token next to the jar.
login_user() {
  local user=$1 pwfile=$2 jar=$3
  jq -n --arg u "$user" --rawfile p "$pwfile" '{username:$u,password:($p|rtrimstr("\n"))}' >"$WORK/login.json"
  : >"$jar"
  rm -f "$jar.csrf"
  sreq "$jar" POST /api/auth/login "$WORK/login.json"
  rm -f "$WORK/login.json"
  [ "$STATUS" = 200 ] || return 1
  sreq "$jar" GET /api/me
  jq -r '.csrf_token' "$WORK/body" >"$jar.csrf"
}

# create_token JAR NAME HEADER-FILE: creates a 1-day token, writes "Authorization: Bearer <secret>" to HEADER-FILE
# and sets TOKEN_ID. The secret never reaches stdout or a command line.
create_token() {
  local jar=$1 name=$2 hdr=$3
  jq -n --arg n "$name" '{name:$n,expires_in_days:1}' >"$WORK/tok.json"
  sreq "$jar" POST /api/tokens "$WORK/tok.json"
  rm -f "$WORK/tok.json"
  if [ "$STATUS" != 200 ] && [ "$STATUS" != 201 ]; then
    echo "create token $name -> HTTP $STATUS $(jq -r '.message // .error // empty' "$WORK/body" 2>/dev/null)" >&2
    return 1
  fi
  printf 'Authorization: Bearer %s\n' "$(jq -r '.secret' "$WORK/body")" >"$hdr"
  TOKEN_ID=$(jq -r '.token.id' "$WORK/body")
  rm -f "$WORK/body"
}

# delete_stale_tokens JAR: removes tokens left behind by an earlier, interrupted run.
delete_stale_tokens() {
  local jar=$1 id
  sreq "$jar" GET /api/tokens
  [ "$STATUS" = 200 ] || return 0
  for id in $(jq -r '.items[]? | select(.name | startswith("mcp-e2e")) | .id' "$WORK/body"); do
    sreq "$jar" DELETE "/api/tokens/$id"
  done
}

# ---- bearer-token helpers against Harbormaster ---------------------------------------------------------------------

# hm_bearer HEADER-FILE METHOD PATH [JSON-BODY] [extra curl args...]
hm_bearer() {
  local hdr=$1 method=$2 path=$3 body=${4:-}
  shift 3
  [ "$#" -gt 0 ] && shift
  local args=(-sS -o "$WORK/body" -w '%{http_code}' -X "$method")
  [ -n "$hdr" ] && args+=(-H "@$hdr")
  [ -n "$body" ] && args+=(-H 'Content-Type: application/json' --data-binary "$body")
  STATUS=$(curl "${args[@]}" "$@" "$BASE$path") || STATUS=000
}
body_has() { grep -q "$1" "$WORK/body" 2>/dev/null; }

# ---- MCP helpers ----------------------------------------------------------------------------------------------------

# parse_rpc FILE: the JSON-RPC message of a response that is plain JSON or an SSE stream of "data:" lines.
parse_rpc() {
  if grep -q '^data:' "$1" 2>/dev/null; then
    sed -n 's/^data: \{0,1\}//p' "$1" | jq -c 'select(type == "object" and (has("result") or has("error")))' 2>/dev/null | tail -n 1 || true
  else
    jq -c '.' "$1" 2>/dev/null || true
  fi
  return 0
}

RPC=""
# mcp_post HEADER-FILE-or-empty BODY [extra curl args...]: sets STATUS, RPC and the response headers file $WORK/mcp.hdr.
mcp_post() {
  local hdr=$1 body=$2
  shift 2
  local args=(-sS -o "$WORK/mcp.raw" -D "$WORK/mcp.hdr" -w '%{http_code}' -X POST
    -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' --data-binary "$body")
  [ -n "$hdr" ] && args+=(-H "@$hdr")
  STATUS=$(curl "${args[@]}" "$@" "$MCP_BASE/mcp") || STATUS=000
  RPC=$(parse_rpc "$WORK/mcp.raw")
}

RPC_ERR="" IS_ERR="" TEXT=""
# call_tool HEADER-FILE TOOL ARGS-JSON: sets STATUS, RPC_ERR (JSON-RPC error), IS_ERR (tool isError) and TEXT.
call_tool() {
  local body
  body=$(jq -nc --arg n "$2" --argjson a "$3" '{jsonrpc:"2.0",id:2,method:"tools/call",params:{name:$n,arguments:$a}}')
  mcp_post "$1" "$body"
  RPC_ERR=$(jq -r '.error.message // empty' <<<"$RPC" 2>/dev/null || true)
  IS_ERR=$(jq -r '.result.isError // false' <<<"$RPC" 2>/dev/null || true)
  TEXT=$(jq -r '[.result.content[]? | select(.type == "text") | .text] | join("")' <<<"$RPC" 2>/dev/null || true)
}
rpc_simple() { # header-file method [params-json]
  local params=${3:-}
  [ -n "$params" ] || params='{}'
  mcp_post "$1" "$(jq -nc --arg m "$2" --argjson p "$params" '{jsonrpc:"2.0",id:1,method:$m,params:$p}')"
}
# tj JQ-FILTER: runs a filter over the last tool TEXT.
tj() { jq -r "$1" <<<"$TEXT" 2>/dev/null || true; }

# ---- start the servers ---------------------------------------------------------------------------------------------

wait_for() { # url seconds
  local attempt
  for attempt in $(seq 1 "$2"); do
    [ "$attempt" -gt 0 ] || break
    curl -fsS -o /dev/null "$1" 2>/dev/null && return 0
    sleep 1
  done
  return 1
}

if curl -fsS -o /dev/null "$BASE/healthz" 2>/dev/null; then
  echo "mcp-e2e: using the Harbormaster already running on $BASE"
else
  "$HERE/serve-tokens.sh" >"$WORK/server.log" 2>&1 &
  SERVER_PID=$!
  if ! wait_for "$BASE/healthz" 60; then
    tail -n 5 "$WORK/server.log" >&2 || true
    die "serve-tokens.sh did not answer on $BASE/healthz within 60 s (is make floci-up and tools/floci/prepare.sh done?)"
  fi
  echo "mcp-e2e: started serve-tokens.sh on $BASE"
fi

"$HERE/connect.sh" >/dev/null || die "connect.sh failed"

if curl -fsS -o /dev/null "$MCP_BASE/healthz" 2>/dev/null; then
  echo "mcp-e2e: something already answers on $MCP_BASE; set HM_FLOCI_MCP_PORT to a free port" >&2
  exit 2
fi
HM_MCP_LISTEN="127.0.0.1:${MCP_PORT}" HM_MCP_UPSTREAM_URL="$BASE" HM_MCP_PUBLIC_URL="$MCP_BASE" \
  HM_MCP_TRUSTED_PROXY=false HM_MCP_AUTHORIZE_URL="$BASE" "$MCP_BIN" serve >"$WORK/gateway.log" 2>&1 &
GATEWAY_PID=$!
wait_for "$MCP_BASE/healthz" 30 || { tail -n 5 "$WORK/gateway.log" >&2 || true; die "the gateway did not answer on $MCP_BASE/healthz"; }
echo "mcp-e2e: gateway on $MCP_BASE -> $BASE"

# ---- sessions and tokens -------------------------------------------------------------------------------------------

ADMIN_JAR="$WORK/admin.jar"
OMER_JAR="$WORK/omer.jar"
ADMIN_HDR="$WORK/admin.hdr"
OMER_HDR="$WORK/omer.hdr"

login_user admin "$PASSWORD_FILE" "$ADMIN_JAR" || die "admin sign-in failed (HTTP $STATUS); if the database was recreated, run make floci-down first"
sreq "$ADMIN_JAR" GET /api/tokens
if [ "$STATUS" = 404 ]; then
  echo "mcp-e2e: Harbormaster on $BASE has access tokens off; start it with tools/floci/serve-tokens.sh" >&2
  exit 2
fi
[ "$STATUS" = 200 ] || die "GET /api/tokens answered HTTP $STATUS"
delete_stale_tokens "$ADMIN_JAR"

# omer's password is not kept anywhere, so the admin resets it (which also ends omer's tokens, so do it first), omer
# signs in with the temporary password and completes the forced change.
sreq "$ADMIN_JAR" GET /api/users
OMER_ID=$(jq -r '.items[]? | select(.username == "omer") | .id' "$WORK/body" | head -n 1)
[ -n "$OMER_ID" ] || die "user omer not found; run tools/floci/connect.sh"
sreq "$ADMIN_JAR" POST "/api/users/$OMER_ID/reset-password"
[ "$STATUS" = 200 ] || die "resetting omer's password answered HTTP $STATUS"
jq -j '.temporary_password' "$WORK/body" >"$WORK/omer.temp"
rm -f "$WORK/body"
login_user omer "$WORK/omer.temp" "$OMER_JAR" || die "omer sign-in with the temporary password failed (HTTP $STATUS)"
sreq "$OMER_JAR" GET /api/me
if [ "$(jq -r '.must_change_password' "$WORK/body")" = true ]; then
  openssl rand -hex 16 | tr -d '\n' >"$WORK/omer.pass"
  jq -n --rawfile c "$WORK/omer.temp" --rawfile n "$WORK/omer.pass" '{current:$c,new:$n}' >"$WORK/pw.json"
  sreq "$OMER_JAR" POST /api/auth/password "$WORK/pw.json"
  rm -f "$WORK/pw.json"
  [ "$STATUS" = 200 ] || [ "$STATUS" = 204 ] || die "omer's password change answered HTTP $STATUS"
  sreq "$OMER_JAR" GET /api/me
  jq -r '.csrf_token' "$WORK/body" >"$OMER_JAR.csrf"
fi

echo "== tokens"
if create_token "$ADMIN_JAR" "mcp-e2e admin" "$ADMIN_HDR"; then
  ADMIN_TOKEN_ID=$TOKEN_ID
  ok "create token for admin (POST /api/tokens)"
else
  fail "create token for admin" "see above"
  echo "mcp-e2e: cannot continue without a token" >&2
  exit 1
fi
if create_token "$OMER_JAR" "mcp-e2e omer" "$OMER_HDR"; then
  ok "create token for omer"
else
  fail "create token for omer" "see above"
  echo "mcp-e2e: cannot continue without omer's token" >&2
  exit 1
fi
sreq "$ADMIN_JAR" GET /api/tokens
LISTED=$(jq -r '[.items[] | select(.name == "mcp-e2e admin")] | length' "$WORK/body")
check "created token is listed and the list never carries a secret" "$([ "$LISTED" = 1 ] && ! jq -e '.. | objects | select(has("secret"))' "$WORK/body" >/dev/null 2>&1 && echo 0 || echo 1)" "listed=$LISTED"

# ---- gateway basics -------------------------------------------------------------------------------------------------

echo "== gateway"
STATUS=$(curl -sS -o "$WORK/body" -w '%{http_code}' "$MCP_BASE/healthz") || STATUS=000
expect_status "gateway /healthz" 200

rpc_simple "$ADMIN_HDR" initialize '{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"mcp-e2e","version":"1"}}'
if [ "$STATUS" = 200 ] && [ -n "$(jq -r '.result.serverInfo.name // empty' <<<"$RPC")" ] && [ -z "$(jq -r '.error // empty' <<<"$RPC")" ]; then
  ok "MCP initialize (server $(jq -r '.result.serverInfo.name' <<<"$RPC"))"
else
  fail "MCP initialize" "HTTP $STATUS, no serverInfo in the answer"
fi

EXPECTED_TOOLS="estimate_log_search get_arm64_readiness get_capacity_analytics get_cluster get_cost_analytics get_delivery_analytics get_desired_count get_image_security get_operations_analytics get_overview get_reliability_analytics get_revision get_scaling get_service get_service_logs get_service_metrics get_service_offhours get_service_security get_service_timeline get_target_health get_task_definition get_timeline_task get_vpc_tree list_alerts list_clusters list_log_search_services list_networks list_region_vpcs list_revisions list_security list_service_operations list_service_tasks list_services list_storage list_tasks search search_config search_logs whoami"
rpc_simple "$ADMIN_HDR" tools/list
GOT_TOOLS=$(jq -r '[.result.tools[]?.name] | sort | join(" ")' <<<"$RPC" 2>/dev/null || true)
GOT_COUNT=$(jq -r '.result.tools | length' <<<"$RPC" 2>/dev/null || echo 0)
if [ "$GOT_TOOLS" = "$EXPECTED_TOOLS" ]; then
  ok "tools/list returns exactly the 39 expected tools"
else
  fail "tools/list returns exactly the 39 expected tools" "got $GOT_COUNT; diff: $(diff <(tr ' ' '\n' <<<"$EXPECTED_TOOLS") <(tr ' ' '\n' <<<"$GOT_TOOLS") | grep '^[<>]' | tr '\n' ' ')"
fi
# The hard-coded list must also match the tool table in the source, so a new tool cannot slip past this check.
if [ -d "$ROOT/internal/mcpgw" ]; then
  SRC_TOOLS=$({
    grep -hoE '^[[:space:]]+Name: "[a-z0-9_]+", Title:' "$ROOT"/internal/mcpgw/tools_*.go | sed -E 's/.*Name: "([a-z0-9_]+)".*/\1/'
    grep -ho 'analyticsTool("[a-z_]*"' "$ROOT/internal/mcpgw/tools_insight.go" | sed -E 's/.*\("(.*)"/\1/'
  } | sort | tr '\n' ' ' | sed 's/ $//')
  if [ "$SRC_TOOLS" = "$EXPECTED_TOOLS" ]; then ok "expected tool list matches internal/mcpgw source"; else fail "expected tool list matches internal/mcpgw source" "update EXPECTED_TOOLS in mcp-e2e.sh"; fi
fi
WRITEY=$(tr ' ' '\n' <<<"$GOT_TOOLS" | grep -E '(^|_)(create|delete|remove|update|edit|set|put|post|write|add|apply|restart|scale|deploy|rollback|stop|start|sleep|wake|exec|terminal|revoke|reset|register|run|kill|terminate|upload)(_|$)' | tr '\n' ' ' || true)
check "no write-sounding tool names" "$([ -z "$WRITEY" ] && echo 0 || echo 1)" "found: $WRITEY"

# ---- auth negatives through the gateway ----------------------------------------------------------------------------

echo "== gateway auth"
INIT_BODY='{"jsonrpc":"2.0","id":1,"method":"tools/list"}'
mcp_post "" "$INIT_BODY"
expect_status "no Authorization header -> 401" 401
check "401 carries WWW-Authenticate with resource_metadata" "$(grep -iq '^www-authenticate:.*resource_metadata=' "$WORK/mcp.hdr" && echo 0 || echo 1)" "headers: $(grep -i '^www-authenticate' "$WORK/mcp.hdr" || echo none)"

printf 'Authorization: Bearer not-a-token\n' >"$WORK/garbage.hdr"
mcp_post "$WORK/garbage.hdr" "$INIT_BODY"
expect_status "garbage bearer -> 401" 401
for prefix in hmp hmo hmr; do
  printf 'Authorization: Bearer %s_%s\n' "$prefix" "$(openssl rand -hex 20)" >"$WORK/garbage.hdr"
  mcp_post "$WORK/garbage.hdr" "$INIT_BODY"
  expect_status "${prefix}_ shaped garbage bearer -> 401" 401
  call_tool "$WORK/garbage.hdr" whoami '{}'
  expect_status "${prefix}_ shaped garbage bearer on tools/call -> 401" 401
done
printf 'Authorization: Basic %s\n' "$(printf 'admin:x' | base64)" >"$WORK/garbage.hdr"
mcp_post "$WORK/garbage.hdr" "$INIT_BODY"
expect_status "non-Bearer Authorization -> 401" 401

mcp_post "$ADMIN_HDR" '[{"jsonrpc":"2.0","id":1,"method":"tools/list"},{"jsonrpc":"2.0","id":2,"method":"tools/list"}]'
if [ "$STATUS" -ge 400 ] && [ "$STATUS" -lt 500 ]; then ok "JSON-RPC batch rejected (HTTP $STATUS)"; else fail "JSON-RPC batch rejected" "HTTP $STATUS"; fi
STATUS=$(curl -sS -o "$WORK/body" -w '%{http_code}' -H @"$ADMIN_HDR" -H 'Accept: application/json, text/event-stream' "$MCP_BASE/mcp") || STATUS=000
if [ "$STATUS" = 405 ]; then ok "GET /mcp -> 405"; else fail "GET /mcp -> 405" "HTTP $STATUS"; fi
mcp_post "$ADMIN_HDR" "$INIT_BODY" -H 'Origin: https://evil.example'
expect_status "foreign Origin refused -> 403" 403

# ---- discovery: ids to call every tool with --------------------------------------------------------------------------

echo "== discovery (admin)"
call_tool "$ADMIN_HDR" whoami '{}'
WHOAMI_TEXT=$TEXT
if [ "$(tj '.user.username')" = admin ] && [ "$(tj '.user.admin')" = true ]; then ok "whoami acts as admin"; else fail "whoami acts as admin" "${TEXT:0:160}"; fi
LS_OK=$(jq -r '.log_search | (has("daily_bytes") and has("used_bytes") and has("max_bytes_per_search") and has("resets_at") and (.daily_bytes > 0))' <<<"$WHOAMI_TEXT" 2>/dev/null || echo false)
check "whoami reports log search budget (daily, used, per-search max, resets_at)" "$([ "$LS_OK" = true ] && echo 0 || echo 1)" "log_search=$(jq -c '.log_search' <<<"$WHOAMI_TEXT" 2>/dev/null)"

call_tool "$ADMIN_HDR" list_clusters '{"limit":50}'
CLUSTERS_TEXT=$TEXT
ADMIN_CLUSTER_COUNT=$(jq -r '.items | length' <<<"$CLUSTERS_TEXT" 2>/dev/null || echo 0)
CORE_ID=$(jq -r '.items[]? | select(.name == "core-services") | .id' <<<"$CLUSTERS_TEXT" | head -n 1)
call_tool "$ADMIN_HDR" list_services '{}'
SERVICES_TEXT=$TEXT
SERVICE_JSON=$(jq -c '[.items[]? | select(.name == "payments-api")][0] // .items[0] // empty' <<<"$SERVICES_TEXT" 2>/dev/null || true)
SERVICE_ID=$(jq -r '.id // empty' <<<"${SERVICE_JSON:-null}")
SERVICE_REVISION=$(jq -r '.revision // empty' <<<"${SERVICE_JSON:-null}")
SERVICE_REGION=$(jq -r '.region // empty' <<<"${SERVICE_JSON:-null}")
if [ -z "$SERVICE_ID" ] || [ -z "$CORE_ID" ]; then
  fail "discover clusters and services" "no core-services cluster or no services ($ADMIN_CLUSTER_COUNT clusters; list_services isError=$IS_ERR rpc_error='$RPC_ERR' text='${TEXT:0:200}'); run make floci-seed and connect.sh"
  exit 1
fi
ok "discovered core-services ($ADMIN_CLUSTER_COUNT clusters visible), service $(jq -r '.name' <<<"$SERVICE_JSON") in $SERVICE_REGION"

call_tool "$ADMIN_HDR" list_revisions "$(jq -nc --arg s "$SERVICE_ID" '{service_id:$s}')"
REVISION=$(jq -r '(.items // .revisions // [])[0] | (.revision // empty)' <<<"$TEXT" 2>/dev/null || true)
REVISION=${REVISION:-$SERVICE_REVISION}
call_tool "$ADMIN_HDR" get_service_timeline "$(jq -nc --arg s "$SERVICE_ID" '{service_id:$s,range:"7d"}')"
TIMELINE_TASK=$(jq -r '[.events[]? | select((.task_id // "") != "") | .task_id][0] // empty' <<<"$TEXT" 2>/dev/null || true)
call_tool "$ADMIN_HDR" list_networks '{}'
NET_ACCOUNT=$(jq -r '[.items[]? | . as $a | .regions[]? | select(.active_vpcs > 0) | {a:$a.account_id, r:.region}][0].a // empty' <<<"$TEXT" 2>/dev/null || true)
NET_REGION=$(jq -r '[.items[]? | . as $a | .regions[]? | select(.active_vpcs > 0) | {a:$a.account_id, r:.region}][0].r // empty' <<<"$TEXT" 2>/dev/null || true)
VPC_ID=""
if [ -n "$NET_ACCOUNT" ]; then
  call_tool "$ADMIN_HDR" list_region_vpcs "$(jq -nc --arg a "$NET_ACCOUNT" --arg r "$NET_REGION" '{account:$a,region:$r}')"
  VPC_ID=$(jq -r '.items[0].vpc_id // empty' <<<"$TEXT" 2>/dev/null || true)
fi
call_tool "$ADMIN_HDR" get_service_security "$(jq -nc --arg s "$SERVICE_ID" '{service_id:$s}')"
DIGEST=$(jq -r '[.images[]? | select((.digest // "") != "") | .digest][0] // empty' <<<"$TEXT" 2>/dev/null || true)
call_tool "$ADMIN_HDR" list_log_search_services '{}'
LOG_SERVICE_ID=$(jq -r '[.items[]? | select(.id == "'"$SERVICE_ID"'")][0].id // .items[0].id // empty' <<<"$TEXT" 2>/dev/null || true)

# ---- every tool ---------------------------------------------------------------------------------------------------------

echo "== every tool (admin)"
# Tools whose upstream (CloudWatch metrics, Logs Insights, a load balancer, the image scanner) floci may not emulate:
# an error from them is recorded as a skip with the reason, never as ok.
MAY_ERROR=" get_service_metrics get_service_logs estimate_log_search search_logs get_target_health get_image_security get_service_security get_timeline_task "
SERVICE_ARGS=$(jq -nc --arg s "$SERVICE_ID" '{service_id:$s}')
LOG_ARGS=$(jq -nc --arg s "${LOG_SERVICE_ID:-$SERVICE_ID}" '{service_ids:[$s],range:"15m",query:"fields @timestamp, @message | limit 5"}')

# tool_args TOOL: the arguments for TOOL, or "SKIP:<reason>" when the estate has nothing to call it with.
tool_args() {
  case "$1" in
    whoami | get_overview | list_alerts | list_networks | list_storage | list_security | list_log_search_services) echo '{}' ;;
    list_clusters) echo '{"limit":50}' ;;
    get_cluster) jq -nc --arg c "$CORE_ID" '{cluster_id:$c}' ;;
    list_services) echo '{}' ;;
    list_tasks) echo '{"filter":"all","limit":20}' ;;
    search) echo '{"q":"payments"}' ;;
    search_config) echo '{"q":"payments"}' ;;
    get_service | list_service_tasks | get_task_definition | list_revisions | get_scaling | get_target_health | get_service_offhours | get_desired_count | get_arm64_readiness | list_service_operations | get_service_security)
      case "$1" in list_service_tasks) jq -nc --arg s "$SERVICE_ID" '{service_id:$s,limit:20}' ;; *) echo "$SERVICE_ARGS" ;; esac ;;
    get_revision)
      if [ -n "$REVISION" ]; then jq -nc --arg s "$SERVICE_ID" --argjson r "$REVISION" '{service_id:$s,revision:$r}'; else echo "SKIP:no task definition revision discovered"; fi ;;
    get_service_timeline) jq -nc --arg s "$SERVICE_ID" '{service_id:$s,range:"24h"}' ;;
    get_timeline_task)
      if [ -n "$TIMELINE_TASK" ]; then jq -nc --arg s "$SERVICE_ID" --arg t "$TIMELINE_TASK" '{service_id:$s,task_id:$t}'; else echo "SKIP:the service timeline has no task event on floci"; fi ;;
    get_service_metrics) jq -nc --arg s "$SERVICE_ID" '{service_id:$s,range:"1h"}' ;;
    get_service_logs) jq -nc --arg s "$SERVICE_ID" '{service_id:$s,limit:5}' ;;
    estimate_log_search | search_logs) echo "$LOG_ARGS" ;;
    get_image_security)
      if [ -n "$DIGEST" ]; then jq -nc --arg d "$DIGEST" '{digest:$d}'; else echo "SKIP:no scanned image digest (image scanning is not populated on floci)"; fi ;;
    get_cost_analytics | get_capacity_analytics | get_reliability_analytics | get_delivery_analytics | get_operations_analytics) echo '{"range":"7d"}' ;;
    list_region_vpcs)
      if [ -n "$NET_ACCOUNT" ]; then jq -nc --arg a "$NET_ACCOUNT" --arg r "$NET_REGION" '{account:$a,region:$r}'; else echo "SKIP:no network region with an active VPC"; fi ;;
    get_vpc_tree)
      if [ -n "$VPC_ID" ]; then jq -nc --arg v "$VPC_ID" --arg a "$NET_ACCOUNT" --arg r "$NET_REGION" '{vpc_id:$v,account:$a,region:$r}'; else echo "SKIP:no VPC discovered on floci"; fi ;;
    *) echo "SKIP:no argument recipe for $1 (new tool? add one to mcp-e2e.sh)" ;;
  esac
}

for tool in $EXPECTED_TOOLS; do
  targs=$(tool_args "$tool")
  case "$targs" in
    SKIP:*) skip "tool $tool" "${targs#SKIP:}"; continue ;;
  esac
  call_tool "$ADMIN_HDR" "$tool" "$targs"
  if [ "$STATUS" != 200 ]; then
    fail "tool $tool" "HTTP $STATUS"
  elif [ -n "$RPC_ERR" ]; then
    fail "tool $tool" "JSON-RPC error: ${RPC_ERR:0:200}"
  elif [ "$IS_ERR" = true ]; then
    if [[ "$MAY_ERROR" == *" $tool "* ]]; then skip "tool $tool" "upstream unsupported on floci: ${TEXT:0:140}"; else fail "tool $tool" "isError: ${TEXT:0:200}"; fi
  elif ! jq -e . <<<"$TEXT" >/dev/null 2>&1; then
    fail "tool $tool" "result is not JSON: ${TEXT:0:120}"
  else
    ok "tool $tool"
  fi
done
# Log search is called as the design says: estimate first, then search with a tiny window (15m, limit 5), both above.

# ---- the read-only gate on Harbormaster itself -------------------------------------------------------------------------

echo "== read-only gate (Harbormaster, bearer admin token)"
gate() { # label method path [body]
  hm_bearer "$ADMIN_HDR" "$2" "$3" "${4:-}"
  if [ "$STATUS" = 403 ] && body_has token_not_allowed; then ok "$1 -> 403 token_not_allowed"; else fail "$1 -> 403 token_not_allowed" "HTTP $STATUS $(head -c 120 "$WORK/body" 2>/dev/null)"; fi
}
gate "POST restart service" POST "/api/services/$SERVICE_ID/restart" '{}'
gate "POST desired-count" POST "/api/services/$SERVICE_ID/desired-count" '{"desired":1}'
gate "POST deploy revision" POST "/api/services/$SERVICE_ID/deploy" '{"revision":1}'
gate "GET deploy-settings" GET "/api/services/$SERVICE_ID/deploy-settings"
gate "GET users (admin only)" GET /api/users
gate "POST users" POST /api/users '{"username":"mcp-e2e-nobody","admin":false}'
gate "GET accounts" GET /api/accounts
gate "GET audit" GET /api/audit
gate "GET settings" GET /api/settings
gate "PUT settings" PUT /api/settings '{}'
gate "GET /api/me" GET /api/me
gate "GET tokens (list)" GET /api/tokens
gate "POST tokens (a token cannot mint tokens)" POST /api/tokens '{"name":"mcp-e2e-escalate","expires_in_days":1}'
gate "DELETE own token via token" DELETE "/api/tokens/$ADMIN_TOKEN_ID"
gate "GET users/{id}/tokens" GET "/api/users/$OMER_ID/tokens"
gate "terminal exec websocket route" GET /api/tasks/does-not-matter/exec
gate "terminal exec-targets" GET /api/tasks/does-not-matter/exec-targets
gate "OAuth consent API (session only)" GET /api/oauth/authorize
gate "OAuth token endpoint refuses a bearer" POST /api/oauth/token ''
hm_bearer "$ADMIN_HDR" GET /api/clusters
expect_status "pinned read route GET /api/clusters -> 200" 200
hm_bearer "$ADMIN_HDR" GET "/api/services/$SERVICE_ID/desired-count"
expect_status "pinned read route GET desired-count -> 200" 200
hm_bearer "$ADMIN_HDR" GET /api/tokens/current
expect_status "pinned read route GET /api/tokens/current -> 200" 200
hm_bearer "" GET /api/tokens/current
expect_status "no credential on /api/tokens/current -> 401" 401
# A bearer token wins over a cookie: the admin session must not widen what the token may do.
hm_bearer "$ADMIN_HDR" GET /api/users "" -b "$ADMIN_JAR"
if [ "$STATUS" = 403 ] && body_has token_not_allowed; then ok "token + admin session cookie: cookie ignored, GET /api/users -> 403"; else fail "token + admin session cookie: cookie ignored" "HTTP $STATUS"; fi
hm_bearer "$ADMIN_HDR" POST "/api/services/$SERVICE_ID/restart" '{}' -b "$ADMIN_JAR" -H "X-CSRF-Token: $(cat "$ADMIN_JAR.csrf")"
if [ "$STATUS" = 403 ] && body_has token_not_allowed; then ok "token + session cookie + CSRF: restart still 403"; else fail "token + session cookie + CSRF: restart still 403" "HTTP $STATUS"; fi
# A token-shaped unknown token (hmp_ + 12 base32 id + _ + 43 base64url secret): only that shape is a token request, any
# other Authorization value is ignored and the session applies, by design.
printf 'Authorization: Bearer hmp_%s_%s\n' "$(LC_ALL=C tr -dc 'a-z2-7' </dev/urandom | head -c 12)" \
  "$(LC_ALL=C tr -dc 'A-Za-z0-9_-' </dev/urandom | head -c 43)" >"$WORK/garbage.hdr"
hm_bearer "$WORK/garbage.hdr" GET /api/clusters "" -b "$ADMIN_JAR"
expect_status "unknown hmp_ token + valid session cookie -> 401 (never falls back to the session)" 401

# ---- visibility ------------------------------------------------------------------------------------------------------------

echo "== visibility (omer, Operator on Dev; core-services is Prod)"
call_tool "$OMER_HDR" whoami '{}'
if [ "$(tj '.user.username')" = omer ] && [ "$(tj '.user.admin')" = false ]; then ok "omer's whoami is omer, not admin"; else fail "omer's whoami" "${TEXT:0:160}"; fi
call_tool "$OMER_HDR" list_clusters '{"limit":50}'
OMER_CLUSTER_COUNT=$(jq -r '.items | length' <<<"$TEXT" 2>/dev/null || echo -1)
OMER_HAS_CORE=$(jq -r '[.items[]? | select(.name == "core-services")] | length' <<<"$TEXT" 2>/dev/null || echo -1)
check "omer's list_clusters hides Prod core-services (admin sees it)" "$([ "$OMER_HAS_CORE" = 0 ] && echo 0 || echo 1)" "omer sees core-services ($OMER_HAS_CORE)"
check "omer sees fewer clusters than admin ($OMER_CLUSTER_COUNT < $ADMIN_CLUSTER_COUNT)" "$([ "$OMER_CLUSTER_COUNT" -ge 0 ] && [ "$OMER_CLUSTER_COUNT" -lt "$ADMIN_CLUSTER_COUNT" ] && echo 0 || echo 1)" "omer $OMER_CLUSTER_COUNT, admin $ADMIN_CLUSTER_COUNT"
call_tool "$OMER_HDR" list_services '{}'
OMER_CORE_SERVICES=$(jq -r '[.items[]? | select((.cluster | if type == "object" then .name else . end) == "core-services")] | length' <<<"$TEXT" 2>/dev/null || echo -1)
check "omer's list_services has no core-services service" "$([ "$OMER_CORE_SERVICES" = 0 ] && echo 0 || echo 1)" "count $OMER_CORE_SERVICES"
call_tool "$OMER_HDR" get_cluster "$(jq -nc --arg c "$CORE_ID" '{cluster_id:$c}')"
check "omer's get_cluster(core-services) is an error" "$([ "$IS_ERR" = true ] && echo 0 || echo 1)" "HTTP $STATUS isError=$IS_ERR ${TEXT:0:100}"
call_tool "$OMER_HDR" get_service "$SERVICE_ARGS"
check "omer's get_service(payments-api in Prod) is an error" "$([ "$IS_ERR" = true ] && echo 0 || echo 1)" "isError=$IS_ERR ${TEXT:0:100}"
call_tool "$OMER_HDR" get_service_logs "$(jq -nc --arg s "$SERVICE_ID" '{service_id:$s,limit:5}')"
check "omer cannot read logs of a Prod service" "$([ "$IS_ERR" = true ] && echo 0 || echo 1)" "isError=$IS_ERR"
call_tool "$OMER_HDR" get_overview '{}'
check "omer's get_overview works on what he can see" "$([ "$IS_ERR" = false ] && [ "$STATUS" = 200 ] && echo 0 || echo 1)" "${TEXT:0:100}"
hm_bearer "$OMER_HDR" GET /api/users
check "omer's token on an admin route -> 403 token_not_allowed" "$([ "$STATUS" = 403 ] && echo 0 || echo 1)" "HTTP $STATUS"

# ---- OAuth --------------------------------------------------------------------------------------------------------------------

echo "== OAuth"
curl -sS -o "$WORK/body" -w '%{http_code}' "$MCP_BASE/.well-known/oauth-protected-resource" >"$WORK/status" || echo 000 >"$WORK/status"
STATUS=$(cat "$WORK/status")
PRM_OK=$(jq -r --arg r "$MCP_BASE/mcp" '(.resource == $r) and (.authorization_servers | index("'"$MCP_BASE"'") != null) and (.scopes_supported | index("harbormaster:read") != null) and (.bearer_methods_supported == ["header"])' "$WORK/body" 2>/dev/null || echo false)
check "gateway /.well-known/oauth-protected-resource (RFC 9728) is sane" "$([ "$STATUS" = 200 ] && [ "$PRM_OK" = true ] && echo 0 || echo 1)" "HTTP $STATUS $(head -c 160 "$WORK/body")"
STATUS=$(curl -sS -o "$WORK/body" -w '%{http_code}' "$MCP_BASE/.well-known/oauth-protected-resource/mcp") || STATUS=000
expect_status "gateway /.well-known/oauth-protected-resource/mcp" 200
STATUS=$(curl -sS -o "$WORK/body" -w '%{http_code}' "$MCP_BASE/.well-known/oauth-authorization-server") || STATUS=000
ASM_OK=$(jq -r --arg i "$MCP_BASE" '(.issuer == $i) and (.code_challenge_methods_supported == ["S256"]) and (has("registration_endpoint") | not) and (.response_types_supported == ["code"]) and (.token_endpoint_auth_methods_supported == ["none"]) and (.authorization_endpoint | endswith("/oauth/authorize"))' "$WORK/body" 2>/dev/null || echo false)
check "gateway /.well-known/oauth-authorization-server (RFC 8414): S256 only, no registration_endpoint" "$([ "$STATUS" = 200 ] && [ "$ASM_OK" = true ] && echo 0 || echo 1)" "HTTP $STATUS $(head -c 200 "$WORK/body")"

authorize() { # label client_id extra-query
  local query
  query="response_type=code&client_id=$(jq -rn --arg v "$2" '$v|@uri')&redirect_uri=http%3A%2F%2F127.0.0.1%3A9%2Fcb&state=s$3"
  sreq "$ADMIN_JAR" GET "/api/oauth/authorize?$query"
  if [ "$STATUS" = 400 ] && ! jq -e 'has("redirect_to")' "$WORK/body" >/dev/null 2>&1; then
    ok "$1 -> 400, not redirectable ($(jq -r '.error' "$WORK/body"))"
  else
    fail "$1 -> 400, not redirectable" "HTTP $STATUS $(head -c 160 "$WORK/body")"
  fi
}
CHALLENGE=$(printf 'mcp-e2e' | openssl dgst -sha256 -binary | openssl base64 -A | tr '+/' '-_' | tr -d '=')
PKCE="&code_challenge=$CHALLENGE&code_challenge_method=S256"
authorize "authorize with a non-https client_id (http://localhost)" "http://localhost/client.json" "$PKCE"
authorize "authorize with a loopback https client_id" "https://127.0.0.1/client.json" "$PKCE"
authorize "authorize with the metadata-service address as client_id" "https://169.254.169.254/latest/meta-data" "$PKCE"
authorize "authorize with a host that is not on the client allow-list" "https://example.invalid/client.json" "$PKCE"
authorize "authorize without PKCE (client unresolvable locally, so refused at the client check first)" "https://example.invalid/client.json" ""
STATUS=$(curl -sS -o "$WORK/body" -w '%{http_code}' -X POST -d 'grant_type=authorization_code&code=garbage&code_verifier=garbage&client_id=https%3A%2F%2Fexample.invalid%2Fc&redirect_uri=http%3A%2F%2F127.0.0.1%3A9%2Fcb' "$MCP_BASE/oauth/token") || STATUS=000
if [ "$STATUS" = 400 ] || [ "$STATUS" = 401 ]; then ok "gateway /oauth/token with a garbage code -> $STATUS"; else fail "gateway /oauth/token with a garbage code -> 400/401" "HTTP $STATUS $(head -c 120 "$WORK/body")"; fi
skip "OAuth consent and token exchange" "needs a public https client metadata document; cannot run against floci locally (UI error state is covered by the Playwright tokens spec)"

# ---- revocation -------------------------------------------------------------------------------------------------------------------

echo "== revocation"
sreq "$ADMIN_JAR" DELETE "/api/tokens/$ADMIN_TOKEN_ID"
check "revoke admin's token (DELETE /api/tokens/{id}, session)" "$([ "$STATUS" = 200 ] || [ "$STATUS" = 204 ] && echo 0 || echo 1)" "HTTP $STATUS"
rpc_simple "$ADMIN_HDR" tools/list
expect_status "revoked admin token: tools/list -> 401" 401
call_tool "$ADMIN_HDR" whoami '{}'
expect_status "revoked admin token: tools/call -> 401" 401
hm_bearer "$ADMIN_HDR" GET /api/clusters
expect_status "revoked admin token: Harbormaster API -> 401" 401

STATUS=0
"$HM_BIN" revoke-tokens >"$WORK/revoke.out" 2>&1 || STATUS=$?
check "revoke-tokens without --all/--user refuses (exit $STATUS)" "$([ "$STATUS" != 0 ] && echo 0 || echo 1)" "it exited 0"
call_tool "$OMER_HDR" whoami '{}'
check "omer's token still works before the CLI revoke" "$([ "$STATUS" = 200 ] && echo 0 || echo 1)" "HTTP $STATUS"
STATUS=0
"$HM_BIN" revoke-tokens --user omer >"$WORK/revoke.out" 2>&1 || STATUS=$?
if [ "$STATUS" = 0 ] && grep -q 'revoked 1 tokens of omer' "$WORK/revoke.out"; then
  ok "bin/harbormaster revoke-tokens --user omer ($(cat "$WORK/revoke.out"))"
else
  fail "bin/harbormaster revoke-tokens --user omer" "exit $STATUS: $(head -c 160 "$WORK/revoke.out")"
fi
rpc_simple "$OMER_HDR" tools/list
expect_status "omer's token after CLI revoke: tools/list -> 401" 401
call_tool "$OMER_HDR" whoami '{}'
expect_status "omer's token after CLI revoke: tools/call -> 401" 401

echo
echo "mcp-e2e: $PASS passed, $FAILED failed, $SKIPPED skipped"
[ "$FAILED" = 0 ] || exit 1
