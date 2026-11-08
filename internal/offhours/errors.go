package offhours

import "errors"

// ErrAccessDenied wraps an AccessDenied / AccessDeniedException from AWS, so callers can tell a missing IAM
// permission (the service is blocked until it is granted) from a failure worth retrying.
var ErrAccessDenied = errors.New("offhours: access denied")

// ErrZeroCapacity is returned by Enrol for a service with nothing to restore: no scalable target and a desired
// count of 0, or a target whose maximum is 0. Nothing has been written to AWS or the database.
var ErrZeroCapacity = errors.New("offhours: service has no capacity to save")

// ErrMinZero is returned by Enrol for a service whose auto scaling minimum is 0: target tracking cannot scale out
// from 0 tasks, so after a wake the service would stay asleep. Nothing has been written. The message is the
// user-facing "not managed" reason.
var ErrMinZero = errors.New("Not managed: its auto scaling minimum is 0, so it could not wake on its own.")

// ErrNeedsAttention is returned by Enrol for a target at 0/0 with Harbormaster actions but no wake action to
// recover the range from. Nothing has been written.
var ErrNeedsAttention = errors.New("Needs attention: asleep with no wake action. Check its scheduled actions.")

// ConflictError is a refusal the user should see as is (the API maps it to 409).
type ConflictError struct{ Message string }

func (e *ConflictError) Error() string { return e.Message }
