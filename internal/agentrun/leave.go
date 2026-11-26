package agentrun

import (
	"math"

	"github.com/rcss-labs/harbormaster/internal/agentclient"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// LeaveFor is the room a run is to leave for others: the worst-case reservation of one run for each of roles, at
// the model each role has now. A role with no model, or whose model the catalogue does not offer, adds nothing.
// The sum stops at the int64 cap.
func LeaveFor(cat agentclient.Catalogue, settings store.AgentSettings, roles ...string) int64 {
	var leave int64
	for _, role := range roles {
		ref, ok := settings.RoleModels[role]
		if !ok || ref.Model == "" {
			continue
		}
		model, found := cat.Find(ref.Provider, ref.Model)
		if !found {
			continue
		}
		cost := max(model.MaxRunCostMicroUSD(cat.MaxRunTokens), 0) // a wrapped price counts as nothing
		if cost > math.MaxInt64-leave {
			return math.MaxInt64
		}
		leave += cost
	}
	return leave
}
