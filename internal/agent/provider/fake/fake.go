// Package fake is a scripted provider for tests and for running the agent
// without a live model.
package fake

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/rcss-labs/harbormaster/internal/agent/provider"
)

// Step is one scripted model turn: text, tool calls, a provider error, or a
// wait for the context to be cancelled.
type Step struct {
	text      string
	toolCalls []provider.ToolCall
	usage     provider.Usage
	err       error
	block     bool
	raw       *provider.Response
}

// Text is a turn that streams text and ends.
func Text(text string, usage provider.Usage) Step { return Step{text: text, usage: usage} }

// ToolCalls is a turn that asks for the given tool calls.
func ToolCalls(usage provider.Usage, calls ...provider.ToolCall) Step {
	return Step{toolCalls: calls, usage: usage}
}

// Raw is a turn that returns resp as given (text with tool calls, Opaque,
// StopReason "max_tokens"). Its Text, if any, is streamed.
func Raw(resp provider.Response) Step { return Step{raw: &resp} }

// Fail is a turn that returns err (typically a *provider.Error).
func Fail(err error) Step { return Step{err: err} }

// Block is a turn that waits until the request context is cancelled.
func Block() Step { return Step{block: true} }

type Provider struct {
	mu       sync.Mutex
	script   []Step
	next     int
	requests []provider.Request
	respond  func(provider.Request) Step // when set, replaces the script
}

// New returns a provider that plays script in order, one step per Generate.
func New(script ...Step) *Provider { return &Provider{script: script} }

// Func returns a provider that answers each request with respond's step.
func Func(respond func(provider.Request) Step) *Provider { return &Provider{respond: respond} }

// Demo returns a provider that calls get_overview, then, once it has the
// result, answers in two sentences. When it is offered submit_result it plays
// DemoInvestigator instead, so one provider serves both roles.
func Demo() *Provider {
	return &Provider{respond: func(req provider.Request) Step {
		for _, tool := range req.Tools {
			if tool.Name == "submit_result" {
				return investigatorStep(req)
			}
		}
		if n := len(req.Messages); n > 0 && req.Messages[n-1].Role == provider.RoleTool {
			return Text("This is a canned answer from the demo model. It did not read your estate.", provider.Usage{InputTokens: 120, OutputTokens: 18})
		}
		return ToolCalls(provider.Usage{InputTokens: 80, OutputTokens: 12},
			provider.ToolCall{ID: "demo-call-1", Name: "get_overview", Arguments: map[string]any{}})
	}}
}

// DemoInvestigator returns a provider that reads the task's first service, its
// timeline and its logs, then submits a result that cites the first piece it was
// shown and the first log line.
func DemoInvestigator() *Provider { return Func(investigatorStep) }

var (
	taskServiceID = regexp.MustCompile(`"service_ids":\["([^"\\]+)"`)
	unitMarker    = regexp.MustCompile(`(?m)^\[(e[0-9]{1,5})\] `)
)

func investigatorStep(req provider.Request) Step {
	var serviceID string
	var firstUnit []string // the first unit id of each tool result, "" when it had none
	for _, m := range req.Messages {
		switch {
		case m.Role == provider.RoleTool:
			unit := ""
			if found := unitMarker.FindStringSubmatch(m.Text); found != nil {
				unit = found[1]
			}
			firstUnit = append(firstUnit, unit)
		case serviceID == "" && m.Role == provider.RoleUser:
			if id := taskServiceID.FindStringSubmatch(m.Text); id != nil {
				serviceID = id[1]
			}
		}
	}
	results := len(firstUnit)
	// A user message after the task tells the model to finish.
	told := len(req.Messages) > 1 && req.Messages[len(req.Messages)-1].Role == provider.RoleUser
	if reads := []string{"get_service", "get_service_timeline", "get_service_logs"}; serviceID != "" && !told && results < len(reads) {
		return ToolCalls(provider.Usage{InputTokens: 80, OutputTokens: 12},
			provider.ToolCall{ID: fmt.Sprintf("demo-read-%d", results+1), Name: reads[results], Arguments: map[string]any{"service_id": serviceID}})
	}
	// It cites the first thing it read and, when the service had log lines, the first line.
	evidence := []any{}
	if len(firstUnit) > 0 && firstUnit[0] != "" {
		evidence = append(evidence, map[string]any{"ref": firstUnit[0], "note": "The first thing the demo model read."})
	}
	if len(firstUnit) > 2 && firstUnit[2] != "" {
		evidence = append(evidence, map[string]any{"ref": firstUnit[2], "note": "The first log line the demo model read."})
	}
	return ToolCalls(provider.Usage{InputTokens: 120, OutputTokens: 30}, provider.ToolCall{
		ID: "demo-submit", Name: "submit_result",
		Arguments: map[string]any{"summary": "This is a canned conclusion from the demo model. It did not judge the service.", "evidence": evidence},
	})
}

func (p *Provider) Name() string { return "fake" }

// Requests returns a copy of every request received so far, in order.
func (p *Provider) Requests() []provider.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]provider.Request(nil), p.requests...)
}

func (p *Provider) Generate(ctx context.Context, req provider.Request, onText func(string)) (provider.Response, error) {
	p.mu.Lock()
	p.requests = append(p.requests, cloneRequest(req))
	var step Step
	switch {
	case p.respond != nil:
		step = p.respond(req)
	case p.next < len(p.script):
		step = p.script[p.next]
		p.next++
	default:
		p.mu.Unlock()
		return provider.Response{}, errors.New("fake provider: script exhausted")
	}
	p.mu.Unlock()

	if step.block {
		<-ctx.Done()
		return provider.Response{}, ctx.Err()
	}
	if step.err != nil {
		return provider.Response{}, step.err
	}
	if err := ctx.Err(); err != nil {
		return provider.Response{}, err
	}
	if step.raw != nil {
		resp := cloneResponse(*step.raw)
		stream(resp.Text, onText)
		return resp, nil
	}
	resp := provider.Response{Text: step.text, ToolCalls: step.toolCalls, Usage: step.usage, StopReason: "end"}
	if len(step.toolCalls) > 0 {
		resp.StopReason = "tool_use"
	}
	stream(step.text, onText)
	return resp, nil
}

// stream sends word-sized deltas: several pieces that join back to the text.
func stream(text string, onText func(string)) {
	if onText == nil {
		return
	}
	for _, piece := range strings.SplitAfter(text, " ") {
		if piece != "" {
			onText(piece)
		}
	}
}

func cloneRequest(r provider.Request) provider.Request {
	msgs := make([]provider.Message, len(r.Messages))
	for i, m := range r.Messages {
		m.ToolCalls = cloneToolCalls(m.ToolCalls)
		m.Opaque = bytes.Clone(m.Opaque)
		msgs[i] = m
	}
	r.Messages = msgs
	tools := make([]provider.ToolDef, len(r.Tools))
	for i, t := range r.Tools {
		t.InputSchema = deepCopyMap(t.InputSchema)
		tools[i] = t
	}
	r.Tools = tools
	return r
}

func cloneResponse(r provider.Response) provider.Response {
	r.ToolCalls = cloneToolCalls(r.ToolCalls)
	r.Opaque = bytes.Clone(r.Opaque)
	return r
}

func cloneToolCalls(calls []provider.ToolCall) []provider.ToolCall {
	if calls == nil {
		return nil
	}
	out := make([]provider.ToolCall, len(calls))
	for i, c := range calls {
		c.Arguments = deepCopyMap(c.Arguments)
		out[i] = c
	}
	return out
}

// deepCopyMap copies JSON-shaped values (maps, slices, scalars).
func deepCopyMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = deepCopyValue(v)
	}
	return out
}

func deepCopyValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		return deepCopyMap(x)
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = deepCopyValue(e)
		}
		return out
	default:
		return v
	}
}
