package api

import (
	"testing"

	"github.com/rcss-labs/harbormaster/internal/store"
)

// A change set's reason is stored in the operation's details; the feed has no other field for it, so it is
// appended to the text the Operations tab shows.
func TestOperationEvent_AppendsTheChangeSetReason(t *testing.T) {
	tests := []struct {
		name    string
		details map[string]any
		want    string
	}{
		{"reason is shown", map[string]any{"changeset_id": "c1", "reason": "raise capacity for launch"}, `Deployed payments-api:43. Reason: "raise capacity for launch".`},
		{"empty reason adds nothing", map[string]any{"changeset_id": "c1", "reason": ""}, "Deployed payments-api:43."},
		{"no details adds nothing", nil, "Deployed payments-api:43."},
		{"non-text reason is ignored", map[string]any{"reason": 7}, "Deployed payments-api:43."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := operationEvent(ServiceRef{}, store.OperationRow{Text: "Deployed payments-api:43.", Details: tt.details})
			if event.Text != tt.want {
				t.Errorf("text = %q, want %q", event.Text, tt.want)
			}
		})
	}
}
