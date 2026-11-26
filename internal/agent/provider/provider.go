// Package provider defines the interface the agent loop uses to talk to a
// language model, and the catalogue of models and prices it may use.
package provider

import (
	"context"
	"encoding/json"
	"regexp"
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
	// ArgError is set by the adapter when the model's arguments were not a valid
	// JSON object; Arguments is then nil and the loop answers with a tool error
	// instead of running the tool.
	ArgError string
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
	// Opaque is adapter-private state the vendor requires to be echoed back with
	// this assistant turn (reasoning items, thought signatures). The loop copies
	// it from Response to the assistant Message unchanged; it lives in memory for
	// one run only and is never logged, stored or sent to another provider.
	Opaque json.RawMessage
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
	// Opaque is adapter-private state the vendor requires to be echoed back with
	// this assistant turn (reasoning items, thought signatures). The loop copies
	// it from Response to the assistant Message unchanged; it lives in memory for
	// one run only and is never logged, stored or sent to another provider.
	Opaque json.RawMessage
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
	// Status is the HTTP status the vendor answered with, 0 when there was none.
	Status int
	// VendorType is the vendor's enumerated error identifier (an error type or
	// code), empty when it has none or it is not a plain identifier. Never a
	// message or body.
	VendorType string
	// Refusal marks a failure that is the model's own refusal or an unexplained
	// stop: Message is then a sentence for the user and is shown as is.
	Refusal bool
}

var vendorTypePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

// SafeVendorType returns s when it is a plain identifier fit for a log line,
// else "". A vendor's error type is enumerated; anything else may carry text.
func SafeVendorType(s string) string {
	if vendorTypePattern.MatchString(s) {
		return s
	}
	return ""
}

func (e *Error) Error() string {
	if e.Message == "" {
		return "provider error: " + e.Kind
	}
	return "provider error: " + e.Kind + ": " + e.Message
}
