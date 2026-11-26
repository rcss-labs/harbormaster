package investigate

import (
	"github.com/rcss-labs/harbormaster/internal/agentjob"
	"github.com/rcss-labs/harbormaster/internal/agentresult"
	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// Accept turns what the agent service answered into what is stored; see agentjob.Accept. taskIDs are the ids of the task.
func Accept(res agentresult.Result, canRead func(key model.ServiceKey, action authz.Action) bool, taskIDs ...string) store.InvestigationResult {
	return agentjob.Accept(res, canRead, agentjob.AcceptOptions{AllowLogs: true, Replacement: agentresult.ReplacementSummary, TaskIDs: taskIDs})
}
