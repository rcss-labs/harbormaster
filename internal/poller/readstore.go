package poller

import (
	"context"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// readStore is every read the poller's loops make. It deliberately has no write methods: the loops write only through
// the fenced *store.SliceWriter, so an unfenced write does not compile.
type readStore interface {
	KnownTaskARNs(ctx context.Context, k model.ClusterKey, lastStatus string) ([]string, error)
	KnownTaskARNsForService(ctx context.Context, k model.ServiceKey, lastStatus string) ([]string, error)
	HasTaskDefinition(ctx context.Context, accountID, region, arn string) (bool, error)
	ListServices(ctx context.Context, f store.TargetFilter, cluster string, vis store.Visibility) ([]store.ServiceRow, error)
	ListTasks(ctx context.Context, tq store.TaskQuery) ([]store.TaskRow, int, error)
	UnhealthyTargets(ctx context.Context, f store.TargetFilter) (map[model.ServiceKey]bool, error)
	StatusRuns(ctx context.Context, t awsx.Target) (map[model.ServiceKey]store.StatusRun, error)
	RecentServiceEventsPerService(ctx context.Context, f store.TargetFilter, limit int) (map[model.ServiceKey][]model.ServiceEvent, error)
	TargetHealthByService(ctx context.Context, f store.TargetFilter, cluster string) (map[model.ServiceKey][]model.TargetHealth, error)
	ListPollLoops(ctx context.Context) ([]store.PollLoop, error)
	ListAccounts(ctx context.Context) ([]store.AWSAccount, error)
	ListAlerts(ctx context.Context, f store.AlertFilter) ([]model.Alert, error)
	ScalingForService(ctx context.Context, k model.ServiceKey) (model.Scaling, error)
	RunningTaskCapacityByCluster(ctx context.Context, target store.TargetFilter) ([]store.ClusterCapacitySample, error)
}

var _ readStore = (*store.Store)(nil)

// fencedNetwork and fencedStorage give the network and storage tiers (which take small store interfaces so tests can
// fake them) reads from the store and writes from the slice's writer.
type fencedNetwork struct {
	readStore
	w *store.SliceWriter
}

func (f fencedNetwork) ReplaceNetwork(ctx context.Context, accountID, region string, vpcs []model.VPC, subnets []model.Subnet, edges []model.VPCEdge, sgs []model.SecurityGroup) error {
	return f.w.ReplaceNetwork(ctx, accountID, region, vpcs, subnets, edges, sgs)
}

type fencedStorage struct{ w *store.SliceWriter }

func (f fencedStorage) ReplaceStorage(ctx context.Context, accountID, region string, fs []model.FileSystem, aps []model.AccessPoint) error {
	return f.w.ReplaceStorage(ctx, accountID, region, fs, aps)
}
