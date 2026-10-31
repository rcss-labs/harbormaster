package awsx

import (
	"errors"

	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/smithy-go"
)

var throttleCodes = map[string]bool{
	"ThrottlingException":      true,
	"TooManyRequestsException": true,
	"Throttling":               true,
	"RequestLimitExceeded":     true,
}

// IsThrottle reports whether err is an AWS throttling error.
func IsThrottle(err error) bool {
	if err == nil {
		return false
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) && throttleCodes[apiErr.ErrorCode()] {
		return true
	}
	return retry.IsErrorThrottles(retry.DefaultThrottles).IsErrorThrottle(err).Bool()
}
