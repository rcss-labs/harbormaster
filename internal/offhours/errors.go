package offhours

import "errors"

// ErrAccessDenied wraps an AccessDenied / AccessDeniedException from AWS, so callers can tell a missing IAM
// permission (the service is blocked until it is granted) from a failure worth retrying.
var ErrAccessDenied = errors.New("offhours: access denied")

// ErrZeroCapacity is returned by Enrol for a service with nothing to restore: no scalable target and a desired
// count of 0, or a target whose maximum is 0. Nothing has been written to AWS or the database.
var ErrZeroCapacity = errors.New("offhours: service has no capacity to save")

// ConflictError is a refusal the user should see as is (the API maps it to 409).
type ConflictError struct{ Message string }

func (e *ConflictError) Error() string { return e.Message }
