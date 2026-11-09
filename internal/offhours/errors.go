package offhours

import "errors"

// ErrAccessDenied wraps an AccessDenied / AccessDeniedException from AWS, so callers can tell a missing IAM
// permission (the service is blocked until it is granted) from a failure worth retrying.
var ErrAccessDenied = errors.New("offhours: access denied")

// ErrZeroCapacity is returned by Enrol for a service with nothing to restore: no scalable target and a desired
// count of 0, or a target whose maximum is 0. Nothing has been written to AWS or the database.
var ErrZeroCapacity = errors.New("offhours: service has no capacity to save")

// The sentences shown to people for the two refusals below. The error values carry lowercase strings so they
// compose in wrapped messages; these constants are what the UI and the operation log say.
const (
	ReasonMinZero        = "Not managed: its auto scaling minimum is 0, so it could not wake on its own."
	ReasonNeedsAttention = "Needs attention: asleep with no wake action. Check its scheduled actions."
)

// ErrMinZero is returned by Enrol for a service whose auto scaling minimum is 0: target tracking cannot scale out
// from 0 tasks, so after a wake the service would stay asleep. Nothing has been written. ReasonMinZero is the
// user-facing sentence.
var ErrMinZero = errors.New("offhours: auto scaling minimum is 0")

// ErrNeedsAttention is returned by Enrol for a target at 0/0 with Harbormaster actions but no wake action to
// recover the range from. Nothing has been written. ReasonNeedsAttention is the user-facing sentence.
var ErrNeedsAttention = errors.New("offhours: asleep with no wake action")

// ErrServiceGone is returned by ReadLive when the ECS service or its cluster no longer exists, or the service is
// INACTIVE (it cannot come back).
var ErrServiceGone = errors.New("offhours: service no longer exists")

// ConflictError is a refusal the user should see as is (the API maps it to 409).
type ConflictError struct{ Message string }

func (e *ConflictError) Error() string { return e.Message }
