// Package bedrock is the agent's provider for Amazon Bedrock, using the
// Converse streaming API.
package bedrock

import (
	"context"
	"math"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"github.com/rcss-labs/harbormaster/internal/agent/provider"
)

type Provider struct {
	client *bedrockruntime.Client
}

// New builds a provider that talks to the regional bedrock-runtime endpoint.
func New(cfg Config) (provider.Provider, error) {
	client, err := newClient(cfg)
	if err != nil {
		return nil, err
	}
	return &Provider{client: client}, nil
}

func (p *Provider) Name() string { return "bedrock" }

type toolCallBuilder struct {
	id, name string
	input    strings.Builder
	closed   bool
}

const argErrorText = "arguments were not a valid JSON object"

func (p *Provider) Generate(ctx context.Context, req provider.Request, onText func(string)) (provider.Response, error) {
	input := &bedrockruntime.ConverseStreamInput{
		ModelId:    &req.Model,
		Messages:   convertMessages(req.Messages),
		ToolConfig: convertTools(req.Tools),
	}
	if req.System != "" {
		input.System = []types.SystemContentBlock{&types.SystemContentBlockMemberText{Value: req.System}}
	}
	if req.MaxOutputTokens > 0 {
		max := int32(min(req.MaxOutputTokens, math.MaxInt32))
		input.InferenceConfig = &types.InferenceConfiguration{MaxTokens: &max}
	}

	out, err := p.client.ConverseStream(ctx, input)
	if err != nil {
		return provider.Response{}, mapError(ctx, err)
	}
	stream := out.GetStream()
	defer stream.Close()

	var (
		resp      provider.Response
		text      strings.Builder
		tools     = map[int32]*toolCallBuilder{}
		toolOrder []int32
		reasoning = map[int32]*reasoningState{}
		reasonIdx []int32
		stopped   bool
		gotUsage  bool
		stop      types.StopReason
	)
	for event := range stream.Events() {
		switch ev := event.(type) {
		case *types.ConverseStreamOutputMemberContentBlockStart:
			if start, ok := ev.Value.Start.(*types.ContentBlockStartMemberToolUse); ok {
				idx := derefInt32(ev.Value.ContentBlockIndex)
				b := &toolCallBuilder{id: deref(start.Value.ToolUseId), name: deref(start.Value.Name)}
				if _, seen := tools[idx]; !seen {
					toolOrder = append(toolOrder, idx)
				}
				tools[idx] = b
			}
		case *types.ConverseStreamOutputMemberContentBlockDelta:
			idx := derefInt32(ev.Value.ContentBlockIndex)
			switch d := ev.Value.Delta.(type) {
			case *types.ContentBlockDeltaMemberText:
				text.WriteString(d.Value)
				if onText != nil && d.Value != "" {
					onText(d.Value)
				}
			case *types.ContentBlockDeltaMemberToolUse:
				if b := tools[idx]; b != nil {
					b.input.WriteString(deref(d.Value.Input))
				}
			case *types.ContentBlockDeltaMemberReasoningContent:
				r := reasoning[idx]
				if r == nil {
					r = &reasoningState{}
					reasoning[idx] = r
					reasonIdx = append(reasonIdx, idx)
				}
				switch rd := d.Value.(type) {
				case *types.ReasoningContentBlockDeltaMemberText:
					r.Text += rd.Value
				case *types.ReasoningContentBlockDeltaMemberSignature:
					r.Signature += rd.Value
				case *types.ReasoningContentBlockDeltaMemberRedactedContent:
					r.Redacted = append(r.Redacted, rd.Value...)
				}
			}
		case *types.ConverseStreamOutputMemberContentBlockStop:
			if b := tools[derefInt32(ev.Value.ContentBlockIndex)]; b != nil {
				b.closed = true
			}
		case *types.ConverseStreamOutputMemberMessageStop:
			stopped = true
			stop = ev.Value.StopReason
		case *types.ConverseStreamOutputMemberMetadata:
			if u := ev.Value.Usage; u != nil {
				resp.Usage = usageFrom(u)
				gotUsage = true
			}
		}
	}
	resp.Text = text.String()

	// A broken stream is retryable only if the caller has not already been
	// given text, so a retry cannot duplicate it.
	interrupted := func() (provider.Response, error) {
		e := newError("unavailable", resp.Text == "", 0)
		e.Message = "the Bedrock response stream was interrupted"
		return resp, e
	}
	if err := stream.Err(); err != nil {
		if ctx.Err() != nil {
			return resp, ctx.Err()
		}
		return interrupted()
	}
	if ctx.Err() != nil {
		return resp, ctx.Err()
	}
	// Without the final usage the turn's cost is unknown: not a success.
	if !stopped || !gotUsage {
		return interrupted()
	}

	for _, idx := range toolOrder {
		b := tools[idx]
		call := provider.ToolCall{ID: b.id, Name: b.name}
		// A call cut off before its block closed is never trusted, even if
		// the partial text happens to parse.
		args, err := parseToolArguments(b.input.String())
		if err != nil || !b.closed {
			call.ArgError = argErrorText
		} else {
			call.Arguments = args
		}
		resp.ToolCalls = append(resp.ToolCalls, call)
	}
	var states []reasoningState
	for _, idx := range reasonIdx {
		states = append(states, *reasoning[idx])
	}
	resp.Opaque = encodeReasoning(states)

	// Every stop reason is mapped on purpose; an unlisted one is an error, never
	// a silent "end".
	switch stop {
	case types.StopReasonEndTurn, types.StopReasonStopSequence:
		resp.StopReason = "end"
	case types.StopReasonToolUse:
		resp.StopReason = "tool_use"
	case types.StopReasonMaxTokens, types.StopReasonModelContextWindowExceeded:
		resp.StopReason = "max_tokens"
	case types.StopReasonGuardrailIntervened, types.StopReasonContentFiltered, "refusal":
		e := newError("invalid_request", false, 0)
		e.Message, e.Refusal = "The model declined to answer.", true
		return resp, e
	case types.StopReasonMalformedModelOutput, types.StopReasonMalformedToolUse:
		e := newError("unavailable", resp.Text == "", 0)
		e.Message = "the model returned malformed output"
		return resp, e
	default:
		e := newError("invalid_request", false, 0)
		e.Message, e.Refusal = "The model stopped for an unknown reason.", true
		return resp, e
	}
	return resp, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func derefInt32(v *int32) int32 {
	if v == nil {
		return 0
	}
	return *v
}

// usageFrom reports every billed token and never fewer. Cache reads and writes
// are billed on top of inputTokens, and any token the total counts that the
// parts do not (reasoning) is attributed to output, the dearer side.
func usageFrom(u *types.TokenUsage) provider.Usage {
	in := int64(derefInt32(u.InputTokens)) + int64(derefInt32(u.CacheReadInputTokens)) + int64(derefInt32(u.CacheWriteInputTokens))
	out := int64(derefInt32(u.OutputTokens))
	if extra := int64(derefInt32(u.TotalTokens)) - in - out; extra > 0 {
		out += extra
	}
	return provider.Usage{InputTokens: in, OutputTokens: out}
}
