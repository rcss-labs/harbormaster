// Package provider defines the interface the agent loop uses to talk to a
// language model, and the catalogue of models and prices it may use.
package provider

import (
	"context"
	"time"
)

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type ToolCall struct {
	ID        string
	Name      string
	Arguments map[string]any
}

// Message is one turn of the conversation. A tool message carries its result
// in Text.
type Message struct {
	Role       Role
	Text       string
	ToolCalls  []ToolCall
	ToolCallID string
	ToolName   string
	IsError    bool
}

type ToolDef struct {
	Name        string
	Description string
	InputSchema map[string]any
}

type Request struct {
	Model           string
	System          string
	Messages        []Message
	Tools           []ToolDef
	MaxOutputTokens int
}

type Usage struct {
	InputTokens  int64
	OutputTokens int64
}

type Response struct {
	Text      string
	ToolCalls []ToolCall
	Usage     Usage
	// StopReason is one of "end", "tool_use" or "max_tokens".
	StopReason string
}

// Provider generates one model turn. onText, when not nil, receives text as it
// is produced; the returned Response holds the complete turn.
type Provider interface {
	Name() string
	Generate(ctx context.Context, req Request, onText func(delta string)) (Response, error)
}

// Error is a provider failure. Message is written by us and never contains a
// provider response body or a key.
type Error struct {
	// Kind is one of "auth", "rate_limited", "unavailable", "invalid_request"
	// or "context_too_long".
	Kind       string
	Retryable  bool
	RetryAfter time.Duration
	Message    string
}

func (e *Error) Error() string {
	if e.Message == "" {
		return "provider error: " + e.Kind
	}
	return "provider error: " + e.Kind + ": " + e.Message
}
