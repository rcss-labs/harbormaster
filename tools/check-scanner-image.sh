#!/usr/bin/env bash
# Verifies the runtime contract of a Harbormaster scanner image (non-root, healthcheck, no shell, Syft and Grype
# present, works on a read-only root filesystem with a writable /work). Local docker only; nothing is pushed.
set -euo pipefail

usage() {
  cat <<USAGE
usage: tools/check-scanner-image.sh [--can-run] <image>

  --can-run      only probe whether this host can execute the image (native or emulated);
                 exit 0 if it can, 1 if not
  CHECK_IMAGE_STATIC_ONLY=1   run only the checks that do not execute the image (image exists, user, healthcheck)

Checks, in order; each prints its name on failure and exits 1:
  image exists, non-root user, HEALTHCHECK runs 'healthcheck', 'version' prints one version line,
  no /bin/sh, 'syft version' and 'grype version' under --read-only --tmpfs /work, 'healthcheck' exits 0.

Exit codes: 0 ok, 1 a check failed, 2 usage error.
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

fail() {
  echo "FAIL: $1" >&2
  exit 1
}

docker image inspect "$image" >/dev/null 2>&1 || fail "image exists: image not found: $image"
echo "OK: image exists ($image)"

platform="$(docker inspect --format '{{.Os}}/{{.Architecture}}' "$image")"

user="$(docker inspect --format '{{.Config.User}}' "$image")"
case "$user" in
  '' | root | root:* | 0 | 0:*) fail "non-root user: Config.User is '$user'" ;;
esac
echo "OK: non-root user ($user)"

healthcheck="$(docker inspect --format '{{if .Config.Healthcheck}}{{json .Config.Healthcheck.Test}}{{else}}none{{end}}' "$image")"
case "$healthcheck" in
  *'"/harbormaster-scanner","healthcheck"'*) echo "OK: HEALTHCHECK runs 'healthcheck' ($healthcheck)" ;;
  *) fail "HEALTHCHECK runs healthcheck: Config.Healthcheck.Test is $healthcheck" ;;
esac

if [ "${CHECK_IMAGE_STATIC_ONLY:-0}" = 1 ]; then
  echo "static checks passed (runtime checks skipped)"
  exit 0
fi

version_output="$(docker run --rm --platform "$platform" "$image" version)" || fail "version: 'version' command failed"
if ! printf '%s\n' "$version_output" | grep -Eq '^[^[:space:]]+$'; then
  fail "version: expected one non-empty version line, got: $version_output"
fi
echo "OK: version prints '$version_output'"

if docker run --rm --platform "$platform" --entrypoint /bin/sh "$image" -c true >/dev/null 2>&1; then
  fail "no /bin/sh: image contains a shell"
fi
echo "OK: no /bin/sh in image"

# Read-only root with a writable /work mirrors the Fargate task (readonlyRootFilesystem plus a volume).
for tool in syft grype; do
  tool_output="$(docker run --rm --platform "$platform" --read-only --tmpfs /work \
    --entrypoint "/usr/local/bin/$tool" "$image" version)" || fail "$tool version: '$tool version' failed on a read-only root"
  printf '%s\n' "$tool_output" | grep -q -i 'version' || fail "$tool version: unexpected output: $tool_output"
  echo "OK: $tool version runs on a read-only root"
done

docker run --rm --platform "$platform" --read-only --tmpfs /work "$image" healthcheck >/dev/null 2>&1 ||
  fail "healthcheck exits 0: 'healthcheck' failed on a read-only root"
echo "OK: healthcheck exits 0"
echo "all scanner image checks passed"
