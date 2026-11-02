package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/aws/smithy-go"

	"github.com/rcss-labs/harbormaster/internal/auth"
	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// APIError is an error that carries its HTTP response; fail passes it through unchanged.
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string { return e.Message }

// invalid builds a 422 validation error.
func invalid(message string) *APIError {
	return &APIError{Status: http.StatusUnprocessableEntity, Code: "invalid", Message: message}
}

// errRateLimited is the 429 for the per-IP bucket on the SSO start routes. The existing "throttled"
// code means AWS is throttling us (503), so it is not reused.
var errRateLimited = &APIError{Status: http.StatusTooManyRequests, Code: "rate_limited", Message: "Too many requests. Try again shortly."}

// Messages for the SSO lockout guards (spec D8).
const (
	noBreakGlassMessage   = "Flag at least one global admin as break-glass before turning on single sign-on."
	lastBreakGlassMessage = "Keep at least one break-glass global admin while single sign-on is on."
	needsPasswordMessage  = "Set a password for this user first."
)

// writeJSON writes v as the JSON response body with the given status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes the standard {"error":{"code","message"}} body.
func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": msg}})
}

// forbiddenAction answers 403 for a visible resource whose action the caller lacks.
func forbiddenAction(w http.ResponseWriter, action authz.Action, tagName string) {
	name := "this action"
	for _, a := range authz.ActionNames() {
		if a.Action == action {
			name = a.Name
		}
	}
	writeError(w, http.StatusForbidden, "forbidden", "You need "+name+" on "+tagName)
}

// fail maps err to its HTTP response. Unrecognised errors are logged and answered with a generic 500.
func (s *Server) fail(w http.ResponseWriter, err error) {
	var apiErr *APIError
	var validation *auth.ValidationError
	var awsErr smithy.APIError
	switch {
	case errors.As(err, &apiErr):
		writeError(w, apiErr.Status, apiErr.Code, apiErr.Message)
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "Not found.")
	case errors.Is(err, store.ErrConflict), errors.Is(err, auth.ErrSetupDone):
		writeError(w, http.StatusConflict, "conflict", "That conflicts with existing data.")
	case errors.As(err, &validation):
		writeError(w, http.StatusUnprocessableEntity, "invalid", validation.Msg)
	case errors.Is(err, store.ErrNoBreakGlassAdmin):
		writeError(w, http.StatusUnprocessableEntity, "invalid", noBreakGlassMessage)
	case errors.Is(err, store.ErrLastBreakGlassAdmin):
		writeError(w, http.StatusUnprocessableEntity, "invalid", lastBreakGlassMessage)
	case errors.Is(err, store.ErrNeedsPassword):
		writeError(w, http.StatusUnprocessableEntity, "invalid", needsPasswordMessage)
	case errors.Is(err, auth.ErrNoPassword):
		writeError(w, http.StatusUnprocessableEntity, "invalid", auth.ErrNoPassword.Error())
	case errors.Is(err, store.ErrAlreadyLinked):
		writeError(w, http.StatusConflict, "already_linked", "This account is already linked to an SSO identity.")
	case errors.Is(err, store.ErrSSOIdentityTaken):
		writeError(w, http.StatusConflict, "sso_identity_taken", "That identity is already linked to, or reserved for, another user.")
	case errors.Is(err, store.ErrNotLinked):
		writeError(w, http.StatusNotFound, "not_found", "Not found.")
	case errors.Is(err, auth.ErrSSORequired):
		writeError(w, http.StatusForbidden, "sso_required", auth.ErrSSORequired.Error())
	case errors.Is(err, auth.ErrBadCredentials):
		writeError(w, http.StatusUnauthorized, "unauthenticated", auth.ErrBadCredentials.Error())
	case errors.Is(err, auth.ErrWrongPassword):
		writeError(w, http.StatusUnauthorized, "unauthenticated", auth.ErrWrongPassword.Error())
	case errors.Is(err, auth.ErrBadToken):
		writeError(w, http.StatusForbidden, "bad_setup_token", auth.ErrBadToken.Error())
	case awsx.IsThrottle(err):
		writeError(w, http.StatusServiceUnavailable, "throttled", "AWS is throttling requests. Try again shortly.")
	case errors.As(err, &awsErr):
		s.log().Warn("aws error", "error", err)
		writeError(w, http.StatusBadGateway, "aws_error", "AWS returned an error: "+awsErr.ErrorMessage())
	default:
		s.log().Error("internal error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal", "Something went wrong on our side.")
	}
}
