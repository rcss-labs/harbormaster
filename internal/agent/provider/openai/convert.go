package openai

import (
	"encoding/json"
	"math"

	sdk "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"

	"github.com/rcss-labs/harbormaster/internal/agent/provider"
)

// buildParams maps a provider request to a stateless Responses request: the
// vendor must not store it, and reasoning state travels in Message.Opaque.
func buildParams(req provider.Request) responses.ResponseNewParams {
	params := responses.ResponseNewParams{
		Model: req.Model,
		Input: responses.ResponseNewParamsInputUnion{OfInputItemList: buildInput(req.Messages)},
		Store: sdk.Bool(false),
		// Encrypted reasoning items let a reasoning model continue after a
		// tool result without server-side state.
		Include: []responses.ResponseIncludable{responses.ResponseIncludableReasoningEncryptedContent},
	}
	if req.System != "" {
		params.Instructions = sdk.String(req.System)
	}
	if req.MaxOutputTokens > 0 {
		params.MaxOutputTokens = sdk.Int(int64(min(req.MaxOutputTokens, math.MaxInt32)))
	}
	for _, tool := range req.Tools {
		schema := tool.InputSchema
		if schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		params.Tools = append(params.Tools, responses.ToolUnionParam{OfFunction: &responses.FunctionToolParam{
			Name:        tool.Name,
			Description: sdk.String(tool.Description),
			Parameters:  schema,
			Strict:      sdk.Bool(false),
		}})
	}
	return params
}

func buildInput(messages []provider.Message) responses.ResponseInputParam {
	items := responses.ResponseInputParam{}
	for _, m := range messages {
		switch m.Role {
		case provider.RoleUser:
			items = append(items, responses.ResponseInputItemParamOfMessage(m.Text, responses.EasyInputMessageRoleUser))
		case provider.RoleAssistant:
			items = append(items, assistantItems(m)...)
		case provider.RoleTool:
			items = append(items, responses.ResponseInputItemUnionParam{OfFunctionCallOutput: &responses.ResponseInputItemFunctionCallOutputParam{
				CallID: sdk.String(m.ToolCallID),
				Output: responses.ResponseInputItemFunctionCallOutputOutputUnionParam{OfString: sdk.String(m.Text)},
			}})
		}
	}
	return items
}

// assistantItems replays the vendor's own output items when Opaque holds them,
// which keeps reasoning items next to their function calls. Without usable
// Opaque state it rebuilds the turn from Text and ToolCalls; the function call
// carries no item id because an id tells the API a reasoning item must precede
// it.
func assistantItems(m provider.Message) []responses.ResponseInputItemUnionParam {
	if raws := parseOpaque(m.Opaque); len(raws) > 0 {
		items := make([]responses.ResponseInputItemUnionParam, 0, len(raws))
		for _, raw := range raws {
			items = append(items, param.Override[responses.ResponseInputItemUnionParam](raw))
		}
		return items
	}
	var items []responses.ResponseInputItemUnionParam
	if m.Text != "" {
		items = append(items, responses.ResponseInputItemParamOfMessage(m.Text, responses.EasyInputMessageRoleAssistant))
	}
	for _, call := range m.ToolCalls {
		args := call.Arguments
		if args == nil { // includes a call with ArgError: the vendor wants an object
			args = map[string]any{}
		}
		encoded, _ := json.Marshal(args)
		items = append(items, responses.ResponseInputItemParamOfFunctionCall(string(encoded), call.ID, call.Name))
	}
	return items
}

// opaqueItemTypes are the output item types buildOpaque writes.
var opaqueItemTypes = map[string]bool{"reasoning": true, "message": true, "function_call": true}

// parseOpaque returns the stored output items, or nil when raw is empty or not
// exactly what buildOpaque writes: a non-empty array of objects whose type is
// one we store.
func parseOpaque(raw json.RawMessage) []json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil || len(items) == 0 {
		return nil
	}
	for _, item := range items {
		var head struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(item, &head); err != nil || !opaqueItemTypes[head.Type] {
			return nil
		}
	}
	return items
}

// buildOpaque keeps the output items that must be echoed back next turn:
// reasoning (with its encrypted content), the assistant message and function
// calls.
func buildOpaque(output []responses.ResponseOutputItemUnion) json.RawMessage {
	var items []json.RawMessage
	for _, item := range output {
		if opaqueItemTypes[item.Type] {
			items = append(items, json.RawMessage(item.RawJSON()))
		}
	}
	if len(items) == 0 {
		return nil
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		return nil
	}
	return encoded
}
