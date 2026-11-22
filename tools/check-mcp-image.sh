#!/usr/bin/env bash
# Verifies the runtime contract of a Harbormaster MCP gateway image (non-root, healthcheck, no shell, static binary,
# no database, cache, AWS or SAML code linked in, starts on a read-only root filesystem). Local docker only;
# nothing is pushed.
set -euo pipefail

usage() {
  cat <<USAGE
usage: tools/check-mcp-image.sh [--can-run] <image> <version>

  version        the VERSION the image was built with; 'version' must print 'harbormaster-mcp <version>'
                 (optionally followed by the commit), so a stale image or 'dev' fails
  --can-run      only probe whether this host can execute the image (native or emulated);
                 exit 0 if it can, 1 if not
  CHECK_IMAGE_STATIC_ONLY=1   run only the checks that do not execute the image: image exists, user, healthcheck,
                 no bin/sh in the exported filesystem, 'file' on the binary (architecture, static) and the
                 embedded module list

Checks, in order; each prints its name on failure and exits 1:
  image exists, non-root user, HEALTHCHECK runs 'healthcheck', 'version' prints the expected line, no /bin/sh,
  static binary of the image's architecture, embedded module list (see below), 'serve' with a minimal environment
  starts on a read-only root and 'healthcheck' exits 0, 'serve' with no environment exits 2.

Embedded module list (go version -m on the exported binary, needs a Go toolchain on this host): the gateway must
contain no database, cache, AWS or SAML code. It fails on any of: ${forbidden_modules[*]:-}.
These modules are allowed on purpose: golang.org/x/oauth2 (and its auth and oauthex packages) is linked by
github.com/modelcontextprotocol/go-sdk v1.8.0, whose auth package imports it; it is only compiled in, and the
gateway never configures it. Every other module is listed in the output for review.

Exit codes: 0 ok, 1 a check failed, 2 usage error.
USAGE
}

# Substrings matched against the module paths in the binary. redis covers go-redis and rueidis style clients.
forbidden_modules=(jackc/pgx valkey-io redis aws-sdk-go-v2 aws/aws-sdk-go crewjam/saml)

can_run_only=0
if [ "${1:-}" = "--can-run" ]; then
  can_run_only=1
  shift
fi

if [ "$#" -ne 2 ] || [ "$1" = "-h" ] || [ "$1" = "--help" ]; then
  usage >&2
  exit 2
fi

image="$1"
expected_version="$2"

if [ "$can_run_only" = 1 ]; then
  platform="$(docker inspect --format '{{.Os}}/{{.Architecture}}' "$image")" || exit 1
  docker run --rm --platform "$platform" "$image" version >/dev/null 2>&1
  exit $?
fi

cleanup_containers=()
cleanup_dirs=()
cleanup() {
  local name dir
  for name in ${cleanup_containers[@]+"${cleanup_containers[@]}"}; do docker rm -f "$name" >/dev/null 2>&1 || true; done
  for dir in ${cleanup_dirs[@]+"${cleanup_dirs[@]}"}; do
    if [ -n "$dir" ] && [ -d "$dir" ]; then rm -r -- "$dir"; fi
  done
  return 0
}
trap cleanup EXIT

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
  *'"/harbormaster-mcp","healthcheck"'*) echo "OK: HEALTHCHECK runs 'healthcheck' ($healthcheck)" ;;
  *) fail "HEALTHCHECK runs healthcheck: Config.Healthcheck.Test is $healthcheck" ;;
esac

# Static checks never execute the image: export the filesystem and inspect it with tar, file and go version -m.
static_checks() {
  local container extract_dir filesystem_listing want_machine file_output modules hit
  container="hm-mcp-check-$$-$(date +%s)"
  extract_dir="$(mktemp -d)"
  cleanup_containers+=("$container")
  cleanup_dirs+=("$extract_dir")
  docker create --name "$container" --platform "$platform" "$image" >/dev/null || fail "image exists: docker create failed"
  # Listing into a variable first: with pipefail, grep -q closing the pipe early would hide a match.
  filesystem_listing="$(docker export "$container" | tar -t)" || fail "no /bin/sh: could not list the exported filesystem"
  if printf '%s\n' "$filesystem_listing" | grep -Eq '^(\./)?bin/sh$'; then
    fail "no /bin/sh: exported filesystem contains bin/sh"
  fi
  echo "OK: no bin/sh in the exported filesystem"

  case "${platform#*/}" in
    amd64) want_machine="x86-64" ;;
    arm64) want_machine="aarch64" ;;
    *) fail "binary architecture: unsupported image architecture ${platform#*/}" ;;
  esac
  docker cp "$container:/harbormaster-mcp" "$extract_dir/harbormaster-mcp" >/dev/null || fail "binary architecture: /harbormaster-mcp is missing"
  file_output="$(file -b "$extract_dir/harbormaster-mcp")"
  case "$file_output" in
    *"$want_machine"*"statically linked"*) ;;
    *) fail "binary architecture: /harbormaster-mcp is not a static $want_machine binary: $file_output" ;;
  esac
  echo "OK: harbormaster-mcp is a static $want_machine binary"

  command -v go >/dev/null 2>&1 || fail "embedded modules: no go toolchain on this host to run 'go version -m'"
  modules="$(go version -m "$extract_dir/harbormaster-mcp")" || fail "embedded modules: 'go version -m' failed on the binary"
  for hit in "${forbidden_modules[@]}"; do
    if printf '%s\n' "$modules" | grep -q "^[[:space:]]*dep[[:space:]]\+[^[:space:]]*$hit"; then
      fail "embedded modules: the binary links a module matching '$hit'"
    fi
  done
  echo "OK: no database, cache, AWS or SAML module in the binary; modules present:"
  printf '%s\n' "$modules" | awk '$1 == "dep" { print "    " $2 " " $3 }'
}

if [ "${CHECK_IMAGE_STATIC_ONLY:-0}" = 1 ]; then
  static_checks
  echo "static checks passed (runtime checks skipped)"
  exit 0
fi

version_output="$(docker run --rm --platform "$platform" "$image" version)" || fail "version: 'version' command failed"
case "$version_output" in
  "harbormaster-mcp $expected_version" | "harbormaster-mcp $expected_version "*) ;;
  *) fail "version: expected 'harbormaster-mcp $expected_version', got: $version_output" ;;
esac
echo "OK: version prints '$version_output'"

if docker run --rm --platform "$platform" --entrypoint /bin/sh "$image" -c true >/dev/null 2>&1; then
  fail "no /bin/sh: image contains a shell"
fi
echo "OK: no /bin/sh in image"

static_checks

serve_container="hm-mcp-serve-$$-$(date +%s)"
cleanup_containers+=("$serve_container")
docker run -d --name "$serve_container" --platform "$platform" --read-only \
  -e HM_MCP_UPSTREAM_URL=https://upstream.invalid -e HM_MCP_PUBLIC_URL=https://mcp.invalid \
  "$image" serve >/dev/null || fail "serve starts read-only: docker run failed"
healthy=0
for _ in 1 2 3 4 5 6 7 8 9 10; do
  if docker exec "$serve_container" /harbormaster-mcp healthcheck >/dev/null 2>&1; then
    healthy=1
    break
  fi
  [ "$(docker inspect --format '{{.State.Running}}' "$serve_container")" = true ] ||
    fail "serve starts read-only: container exited: $(docker logs "$serve_container" 2>&1 | tail -n 3)"
  sleep 1
done
[ "$healthy" = 1 ] || fail "healthcheck exits 0: 'healthcheck' did not succeed against a running 'serve'"
echo "OK: serve starts on a read-only root and healthcheck exits 0"
docker rm -f "$serve_container" >/dev/null 2>&1 || true

set +e
docker run --rm --platform "$platform" --read-only "$image" serve >/dev/null 2>&1
serve_status=$?
set -e
[ "$serve_status" = 2 ] || fail "serve without configuration exits 2: exit status was $serve_status"
echo "OK: serve without configuration exits 2"
echo "all MCP image checks passed"
