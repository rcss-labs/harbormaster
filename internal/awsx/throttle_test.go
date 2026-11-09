package awsx

import (
	"errors"
	"fmt"
	"testing"

	"github.com/aws/smithy-go"
)

func TestIsThrottle_Table(t *testing.T) {
	for _, code := range []string{"ThrottlingException", "TooManyRequestsException", "Throttling", "RequestLimitExceeded"} {
		err := fmt.Errorf("wrapped: %w", &smithy.GenericAPIError{Code: code})
		if !IsThrottle(err) {
			t.Errorf("%s should be a throttle", code)
		}
	}
	if IsThrottle(&smithy.GenericAPIError{Code: "AccessDeniedException"}) {
		t.Error("AccessDeniedException must not be a throttle")
	}
	if IsThrottle(nil) {
		t.Error("nil must not be a throttle")
	}
	if IsThrottle(errors.New("boom")) {
		t.Error("plain error must not be a throttle")
	}
}

func TestIsAccessDenied(t *testing.T) {
	for _, code := range []string{"AccessDeniedException", "AccessDenied", "UnauthorizedOperation"} {
		if !IsAccessDenied(&smithy.GenericAPIError{Code: code}) {
			t.Errorf("%s should be access denied", code)
		}
	}
	if !IsAccessDenied(fmt.Errorf("wrapped: %w", &smithy.GenericAPIError{Code: "AccessDeniedException"})) {
		t.Error("wrapped AccessDeniedException should be access denied")
	}
	if IsAccessDenied(&smithy.GenericAPIError{Code: "ThrottlingException"}) {
		t.Error("a throttle is not access denied")
	}
	if IsAccessDenied(nil) || IsAccessDenied(errors.New("boom")) {
		t.Error("nil and plain errors are not access denied")
	}
}
