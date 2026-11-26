package bedrock

import (
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"github.com/rcss-labs/harbormaster/internal/agent/provider"
)

// emptyToolResult stands in for a blank tool result, which Converse rejects.
const emptyToolResult = "(no output)"

// convertMessages maps the conversation to Converse messages. Converse needs
// strictly alternating roles and every tool result of one turn in the single
// user message that follows the assistant's tool calls, so consecutive
// messages that map to the same role are merged.
func convertMessages(msgs []provider.Message) []types.Message {
	var out []types.Message
	add := func(role types.ConversationRole, blocks ...types.ContentBlock) {
		if len(blocks) == 0 {
			return
		}
		if n := len(out); n > 0 && out[n-1].Role == role {
			out[n-1].Content = append(out[n-1].Content, blocks...)
			return
		}
		out = append(out, types.Message{Role: role, Content: blocks})
	}
	for _, m := range msgs {
		switch m.Role {
		case provider.RoleUser:
			if m.Text != "" {
				add(types.ConversationRoleUser, &types.ContentBlockMemberText{Value: m.Text})
			}
		case provider.RoleAssistant:
			var blocks []types.ContentBlock
			blocks = append(blocks, opaqueBlocks(m.Opaque)...)
			if m.Text != "" {
				blocks = append(blocks, &types.ContentBlockMemberText{Value: m.Text})
			}
			for _, tc := range m.ToolCalls {
				args := tc.Arguments
				if args == nil {
					args = map[string]any{}
				}
				id, name := tc.ID, tc.Name
				blocks = append(blocks, &types.ContentBlockMemberToolUse{Value: types.ToolUseBlock{
					ToolUseId: &id,
					Name:      &name,
					Input:     document.NewLazyDocument(args),
				}})
			}
			add(types.ConversationRoleAssistant, blocks...)
		case provider.RoleTool:
			text := m.Text
			if text == "" {
				text = emptyToolResult
			}
			id := m.ToolCallID
			block := types.ToolResultBlock{
				ToolUseId: &id,
				Content:   []types.ToolResultContentBlock{&types.ToolResultContentBlockMemberText{Value: text}},
			}
			if m.IsError {
				block.Status = types.ToolResultStatusError
			}
			add(types.ConversationRoleUser, &types.ContentBlockMemberToolResult{Value: block})
		}
	}
	return out
}

func convertTools(defs []provider.ToolDef) *types.ToolConfiguration {
	if len(defs) == 0 {
		return nil
	}
	tools := make([]types.Tool, 0, len(defs))
	for _, d := range defs {
		name, desc := d.Name, d.Description
		schema := d.InputSchema
		if schema == nil {
			schema = map[string]any{"type": "object"}
		}
		spec := types.ToolSpecification{
			Name:        &name,
			InputSchema: &types.ToolInputSchemaMemberJson{Value: document.NewLazyDocument(schema)},
		}
		if desc != "" {
			spec.Description = &desc
		}
		tools = append(tools, &types.ToolMemberToolSpec{Value: spec})
	}
	return &types.ToolConfiguration{Tools: tools}
}

// parseToolArguments decodes the concatenated input deltas of one tool call.
func parseToolArguments(raw string) (map[string]any, error) {
	if raw == "" {
		return map[string]any{}, nil
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(raw), &args); err != nil || args == nil {
		return nil, fmt.Errorf("tool arguments are not a JSON object")
	}
	return args, nil
}
