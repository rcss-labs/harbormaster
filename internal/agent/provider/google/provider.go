package google

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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
		gotUsage     bool
		lastCallPart = -1
		emittedModel bool // any text or tool call has been received
	)

	for chunk, err := range p.client.Models.GenerateContentStream(ctx, req.Model, buildContents(req.Messages), buildConfig(req)) {
		if err != nil {
			streamErr = err
			break
		}
		if u := chunk.UsageMetadata; u != nil {
			gotUsage = true
			usage = usageFrom(u)
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
				call := provider.ToolCall{ID: id, Name: part.FunctionCall.Name, Arguments: part.FunctionCall.Args}
				if len(part.FunctionCall.PartialArgs) > 0 {
					call.Arguments, call.ArgError = nil, argErrorText
				}
				calls = append(calls, call)
				if call.ArgError != "" {
					part = withoutArgs(part)
				}
				parts = append(parts, part)
				lastCallPart = len(parts) - 1
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
		return resp, declined()
	}
	if !gotUsage {
		// Without the vendor's usage the cost is unknown: treat it as a broken stream.
		return resp, &provider.Error{Kind: "unavailable", Retryable: !emittedModel, Message: "the model stream ended early"}
	}
	if finish == "" {
		return resp, &provider.Error{Kind: "unavailable", Retryable: !emittedModel, Message: "the model stream ended early"}
	}

	switch stopReasonOutcome(finish) {
	case outcomeEnd:
		resp.StopReason = "end"
		if len(calls) > 0 {
			resp.StopReason = "tool_use"
		}
	case outcomeCutOff:
		resp.StopReason = "max_tokens"
		// A call that was being written when the output ran out cannot be trusted.
		if n := len(calls); n > 0 {
			calls[n-1].Arguments, calls[n-1].ArgError = nil, argErrorText
			resp.ToolCalls = calls
			if lastCallPart >= 0 {
				parts[lastCallPart] = withoutArgs(parts[lastCallPart])
			}
		}
	case outcomeDeclined:
		return resp, declined()
	default:
		return resp, &provider.Error{Kind: "invalid_request", Refusal: true, Message: "The model stopped for an unknown reason."}
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
	perr := &provider.Error{Status: apiErr.Code, VendorType: provider.SafeVendorType(apiErr.Status)}
	switch {
	case apiErr.Code == 401 || apiErr.Code == 403:
		perr.Kind, perr.Message = "auth", "the provider rejected the credential"
	case apiErr.Code == 429:
		perr.Kind, perr.Retryable, perr.Message = "rate_limited", true, "the provider rate limit was reached"
		perr.RetryAfter = parseRetryAfter(sink.value)
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

const argErrorText = "arguments were not a valid JSON object"

func declined() error {
	return &provider.Error{Kind: "invalid_request", Refusal: true, Message: "The model declined to answer."}
}

type stopOutcome int

const (
	outcomeUnknown stopOutcome = iota
	outcomeEnd
	outcomeCutOff
	outcomeDeclined
)

// stopReasonOutcome is the table of Gemini finish reasons. Anything not listed
// is unknown and becomes an error, never a silent end.
func stopReasonOutcome(r genai.FinishReason) stopOutcome {
	switch r {
	case genai.FinishReasonStop:
		return outcomeEnd
	case genai.FinishReasonMaxTokens, genai.FinishReasonContinuation:
		return outcomeCutOff
	case genai.FinishReasonSafety, genai.FinishReasonRecitation, genai.FinishReasonLanguage,
		genai.FinishReasonBlocklist, genai.FinishReasonProhibitedContent, genai.FinishReasonSPII,
		genai.FinishReasonImageSafety, genai.FinishReasonImageProhibitedContent,
		genai.FinishReasonImageRecitation, genai.FinishReasonModelArmor:
		return outcomeDeclined
	}
	return outcomeUnknown
}

// usageFrom never understates cost: thinking and tool-use prompt tokens are
// counted, and any part of the vendor's total not covered by its breakdown is
// added to the output (the dearer price). promptTokenCount already includes cached tokens, so
// cachedContentTokenCount is not added again.
func usageFrom(u *genai.GenerateContentResponseUsageMetadata) provider.Usage {
	in := int64(u.PromptTokenCount) + int64(u.ToolUsePromptTokenCount)
	out := int64(u.CandidatesTokenCount) + int64(u.ThoughtsTokenCount)
	if extra := int64(u.TotalTokenCount) - in - out; extra > 0 {
		out += extra
	}
	return provider.Usage{InputTokens: in, OutputTokens: out}
}

// withoutArgs copies a function-call part without its arguments, so a call that
// was cut off is never replayed to the vendor with partial arguments.
func withoutArgs(part *genai.Part) *genai.Part {
	cp := *part
	fc := *part.FunctionCall
	fc.Args, fc.PartialArgs = nil, nil
	cp.FunctionCall = &fc
	return &cp
}

// parseRetryAfter reads both forms of the header: seconds and an HTTP date.
func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	if at, err := http.ParseTime(v); err == nil {
		if d := time.Until(at); d > 0 {
			return d
		}
	}
	return 0
}
