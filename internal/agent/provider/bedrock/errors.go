package bedrock

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"github.com/rcss-labs/harbormaster/internal/agent/provider"
)

// mapError turns an SDK failure into a provider.Error whose message is ours:
// it never carries the vendor's message or body. A cancelled or expired
// context is returned as is.
func mapError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	status, retryAfter := responseDetails(err)

	var (
		accessDenied *types.AccessDeniedException
		throttling   *types.ThrottlingException
		quota        *types.ServiceQuotaExceededException
		validation   *types.ValidationException
		notFound     *types.ResourceNotFoundException
		internal     *types.InternalServerException
		unavailable  *types.ServiceUnavailableException
		timeout      *types.ModelTimeoutException
		notReady     *types.ModelNotReadyException
		modelErr     *types.ModelErrorException
		streamErr    *types.ModelStreamErrorException
	)
	switch {
	case errors.As(err, &accessDenied):
		return newError("auth", false, 0)
	case errors.As(err, &throttling), errors.As(err, &quota):
		return newError("rate_limited", true, retryAfter)
	case errors.As(err, &validation):
		if looksLikeContextLength(validation.ErrorMessage()) {
			return newError("context_too_long", false, 0)
		}
		return newError("invalid_request", false, 0)
	case errors.As(err, &notFound):
		return newError("invalid_request", false, 0)
	case errors.As(err, &internal), errors.As(err, &unavailable), errors.As(err, &timeout),
		errors.As(err, &notReady), errors.As(err, &modelErr), errors.As(err, &streamErr):
		return newError("unavailable", true, retryAfter)
	}

	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return newError("auth", false, 0)
	case status == http.StatusTooManyRequests:
		return newError("rate_limited", true, retryAfter)
	case status >= 500:
		return newError("unavailable", true, retryAfter)
	case status >= 300:
		return newError("invalid_request", false, 0)
	}
	if strings.Contains(err.Error(), "failed to retrieve credentials") || strings.Contains(err.Error(), "get credentials") {
		return newError("auth", false, 0)
	}
	// Transport failure before any response.
	return newError("unavailable", true, 0)
}

func responseDetails(err error) (status int, retryAfter time.Duration) {
	var respErr *awshttp.ResponseError
	if !errors.As(err, &respErr) || respErr.Response == nil || respErr.Response.Response == nil {
		return 0, 0
	}
	if v := respErr.Response.Header.Get("Retry-After"); v != "" {
		if secs, convErr := strconv.Atoi(strings.TrimSpace(v)); convErr == nil && secs >= 0 {
			retryAfter = time.Duration(secs) * time.Second
		}
	}
	return respErr.HTTPStatusCode(), retryAfter
}

func looksLikeContextLength(msg string) bool {
	msg = strings.ToLower(msg)
	for _, needle := range []string{"too long", "context length", "context window", "too many tokens", "maximum context"} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

var errorMessages = map[string]string{
	"auth":             "Bedrock rejected the task role's credentials or access to the model",
	"rate_limited":     "Bedrock is rate limiting requests",
	"unavailable":      "Bedrock is unavailable",
	"invalid_request":  "Bedrock rejected the request",
	"context_too_long": "the conversation is too long for the model",
}

func newError(kind string, retryable bool, retryAfter time.Duration) *provider.Error {
	return &provider.Error{Kind: kind, Retryable: retryable, RetryAfter: retryAfter, Message: errorMessages[kind]}
}
