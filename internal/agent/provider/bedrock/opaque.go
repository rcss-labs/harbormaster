package bedrock

import (
	"encoding/json"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
)

// reasoningState is what Converse needs echoed back before the other blocks of
// an assistant turn when the model returned reasoning: the text with its
// signature, or redacted bytes. It travels in provider.Response.Opaque.
type reasoningState struct {
	Text      string `json:"text,omitempty"`
	Signature string `json:"signature,omitempty"`
	Redacted  []byte `json:"redacted,omitempty"`
}

// replayable reports whether Converse accepts the block back: reasoning text
// only with its signature, or redacted bytes.
func (s reasoningState) replayable() bool {
	return len(s.Redacted) > 0 || (s.Text != "" && s.Signature != "")
}

func encodeReasoning(blocks []reasoningState) json.RawMessage {
	var keep []reasoningState
	for _, b := range blocks {
		if b.replayable() {
			keep = append(keep, b)
		}
	}
	if len(keep) == 0 {
		return nil
	}
	raw, err := json.Marshal(keep)
	if err != nil {
		return nil
	}
	return raw
}

// opaqueBlocks turns Opaque back into reasoning content blocks. State that
// cannot be parsed is ignored.
func opaqueBlocks(raw json.RawMessage) []types.ContentBlock {
	if len(raw) == 0 {
		return nil
	}
	var states []reasoningState
	if err := json.Unmarshal(raw, &states); err != nil {
		return nil
	}
	var out []types.ContentBlock
	for _, s := range states {
		switch {
		case len(s.Redacted) > 0:
			out = append(out, &types.ContentBlockMemberReasoningContent{
				Value: &types.ReasoningContentBlockMemberRedactedContent{Value: s.Redacted},
			})
		case s.replayable():
			text, sig := s.Text, s.Signature
			rt := types.ReasoningTextBlock{Text: &text, Signature: &sig}
			out = append(out, &types.ContentBlockMemberReasoningContent{
				Value: &types.ReasoningContentBlockMemberReasoningText{Value: rt},
			})
		}
	}
	return out
}
