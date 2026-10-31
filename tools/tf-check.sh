#!/usr/bin/env bash
# Validates every Terraform root under terraform/ and checks that each module has an example.
# Local checks only: fmt, init -backend=false, validate and optional tflint. Never plans or applies.
set -euo pipefail

usage() {
  cat <<'USAGE'
Usage: tools/tf-check.sh [-h|--help]

Runs fmt, init (no backend), validate and tflint (if installed) on every
directory under terraform/ that contains *.tf files, and checks that each
terraform/modules/<name> has a terraform/examples/<name> with *.tf files.

Exit codes:
  0  all checks passed
  1  one or more checks failed
  2  usage error or terraform not found on PATH
USAGE
}

case "${1:-}" in
  -h | --help)
    usage
    exit 0
    ;;
  "") ;;
  *)
    usage >&2
    exit 2
    ;;
esac

if ! command -v terraform >/dev/null 2>&1; then
  echo "tf-check: terraform not found on PATH" >&2
  exit 2
fi

script_dir="$(cd "$(dirname "$0")" && pwd)"
cd "$script_dir/.."

# Runs every check on one directory and prints a single OK or FAIL line.
# Returns non-zero when the directory fails.
check_directory() {
  local dir="$1"

  if ! terraform fmt -check -diff "$dir"; then
    echo "FAIL $dir: formatting differs from terraform fmt"
    return 1
  fi
  if ! terraform -chdir="$dir" init -backend=false -input=false >/dev/null; then
    echo "FAIL $dir: terraform init failed"
    return 1
  fi
  if ! terraform -chdir="$dir" validate -no-color; then
    echo "FAIL $dir: terraform validate failed"
    return 1
  fi
  if command -v tflint >/dev/null 2>&1 && ! tflint --chdir "$dir"; then
    echo "FAIL $dir: tflint reported issues"
    return 1
  fi
  echo "OK $dir"
}

failures=0

root_dirs="$(find terraform -type f -name '*.tf' ! -path '*.terraform*' -exec dirname {} \; | sort -u)"
while IFS= read -r dir; do
  check_directory "$dir" || failures=$((failures + 1))
done <<< "$root_dirs"

for module_dir in terraform/modules/*/; do
  [ -d "$module_dir" ] || continue
  module_name="$(basename "$module_dir")"
  if ! compgen -G "terraform/examples/$module_name/*.tf" >/dev/null; then
    echo "FAIL terraform/examples/$module_name: no *.tf files"
    failures=$((failures + 1))
  fi
done

if [ "$failures" -gt 0 ]; then
  echo "tf-check: $failures check(s) failed" >&2
  exit 1
fi
echo "tf-check: all checks passed"
exit 0
