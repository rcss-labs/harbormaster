package google

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"math"
	"sort"
	"strings"

	"google.golang.org/genai"

	"github.com/rcss-labs/harbormaster/internal/agent/provider"
)

// syntheticIDPrefix marks call ids we made up because Gemini sent none. They
// are never sent back.
const syntheticIDPrefix = "gemini-call-"

func newSyntheticID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return syntheticIDPrefix + hex.EncodeToString(b[:])
}

func buildConfig(req provider.Request) *genai.GenerateContentConfig {
	off := false
	cfg := &genai.GenerateContentConfig{
		// A continuation would resend the request; the loop owns retries.
		AutomaticContinuation: &off,
	}
	if req.System != "" {
		cfg.SystemInstruction = genai.NewContentFromText(req.System, genai.RoleUser)
	}
	if req.MaxOutputTokens > 0 {
		cfg.MaxOutputTokens = int32(min(req.MaxOutputTokens, math.MaxInt32))
	}
	if len(req.Tools) > 0 {
		decls := make([]*genai.FunctionDeclaration, 0, len(req.Tools))
		for _, t := range req.Tools {
			d := &genai.FunctionDeclaration{Name: t.Name, Description: t.Description}
			if t.InputSchema != nil {
				d.ParametersJsonSchema = t.InputSchema
			}
			decls = append(decls, d)
		}
		cfg.Tools = []*genai.Tool{{FunctionDeclarations: decls}}
	}
	return cfg
}

// buildContents maps our messages to Gemini contents. Adjacent turns of the same
// role are merged: parallel tool results must share one content, and Gemini
// pairs them with the calls by position, so they follow the call order of the
// assistant turn that made them.
func buildContents(msgs []provider.Message) []*genai.Content {
	var out []*genai.Content
	add := func(role genai.Role, parts ...*genai.Part) {
		if len(parts) == 0 {
			return
		}
		if n := len(out); n > 0 && out[n-1].Role == string(role) {
			out[n-1].Parts = append(out[n-1].Parts, parts...)
			return
		}
		out = append(out, &genai.Content{Role: string(role), Parts: parts})
	}
	var callOrder []string // ids of the most recent assistant turn's calls
	for i := 0; i < len(msgs); i++ {
		m := msgs[i]
		switch m.Role {
		case provider.RoleUser:
			add(genai.RoleUser, genai.NewPartFromText(m.Text))
		case provider.RoleAssistant:
			callOrder = callOrder[:0]
			for _, tc := range m.ToolCalls {
				callOrder = append(callOrder, tc.ID)
			}
			add(genai.RoleModel, assistantParts(m)...)
		case provider.RoleTool:
			j := i
			for j < len(msgs) && msgs[j].Role == provider.RoleTool {
				j++
			}
			run := append([]provider.Message(nil), msgs[i:j]...)
			sort.SliceStable(run, func(a, b int) bool {
				return callIndex(callOrder, run[a].ToolCallID) < callIndex(callOrder, run[b].ToolCallID)
			})
			for _, tm := range run {
				add(genai.RoleUser, toolResultPart(tm))
			}
			i = j - 1
		}
	}
	return out
}

func callIndex(order []string, id string) int {
	for i, v := range order {
		if v == id {
			return i
		}
	}
	return len(order)
}

func toolResultPart(m provider.Message) *genai.Part {
	fr := &genai.FunctionResponse{Name: m.ToolName}
	if m.IsError {
		fr.Response = map[string]any{"error": m.Text}
	} else {
		fr.Response = map[string]any{"output": m.Text}
	}
	if m.ToolCallID != "" && !strings.HasPrefix(m.ToolCallID, syntheticIDPrefix) {
		fr.ID = m.ToolCallID
	}
	return &genai.Part{FunctionResponse: fr}
}

// assistantParts prefers the parts the model produced (Opaque), which carry its
// thought signatures; an Opaque that is not ours is ignored.
func assistantParts(m provider.Message) []*genai.Part {
	if len(m.Opaque) > 0 {
		var parts []*genai.Part
		if err := json.Unmarshal(m.Opaque, &parts); err == nil && hasContent(parts) {
			return parts
		}
	}
	var parts []*genai.Part
	if m.Text != "" {
		parts = append(parts, genai.NewPartFromText(m.Text))
	}
	for _, tc := range m.ToolCalls {
		fc := &genai.FunctionCall{Name: tc.Name, Args: tc.Arguments}
		if tc.ArgError != "" || fc.Args == nil {
			fc.Args = map[string]any{}
		}
		if !strings.HasPrefix(tc.ID, syntheticIDPrefix) {
			fc.ID = tc.ID
		}
		parts = append(parts, &genai.Part{FunctionCall: fc})
	}
	return parts
}

func hasContent(parts []*genai.Part) bool {
	for _, p := range parts {
		if p != nil && (p.Text != "" || p.FunctionCall != nil) {
			return true
		}
	}
	return false
}
