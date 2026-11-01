#!/usr/bin/env bash
# Verifies the runtime contract of a Harbormaster image (non-root, healthcheck, distroless,
# starts healthy on a read-only root filesystem). Local docker only; nothing is pushed.
# Works for any image architecture; a non-native image runs under emulation.
set -euo pipefail

usage() {
  cat <<USAGE
usage: tools/check-image.sh [--can-run] <image>

  --can-run      only probe whether this host can execute the image (native or emulated);
                 exit 0 if it can, 1 if not
  CHECK_IMAGE_STATIC_ONLY=1   run only the checks that do not execute the image (1-3)

Checks the image's runtime contract:
  1. image exists locally
  2. Config.User is nonroot:nonroot
  3. Config.Healthcheck runs 'healthcheck'
  4. 'version' prints "harbormaster <version>"
  5. no /bin/sh (distroless)
  6. 'serve --fake-aws' against a throwaway postgres, with --read-only, reaches healthy within 90s

Exit codes: 0 ok, 1 a check failed, 2 usage error, 3 health timeout.
USAGE
}

can_run_only=0
if [ "${1:-}" = "--can-run" ]; then
  can_run_only=1
  shift
fi

if [ "$#" -ne 1 ] || [ "$1" = "-h" ] || [ "$1" = "--help" ]; then
  usage >&2
  exit 2
fi

image="$1"

if [ "$can_run_only" = 1 ]; then
  platform="$(docker inspect --format '{{.Os}}/{{.Architecture}}' "$image")" || exit 1
  docker run --rm --platform "$platform" "$image" version >/dev/null 2>&1
  exit $?
fi
# postgres:16.4-alpine pinned by digest
postgres_image="postgres:16.4-alpine@sha256:5660c2cbfea50c7a9127d17dc4e48543eedd3d7a41a595a2dfa572471e37e64c"
suffix="$$-$(date +%s)"
network="hm-check-net-${suffix}"
pg_name="hm-check-pg-${suffix}"
app_name="hm-check-app-${suffix}"

fail() {
  echo "FAIL: $1" >&2
  exit 1
}

cleanup() {
  docker rm -f "$app_name" "$pg_name" >/dev/null 2>&1 || true
  docker network rm "$network" >/dev/null 2>&1 || true
}
trap cleanup EXIT

docker image inspect "$image" >/dev/null 2>&1 || fail "image not found: $image"
echo "OK: image exists ($image)"

# Run the image as built: on a host of another architecture Docker emulates it (slower, but it is
# the exact artifact that ships).
platform="$(docker inspect --format '{{.Os}}/{{.Architecture}}' "$image")"
echo "OK: image platform $platform (host: $(docker info --format '{{.OSType}}/{{.Architecture}}'))"

user="$(docker inspect --format '{{.Config.User}}' "$image")"
[ "$user" = "nonroot:nonroot" ] || fail "Config.User is '$user', want 'nonroot:nonroot'"
echo "OK: runs as nonroot:nonroot"

healthcheck="$(docker inspect --format '{{json .Config.Healthcheck.Test}}' "$image")"
case "$healthcheck" in
  *healthcheck*) echo "OK: HEALTHCHECK runs 'healthcheck' ($healthcheck)" ;;
  *) fail "Config.Healthcheck.Test does not contain 'healthcheck': $healthcheck" ;;
esac

if [ "${CHECK_IMAGE_STATIC_ONLY:-0}" = 1 ]; then
  echo "static checks passed (runtime checks skipped)"
  exit 0
fi

version_output="$(docker run --rm --platform "$platform" "$image" version)" || fail "'version' command failed"
printf '%s\n' "$version_output" | grep -Eq '^harbormaster ' || fail "version output does not start with 'harbormaster ': $version_output"
echo "OK: version prints '$version_output'"

if docker run --rm --platform "$platform" --entrypoint /bin/sh "$image" -c true >/dev/null 2>&1; then
  fail "image contains /bin/sh; expected distroless"
fi
echo "OK: no /bin/sh in image (distroless)"

session_key="$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')"
docker network create "$network" >/dev/null
docker run -d --name "$pg_name" --network "$network" \
  -e POSTGRES_USER=hm -e POSTGRES_PASSWORD=hm -e POSTGRES_DB=hm \
  "$postgres_image" >/dev/null
for _ in $(seq 1 30); do
  docker exec "$pg_name" pg_isready -h 127.0.0.1 -U hm -d hm >/dev/null 2>&1 && break
  sleep 1
done
docker exec "$pg_name" pg_isready -h 127.0.0.1 -U hm -d hm >/dev/null 2>&1 || fail "postgres did not become ready"

# --read-only without a tmpfs proves the binary needs no writable root filesystem (matches ECS).
docker run -d --name "$app_name" --network "$network" --platform "$platform" --read-only \
  -e "HM_DATABASE_URL=postgres://hm:hm@${pg_name}:5432/hm?sslmode=disable" \
  -e "HM_SESSION_KEY=${session_key}" \
  -e HM_COOKIE_SECURE=false \
  "$image" serve --fake-aws >/dev/null

status="none"
for _ in $(seq 1 90); do
  status="$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$app_name" 2>/dev/null || echo gone)"
  [ "$status" = "healthy" ] && break
  if [ "$(docker inspect --format '{{.State.Running}}' "$app_name" 2>/dev/null || echo false)" != "true" ]; then
    docker logs "$app_name" 2>&1 | tail -20 >&2 || true
    fail "app container exited before becoming healthy"
  fi
  sleep 1
done
if [ "$status" != "healthy" ]; then
  docker logs "$app_name" 2>&1 | tail -20 >&2 || true
  echo "FAIL: container not healthy within 90s (status: $status)" >&2
  exit 3
fi
echo "OK: container healthy on read-only root filesystem"
echo "all image checks passed"
