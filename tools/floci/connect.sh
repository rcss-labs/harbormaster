#!/usr/bin/env bash
# Connects a running Harbormaster (make floci-serve) to the seeded floci estate through its API.
#
# Usage: tools/floci/connect.sh [--help]
#   HM_FLOCI_PORT   port of the Harbormaster server (default 8482)
#
# Signs in as admin (completing the forced password change on the first sign-in and keeping
# tools/floci/.admin-password current), connects the AWS account "floci" with its three regions, tags the
# core-services cluster Prod, creates user omer (Operator on Dev) and makes one restart and one desired-count change
# on notifications so History is not empty. Idempotent. Never prints the password.
# Exit codes: 0 connected, 2 refused or bad usage, 1 failure.
set -euo pipefail

case "${1:-}" in
  -h | --help) sed -n '2,12p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
  "") ;;
  *) echo "usage: tools/floci/connect.sh [--help]" >&2; exit 2 ;;
esac

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PASSWORD_FILE="$HERE/.admin-password"
# shellcheck source=server-env.sh
. "$HERE/server-env.sh"

BASE="$HM_BASE_URL"
ROLE_ARN="arn:aws:iam::000000000000:role/harbormaster-access"
REGIONS='["eu-west-1","us-east-1","eu-central-1"]'
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
JAR="$WORK/cookies"
CSRF=""

die() { echo "connect: $*" >&2; exit 1; }

# api METHOD PATH [JSON-BODY-FILE]: prints the response body, fails with the status on 4xx/5xx.
api() {
  local method=$1 path=$2 body=${3:-} status
  local args=(-sS -o "$WORK/body" -w '%{http_code}' -X "$method" -b "$JAR" -c "$JAR" -H "X-CSRF-Token: $CSRF")
  [ -n "$body" ] && args+=(-H 'Content-Type: application/json' --data-binary "@$body")
  status=$(curl "${args[@]}" "$BASE$path") || die "cannot reach $BASE; is make floci-serve running?"
  if [ "$status" -ge 400 ]; then
    die "$method $path -> $status $(jq -r '.message // .error // empty' "$WORK/body" 2>/dev/null)"
  fi
  cat "$WORK/body"
}
json() { local file="$WORK/req.json"; printf '%s' "$1" >"$file"; echo "$file"; }

wait_for_server() {
  local i
  for i in $(seq 1 60); do
    curl -fsS -o /dev/null "$BASE/healthz" 2>/dev/null && return 0
    sleep 1
  done
  die "$BASE/healthz did not answer; is make floci-serve running?"
}

sign_in() {
  [ -s "$PASSWORD_FILE" ] || die "no admin password file; run tools/floci/prepare.sh (or make floci-down for a clean start)"
  local password; password=$(cat "$PASSWORD_FILE")
  jq -n --arg u admin --arg p "$password" '{username:$u,password:$p}' >"$WORK/login.json"
  api POST /api/auth/login "$WORK/login.json" >/dev/null 2>"$WORK/login.err" || {
    sed 's/^/connect: /' "$WORK/login.err" >&2
    die "admin sign-in failed; if the database was recreated, run make floci-down first"
  }
  CSRF=$(api GET /api/me | jq -r '.csrf_token')
  if [ "$(api GET /api/me | jq -r '.must_change_password')" = true ]; then
    local next; next=$(openssl rand -hex 16)
    ( umask 077; printf '%s' "$next" >"$PASSWORD_FILE.next" )
    jq -n --arg c "$password" --arg n "$next" '{current:$c,new:$n}' >"$WORK/pw.json"
    api POST /api/auth/password "$WORK/pw.json" >/dev/null
    mv "$PASSWORD_FILE.next" "$PASSWORD_FILE"
    chmod 600 "$PASSWORD_FILE"
    CSRF=$(api GET /api/me | jq -r '.csrf_token')
    echo "connect: completed the forced password change"
  fi
}

connect_account() {
  if api GET /api/accounts | jq -e '.items[]? | select(.name == "floci")' >/dev/null; then
    echo "connect: account floci already connected"
    return
  fi
  local external_id; external_id=$(api GET /api/accounts/external-id | jq -r '.external_id')
  api POST /api/accounts "$(json "$(jq -n --arg role "$ROLE_ARN" --arg ext "$external_id" --argjson regions "$REGIONS" \
    '{name:"floci",role_arn:$role,external_id:$ext,regions:$regions}')")" >/dev/null
  echo "connect: connected account floci"
}

# Prints the id of the tag called $1, creating it with colour $2 when missing.
ensure_tag() {
  local name=$1 color=$2 id
  id=$(api GET /api/tags | jq -r --arg n "$name" '.items[]? | select(.name == $n) | .id' | head -n 1)
  if [ -z "$id" ]; then
    id=$(api POST /api/tags "$(json "$(jq -n --arg n "$name" --arg c "$color" '{name:$n,color:$c}')")" | jq -r '.id')
  fi
  echo "$id"
}

wait_for_clusters() {
  local i count=0
  for i in $(seq 1 90); do
    count=$(api GET /api/clusters | jq '.items | length')
    [ "$count" -ge 4 ] && return 0
    sleep 2
  done
  die "only $count clusters appeared after 180 s"
}

cluster_id() { api GET /api/clusters | jq -r --arg n "$1" '.items[] | select(.name == $n) | .id' | head -n 1; }

tag_core_services_prod() {
  local prod_id cluster; prod_id=$(ensure_tag Prod red)
  cluster=$(cluster_id core-services)
  [ -n "$cluster" ] || die "cluster core-services not found"
  api PUT "/api/tag-assignments/clusters/$cluster" "$(json "$(jq -n --argjson t "$prod_id" '{tag_id:$t}')")" >/dev/null
  echo "connect: tagged core-services Prod"
}

create_omer() {
  local dev_id user_id
  dev_id=$(ensure_tag Dev neutral)
  user_id=$(api GET /api/users | jq -r '.items[]? | select(.username == "omer") | .id' | head -n 1)
  if [ -z "$user_id" ]; then
    # The temporary password in the response is discarded on purpose.
    user_id=$(api POST /api/users "$(json '{"username":"omer","admin":false}')" | jq -r '.user.id')
  fi
  # 7 = View + Logs + Operate, the Operator preset.
  api PUT "/api/permissions/$user_id/$dev_id" "$(json '{"actions":7}')" >/dev/null
  echo "connect: user omer is Operator on Dev"
}

# F7: one restart and one desired-count change through the API, so History and the activity drawer are not empty.
make_changes() {
  local service
  service=$(api GET /api/clusters | jq -r '.items[] | select(.name == "core-services") | .services[]? | select(.name == "notifications") | .id' | head -n 1)
  [ -n "$service" ] || die "service notifications not found"
  if [ "$(api GET "/api/services/$service/operations" | jq '[.items[] | select(.source == "harbormaster" and (.text | test("estart|esired"; "i")))] | length')" -gt 0 ]; then
    echo "connect: notifications already has operations"
    return
  fi
  api POST "/api/services/$service/restart" >/dev/null
  api POST "/api/services/$service/desired-count" "$(json '{"desired":3}')" >/dev/null
  echo "connect: restarted notifications and set desired count to 3"
}

wait_for_server
sign_in
connect_account
wait_for_clusters
tag_core_services_prod
create_omer
make_changes
echo "connect: done ($(api GET /api/clusters | jq '.items | length') clusters)"
