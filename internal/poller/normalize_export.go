package poller

import (
	"time"

	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/model"
)

// NormalizeTaskDefinition summarises a described task definition for the store's
// snapshot table. The deploy package uses it for revisions it registers itself.
func NormalizeTaskDefinition(t awsx.Target, td ecstypes.TaskDefinition, now time.Time) model.TaskDefinitionRef {
	return normalizeTaskDefinition(t, td, now)
}
