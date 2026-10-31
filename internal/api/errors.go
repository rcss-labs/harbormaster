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
