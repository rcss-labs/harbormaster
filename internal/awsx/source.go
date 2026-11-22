package awsx

import "context"

// Source says what caused an AWS call, so the call budget can be checked in production.
type Source string

const (
	SourceTier    Source = "tier"    // a polling tier's cycle
	SourceFocus   Source = "focus"   // a focus cycle for a page that is open
	SourceRequest Source = "request" // a web request or anything not labelled
	SourceJob     Source = "job"     // a coordinator job
)

type sourceKey struct{}

// WithSource labels every AWS call made with the returned context.
func WithSource(ctx context.Context, s Source) context.Context {
	return context.WithValue(ctx, sourceKey{}, s)
}

// SourceFrom returns the context's label, or SourceRequest when it has none.
func SourceFrom(ctx context.Context) Source {
	if s, ok := ctx.Value(sourceKey{}).(Source); ok && s != "" {
		return s
	}
	return SourceRequest
}
