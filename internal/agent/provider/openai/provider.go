// Package openai implements provider.Provider on OpenAI's Responses API.
package openai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	sdk "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"

	"github.com/rcss-labs/harbormaster/internal/agent/provider"
)

// Config holds what New needs. BaseURL exists for tests; it is empty in
// production, which pins requests to OpenAI's host.
type Config struct {
	APIKey  string
	BaseURL string
}

type openaiProvider struct {
	responses responses.ResponseService
}

// New returns the OpenAI provider.
func New(cfg Config) (provider.Provider, error) {
	if cfg.APIKey == "" {
		return nil, errors.New("openai: API key is required")
	}
	svc, err := newResponses(cfg.APIKey, cfg.BaseURL)
	if err != nil {
		return nil, errors.New("openai: invalid base URL")
	}
	return &openaiProvider{responses: svc}, nil
}

func (p *openaiProvider) Name() string { return "openai" }

func (p *openaiProvider) Generate(ctx context.Context, req provider.Request, onText func(delta string)) (provider.Response, error) {
	stream := p.responses.NewStreaming(ctx, buildParams(req))
	defer func() { _ = stream.Close() }()

	var (
		text    strings.Builder
		resp    provider.Response
		emitted bool
		done    bool
	)
	for stream.Next() {
		event := stream.Current()
		switch event.Type {
		case "response.output_text.delta", "response.refusal.delta":
			if event.Delta == "" {
				continue
			}
			emitted = true
			text.WriteString(event.Delta)
			if onText != nil {
				onText(event.Delta)
			}
		case "response.completed", "response.incomplete":
			// A terminal event without usage would price the turn at zero.
			if !event.Response.Usage.JSON.InputTokens.Valid() || !event.Response.Usage.JSON.OutputTokens.Valid() {
				resp.Text = text.String()
				return resp, &provider.Error{Kind: "unavailable", Retryable: !emitted, Message: "the OpenAI response ended without usage"}
			}
			resp = finalResponse(event.Response, text.String(), event.Type == "response.incomplete")
			if event.Type == "response.incomplete" && event.Response.IncompleteDetails.Reason == "content_filter" {
				return resp, &provider.Error{Kind: "invalid_request", Refusal: true, Message: "The model declined to answer."}
			}
			done = true
		case "response.failed":
			resp.Usage = usageOf(event.Response)
			return resp, failedError(event.Response, emitted)
		case "error":
			return resp, eventError(event.Code, emitted)
		}
		if done {
			break
		}
	}
	if done {
		return resp, nil
	}
	if ctx.Err() != nil {
		return resp, ctx.Err()
	}
	if err := stream.Err(); err != nil {
		var apiErr *sdk.Error
		if errors.As(err, &apiErr) {
			return resp, mapAPIError(apiErr, emitted)
		}
	}
	// The stream ended or broke without a terminal event; keep what streamed.
	resp.Text = text.String()
	return resp, &provider.Error{
		Kind:      "unavailable",
		Retryable: !emitted,
		Message:   "the OpenAI response stream ended early",
	}
}

func usageOf(r responses.Response) provider.Usage {
	return provider.Usage{InputTokens: r.Usage.InputTokens, OutputTokens: r.Usage.OutputTokens}
}

func finalResponse(r responses.Response, text string, incomplete bool) provider.Response {
	resp := provider.Response{Text: text, Usage: usageOf(r), StopReason: "end", Opaque: buildOpaque(r.Output)}
	for _, item := range r.Output {
		if item.Type != "function_call" {
			continue
		}
		call := provider.ToolCall{ID: item.CallID, Name: item.Name}
		if args, ok := parseArguments(item.Arguments.OfString); ok {
			call.Arguments = args
		} else {
			call.ArgError = "arguments were not a valid JSON object"
		}
		resp.ToolCalls = append(resp.ToolCalls, call)
	}
	switch {
	case incomplete && r.IncompleteDetails.Reason == "max_output_tokens":
		resp.StopReason = "max_tokens"
	case len(resp.ToolCalls) > 0:
		resp.StopReason = "tool_use"
	}
	return resp
}

func failedError(r responses.Response, emitted bool) error {
	return eventError(string(r.Error.Code), emitted)
}

// eventError maps an error code reported inside the stream.
func eventError(code string, emitted bool) error {
	perr := classifyEvent(code, emitted)
	perr.VendorType = provider.SafeVendorType(code)
	return perr
}

func classifyEvent(code string, emitted bool) *provider.Error {
	switch code {
	case "rate_limit_exceeded":
		return &provider.Error{Kind: "rate_limited", Retryable: !emitted, Message: "OpenAI rate limit reached"}
	case "context_length_exceeded":
		return &provider.Error{Kind: "context_too_long", Message: "the conversation is too long for the model"}
	case "server_error", "":
		return &provider.Error{Kind: "unavailable", Retryable: !emitted, Message: "OpenAI reported a server error"}
	}
	return &provider.Error{Kind: "invalid_request", Message: "OpenAI rejected the request"}
}

func mapAPIError(e *sdk.Error, emitted bool) error {
	perr := classifyAPIError(e, emitted)
	perr.Status = e.StatusCode
	if perr.VendorType = provider.SafeVendorType(e.Code); perr.VendorType == "" {
		perr.VendorType = provider.SafeVendorType(e.Type)
	}
	return perr
}

func classifyAPIError(e *sdk.Error, emitted bool) *provider.Error {
	switch {
	case e.StatusCode == http.StatusUnauthorized || e.StatusCode == http.StatusForbidden:
		return &provider.Error{Kind: "auth", Message: "OpenAI rejected the credentials"}
	case e.StatusCode == http.StatusTooManyRequests:
		return &provider.Error{Kind: "rate_limited", Retryable: !emitted, RetryAfter: retryAfter(e.Response), Message: "OpenAI rate limit reached"}
	case e.StatusCode >= 500:
		return &provider.Error{Kind: "unavailable", Retryable: !emitted, Message: "OpenAI is unavailable"}
	case e.Code == "context_length_exceeded":
		return &provider.Error{Kind: "context_too_long", Message: "the conversation is too long for the model"}
	}
	return &provider.Error{Kind: "invalid_request", Message: "OpenAI rejected the request"}
}

func retryAfter(resp *http.Response) time.Duration {
	if resp == nil {
		return 0
	}
	if ms, err := strconv.ParseFloat(resp.Header.Get("Retry-After-Ms"), 64); err == nil && ms >= 0 {
		return time.Duration(ms * float64(time.Millisecond))
	}
	if s, err := strconv.ParseFloat(resp.Header.Get("Retry-After"), 64); err == nil && s >= 0 {
		return time.Duration(s * float64(time.Second))
	}
	return 0
}

// parseArguments decodes a JSON object; anything else, including truncated or
// empty text, is reported as not ok.
func parseArguments(raw string) (map[string]any, bool) {
	var args map[string]any
	if err := json.Unmarshal([]byte(raw), &args); err != nil || args == nil {
		return nil, false
	}
	return args, true
}
