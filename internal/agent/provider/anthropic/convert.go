package anthropic

import (
	"encoding/json"
	"errors"
	"strings"

	sdk "github.com/anthropics/anthropic-sdk-go"

	"github.com/rcss-labs/harbormaster/internal/agent/provider"
)

func buildParams(req provider.Request) sdk.MessageNewParams {
	params := sdk.MessageNewParams{
		Model:     sdk.Model(req.Model),
		MaxTokens: int64(max(req.MaxOutputTokens, 1)),
		Messages:  convertMessages(req.Messages),
	}
	if req.System != "" {
		params.System = []sdk.TextBlockParam{{Text: req.System}}
	}
	for _, def := range req.Tools {
		params.Tools = append(params.Tools, sdk.ToolUnionParam{OfTool: toolParam(def)})
	}
	return params
}

func toolParam(def provider.ToolDef) *sdk.ToolParam {
	schema := sdk.ToolInputSchemaParam{}
	extra := map[string]any{}
	for key, value := range def.InputSchema {
		switch key {
		case "type":
		case "properties":
			schema.Properties = value
		case "required":
			schema.Required = stringList(value)
		default:
			extra[key] = value
		}
	}
	if len(extra) > 0 {
		schema.ExtraFields = extra
	}
	tool := &sdk.ToolParam{Name: def.Name, InputSchema: schema}
	if def.Description != "" {
		tool.Description = sdk.String(def.Description)
	}
	return tool
}

func stringList(value any) []string {
	switch list := value.(type) {
	case []string:
		return list
	case []any:
		out := make([]string, 0, len(list))
		for _, item := range list {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// convertMessages maps turns to the Messages API. Tool results travel as
// tool_result blocks in a user turn, and the API wants consecutive same-role
// turns merged, so all results for one assistant turn share one user message.
func convertMessages(messages []provider.Message) []sdk.MessageParam {
	var out []sdk.MessageParam
	add := func(role sdk.MessageParamRole, blocks []sdk.ContentBlockParamUnion) {
		if len(blocks) == 0 {
			return
		}
		if n := len(out); n > 0 && out[n-1].Role == role {
			out[n-1].Content = append(out[n-1].Content, blocks...)
			return
		}
		out = append(out, sdk.MessageParam{Role: role, Content: blocks})
	}
	for _, m := range messages {
		switch m.Role {
		case provider.RoleUser:
			if m.Text != "" {
				add(sdk.MessageParamRoleUser, []sdk.ContentBlockParamUnion{sdk.NewTextBlock(m.Text)})
			}
		case provider.RoleAssistant:
			if blocks, ok := replayFromOpaque(m); ok {
				add(sdk.MessageParamRoleAssistant, blocks)
				continue
			}
			var blocks []sdk.ContentBlockParamUnion
			if m.Text != "" {
				blocks = append(blocks, sdk.NewTextBlock(m.Text))
			}
			for _, call := range m.ToolCalls {
				blocks = append(blocks, toolUseBlock(call))
			}
			add(sdk.MessageParamRoleAssistant, blocks)
		case provider.RoleTool:
			add(sdk.MessageParamRoleUser, []sdk.ContentBlockParamUnion{sdk.NewToolResultBlock(m.ToolCallID, m.Text, m.IsError)})
		}
	}
	return out
}

// parseArguments accepts only a JSON object; anything else (including a call
// cut off by max_tokens) is an error the loop reports back to the model.
func parseArguments(raw *strings.Builder) (map[string]any, error) {
	if raw == nil || strings.TrimSpace(raw.String()) == "" {
		return map[string]any{}, nil
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(raw.String()), &args); err != nil || args == nil {
		return nil, errors.New("not a JSON object")
	}
	return args, nil
}

// stopReason maps every documented stop reason. A cut-off of any kind is
// max_tokens; a refusal or an unknown value is an error, never a silent end.
func stopReason(reason sdk.StopReason) (string, error) {
	switch reason {
	case sdk.StopReasonEndTurn, sdk.StopReasonStopSequence:
		return "end", nil
	case sdk.StopReasonToolUse:
		return "tool_use", nil
	case sdk.StopReasonMaxTokens, sdk.StopReasonModelContextWindowExceeded, sdk.StopReasonPauseTurn:
		return "max_tokens", nil
	case sdk.StopReasonRefusal:
		return "", &provider.Error{Kind: "invalid_request", Refusal: true, Message: "The model declined to answer."}
	}
	return "", &provider.Error{Kind: "invalid_request", Refusal: true, Message: "The model stopped for an unknown reason."}
}

// opaqueBlock is one content block of an assistant turn, kept in stream order
// so thinking can be replayed unchanged in its original position.
type opaqueBlock struct {
	Type      string `json:"type"`
	Thinking  string `json:"thinking,omitempty"`
	Signature string `json:"signature,omitempty"`
	Data      string `json:"data,omitempty"`
	Text      string `json:"text,omitempty"`
	ID        string `json:"id,omitempty"`
}

// buildOpaque records the turn's block order, but only when it has thinking;
// a turn without thinking replays from Text and ToolCalls alone.
func buildOpaque(content []sdk.ContentBlockUnion) json.RawMessage {
	var blocks []opaqueBlock
	hasThinking := false
	for _, block := range content {
		switch block.Type {
		case "thinking":
			hasThinking = true
			blocks = append(blocks, opaqueBlock{Type: "thinking", Thinking: block.Thinking, Signature: block.Signature})
		case "redacted_thinking":
			hasThinking = true
			blocks = append(blocks, opaqueBlock{Type: "redacted_thinking", Data: block.Data})
		case "text":
			blocks = append(blocks, opaqueBlock{Type: "text", Text: block.Text})
		case "tool_use":
			blocks = append(blocks, opaqueBlock{Type: "tool_use", ID: block.ID})
		}
	}
	if !hasThinking {
		return nil
	}
	raw, err := json.Marshal(blocks)
	if err != nil {
		return nil
	}
	return raw
}

// replayFromOpaque rebuilds an assistant turn from Opaque. It returns false
// (and the caller falls back to Text and ToolCalls) unless the Opaque is our
// own shape and agrees with the message's text and tool calls.
func replayFromOpaque(m provider.Message) ([]sdk.ContentBlockParamUnion, bool) {
	if len(m.Opaque) == 0 {
		return nil, false
	}
	var blocks []opaqueBlock
	if err := json.Unmarshal(m.Opaque, &blocks); err != nil || len(blocks) == 0 {
		return nil, false
	}
	var out []sdk.ContentBlockParamUnion
	var text strings.Builder
	nextCall := 0
	hasThinking := false
	for _, b := range blocks {
		switch b.Type {
		case "thinking":
			if b.Signature == "" {
				return nil, false
			}
			hasThinking = true
			out = append(out, sdk.NewThinkingBlock(b.Signature, b.Thinking))
		case "redacted_thinking":
			if b.Data == "" {
				return nil, false
			}
			hasThinking = true
			out = append(out, sdk.NewRedactedThinkingBlock(b.Data))
		case "text":
			text.WriteString(b.Text)
			if b.Text != "" {
				out = append(out, sdk.NewTextBlock(b.Text))
			}
		case "tool_use":
			if nextCall >= len(m.ToolCalls) || m.ToolCalls[nextCall].ID != b.ID || b.ID == "" {
				return nil, false
			}
			out = append(out, toolUseBlock(m.ToolCalls[nextCall]))
			nextCall++
		default:
			return nil, false
		}
	}
	if !hasThinking || nextCall != len(m.ToolCalls) || text.String() != m.Text {
		return nil, false
	}
	return out, true
}

func toolUseBlock(call provider.ToolCall) sdk.ContentBlockParamUnion {
	args := call.Arguments
	if args == nil {
		args = map[string]any{}
	}
	return sdk.NewToolUseBlock(call.ID, args, call.Name)
}
