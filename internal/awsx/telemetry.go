package awsx

import (
	"context"

	awsmiddleware "github.com/aws/aws-sdk-go-v2/aws/middleware"
	"github.com/aws/smithy-go/middleware"

	"github.com/rcss-labs/harbormaster/internal/telemetry"
)

// TelemetryMiddleware counts every logical SDK call (after retries) by account, region,
// service, operation and result (ok, throttled or error).
func TelemetryMiddleware(account, region string, reg *telemetry.Registry) func(*middleware.Stack) error {
	return func(stack *middleware.Stack) error {
		return stack.Finalize.Add(middleware.FinalizeMiddlewareFunc("HarbormasterTelemetry",
			func(ctx context.Context, in middleware.FinalizeInput, next middleware.FinalizeHandler) (middleware.FinalizeOutput, middleware.Metadata, error) {
				out, md, err := next.HandleFinalize(ctx, in)
				result := "ok"
				switch {
				case err == nil:
				case IsThrottle(err):
					result = "throttled"
				default:
					result = "error"
				}
				reg.CountAWSCall(account, region, awsmiddleware.GetServiceID(ctx), awsmiddleware.GetOperationName(ctx), result)
				return out, md, err
			}), middleware.Before)
	}
}
