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
	"anchore/syft"
	"anchore/grype"
	"valkey-io/valkey-go"
	"cloud.google.com/go"
	"cloud.google.com/go/auth"
	"cloud.google.com/go/compute/metadata"
	"github.com/anthropics/anthropic-sdk-go"
	"github.com/aws/aws-sdk-go-v2"
	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/ec2/imds"
	"github.com/aws/aws-sdk-go-v2/internal/configsources"
	"github.com/aws/aws-sdk-go-v2/internal/endpoints/v2"
	"github.com/aws/aws-sdk-go-v2/internal/v4a"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding"
	"github.com/aws/aws-sdk-go-v2/service/internal/presigned-url"
	"github.com/aws/aws-sdk-go-v2/service/signin"
	"github.com/aws/aws-sdk-go-v2/service/sso"
	"github.com/aws/aws-sdk-go-v2/service/ssooidc"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"
	"github.com/bahlo/generic-list-go"
	"github.com/buger/jsonparser"
	"github.com/cespare/xxhash/v2"
	"github.com/coder/websocket"
	"github.com/felixge/httpsnoop"
	"github.com/go-logr/logr"
	"github.com/go-logr/stdr"
	"github.com/google/go-cmp"
	"github.com/google/s2a-go"
	"github.com/google/uuid"
	"github.com/googleapis/enterprise-certificate-proxy"
	"github.com/googleapis/gax-go/v2"
	"github.com/gorilla/websocket"
	"github.com/invopop/jsonschema"
	"github.com/openai/openai-go/v3"
	"github.com/pb33f/ordered-map/v2"
	"github.com/standard-webhooks/standard-webhooks/libraries"
	"github.com/tidwall/gjson"
	"github.com/tidwall/match"
	"github.com/tidwall/pretty"
	"github.com/tidwall/sjson"
	"go.opentelemetry.io/auto/sdk"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"go.yaml.in/yaml/v4"
	"golang.org/x/crypto"
	"golang.org/x/net"
	"golang.org/x/sync"
	"golang.org/x/sys"
	"golang.org/x/text"
	"google.golang.org/genai"
	"google.golang.org/genproto/googleapis/rpc"
	"google.golang.org/grpc"
	"google.golang.org/protobuf"
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
