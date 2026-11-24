package google

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"google.golang.org/genai"

	"github.com/rcss-labs/harbormaster/internal/agent/provider"
)

type googleProvider struct {
	client *genai.Client
}

// New returns a provider backed by the Gemini API.
func New(cfg Config) (provider.Provider, error) {
	client, err := newSDKClient(cfg)
	if err != nil {
		return nil, err
	}
	return &googleProvider{client: client}, nil
}

func (p *googleProvider) Name() string { return "google" }

func (p *googleProvider) Generate(ctx context.Context, req provider.Request, onText func(string)) (provider.Response, error) {
	sink := &retryAfterSink{}
	ctx = context.WithValue(ctx, sinkKey{}, sink)

	var (
		text         strings.Builder
		parts        []*genai.Part // the model's parts in order, echoed back via Opaque
		calls        []provider.ToolCall
		usage        provider.Usage
		finish       genai.FinishReason
		blockReason  genai.BlockedReason
		streamErr    error
		emittedModel bool // any text or tool call has been received
	)

	for chunk, err := range p.client.Models.GenerateContentStream(ctx, req.Model, buildContents(req.Messages), buildConfig(req)) {
		if err != nil {
			streamErr = err
			break
		}
		if u := chunk.UsageMetadata; u != nil {
			usage.InputTokens = int64(u.PromptTokenCount) + int64(u.ToolUsePromptTokenCount)
			usage.OutputTokens = int64(u.CandidatesTokenCount) + int64(u.ThoughtsTokenCount)
		}
		if chunk.PromptFeedback != nil && chunk.PromptFeedback.BlockReason != "" {
			blockReason = chunk.PromptFeedback.BlockReason
		}
		if len(chunk.Candidates) == 0 {
			continue
		}
		cand := chunk.Candidates[0]
		if cand.FinishReason != "" {
			finish = cand.FinishReason
		}
		if cand.Content == nil {
			continue
		}
		for _, part := range cand.Content.Parts {
			if part == nil {
				continue
			}
			switch {
			case part.FunctionCall != nil:
				id := part.FunctionCall.ID
				if id == "" {
					id = newSyntheticID()
				}
				calls = append(calls, provider.ToolCall{ID: id, Name: part.FunctionCall.Name, Arguments: part.FunctionCall.Args})
				parts = append(parts, part)
				emittedModel = true
			case part.Text != "" && !part.Thought:
				text.WriteString(part.Text)
				if onText != nil {
					onText(part.Text)
				}
				emittedModel = true
				parts = appendText(parts, part)
			default:
				// Thought parts and signature-only parts are kept verbatim.
				if part.Thought || len(part.ThoughtSignature) > 0 {
					parts = append(parts, part)
				}
			}
		}
	}

	resp := provider.Response{Text: text.String(), ToolCalls: calls, Usage: usage}

	if streamErr != nil {
		if ctx.Err() != nil {
			return resp, ctx.Err()
		}
		return resp, mapError(streamErr, sink, emittedModel)
	}
	if ctx.Err() != nil {
		return resp, ctx.Err()
	}
	if blockReason != "" && finish == "" {
		return resp, &provider.Error{Kind: "invalid_request", Message: "the model refused the request (" + string(blockReason) + ")"}
	}
	if finish == "" {
		return resp, &provider.Error{Kind: "unavailable", Retryable: !emittedModel, Message: "the model stream ended early"}
	}

	switch finish {
	case genai.FinishReasonStop:
		resp.StopReason = "end"
		if len(calls) > 0 {
			resp.StopReason = "tool_use"
		}
	case genai.FinishReasonMaxTokens:
		resp.StopReason = "max_tokens"
	default:
		return resp, &provider.Error{Kind: "invalid_request", Message: "the model stopped without an answer (" + string(finish) + ")"}
	}

	if len(parts) > 0 {
		if raw, err := json.Marshal(parts); err == nil {
			resp.Opaque = raw
		}
	}
	return resp, nil
}

// appendText adds a text part, merging it into the previous plain text part so
// the echoed turn is not split into one part per streamed delta. A part that
// carries a signature stays separate.
func appendText(parts []*genai.Part, part *genai.Part) []*genai.Part {
	if n := len(parts); n > 0 {
		last := parts[n-1]
		if last.Text != "" && !last.Thought && last.FunctionCall == nil && len(last.ThoughtSignature) == 0 && len(part.ThoughtSignature) == 0 {
			merged := *last
			merged.Text += part.Text
			parts[n-1] = &merged
			return parts
		}
	}
	cp := *part
	return append(parts, &cp)
}

// mapError turns an SDK or transport error into a provider.Error. The vendor's
// message and body are never copied into ours.
func mapError(err error, sink *retryAfterSink, emitted bool) error {
	var apiErr genai.APIError
	if !errors.As(err, &apiErr) {
		return &provider.Error{Kind: "unavailable", Retryable: !emitted, Message: "the request to the model failed"}
	}
	perr := &provider.Error{}
	switch {
	case apiErr.Code == 401 || apiErr.Code == 403:
		perr.Kind, perr.Message = "auth", "the provider rejected the credential"
	case apiErr.Code == 429:
		perr.Kind, perr.Retryable, perr.Message = "rate_limited", true, "the provider rate limit was reached"
		if secs, convErr := strconv.Atoi(strings.TrimSpace(sink.value)); convErr == nil && secs > 0 {
			perr.RetryAfter = time.Duration(secs) * time.Second
		}
	case apiErr.Code >= 500:
		perr.Kind, perr.Retryable, perr.Message = "unavailable", true, "the provider is unavailable"
	case apiErr.Code == 400 && looksLikeContextLength(apiErr.Message):
		perr.Kind, perr.Message = "context_too_long", "the conversation is too long for the model"
	case apiErr.Code >= 300 && apiErr.Code < 400:
		perr.Kind, perr.Message = "invalid_request", fmt.Sprintf("the provider answered with an unexpected redirect (%d)", apiErr.Code)
	default:
		perr.Kind, perr.Message = "invalid_request", fmt.Sprintf("the provider rejected the request (%d)", apiErr.Code)
	}
	// Once text or a call reached the caller, a retry would duplicate it.
	if emitted {
		perr.Retryable = false
	}
	return perr
}

func looksLikeContextLength(msg string) bool {
	m := strings.ToLower(msg)
	if !strings.Contains(m, "token") {
		return false
	}
	for _, marker := range []string{"exceed", "too long", "too large", "maximum", "limit"} {
		if strings.Contains(m, marker) {
			return true
		}
	}
	return false
}
