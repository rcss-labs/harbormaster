// Package fake is a scripted provider for tests and for running the agent
// without a live model.
package fake

import (
	"context"
	"errors"
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
}

// Text is a turn that streams text and ends.
func Text(text string, usage provider.Usage) Step { return Step{text: text, usage: usage} }

// ToolCalls is a turn that asks for the given tool calls.
func ToolCalls(usage provider.Usage, calls ...provider.ToolCall) Step {
	return Step{toolCalls: calls, usage: usage}
}

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

// Demo returns a provider that calls get_overview, then, once it has the
// result, answers in two sentences.
func Demo() *Provider {
	return &Provider{respond: func(req provider.Request) Step {
		if n := len(req.Messages); n > 0 && req.Messages[n-1].Role == provider.RoleTool {
			return Text("This is a canned answer from the demo model. It did not read your estate.", provider.Usage{InputTokens: 120, OutputTokens: 18})
		}
		return ToolCalls(provider.Usage{InputTokens: 80, OutputTokens: 12},
			provider.ToolCall{ID: "demo-call-1", Name: "get_overview", Arguments: map[string]any{}})
	}}
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
	resp := provider.Response{Text: step.text, ToolCalls: step.toolCalls, Usage: step.usage, StopReason: "end"}
	if len(step.toolCalls) > 0 {
		resp.StopReason = "tool_use"
	}
	if onText != nil {
		// Word-sized deltas: several pieces that join back to the text.
		for _, piece := range strings.SplitAfter(step.text, " ") {
			if piece != "" {
				onText(piece)
			}
		}
	}
	return resp, nil
}

func cloneRequest(r provider.Request) provider.Request {
	r.Messages = append([]provider.Message(nil), r.Messages...)
	r.Tools = append([]provider.ToolDef(nil), r.Tools...)
	return r
}
