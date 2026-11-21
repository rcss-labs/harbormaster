package awsx

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ErrBadRoleARN marks a stored role ARN that is not an IAM role ARN, so no derived name can be built from it.
var ErrBadRoleARN = errors.New("not an IAM role ARN")

// SuffixRoles wraps a RoleSource and appends suffix to the role name of the ARN it returns, so one stored role per
// account yields one least-privilege role per service (harbormaster-access-poller, -coordinator). An empty suffix
// returns inner unchanged.
func SuffixRoles(inner RoleSource, suffix string) RoleSource {
	if suffix == "" {
		return inner
	}
	return suffixRoles{inner: inner, suffix: suffix}
}

type suffixRoles struct {
	inner  RoleSource
	suffix string
}

func (s suffixRoles) Role(ctx context.Context, accountID string) (string, string, error) {
	arn, external, err := s.inner.Role(ctx, accountID)
	if err != nil {
		return "", "", err
	}
	derived, err := withRoleSuffix(arn, s.suffix)
	if err != nil {
		return "", "", err
	}
	return derived, external, nil
}

// withRoleSuffix turns arn:<partition>:iam::<account>:role/<path/>name into the same ARN with suffix on the name. The
// path stays where it is: a role's name is its last segment.
func withRoleSuffix(arn, suffix string) (string, error) {
	parts := strings.SplitN(arn, ":", 6)
	if len(parts) != 6 || parts[0] != "arn" || parts[2] != "iam" || parts[4] == "" {
		return "", fmt.Errorf("%w: %q", ErrBadRoleARN, arn)
	}
	resource, ok := strings.CutPrefix(parts[5], "role/")
	if !ok || resource == "" || strings.HasSuffix(resource, "/") {
		return "", fmt.Errorf("%w: %q", ErrBadRoleARN, arn)
	}
	return strings.Join(parts[:5], ":") + ":role/" + resource + suffix, nil
}
