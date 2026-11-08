package awsx

import (
	"errors"

	"github.com/aws/smithy-go"
)

var accessDeniedCodes = map[string]bool{
	"AccessDeniedException": true,
	"AccessDenied":          true,
	"UnauthorizedOperation": true,
}

// IsAccessDenied reports whether err is an AWS authorization failure, so callers
// can tell a missing permission apart from a throttle or an outage.
func IsAccessDenied(err error) bool {
	var apiErr smithy.APIError
	return errors.As(err, &apiErr) && accessDeniedCodes[apiErr.ErrorCode()]
}
