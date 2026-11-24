// Package anthropic is the agent's provider for the Anthropic Messages API.
package anthropic

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	sdk "github.com/anthropics/anthropic-sdk-go"

	"github.com/rcss-labs/harbormaster/internal/agent/provider"
)

// Config holds the credential. HTTPClient and Endpoint exist for tests; in
// production both are empty and requests go only to the vendor's fixed host.
type Config struct {
	APIKey     string
	HTTPClient *http.Client
	Endpoint   string
}

type Provider struct {
	client sdk.Client
}

func New(cfg Config) (provider.Provider, error) {
	if cfg.APIKey == "" {
		return nil, errors.New("anthropic: API key is not set")
	}
	return &Provider{client: newSDKClient(cfg)}, nil
}

func (p *Provider) Name() string { return "anthropic" }

func (p *Provider) Generate(ctx context.Context, req provider.Request, onText func(delta string)) (provider.Response, error) {
	stream := p.client.Messages.NewStreaming(ctx, buildParams(req))
	defer stream.Close()

	var msg sdk.Message
	var text strings.Builder
	// The SDK replaces a cut-off tool input with {}, which would hide the cut;
	// keep the raw argument text per block instead.
	rawArgs := map[int64]*strings.Builder{}
	sawFinalUsage := false
	for stream.Next() {
		event := stream.Current()
		if err := msg.Accumulate(event); err != nil {
			return usageOnly(msg), streamError(ctx, err, text.Len() > 0)
		}
		if event.Type == "message_delta" && event.Usage.JSON.OutputTokens.Valid() {
			sawFinalUsage = true
		}
		if event.Type == "content_block_delta" && event.Delta.Type == "input_json_delta" {
			if rawArgs[event.Index] == nil {
				rawArgs[event.Index] = &strings.Builder{}
			}
			rawArgs[event.Index].WriteString(event.Delta.PartialJSON)
		}
		if event.Type == "content_block_delta" && event.Delta.Type == "text_delta" && event.Delta.Text != "" {
			text.WriteString(event.Delta.Text)
			if onText != nil {
				onText(event.Delta.Text)
			}
		}
	}
	if err := stream.Err(); err != nil {
		return usageOnly(msg), streamError(ctx, err, text.Len() > 0)
	}
	if msg.StopReason == "" || !sawFinalUsage {
		// The connection closed before the final stop reason and usage arrived.
		return usageOnly(msg), streamError(ctx, errors.New("stream ended early"), text.Len() > 0)
	}

	resp := usageOnly(msg)
	resp.Text = text.String()
	stop, stopErr := stopReason(msg.StopReason)
	if stopErr != nil {
		return resp, stopErr
	}
	resp.StopReason = stop
	resp.Opaque = buildOpaque(msg.Content)
	for index, block := range msg.Content {
		if block.Type != "tool_use" {
			continue
		}
		call := provider.ToolCall{ID: block.ID, Name: block.Name}
		if args, err := parseArguments(rawArgs[int64(index)]); err != nil {
			call.ArgError = "arguments were not a valid JSON object"
		} else {
			call.Arguments = args
		}
		resp.ToolCalls = append(resp.ToolCalls, call)
	}
	return resp, nil
}

func usageOnly(msg sdk.Message) provider.Response {
	return provider.Response{Usage: provider.Usage{InputTokens: msg.Usage.InputTokens + msg.Usage.CacheCreationInputTokens + msg.Usage.CacheReadInputTokens, OutputTokens: msg.Usage.OutputTokens}}
}

// streamError classifies a failure from the stream. Our own message text is
// used throughout: neither the vendor's body nor the SDK's error string (which
// embeds the body and URL) is ever copied into it.
func streamError(ctx context.Context, err error, emittedText bool) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	var apiErr *sdk.Error
	if errors.As(err, &apiErr) && apiErr.Response != nil {
		if apiErr.StatusCode >= 400 {
			return httpError(apiErr)
		}
		// An error event inside a 200 stream: classify by its type.
		return eventError(apiErr, emittedText)
	}
	return &provider.Error{Kind: "unavailable", Retryable: !emittedText, Message: "the connection to the model provider failed or was cut short"}
}

// eventError maps an in-stream error event. A retry after text was emitted
// would duplicate that text, so only an early failure is retryable.
func eventError(apiErr *sdk.Error, emittedText bool) *provider.Error {
	body := strings.ToLower(apiErr.RawJSON())
	switch string(apiErr.Type()) {
	case "authentication_error", "permission_error":
		return &provider.Error{Kind: "auth", Message: "the model provider rejected the API key"}
	case "invalid_request_error", "not_found_error", "request_too_large":
		if isContextTooLong(body) {
			return &provider.Error{Kind: "context_too_long", Message: "the conversation is too long for the model"}
		}
		return &provider.Error{Kind: "invalid_request", Message: "the model provider rejected the request"}
	case "rate_limit_error":
		return &provider.Error{Kind: "rate_limited", Retryable: !emittedText, Message: "the model provider is rate limiting requests"}
	}
	return &provider.Error{Kind: "unavailable", Retryable: !emittedText, Message: "the model provider is unavailable or overloaded"}
}

func httpError(apiErr *sdk.Error) *provider.Error {
	status := apiErr.StatusCode
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return &provider.Error{Kind: "auth", Message: "the model provider rejected the API key"}
	case status == http.StatusTooManyRequests:
		return &provider.Error{Kind: "rate_limited", Retryable: true, RetryAfter: retryAfter(apiErr.Response.Header), Message: "the model provider is rate limiting requests"}
	case status >= 500:
		return &provider.Error{Kind: "unavailable", Retryable: true, Message: "the model provider is unavailable or overloaded"}
	case status == http.StatusBadRequest && isContextTooLong(apiErr.RawJSON()):
		return &provider.Error{Kind: "context_too_long", Message: "the conversation is too long for the model"}
	}
	return &provider.Error{Kind: "invalid_request", Message: "the model provider rejected the request (HTTP " + strconv.Itoa(status) + ")"}
}

func isContextTooLong(body string) bool {
	body = strings.ToLower(body)
	return strings.Contains(body, "prompt is too long") || strings.Contains(body, "context window") || strings.Contains(body, "context length")
}

func retryAfter(h http.Header) time.Duration {
	if ms, err := strconv.ParseFloat(h.Get("Retry-After-Ms"), 64); err == nil && ms >= 0 {
		return time.Duration(ms * float64(time.Millisecond))
	}
	value := h.Get("Retry-After")
	if secs, err := strconv.ParseFloat(value, 64); err == nil && secs >= 0 {
		return time.Duration(secs * float64(time.Second))
	}
	if when, err := http.ParseTime(value); err == nil {
		if d := time.Until(when); d > 0 {
			return d
		}
	}
	return 0
}
