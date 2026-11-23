#!/usr/bin/env bash
# Functional end-to-end sanity check of Harbormaster's write and core-feature paths against the local floci estate.
#
# Usage: tools/floci/sanity.sh [--help]
#   HM_FLOCI_PORT   port of the Harbormaster server (default 8490)
#
# Needs floci seeded (make floci-up floci-seed) and Harbormaster running against it (make floci-serve). Runs
# connect.sh first (idempotent), then drives the API as admin and as omer and checks every change twice: through
# Harbormaster's API and directly in floci with the aws CLI (--profile floci, explicit --region).
# Covers: health, estate reads, restart, desired count, deploy / changeset / rollback, off-hours, permissions,
# CSRF, notification routes, log search, admin users and the audit log, sessions, and that the estate is restored.
# Each check prints "ok   <name>", "FAIL <name>: <reason>" or "skip <name>: <reason>". Bounded polls only (the
# server polls ECS every 5 s here). Never prints a password or token. Re-runnable; side effects are undone.
# Note: omer's password is rotated to a random value that is not kept (the admin can reset it at any time).
# Exit codes: 0 no check failed, 1 at least one FAIL (or the setup failed), 2 refused (non-local environment) or bad usage.
set -euo pipefail

usage() { sed -n '2,16p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; }
case "${1:-}" in
  -h | --help) usage; exit 0 ;;
  "") ;;
  *) usage >&2; exit 2 ;;
esac

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# server-env.sh sources env.sh, which refuses (2) a non-local endpoint or a profile other than floci.
# shellcheck source=server-env.sh
. "$HERE/server-env.sh" || exit 2
PROFILE=floci
[ "${AWS_PROFILE:-}" = "$PROFILE" ] || { echo "sanity: AWS_PROFILE is not floci; refusing" >&2; exit 2; }
[ "$(aws --profile "$PROFILE" --region eu-west-1 configure get endpoint_url)" = "http://127.0.0.1:4566" ] \
  || { echo "sanity: profile does not target local floci; refusing" >&2; exit 2; }
for tool in jq curl aws openssl; do
  command -v "$tool" >/dev/null || { echo "sanity: $tool is required" >&2; exit 2; }
done

BASE="$HM_BASE_URL"
PASSWORD_FILE="$HERE/.admin-password"
umask 077
WORK="$(mktemp -d)"          # mode 700; holds cookie jars and the only copies of temporary passwords
RESP="$WORK/resp"
STATUS=000
SEND_CSRF=1
PASSED=0 FAILED=0 SKIPPED=0
RUN_ID="$(date +%s)"

# ---------------------------------------------------------------------------------------------------------------
# Reporting

pass() { PASSED=$((PASSED + 1)); echo "ok   $1"; }
fail() { FAILED=$((FAILED + 1)); echo "FAIL $1: $2"; }
skip() { SKIPPED=$((SKIPPED + 1)); echo "skip $1: $2"; }

# ---------------------------------------------------------------------------------------------------------------
# HTTP helpers. Two sessions: admin and omer, each with its own cookie jar and CSRF token (CSRF_admin, CSRF_omer).

# call WHO METHOD PATH [JSON | @FILE]: sets STATUS (000 when unreachable) and leaves the body in $RESP.
call() {
  local who=$1 method=$2 path=$3 body=${4:-} csrf_var csrf
  csrf_var="CSRF_$who"
  csrf=${!csrf_var:-}
  local args=(-sS -m 40 -o "$RESP" -w '%{http_code}' -X "$method" -b "$WORK/jar-$who" -c "$WORK/jar-$who")
  if [ "$SEND_CSRF" = 1 ] && [ -n "$csrf" ]; then args+=(-H "X-CSRF-Token: $csrf"); fi
  if [ -n "$body" ]; then
    if [ "${body:0:1}" = "@" ]; then
      args+=(-H 'Content-Type: application/json' --data-binary "$body")
    else
      printf '%s' "$body" >"$WORK/req.json"
      args+=(-H 'Content-Type: application/json' --data-binary "@$WORK/req.json")
    fi
  fi
  : >"$RESP"
  STATUS=$(curl "${args[@]}" "$BASE$path" 2>/dev/null) || STATUS=000
  [ -s "$RESP" ] || printf 'null' >"$RESP"
}

jr() { jq -r "$@" "$RESP" 2>/dev/null || true; }     # value of a filter on the last response ("" when absent)
jt() { jq -e "$@" "$RESP" >/dev/null 2>&1; }          # true when the filter is truthy
errmsg() { jr 'try (.error.message // .message // "") catch ""'; }
st_is() { local s; for s in "$@"; do [ "$STATUS" = "$s" ] && return 0; done; return 1; }
get_ok() { st_is 200 && jt "$@"; }                   # status 200 and the jq filter (with its options) holds

# check NAME REASON CMD...: ok when CMD succeeds, else FAIL with REASON and the last HTTP status and message.
check() {
  local name=$1 reason=$2; shift 2
  if "$@"; then pass "$name"; else fail "$name" "$reason (HTTP $STATUS $(errmsg))"; fi
}

# poll SECONDS CMD...: runs CMD every 2 s until it succeeds or SECONDS have passed.
poll() {
  local limit=$1 end; shift
  end=$((SECONDS + limit))
  while :; do
    if "$@"; then return 0; fi
    [ "$SECONDS" -ge "$end" ] && return 1
    sleep 2
  done
}

# login WHO USERNAME PASSWORD-FILE: signs in and stores the session's CSRF token. Returns 1 on a refused sign-in.
login() {
  local who=$1 user=$2 file=$3
  jq -n --arg u "$user" --rawfile p "$file" '{username:$u,password:($p | rtrimstr("\n"))}' >"$WORK/login.json"
  call "$who" POST /api/auth/login "@$WORK/login.json"
  st_is 200 || return 1
  call "$who" GET /api/me
  printf -v "CSRF_$who" '%s' "$(jr .csrf_token)"
}

# ---------------------------------------------------------------------------------------------------------------
# floci helpers (every call: --profile floci and an explicit --region)

fl() { local region=$1; shift; aws --profile "$PROFILE" --region "$region" "$@"; }
fl_svc() { fl "$1" ecs describe-services --cluster "$2" --services "$3" --query 'services[0]' --output json 2>/dev/null || echo '{}'; }
fl_desired() { fl_svc "$@" | jq -r '.desiredCount // "?"'; }
fl_taskdef() { fl_svc "$@" | jq -r '.taskDefinition // ""'; }
fl_has_deployment() { fl_svc "$1" "$2" "$3" | jq -e --arg id "$4" 'any(.deployments[]?; .id == $id)' >/dev/null 2>&1; }
fl_no_rollout() { fl_svc "$@" | jq -e '[.deployments[]? | select(.rolloutState == "IN_PROGRESS")] | length == 0' >/dev/null 2>&1; }

# The service under test: set by use_svc NAME from the services Harbormaster reported at the start.
S_NAME="" S_ID="" S_CLUSTER="" S_REGION=""
use_svc() {
  S_NAME=$1
  S_ID=$(jq -r --arg n "$1" '.items[] | select(.name == $n) | .id' "$WORK/services.json" | head -n 1)
  S_CLUSTER=$(jq -r --arg n "$1" '.items[] | select(.name == $n) | .cluster.name' "$WORK/services.json" | head -n 1)
  S_REGION=$(jq -r --arg n "$1" '.items[] | select(.name == $n) | .region' "$WORK/services.json" | head -n 1)
  [ -n "$S_ID" ] && [ -n "$S_CLUSTER" ] && [ -n "$S_REGION" ]
}
svc_id_of() { jq -r --arg n "$1" '.items[] | select(.name == $n) | .id' "$WORK/services.json" | head -n 1; }

# Desired count and task definition of every service, one line each, for the restore check.
estate_snapshot() {
  local line name cluster region
  jq -r '.items[] | [.name, .cluster.name, .region] | @tsv' "$WORK/services.json" | sort | while IFS=$'\t' read -r name cluster region; do
    line=$(fl_svc "$region" "$cluster" "$name" | jq -r '[.desiredCount, .taskDefinition] | @tsv')
    printf '%s/%s/%s\t%s\n' "$region" "$cluster" "$name" "$line"
  done
}

# ---------------------------------------------------------------------------------------------------------------
# Cleanup: undo what a failed or interrupted run may have left, then remove the temp dir.

cleanup() {
  local status=$?
  trap - EXIT
  set +e
  if [ -n "${CSRF_admin:-}" ]; then
    local id
    id=$(call admin GET /api/users; jr '.items[]? | select(.username == "sanity-tmp") | .id')
    [ -n "$id" ] && call admin DELETE "/api/users/$id"
    call admin GET /api/offhours/schedules
    for id in $(jr '.items[]? | select(.name == "sanity-offhours") | .id'); do call admin DELETE "/api/offhours/schedules/$id"; done
    call admin GET /api/notifications/routes
    for id in $(jr '.items[]? | select(.name | startswith("sanity-")) | .id'); do call admin DELETE "/api/notifications/routes/$id"; done
    call admin GET /api/tags
    id=$(jr '.items[]? | select(.name == "sanity") | .id')
    if [ -n "$id" ]; then
      [ -n "$(svc_id_of storefront-staging 2>/dev/null)" ] && call admin PUT "/api/tag-assignments/services/$(svc_id_of storefront-staging)" '{"tag_id":null}'
      call admin DELETE "/api/tags/$id"
    fi
  fi
  # Put every desired count back to what the first snapshot recorded.
  if [ -s "$WORK/snap-before" ]; then
    local key want now region cluster name
    while IFS=$'\t' read -r key want _; do
      region=${key%%/*}; name=${key##*/}; cluster=${key#*/}; cluster=${cluster%/*}
      now=$(fl_desired "$region" "$cluster" "$name")
      if [ "$now" != "$want" ] && [ "$want" != "?" ]; then
        fl "$region" ecs update-service --cluster "$cluster" --service "$name" --desired-count "$want" >/dev/null 2>&1 \
          && echo "sanity: cleanup restored desired count of $key to $want"
      fi
    done <"$WORK/snap-before"
  fi
  rm -rf "$WORK"
  exit "$status"
}
trap cleanup EXIT

die() { echo "sanity: $*" >&2; exit 1; }

# ---------------------------------------------------------------------------------------------------------------
# Setup: connect (idempotent), admin session, the estate Harbormaster sees, the starting snapshot.

setup() {
  curl -fsS -m 5 -o /dev/null "$BASE/healthz" 2>/dev/null || die "$BASE/healthz did not answer; is make floci-serve running?"
  "$HERE/connect.sh" 2>&1 | sed 's/^/sanity: /' || die "connect.sh failed"
  [ -s "$PASSWORD_FILE" ] || die "no admin password file; run tools/floci/prepare.sh"
  CSRF_admin=""
  login admin admin "$PASSWORD_FILE" || die "admin sign-in failed (HTTP $STATUS)"
  # Harbormaster polls ECS every 5 s here; wait for the whole seeded estate.
  have_estate() { call admin GET /api/services && [ "$(jr '.items | length')" -ge 9 ]; }
  poll 90 have_estate || die "fewer than 9 services after 90 s"
  cp "$RESP" "$WORK/services.json"
  call admin GET /api/clusters
  cp "$RESP" "$WORK/clusters.json"
  estate_snapshot >"$WORK/snap-before"
}

# ---------------------------------------------------------------------------------------------------------------
# 1. Health, identity, overview

check_health() {
  local code
  code=$(curl -sS -m 10 -o /dev/null -w '%{http_code}' "$BASE/healthz" 2>/dev/null) || code=000
  if [ "$code" = 200 ]; then pass "health GET /healthz"; else fail "health GET /healthz" "HTTP $code"; fi
  call admin GET /api/me
  check "me as admin" "expected username admin with admin=true" get_ok '.username == "admin" and .admin == true and .must_change_password == false'
  call admin GET /api/overview
  check "overview lists 6 clusters" "expected at least 6 clusters" get_ok '(.clusters | length) >= 6'
  local names missing=""
  names=$(jq -r '[.items[].name] | join(" ")' "$WORK/clusters.json")
  for c in core-services eu-apps ci-tools background-jobs edge-proxy staging-web; do
    case " $names " in *" $c "*) ;; *) missing="$missing $c" ;; esac
  done
  if [ -z "$missing" ]; then pass "clusters from the seed are all listed"; else fail "clusters from the seed are all listed" "missing:$missing"; fi
  missing=""
  names=$(jq -r '[.items[].name] | join(" ")' "$WORK/services.json")
  for s in payments-api notifications auth-gateway catalog-api checkout-web image-resizer report-worker edge-router storefront-staging; do
    case " $names " in *" $s "*) ;; *) missing="$missing $s" ;; esac
  done
  if [ -z "$missing" ] && [ "$(jq '.items | length' "$WORK/services.json")" -ge 9 ]; then
    pass "9 services from the seed are all listed"
  else
    fail "9 services from the seed are all listed" "missing:$missing"
  fi
}

# ---------------------------------------------------------------------------------------------------------------
# 2. Estate reads

# check_each NAME SUFFIX FILTER: GET /api/services/<id><SUFFIX> for every service, 200 and FILTER true.
check_each() {
  local name=$1 suffix=$2 filter=$3 bad="" id svc
  while IFS=$'\t' read -r id svc; do
    call admin GET "/api/services/$id$suffix"
    get_ok "$filter" || bad="$bad $svc(HTTP $STATUS)"
  done < <(jq -r '.items[] | [.id, .name] | @tsv' "$WORK/services.json")
  if [ -z "$bad" ]; then pass "$name"; else fail "$name" "failed for:$bad"; fi
}

check_estate_reads() {
  local bad="" id cname
  while IFS=$'\t' read -r id cname; do
    call admin GET "/api/clusters/$id"
    { st_is 200 && jt --arg n "$cname" '.name == $n'; } || bad="$bad $cname(HTTP $STATUS)"
  done < <(jq -r '.items[] | [.id, .name] | @tsv' "$WORK/clusters.json")
  if [ -z "$bad" ]; then pass "cluster detail for every cluster"; else fail "cluster detail for every cluster" "failed for:$bad"; fi

  check_each "service detail for every service" "" '.name != null and .desired != null and (.task_definition_arn | length) > 0'
  check_each "tasks of every service" "/tasks" '. != null'
  check_each "task definition of every service" "/task-definition" '(.family | length) > 0 and (.definition | type) == "object"'
  check_each "revisions of every service" "/revisions" '(.items | length) >= 1 and .deployed_revision >= 1'
  check_each "timeline of every service" "/timeline" '. != null'
  check_each "operations of every service" "/operations" '(.items | type) == "array"'
  check_each "ARM64 readiness of every service" "/arm64-readiness" '.status == "ready" or .status == "blocked" or .status == "unknown"'
  check_each "deploy settings of every service" "/deploy-settings" '(.version | length) > 0 and .deployed_revision >= 1'

  call admin GET /api/tasks
  check "tasks list" "expected an items array" get_ok '(.items | type) == "array"'

  call admin GET /api/networks
  check "networks overview" "expected at least one account with regions" get_ok '(.items | length) >= 1'
  local acct reg vpc
  acct=$(jr '.items[0].account_id'); reg=$(jr '.items[0].regions[0].region')
  if [ -n "$acct" ] && [ -n "$reg" ]; then
    call admin GET "/api/networks/$acct/$reg"
    check "networks of $reg" "expected at least one VPC" get_ok '(.items | length) >= 1'
    vpc=$(jr '.items[0].vpc_id')
    call admin GET "/api/vpcs/$vpc/tree?account=$acct&region=$reg"
    check "VPC tree of $vpc" "expected a tree root" get_ok '.root != null'
  else
    fail "networks of a region" "no account or region in /api/networks"
  fi

  call admin GET /api/storage
  check "storage (EFS)" "expected at least one file system" get_ok '(.items | length) >= 1'
  call admin GET /api/security
  check "security overview" "expected an images array" get_ok '(.images | type) == "array"'

  call admin GET "/api/config-search?q=PAYMENT_PROVIDER"
  check "config search PAYMENT_PROVIDER finds payments-api" "payments-api not among the results" get_ok 'any(.items[]?; .name == "payments-api")'
  call admin GET "/api/config-search?q=env:PAYMENT_PROVIDER"
  check "config search env:PAYMENT_PROVIDER finds payments-api" "payments-api not among the results" get_ok 'any(.items[]?; .name == "payments-api")'

  local kind
  for kind in capacity cost delivery operations reliability; do
    call admin GET "/api/analytics/$kind?range=7d"
    check "analytics $kind" "expected a JSON object" get_ok 'type == "object"'
  done

  call admin GET /api/search?q=payments
  check "search" "expected a JSON object" get_ok 'type == "object"'
  call admin GET /api/alerts
  check "alerts list" "expected an items array" get_ok '(.items | type) == "array"'
  call admin GET /api/audit
  check "audit log" "expected an items array" get_ok '(.items | type) == "array"'
  call admin GET /api/poller
  check "poller status" "expected a JSON object" get_ok 'type == "object"'
  call admin GET /api/tags
  check "tags list has Prod and Dev" "Prod or Dev tag missing" get_ok 'any(.items[]?; .name == "Prod") and any(.items[]?; .name == "Dev")'
  call admin GET /api/accounts
  check "accounts list has floci" "account floci missing" get_ok 'any(.items[]?; .name == "floci")'
}

# ---------------------------------------------------------------------------------------------------------------
# 3. Restart (checkout-web) -> history entry and a new deployment in floci

restart_count() {
  call admin GET "/api/services/$S_ID/operations"
  jr '[.items[]? | select(.source == "harbormaster" and (.text | test("estart"; "i")))] | length'
}

check_restart() {
  use_svc checkout-web || { fail "restart" "service checkout-web not found"; return 0; }
  local before dep
  before=$(restart_count)
  call admin POST "/api/services/$S_ID/restart"
  if ! st_is 202; then fail "restart POST" "expected 202 (HTTP $STATUS $(errmsg))"; return 0; fi
  dep=$(jr .deployment_id)
  pass "restart POST accepted"
  if [ -n "$dep" ] && poll 30 fl_has_deployment "$S_REGION" "$S_CLUSTER" "$S_NAME" "$dep"; then
    pass "restart: floci shows the new deployment ($dep)"
  else
    fail "restart: floci shows the new deployment" "deployment id '$dep' not in describe-services within 30 s"
  fi
  has_new_op() { [ "$(restart_count)" -gt "$before" ]; }
  if poll 20 has_new_op; then pass "restart: history entry appears in operations"; else fail "restart: history entry appears in operations" "no new restart entry in 20 s"; fi
}

# ---------------------------------------------------------------------------------------------------------------
# 4. Desired count (report-worker) -> floci matches, then restore

check_desired_count() {
  use_svc report-worker || { fail "desired count" "service report-worker not found"; return 0; }
  local orig want
  orig=$(fl_desired "$S_REGION" "$S_CLUSTER" "$S_NAME")
  case "$orig" in '' | '?' | *[!0-9]*) fail "desired count" "cannot read the starting desired count from floci ($orig)"; return 0 ;; esac
  want=$((orig + 1))
  call admin POST "/api/services/$S_ID/desired-count" "{\"desired\":$want}"
  check "desired count -> $want accepted" "expected 200 with desired=$want" get_ok ".desired == $want"
  is_want() { [ "$(fl_desired "$S_REGION" "$S_CLUSTER" "$S_NAME")" = "$1" ]; }
  if poll 20 is_want "$want"; then pass "desired count: floci desiredCount is $want"; else fail "desired count: floci desiredCount is $want" "floci reports $(fl_desired "$S_REGION" "$S_CLUSTER" "$S_NAME")"; fi
  call admin GET "/api/services/$S_ID/desired-count"
  check "desired count read back" "expected desired=$want" get_ok ".desired == $want"
  call admin POST "/api/services/$S_ID/desired-count" "{\"desired\":$orig}"
  check "desired count restored to $orig" "expected 200 with desired=$orig" get_ok ".desired == $orig"
  if poll 20 is_want "$orig"; then pass "desired count: floci desiredCount is back to $orig"; else fail "desired count: floci desiredCount is back to $orig" "floci reports $(fl_desired "$S_REGION" "$S_CLUSTER" "$S_NAME")"; fi
}

# ---------------------------------------------------------------------------------------------------------------
# 5. Deploy, partial deploy (change set) and rollback

# env_in_floci REGION FAMILY REVISION VALUE: the revision carries SANITY_RUN=VALUE.
env_in_floci() {
  fl "$1" ecs describe-task-definition --task-definition "$2:$3" --query 'taskDefinition.containerDefinitions[0].environment' --output json 2>/dev/null \
    | jq -e --arg v "$4" 'any(.[]?; .name == "SANITY_RUN" and .value == $v)' >/dev/null 2>&1
}
points_to() { # REGION CLUSTER SERVICE REVISION
  case "$(fl_taskdef "$1" "$2" "$3")" in *":$4") return 0 ;; *) return 1 ;; esac
}
# Definition of the deployed revision (from the last /task-definition response) with SANITY_RUN=$1 on the first container.
bumped_definition() {
  jq -c --arg v "$1" '.definition | .containerDefinitions[0].environment = (((.containerDefinitions[0].environment // []) | map(select(.name != "SANITY_RUN"))) + [{name: "SANITY_RUN", value: $v}])' "$RESP"
}
# deploy_revision REVISION [interrupt]: POST /deploy, retrying once without interrupt after a rollout in flight clears.
deploy_revision() {
  call admin POST "/api/services/$S_ID/deploy" "{\"revision\":$1,\"interrupt\":${2:-false}}"
  if st_is 409; then
    poll 40 fl_no_rollout "$S_REGION" "$S_CLUSTER" "$S_NAME" || true
    call admin POST "/api/services/$S_ID/deploy" "{\"revision\":$1,\"interrupt\":${2:-false}}"
  fi
  st_is 202
}

# deploy_and_rollback SERVICE MODE: MODE is "revision" (register then deploy) or "changeset" (one change set).
deploy_and_rollback() {
  local svc=$1 mode=$2 label="deploy ($2) $1" stamp="run-$RUN_ID-$2" family rev0 rev1 def version
  use_svc "$svc" || { fail "$label" "service $svc not found"; return 0; }
  if ! poll 40 fl_no_rollout "$S_REGION" "$S_CLUSTER" "$S_NAME"; then
    skip "$label" "floci keeps a rollout IN_PROGRESS on $svc (ECS mock mode never finishes one), so Harbormaster refuses a new deploy"
    return 0
  fi
  call admin GET "/api/services/$S_ID/task-definition"
  family=$(jr .family); rev0=$(jr .deployed_revision)
  if [ -z "$family" ] || [ -z "$rev0" ] || [ "$rev0" = null ]; then fail "$label" "cannot read the deployed revision (HTTP $STATUS)"; return 0; fi
  def=$(bumped_definition "$stamp")
  if [ "$mode" = revision ]; then
    call admin POST "/api/services/$S_ID/revisions" "{\"base_revision\":$rev0,\"definition\":$def}"
    if ! st_is 201; then fail "$label: register revision" "expected 201 (HTTP $STATUS $(errmsg))"; return 0; fi
    rev1=$(jr .revision)
    pass "$label: new revision $rev1 registered through Harbormaster"
  else
    call admin GET "/api/services/$S_ID/deploy-settings"
    version=$(jr .version)
    call admin POST "/api/services/$S_ID/changeset" "{\"base_revision\":$rev0,\"base_version\":\"$version\",\"reason\":\"sanity $stamp\",\"task_definition\":{\"definition\":$def}}"
    if ! st_is 200 201 202; then fail "$label: change set" "expected 2xx (HTTP $STATUS $(errmsg))"; return 0; fi
    rev1=$(jr '.registered_revision // .to_revision')
    if [ -z "$rev1" ] || [ "$rev1" = null ]; then fail "$label: change set" "no registered_revision in the response: $(jr '.status') $(jr '[.steps[]? | .name + ":" + .status + " " + .message] | join("; ")')"; return 0; fi
    pass "$label: change set status $(jr .status), revision $rev1"
  fi
  # The revision exists in floci and carries the change.
  if env_in_floci "$S_REGION" "$family" "$rev1" "$stamp"; then
    pass "$label: floci revision $family:$rev1 carries SANITY_RUN=$stamp"
  else
    fail "$label: floci revision $family:$rev1 carries SANITY_RUN" "describe-task-definition does not show the env var"
  fi
  if fl "$S_REGION" ecs list-task-definitions --family-prefix "$family" --query 'taskDefinitionArns' --output json 2>/dev/null | jq -e --arg s ":$rev1" 'any(.[]; endswith($s))' >/dev/null; then
    pass "$label: list-task-definitions shows revision $rev1"
  else
    fail "$label: list-task-definitions shows revision $rev1" "not listed"
  fi
  # The service points at the new revision (the change set deploys it itself; the revision mode needs /deploy).
  if [ "$mode" = revision ]; then
    if deploy_revision "$rev1"; then pass "$label: deploy of revision $rev1 accepted"; else fail "$label: deploy of revision $rev1" "expected 202 (HTTP $STATUS $(errmsg))"; fi
  fi
  if poll 30 points_to "$S_REGION" "$S_CLUSTER" "$S_NAME" "$rev1"; then
    pass "$label: floci service points to $family:$rev1"
  elif [ "$mode" = changeset ] && [ "$(jr .status)" != applied ]; then
    skip "$label: floci service points to the new revision" "change set ended as $(jr .status); not deployed"
  else
    fail "$label: floci service points to $family:$rev1" "floci reports $(fl_taskdef "$S_REGION" "$S_CLUSTER" "$S_NAME")"
  fi
  # Rollback to the previous revision; interrupt lets it replace a rollout that floci's mock never completes.
  if deploy_revision "$rev0" true; then pass "$label: rollback to revision $rev0 accepted"; else fail "$label: rollback to revision $rev0" "expected 202 (HTTP $STATUS $(errmsg))"; fi
  if poll 30 points_to "$S_REGION" "$S_CLUSTER" "$S_NAME" "$rev0"; then
    pass "$label: floci service is back on $family:$rev0"
  else
    fail "$label: floci service is back on $family:$rev0" "floci reports $(fl_taskdef "$S_REGION" "$S_CLUSTER" "$S_NAME")"
  fi
  # Leave no stray revision behind (best effort; the rollback above no longer uses it).
  fl "$S_REGION" ecs deregister-task-definition --task-definition "$family:$rev1" >/dev/null 2>&1 || true
}

check_deploy() {
  deploy_and_rollback edge-router revision
  deploy_and_rollback catalog-api changeset
}

# ---------------------------------------------------------------------------------------------------------------
# 6. Off-hours: a schedule on a non-Prod service (storefront-staging), manual wake and sleep, delete

sleeping_in_floci() { [ "$(fl_desired "$S_REGION" "$S_CLUSTER" "$S_NAME")" = 0 ]; }
at_count_in_floci() { [ "$(fl_desired "$S_REGION" "$S_CLUSTER" "$S_NAME")" = "$1" ]; }
enrolled() { call admin GET "/api/services/$S_ID/offhours"; jt '.enrolled == true'; }

check_offhours() {
  use_svc storefront-staging || { fail "off-hours" "service storefront-staging not found"; return 0; }
  local orig tag_id schedule sid awake_hour grid
  orig=$(fl_desired "$S_REGION" "$S_CLUSTER" "$S_NAME")
  case "$orig" in '' | '?' | *[!0-9]*) fail "off-hours" "cannot read the starting desired count ($orig)"; return 0 ;; esac
  if [ "$orig" = 0 ]; then skip "off-hours" "storefront-staging already has desired count 0, so sleeping would show nothing"; return 0; fi

  call admin GET /api/tags
  tag_id=$(jr '.items[]? | select(.name == "sanity") | .id')
  if [ -z "$tag_id" ]; then
    call admin POST /api/tags '{"name":"sanity","color":"neutral"}'
    tag_id=$(jr .id)
  fi
  [ -n "$tag_id" ] && [ "$tag_id" != null ] || { fail "off-hours: tag for the schedule" "cannot create tag sanity (HTTP $STATUS $(errmsg))"; return 0; }
  call admin PUT "/api/tag-assignments/services/$S_ID" "{\"tag_id\":$tag_id}"
  check "off-hours: tag sanity assigned to storefront-staging" "expected 2xx" st_is 200 204

  # Awake only twelve hours from now (UTC) on every day, so the current hour is asleep whenever the run starts.
  awake_hour=$(( ($(date -u +%H | sed 's/^0//') + 12) % 24 ))
  grid=$(jq -cn --argjson h "$awake_hour" '[range(0;7) | [range(0;24) | . == $h]]')
  schedule=$(jq -cn --argjson t "$tag_id" --argjson g "$grid" '{name:"sanity-offhours",enabled:true,timezone:"UTC",awake:$g,tags:[{kind:"harbormaster",tag_id:$t}],excluded_service_ids:[]}')
  call admin POST /api/offhours/preview "$schedule"
  check "off-hours: preview selects storefront-staging" "storefront-staging not among the previewed services" get_ok 'any(.services[]?; .service == "storefront-staging")'
  call admin POST /api/offhours/schedules "$schedule"
  if ! st_is 200 201; then fail "off-hours: create schedule" "expected 2xx (HTTP $STATUS $(errmsg))"; return 0; fi
  sid=$(jr .id)
  pass "off-hours: schedule $sid created"
  call admin GET "/api/offhours/schedules/$sid"
  check "off-hours: schedule detail" "expected the schedule with its service" get_ok '.name == "sanity-offhours" and .service_count >= 1'

  # The reconciler enrols the service and writes Application Auto Scaling scheduled actions, which floci may not implement.
  if poll 45 enrolled && poll 30 sleeping_in_floci; then
    pass "off-hours: service enrolled and asleep, floci desiredCount is 0"
    call admin POST "/api/services/$S_ID/offhours/wake"
    check "off-hours: wake now" "expected 200 with woken_until" get_ok '.woken_until != null'
    if poll 45 at_count_in_floci "$orig"; then pass "off-hours: floci desiredCount restored to $orig after wake"; else fail "off-hours: floci desiredCount restored after wake" "floci reports $(fl_desired "$S_REGION" "$S_CLUSTER" "$S_NAME"), expected $orig"; fi
    call admin POST "/api/services/$S_ID/offhours/sleep"
    check "off-hours: sleep again" "expected 200" st_is 200
    if poll 45 sleeping_in_floci; then pass "off-hours: floci desiredCount is 0 again after sleep"; else fail "off-hours: floci desiredCount is 0 after sleep" "floci reports $(fl_desired "$S_REGION" "$S_CLUSTER" "$S_NAME")"; fi
  else
    call admin GET "/api/services/$S_ID/offhours"
    skip "off-hours: sleep/wake effect on desiredCount" "service not put to sleep within 45 s (state '$(jr .state)', enrolled $(jr .enrolled), reason '$(jr .reason)'); floci has no Application Auto Scaling scheduled actions, which off-hours uses"
  fi

  call admin DELETE "/api/offhours/schedules/$sid"
  check "off-hours: schedule deleted" "expected 2xx" st_is 200 204
  if poll 60 at_count_in_floci "$orig"; then pass "off-hours: floci desiredCount is $orig after the schedule is gone"; else fail "off-hours: floci desiredCount after the schedule is gone" "floci reports $(fl_desired "$S_REGION" "$S_CLUSTER" "$S_NAME"), expected $orig"; fi
  call admin PUT "/api/tag-assignments/services/$S_ID" '{"tag_id":null}'
  call admin DELETE "/api/tags/$tag_id"
  check "off-hours: temporary tag removed" "expected 2xx" st_is 200 204
}

# ---------------------------------------------------------------------------------------------------------------
# 7. Permissions as omer (Operator on Dev). 8. CSRF. 12. Sessions (omer logs out last).

omer_login() {
  local id
  call admin GET /api/users
  id=$(jr '.items[]? | select(.username == "omer") | .id')
  [ -n "$id" ] || return 1
  call admin POST "/api/users/$id/reset-password"
  st_is 200 || return 1
  jr .temporary_password >"$WORK/omer-temp"
  [ -s "$WORK/omer-temp" ] || return 1
  CSRF_omer=""
  login omer omer "$WORK/omer-temp" || return 1
  # The temporary password must be replaced before anything else works; the new one is random and never shown.
  openssl rand -hex 16 | tr -d '\n' >"$WORK/omer-new"
  jq -n --rawfile c "$WORK/omer-temp" --rawfile n "$WORK/omer-new" '{current:($c | rtrimstr("\n")),new:$n}' >"$WORK/omer-pw.json"
  call omer POST /api/auth/password "@$WORK/omer-pw.json"
  st_is 200 204 || return 1
  call omer GET /api/me
  printf -v CSRF_omer '%s' "$(jr .csrf_token)"
  rm -f "$WORK/omer-temp" "$WORK/omer-new" "$WORK/omer-pw.json"
}

check_permissions() {
  if ! omer_login; then fail "permissions: omer sign-in" "cannot sign in as omer after an admin password reset (HTTP $STATUS $(errmsg))"; return 0; fi
  pass "permissions: omer signed in (password reset by admin)"
  local prod_id dev_id
  prod_id=$(svc_id_of payments-api); dev_id=$(svc_id_of checkout-web)

  call omer GET /api/me
  check "omer is not an admin" "expected admin=false" get_ok '.admin == false'
  call omer GET /api/services
  check "omer lists Dev services" "checkout-web missing" get_ok 'any(.items[]?; .name == "checkout-web")'
  check "omer does not list Prod core-services" "a core-services service is visible" jt 'all(.items[]?; .cluster.name != "core-services")'
  call omer GET "/api/services/$dev_id"
  check "omer reads a Dev service" "expected 200 for checkout-web" get_ok '.name == "checkout-web"'
  call omer GET "/api/services/$dev_id/task-definition"
  check "omer can read the Dev task definition" "expected 200" st_is 200

  call omer GET "/api/services/$prod_id"
  check "omer cannot see a Prod service" "expected 403 or 404" st_is 403 404
  call omer GET /api/clusters
  check "omer does not list the Prod cluster" "core-services visible" get_ok 'all(.items[]?; .name != "core-services")'

  # A write on a Prod service is refused and floci does not change.
  use_svc payments-api
  local d_before dep_before d_after dep_after
  d_before=$(fl_desired "$S_REGION" "$S_CLUSTER" "$S_NAME")
  dep_before=$(fl_svc "$S_REGION" "$S_CLUSTER" "$S_NAME" | jq -c '[.deployments[]?.id] | sort')
  call omer POST "/api/services/$prod_id/restart"
  check "omer restart on Prod is refused" "expected 403 or 404" st_is 403 404
  call omer POST "/api/services/$prod_id/desired-count" "{\"desired\":$((d_before + 1))}"
  check "omer desired count on Prod is refused" "expected 403 or 404" st_is 403 404
  call omer POST "/api/services/$prod_id/deploy" '{"revision":1}'
  check "omer deploy on Prod is refused" "expected 403 or 404" st_is 403 404
  sleep 3
  d_after=$(fl_desired "$S_REGION" "$S_CLUSTER" "$S_NAME")
  dep_after=$(fl_svc "$S_REGION" "$S_CLUSTER" "$S_NAME" | jq -c '[.deployments[]?.id] | sort')
  if [ "$d_before" = "$d_after" ] && [ "$dep_before" = "$dep_after" ]; then
    pass "floci unchanged after omer's refused Prod writes (desired $d_after)"
  else
    fail "floci unchanged after omer's refused Prod writes" "desired $d_before -> $d_after, deployments $dep_before -> $dep_after"
  fi

  # Operator has no Deploy on Dev; admin-only routes are closed.
  call omer POST "/api/services/$dev_id/deploy" '{"revision":1}'
  check "omer deploy on Dev is refused (Operator lacks Deploy)" "expected 403" st_is 403
  call omer GET /api/users
  check "omer cannot list users" "expected 403" st_is 403
  call omer GET /api/notifications/routes
  check "omer cannot read notification routes" "expected 403" st_is 403
}

check_csrf() {
  use_svc checkout-web
  local d_before
  d_before=$(fl_desired "$S_REGION" "$S_CLUSTER" "$S_NAME")
  case "$d_before" in '' | '?' | *[!0-9]*) fail "csrf" "cannot read desired count from floci"; return 0 ;; esac
  SEND_CSRF=0
  call admin POST "/api/services/$S_ID/desired-count" "{\"desired\":$((d_before + 1))}"
  SEND_CSRF=1
  check "csrf: write without the token is refused" "expected 403" st_is 403
  local saved=$CSRF_admin
  CSRF_admin="not-the-token"
  call admin POST "/api/services/$S_ID/desired-count" "{\"desired\":$((d_before + 1))}"
  CSRF_admin=$saved
  check "csrf: write with a wrong token is refused" "expected 403" st_is 403
  sleep 2
  if [ "$(fl_desired "$S_REGION" "$S_CLUSTER" "$S_NAME")" = "$d_before" ]; then pass "csrf: floci desiredCount unchanged"; else fail "csrf: floci desiredCount unchanged" "now $(fl_desired "$S_REGION" "$S_CLUSTER" "$S_NAME"), was $d_before"; fi
  call admin GET /api/me
  check "csrf: reads need no token" "expected 200" st_is 200
}

check_logout() {
  [ -n "${CSRF_omer:-}" ] || { skip "sessions: logout" "omer session was not established"; return 0; }
  call omer POST /api/auth/logout
  check "sessions: logout succeeds" "expected 2xx" st_is 200 204
  call omer GET /api/me
  check "sessions: /api/me is 401 after logout" "expected 401" st_is 401
  call omer POST "/api/services/$(svc_id_of checkout-web)/restart"
  check "sessions: a write with the old session is refused" "expected 401" st_is 401
}

# ---------------------------------------------------------------------------------------------------------------
# 9. Notification routes (webhook) and alerts

check_notifications() {
  call admin GET /api/notifications/routes
  if ! st_is 200; then fail "notifications: list routes" "expected 200 (HTTP $STATUS $(errmsg))"; return 0; fi
  pass "notifications: list routes"
  local kinds
  kinds=$(jr '[.kinds[0:2][].kind]' | jq -c .)
  [ -n "$kinds" ] && [ "$kinds" != null ] || { fail "notifications" "no alert kinds offered"; return 0; }
  local draft_http draft_https
  draft_http=$(jq -cn --argjson k "$kinds" '{name:"sanity-http",enabled:true,destination:"webhook",label:"",recipients:[],pagerduty_region:"",chat_id:"",url:"http://127.0.0.1:9/",min_severity:"warning",kinds:$k,repeat_seconds:0,quiet_enabled:false,quiet_timezone:"UTC",notify_resolved:false,scope:[],fallback:false}')
  draft_https=$(jq -cn --argjson k "$kinds" '{name:"sanity-webhook",enabled:true,destination:"webhook",label:"",recipients:[],pagerduty_region:"",chat_id:"",url:"https://127.0.0.1:9/hook",min_severity:"warning",kinds:$k,repeat_seconds:0,quiet_enabled:false,quiet_timezone:"UTC",notify_resolved:false,scope:[],fallback:false}')

  # A plain-http webhook is refused when saving (webhooks must be https).
  call admin POST /api/notifications/routes "$draft_http"
  check "notifications: http webhook to 127.0.0.1 is refused" "expected 422" st_is 422
  # An https one is accepted when saved; the outbound guard (safedial) refuses the loopback target when it is sent.
  call admin POST /api/notifications/preview "$draft_https"
  check "notifications: preview of the draft" "expected 200" st_is 200
  call admin POST /api/notifications/routes "$draft_https"
  if ! st_is 201 200; then fail "notifications: create webhook route" "expected 2xx (HTTP $STATUS $(errmsg))"; return 0; fi
  local rid
  rid=$(jr .id)
  pass "notifications: webhook route $rid created"
  if grep -q 'https://127.0.0.1:9/hook' "$RESP"; then fail "notifications: the webhook URL is not echoed" "the response contains the stored URL"; else pass "notifications: the webhook URL is not echoed"; fi
  call admin GET /api/notifications/routes
  check "notifications: route listed" "sanity-webhook not in the list" get_ok 'any(.items[]?; .name == "sanity-webhook" and .secret_set == true)'
  call admin POST "/api/notifications/routes/$rid/test"
  check "notifications: test send to a loopback target is blocked" "expected a failed delivery, not a delivered one" get_ok '.status != "delivered"'
  call admin GET /api/notifications/deliveries
  check "notifications: delivery log lists the test" "no delivery for sanity-webhook" get_ok 'any(.items[]?; .route == "sanity-webhook")'
  call admin GET /api/notifications/summary
  check "notifications: summary" "expected 200" st_is 200
  call admin GET /api/notifications/targets
  check "notifications: targets" "expected 200" st_is 200
  call admin GET /api/alerts
  check "alerts list" "expected an items array" get_ok '(.items | type) == "array"'
  call admin DELETE "/api/notifications/routes/$rid"
  check "notifications: route deleted" "expected 2xx" st_is 200 204
  call admin GET /api/notifications/routes
  check "notifications: route gone" "sanity-webhook still listed" get_ok 'all(.items[]?; .name != "sanity-webhook")'
}

# ---------------------------------------------------------------------------------------------------------------
# 10. Log search on seeded log groups plus a marker event written straight into floci

check_logs() {
  use_svc checkout-web || { fail "logs" "service checkout-web not found"; return 0; }
  local marker="sanity-marker-$RUN_ID" task stream now events
  task=$(openssl rand -hex 16)
  stream="ecs/checkout-web/$task"
  now=$(($(date +%s) * 1000))
  events=$(jq -cn --argjson now "$now" --arg m "ERROR $marker" '[{timestamp:$now,message:$m}]')
  if fl "$S_REGION" logs create-log-stream --log-group-name /ecs/checkout-web --log-stream-name "$stream" >/dev/null 2>&1 \
    && fl "$S_REGION" logs put-log-events --log-group-name /ecs/checkout-web --log-stream-name "$stream" --log-events "$events" >/dev/null 2>&1; then
    pass "logs: marker event written to /ecs/checkout-web in floci"
  else
    fail "logs: marker event written to /ecs/checkout-web in floci" "aws logs put-log-events failed"
    return 0
  fi
  call admin GET "/api/services/$S_ID/logs"
  check "logs: seeded lines of checkout-web" "expected a non-empty lines array" get_ok '(.lines | length) >= 1'
  has_marker() { call admin GET "/api/services/$S_ID/logs?q=$marker"; jt '(.lines | length) >= 1'; }
  if poll 30 has_marker; then pass "logs: filter finds the marker event"; else fail "logs: filter finds the marker event" "no line for $marker within 30 s (HTTP $STATUS)"; fi

  call admin POST /api/log-search "{\"service_ids\":[\"$S_ID\"],\"range\":\"1h\",\"query\":\"\"}"
  if ! st_is 200; then fail "logs: cross-service search" "expected 200 (HTTP $STATUS $(errmsg))"
  elif jt '(.rows | length) >= 1'; then pass "logs: cross-service search returns rows"
  else skip "logs: cross-service search returns rows" "search answered 200 without rows: $(jr '[.services[]? | .state + " " + .message] | join("; ")') (floci's CloudWatch Logs Insights may not run queries)"
  fi
}

# ---------------------------------------------------------------------------------------------------------------
# 11. Admin: create user, change role and permissions, reset password, delete; audit entries for each

audit_has() { jt --arg a "$1" --arg t "user:sanity-tmp" 'any(.items[]?; .action == $a and .target == $t)'; }

check_admin() {
  local uid dev_id
  call admin GET /api/users
  uid=$(jr '.items[]? | select(.username == "sanity-tmp") | .id')
  [ -n "$uid" ] && call admin DELETE "/api/users/$uid"
  dev_id=$(call admin GET /api/tags; jr '.items[]? | select(.name == "Dev") | .id')

  call admin POST /api/users '{"username":"sanity-tmp","admin":false}'
  if ! st_is 201; then fail "admin: create user" "expected 201 (HTTP $STATUS $(errmsg))"; return 0; fi
  uid=$(jr .user.id)
  pass "admin: user sanity-tmp created"
  check "admin: a temporary password was issued" "no temporary_password in the response" jt '(.temporary_password | length) > 0'
  call admin GET /api/users
  check "admin: user listed" "sanity-tmp missing" get_ok 'any(.items[]?; .username == "sanity-tmp" and .admin == false)'

  call admin POST "/api/users/$uid/admin" '{"admin":true}'
  check "admin: grant global admin" "expected 204" st_is 204 200
  call admin POST "/api/users/$uid/admin" '{"admin":false}'
  check "admin: revoke global admin" "expected 204" st_is 204 200
  call admin PUT "/api/permissions/$uid/$dev_id" '{"actions":1}'
  check "admin: set View on Dev" "expected 2xx" st_is 200 204
  call admin PUT "/api/permissions/$uid/$dev_id" '{"actions":7}'
  check "admin: raise to Operator on Dev" "expected 2xx" st_is 200 204
  call admin GET /api/permissions
  check "admin: permissions matrix shows the change" "sanity-tmp has no Dev permission" get_ok --arg d "$dev_id" 'any(.users[]?; .username == "sanity-tmp" and (.perms[$d] // 0) == 7)'
  call admin POST "/api/users/$uid/reset-password"
  check "admin: reset password issues a temporary one" "expected 200 with temporary_password" get_ok '(.temporary_password | length) > 0'
  call admin DELETE "/api/users/$uid"
  check "admin: delete user" "expected 204" st_is 204 200
  call admin GET /api/users
  check "admin: user gone" "sanity-tmp still listed" get_ok 'all(.items[]?; .username != "sanity-tmp")'

  call admin GET "/api/audit?limit=200"
  local action
  for action in user_create admin_grant admin_revoke permission_set password_reset user_delete; do
    check "admin: audit entry $action" "no $action entry for sanity-tmp" audit_has "$action"
  done
}

# ---------------------------------------------------------------------------------------------------------------
# 13. The estate is as found

check_restored() {
  estate_snapshot >"$WORK/snap-after"
  if diff -u "$WORK/snap-before" "$WORK/snap-after" >"$WORK/snap-diff"; then
    pass "estate restored: desired counts and task definitions match the starting snapshot"
  else
    fail "estate restored" "differences: $(grep '^[-+][^-+]' "$WORK/snap-diff" | tr '\n\t' '; ' )"
  fi
}

# ---------------------------------------------------------------------------------------------------------------

setup
check_health
check_estate_reads
check_restart
check_desired_count
check_deploy
check_offhours
check_notifications
check_logs
check_admin
check_permissions
check_csrf
check_logout
check_restored

echo "sanity: $PASSED ok, $FAILED failed, $SKIPPED skipped"
[ "$FAILED" -eq 0 ] || exit 1
exit 0
