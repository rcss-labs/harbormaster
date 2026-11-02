package saml

import (
	"errors"
	"fmt"
)

// Reason codes are the closed set from the spec's validation table plus
// validator_disagreement and internal. They are written to the audit log and
// never shown to users; UserMessageFor maps them to the three login-page
// messages.
const (
	ReasonMalformed             = "malformed"
	ReasonUnknownRequest        = "unknown_request"
	ReasonBindingMismatch       = "binding_mismatch"
	ReasonMultipleAssertions    = "multiple_assertions"
	ReasonIdPStatus             = "idp_status"
	ReasonUnsigned              = "unsigned"
	ReasonInvalidSignature      = "invalid_signature"
	ReasonDecryptFailed         = "decrypt_failed"
	ReasonSPKeyUnavailable      = "sp_key_unavailable"
	ReasonIssuer                = "issuer"
	ReasonDestination           = "destination"
	ReasonRecipient             = "recipient"
	ReasonInResponseTo          = "in_response_to"
	ReasonExpired               = "expired"
	ReasonNotYetValid           = "not_yet_valid"
	ReasonAudience              = "audience"
	ReasonReplay                = "replay"
	ReasonNoNameID              = "no_nameid"
	ReasonTransientNameID       = "transient_nameid"
	ReasonNoAuthnStatement      = "no_authn_statement"
	ReasonUserCreateFailed      = "user_create_failed"
	ReasonSSOOff                = "sso_off"
	ReasonValidatorDisagreement = "validator_disagreement"
	ReasonInternal              = "internal"
)

// Error is a verification failure with a stable reason code. Err carries
// diagnostic detail for server logs; it never contains raw assertions or key
// material.
type Error struct {
	Reason string
	Err    error
}

func (e *Error) Error() string {
	if e.Err == nil {
		return "saml: " + e.Reason
	}
	return fmt.Sprintf("saml: %s: %v", e.Reason, e.Err)
}

func (e *Error) Unwrap() error { return e.Err }

func newError(reason, format string, args ...any) *Error {
	return &Error{Reason: reason, Err: fmt.Errorf(format, args...)}
}

// ReasonOf returns the reason code of err: "" for nil, ReasonInternal for
// errors that are not an *Error.
func ReasonOf(err error) string {
	if err == nil {
		return ""
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Reason
	}
	return ReasonInternal
}

// UserMessageFor maps a reason code to the login page's ?error= value.
func UserMessageFor(reason string) string {
	switch reason {
	case ReasonIdPStatus:
		return "sso_declined"
	case ReasonSSOOff:
		return "sso_off"
	default:
		return "sso_failed"
	}
}
