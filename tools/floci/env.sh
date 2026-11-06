#!/usr/bin/env bash
# Pins the shell to the local floci emulator. Source it (". tools/floci/env.sh"); never reach real AWS from here.
#
# Usage: . tools/floci/env.sh        (sourced; returns 2 when the environment is not safe)
#        tools/floci/env.sh --help
#        tools/floci/env.sh          (run directly: only checks and prints "floci environment ok")
#
# Refuses (status 2) when AWS_ENDPOINT_URL is set to anything but a 127.0.0.1 or localhost host, or when AWS_PROFILE
# is set to anything but "floci". It then points the AWS CLI and SDKs at the isolated config and credentials files
# in this directory, so neither ~/.aws nor any ambient credential is read.

_floci_sourced=0
[[ "${BASH_SOURCE[0]}" != "$0" ]] && _floci_sourced=1

_floci_fail() { # status message
  echo "floci env: $2" >&2
  if [[ $_floci_sourced -eq 1 ]]; then return "$1"; else exit "$1"; fi
}

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
  sed -n '2,10p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
  if [[ $_floci_sourced -eq 1 ]]; then return 0; else exit 0; fi
fi

FLOCI_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
_floci_endpoint="${AWS_ENDPOINT_URL:-http://127.0.0.1:4566}"
_floci_host="${_floci_endpoint#*://}"
_floci_host="${_floci_host%%[:/]*}"

case "$_floci_host" in
  127.0.0.1 | localhost) ;;
  *) _floci_fail 2 "AWS_ENDPOINT_URL host '$_floci_host' is not local; refusing" || return $? ;;
esac
case "${AWS_PROFILE:-floci}" in
  floci) ;;
  *) _floci_fail 2 "AWS_PROFILE is '${AWS_PROFILE}', not floci; refusing" || return $? ;;
esac

export AWS_CONFIG_FILE="$FLOCI_DIR/aws-config"
export AWS_SHARED_CREDENTIALS_FILE="$FLOCI_DIR/aws-credentials"
export AWS_PROFILE=floci
export AWS_ENDPOINT_URL="$_floci_endpoint"
export AWS_EC2_METADATA_DISABLED=true
export AWS_SDK_LOAD_CONFIG=1
export AWS_DEFAULT_REGION=eu-west-1
# Drop ambient credentials so the dummy floci keys are the only ones in play.
unset AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY AWS_SESSION_TOKEN AWS_SECURITY_TOKEN AWS_ROLE_ARN AWS_WEB_IDENTITY_TOKEN_FILE AWS_CONTAINER_CREDENTIALS_FULL_URI AWS_CONTAINER_CREDENTIALS_RELATIVE_URI
unset _floci_endpoint _floci_host

if [[ $_floci_sourced -eq 0 ]]; then echo "floci environment ok"; fi
