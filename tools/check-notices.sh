#!/usr/bin/env bash
# Verifies that licence files and third-party notices are present and consistent.
# Usage: tools/check-notices.sh   (no arguments; run from anywhere inside the repo)
# Exit codes: 0 all checks pass, 1 one or more checks failed.
set -euo pipefail

cd "$(dirname "$0")/.."

failures=()

fail() {
	failures+=("$1")
}

# LICENSE must be the MIT licence.
if [[ ! -f LICENSE ]]; then
	fail "LICENSE is missing"
elif [[ "$(head -n 1 LICENSE)" != "MIT License" ]]; then
	fail "LICENSE first line is not 'MIT License'"
fi

# Every execbridge Go file ported from AWS must carry the Apache 2.0 header.
while IFS= read -r goFile; do
	if grep -q -F "Amazon.com" "$goFile" && ! grep -q -F "Licensed under the Apache License, Version 2.0" "$goFile"; then
		fail "$goFile mentions Amazon.com but lacks the Apache-2.0 licence header"
	fi
done < <(find internal/execbridge -name '*.go' -type f)

# Apache-2.0 licence copy for the session-manager-plugin port.
apacheCopy="third_party/licenses/session-manager-plugin-APACHE-2.0.txt"
if [[ ! -f "$apacheCopy" ]]; then
	fail "$apacheCopy is missing"
elif ! grep -q -F "Apache License" "$apacheCopy"; then
	fail "$apacheCopy does not contain 'Apache License'"
fi

# Notices must name every bundled third-party component and its licence.
notices="THIRD_PARTY_NOTICES.md"
requiredNoticeStrings=(
	"session-manager-plugin"
	"Apache-2.0"
	"IBM Plex Sans"
	"Geist Mono"
	"SIL Open Font License"
	"crewjam/saml"
	"BSD-2-Clause"
	"beevik/etree"
	"russellhaering/goxmldsig"
	"mattermost/xml-roundtrip-validator"
	"jonboulle/clockwork"
	"golang-jwt/jwt"
)
if [[ ! -f "$notices" ]]; then
	fail "$notices is missing"
else
	for requiredString in "${requiredNoticeStrings[@]}"; do
		if ! grep -q -F "$requiredString" "$notices"; then
			fail "$notices does not contain '$requiredString'"
		fi
	done
fi

# Font licence copies must exist and be copies of the OFL text (which starts with the copyright line).
for fontLicence in third_party/licenses/OFL-ibm-plex-sans.txt third_party/licenses/OFL-geist-mono.txt; do
	if [[ ! -f "$fontLicence" ]]; then
		fail "$fontLicence is missing"
	elif [[ "$(head -c 9 "$fontLicence")" != "Copyright" ]]; then
		fail "$fontLicence does not start with 'Copyright'"
	fi
done

if [[ ${#failures[@]} -gt 0 ]]; then
	for message in "${failures[@]}"; do
		echo "check-notices: FAIL: $message"
	done
	exit 1
fi

echo "check-notices: OK"
exit 0
