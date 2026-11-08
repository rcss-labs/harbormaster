package scanner

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"

	"github.com/rcss-labs/harbormaster/internal/scan"
)

// ECRClient is the part of the ECR API the scanner uses.
type ECRClient interface {
	GetAuthorizationToken(ctx context.Context, in *ecr.GetAuthorizationTokenInput, opts ...func(*ecr.Options)) (*ecr.GetAuthorizationTokenOutput, error)
	BatchGetImage(ctx context.Context, in *ecr.BatchGetImageInput, opts ...func(*ecr.Options)) (*ecr.BatchGetImageOutput, error)
	DescribeImages(ctx context.Context, in *ecr.DescribeImagesInput, opts ...func(*ecr.Options)) (*ecr.DescribeImagesOutput, error)
}

const (
	mediaOCIIndex     = "application/vnd.oci.image.index.v1+json"
	mediaDockerList   = "application/vnd.docker.distribution.manifest.list.v2+json"
	mediaOCIManifest  = "application/vnd.oci.image.manifest.v1+json"
	mediaDockerV2     = "application/vnd.docker.distribution.manifest.v2+json"
	scanSessionName   = "harbormaster-scan"
	defaultPlatform   = "linux/amd64"
	accessDeniedCode  = "AccessDenied"
	accessDeniedCode2 = "AccessDeniedException"
	throttlingCode    = "ThrottlingException"
)

// NewAssumedECRClient returns a NewECR function that assumes the per-account
// scan role with its external ID and builds a regional ECR client.
func NewAssumedECRClient(base aws.Config) func(ctx context.Context, role, externalID, region string) (ECRClient, error) {
	return func(_ context.Context, role, externalID, region string) (ECRClient, error) {
		cfg := base.Copy()
		cfg.Region = region
		provider := stscreds.NewAssumeRoleProvider(sts.NewFromConfig(base), role, func(o *stscreds.AssumeRoleOptions) {
			o.ExternalID = aws.String(externalID)
			o.RoleSessionName = scanSessionName
		})
		cfg.Credentials = aws.NewCredentialsCache(provider)
		return ecr.NewFromConfig(cfg), nil
	}
}

// ResolvePlatformDigest picks the manifest of platform ("linux/arm64",
// optionally with a variant) from an OCI index or Docker manifest list.
// For a single-image manifest it returns an empty digest and isIndex false:
// the contract is that "" means the caller's own image digest is already the
// platform digest. A digest from the index must match sha256:<64 hex>.
func ResolvePlatformDigest(manifest []byte, mediaType, platform string) (digest string, isIndex bool, err error) {
	if mediaType != mediaOCIIndex && mediaType != mediaDockerList {
		return "", false, nil
	}
	if platform == "" {
		platform = defaultPlatform
	}
	parts := strings.Split(platform, "/")
	if len(parts) < 2 {
		return "", true, fmt.Errorf("platform %q is not os/architecture", platform)
	}
	var index struct {
		Manifests []struct {
			Digest   string `json:"digest"`
			Platform struct {
				OS           string `json:"os"`
				Architecture string `json:"architecture"`
				Variant      string `json:"variant"`
			} `json:"platform"`
		} `json:"manifests"`
	}
	if err := json.Unmarshal(manifest, &index); err != nil {
		return "", true, errors.New("image index is not valid JSON")
	}
	for _, m := range index.Manifests {
		if m.Platform.OS == parts[0] && m.Platform.Architecture == parts[1] &&
			(len(parts) < 3 || m.Platform.Variant == parts[2]) {
			if !sbomDigestPattern.MatchString(m.Digest) {
				return "", true, errors.New("image index lists a malformed digest")
			}
			return m.Digest, true, nil
		}
	}
	return "", true, fmt.Errorf("image index has no manifest for %s", platform)
}

// awsFailureCode maps an AWS error to a coded outcome; ok is false for other errors.
func awsFailureCode(err error) (code string, ok bool) {
	switch apiErrorCode(err) {
	case accessDeniedCode, accessDeniedCode2:
		return scan.CodePullDenied, true
	case throttlingCode:
		return scan.CodeRateLimited, true
	}
	return "", false
}

// registryAuth is the ECR login for one registry host.
type registryAuth struct{ username, password string }

func fetchRegistryAuth(ctx context.Context, client ECRClient) (registryAuth, error) {
	out, err := client.GetAuthorizationToken(ctx, &ecr.GetAuthorizationTokenInput{})
	if err != nil {
		return registryAuth{}, err
	}
	if len(out.AuthorizationData) == 0 {
		return registryAuth{}, errors.New("ECR returned no authorization data")
	}
	raw, err := base64.StdEncoding.DecodeString(aws.ToString(out.AuthorizationData[0].AuthorizationToken))
	if err != nil {
		return registryAuth{}, errors.New("ECR authorization token is not valid")
	}
	username, password, found := strings.Cut(string(raw), ":")
	if !found {
		return registryAuth{}, errors.New("ECR authorization token is not valid")
	}
	return registryAuth{username: username, password: password}, nil
}

// apiErrorCode is the AWS error code of err, or "" for any other error.
func apiErrorCode(err error) string {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode()
	}
	return ""
}
