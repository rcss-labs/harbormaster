package agent

import (
	"log/slog"

	"github.com/rcss-labs/harbormaster/internal/agent/provider"
)

// providerMessage is the sentence a user reads for a failed model call: one
// per error kind, written here. What an adapter put in Message is for its own
// errors and tests and is never shown, except a refusal's sentence, which is
// written for the user.
func providerMessage(e *provider.Error) string {
	if e.Refusal && e.Message != "" {
		return e.Message
	}
	switch e.Kind {
	case "auth":
		return "The model provider rejected the agent's credentials. Ask an operator to check them."
	case "rate_limited":
		return "The model provider is busy. Try again shortly."
	case "context_too_long":
		return "This conversation is too long for the model. Start a new one."
	case "invalid_request":
		return "The model could not answer this."
	}
	return "The model provider is unavailable. Try again shortly."
}

// logModelFailure writes the one line a failed model call leaves in the agent
// log: which provider and model, the error kind, and the HTTP status and
// vendor error type when there are any. Never the vendor's message or body.
func logModelFailure(log *slog.Logger, runID, providerID, model string, e *provider.Error) {
	if log == nil {
		return
	}
	attrs := []any{"provider", providerID, "model", model, "kind", e.Kind}
	if runID != "" {
		attrs = append([]any{"run_id", runID}, attrs...)
	}
	if e.Status != 0 {
		attrs = append(attrs, "status", e.Status)
	}
	if vendorType := provider.SafeVendorType(e.VendorType); vendorType != "" {
		attrs = append(attrs, "vendor_type", vendorType)
	}
	log.Warn("model call failed", attrs...)
}
