package devseed

import (
	"context"
	"fmt"
	"hash/fnv"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/jackc/pgx/v5"

	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// analyticsHistoryDays is how many complete UTC days before today the demo history covers. Sixty days fill the
// 30-day comparison of "Services getting less available" as well as the 90-day charts.
const analyticsHistoryDays = 60

// samplesPerDay is the number of 5-minute counts samples in a day.
const samplesPerDay = 288

// SeedAnalyticsHistory writes deterministic daily rollup rows for the fake estate so the Analytics 90-day panels show
// data from the first start: a fresh fake environment only has counts history from boot, and the rollups only cover
// complete days. It also records a few completed and failed rollouts, with failure reasons, over the last 20 days.
// Existing rows are left alone, so it is safe to run on every start. Only the fake-AWS start-up calls it.
func SeedAnalyticsHistory(ctx context.Context, st *store.Store, now time.Time) error {
	estate := fake.MockupEstate(func() time.Time { return now })
	estate.Mu.Lock()
	defer estate.Mu.Unlock()

	today := now.UTC().Truncate(24 * time.Hour)
	if err := seedAvailability(ctx, st, estate, today); err != nil {
		return err
	}
	if err := seedClusterCapacity(ctx, st, estate, today); err != nil {
		return err
	}
	if err := seedUtil(ctx, st, estate, today); err != nil {
		return err
	}
	return seedRollouts(ctx, st, estate, now)
}

// sendBatch runs the queued inserts in one round trip: a day of history per service is hundreds of rows.
func sendBatch(ctx context.Context, st *store.Store, batch *pgx.Batch, what string) error {
	results := st.Pool.SendBatch(ctx, batch)
	defer results.Close()
	for range batch.Len() {
		if _, err := results.Exec(); err != nil {
			return fmt.Errorf("seed %s: %w", what, err)
		}
	}
	return nil
}

// pick is a stable pseudo-random number in [0, n) for the given parts.
func pick(n int, parts ...string) int {
	h := fnv.New32a()
	for _, p := range parts {
		_, _ = h.Write([]byte(p))
		_, _ = h.Write([]byte{0})
	}
	return int(h.Sum32() % uint32(n))
}

// wave is a slow deterministic variation in [0, 1) over the days, different for every key.
func wave(key string, daysAgo int) float64 {
	return float64(pick(1000, key, strconv.Itoa(daysAgo))) / 1000
}

func seedAvailability(ctx context.Context, st *store.Store, estate *fake.Estate, today time.Time) error {
	batch := &pgx.Batch{}
	for _, svc := range estate.Services {
		name := aws.ToString(svc.Service.ServiceName)
		key := svc.AccountID + svc.Region + svc.ClusterName + name
		// Every fourth service had a rougher last 30 days than the 30 before, to fill the getting-worse table.
		decaying := pick(4, key) == 0
		for daysAgo := 1; daysAgo <= analyticsHistoryDays; daysAgo++ {
			belowSamples := 0
			if r := wave(key+"b", daysAgo); r > 0.9 {
				belowSamples = 1 + int(r*100)%6
			}
			if decaying && daysAgo <= 30 {
				belowSamples += 2 + pick(8, key, strconv.Itoa(daysAgo))
			}
			stops := 0
			if belowSamples > 3 {
				stops = 1
			}
			batch.Queue(
				`insert into service_availability_daily
				   (account_id, region, cluster_name, service_name, day, samples, samples_ok, minutes_below, unexpected_stops)
				 values ($1, $2, $3, $4, $5::date, $6, $7, $8, $9)
				 on conflict do nothing`,
				svc.AccountID, svc.Region, svc.ClusterName, name, today.AddDate(0, 0, -daysAgo).Format(time.DateOnly),
				samplesPerDay, samplesPerDay-belowSamples, belowSamples*5, stops)
		}
	}
	return sendBatch(ctx, st, batch, "service availability")
}

type clusterTotals struct {
	vcpu, memGiB float64
	tasks, spot  int
}

func seedClusterCapacity(ctx context.Context, st *store.Store, estate *fake.Estate, today time.Time) error {
	totals := map[model.ClusterKey]*clusterTotals{}
	for _, c := range estate.Clusters {
		totals[model.ClusterKey{AccountID: c.AccountID, Region: c.Region, Cluster: aws.ToString(c.Cluster.ClusterName)}] = &clusterTotals{}
	}
	for _, task := range estate.Tasks {
		if aws.ToString(task.Task.LastStatus) != "RUNNING" {
			continue
		}
		t := totals[model.ClusterKey{AccountID: task.AccountID, Region: task.Region, Cluster: task.ClusterName}]
		if t == nil {
			continue
		}
		cpu, _ := strconv.Atoi(aws.ToString(task.Task.Cpu))
		mem, _ := strconv.Atoi(aws.ToString(task.Task.Memory))
		t.vcpu += float64(cpu) / 1024
		t.memGiB += float64(mem) / 1024
		t.tasks++
		if aws.ToString(task.Task.CapacityProviderName) == "FARGATE_SPOT" {
			t.spot++
		}
	}
	batch := &pgx.Batch{}
	for key, t := range totals {
		id := key.AccountID + key.Region + key.Cluster
		for daysAgo := 1; daysAgo <= analyticsHistoryDays; daysAgo++ {
			// Usage drifts between 80% and 110% of today's size, so the lines are not flat.
			scale := 0.8 + 0.3*wave(id, daysAgo)
			vcpu, mem := t.vcpu*scale, t.memGiB*scale
			tasks := int(float64(t.tasks)*scale + 0.5)
			spot := min(int(float64(t.spot)*scale+0.5), tasks)
			batch.Queue(
				`insert into cluster_capacity_daily
				   (account_id, region, cluster_name, day, samples, vcpu_sum, vcpu_max, mem_gib_sum, mem_gib_max, tasks_sum, tasks_max, spot_sum)
				 values ($1, $2, $3, $4::date, $5, $6, $7, $8, $9, $10, $11, $12)
				 on conflict do nothing`,
				key.AccountID, key.Region, key.Cluster, today.AddDate(0, 0, -daysAgo).Format(time.DateOnly),
				samplesPerDay, vcpu*samplesPerDay, vcpu*1.1, mem*samplesPerDay, mem*1.1, tasks*samplesPerDay, tasks+1, spot*samplesPerDay)
		}
	}
	return sendBatch(ctx, st, batch, "cluster capacity")
}

// taskDefinitionSize is the CPU units and memory MiB of the task definition a service currently runs.
func taskDefinitionSize(estate *fake.Estate, svc fake.Service) (cpu, mem int, ok bool) {
	arn := aws.ToString(svc.Service.TaskDefinition)
	for _, td := range estate.TaskDefinitions {
		if td.AccountID == svc.AccountID && td.Region == svc.Region && aws.ToString(td.Definition.TaskDefinitionArn) == arn {
			cpu, cpuErr := strconv.Atoi(aws.ToString(td.Definition.Cpu))
			mem, memErr := strconv.Atoi(aws.ToString(td.Definition.Memory))
			return cpu, mem, cpuErr == nil && memErr == nil
		}
	}
	return 0, 0, false
}

// costDemoUtil pins the use of a few services so the Cost tab shows each case: low use gives a suggestion, high use
// gives none, and a short history is too short to suggest from. Services without rows (edge-proxy) show no data.
var costDemoUtil = map[string]struct {
	cpuBase, memBase float64
	days             int // zero: the full history
}{
	"jenkins-controller": {9, 24, 0},
	"search-indexer":     {9, 24, 0},
	"report-worker":      {9, 24, 0},
	"payments-api":       {70, 75, 0},
	"artifact-proxy":     {30, 45, 5},
}

func seedUtil(ctx context.Context, st *store.Store, estate *fake.Estate, today time.Time) error {
	batch := &pgx.Batch{}
	for _, svc := range estate.Services {
		cluster := model.ClusterKey{AccountID: svc.AccountID, Region: svc.Region, Cluster: svc.ClusterName}
		if mode, ok := estate.ContainerInsights[cluster]; !ok || mode == "disabled" {
			continue // no Container Insights, no utilisation
		}
		cpuUnits, memMiB, ok := taskDefinitionSize(estate, svc)
		if !ok {
			continue
		}
		name := aws.ToString(svc.Service.ServiceName)
		key := svc.AccountID + svc.Region + svc.ClusterName + name
		// A third of the services use a small share of what they reserve.
		cpuBase, memBase := 30.0, 45.0
		if pick(3, key) == 0 {
			cpuBase, memBase = 9, 24
		}
		historyDays := analyticsHistoryDays
		if demo, ok := costDemoUtil[name]; ok {
			cpuBase, memBase = demo.cpuBase, demo.memBase
			if demo.days > 0 {
				historyDays = demo.days
			}
		}
		for daysAgo := 1; daysAgo <= historyDays; daysAgo++ {
			cpuAvg := cpuBase + 12*wave(key+"c", daysAgo)
			memAvg := memBase + 10*wave(key+"m", daysAgo)
			batch.Queue(
				`insert into service_util_daily
				   (account_id, region, cluster_name, service_name, day, cpu_avg, cpu_p95, mem_avg, mem_p95, cpu_reserved_units, mem_reserved_mib)
				 values ($1, $2, $3, $4, $5::date, $6, $7, $8, $9, $10, $11)
				 on conflict do nothing`,
				svc.AccountID, svc.Region, svc.ClusterName, name, today.AddDate(0, 0, -daysAgo).Format(time.DateOnly),
				cpuAvg, min(cpuAvg*1.6, 100), memAvg, min(memAvg*1.25, 100), cpuUnits, memMiB)
		}
	}
	return sendBatch(ctx, st, batch, "service utilisation")
}

var failureSeeds = []struct{ group, detail string }{
	{"circuit_breaker", "ECS deployment circuit breaker: tasks failed to start."},
	{"health_check", "Task failed ELB health checks"},
	{"out_of_memory", "OutOfMemoryError: Container killed due to memory usage"},
	{"failed_to_start", "CannotPullContainerError: image not found"},
	{"circuit_breaker", "ECS deployment circuit breaker: tasks failed to start."},
}

// seedRollouts records, for the first services of the estate, one completed rollout and one failed rollout with a
// reason every few days.
func seedRollouts(ctx context.Context, st *store.Store, estate *fake.Estate, now time.Time) error {
	const failures = 8
	for i := 0; i < failures && i < len(estate.Services); i++ {
		svc := estate.Services[i]
		key := model.ServiceKey{
			ClusterKey: model.ClusterKey{AccountID: svc.AccountID, Region: svc.Region, Cluster: svc.ClusterName},
			Service:    aws.ToString(svc.Service.ServiceName),
		}
		seed := failureSeeds[i%len(failureSeeds)]
		failedAt := now.Add(-time.Duration(2+i*2)*24*time.Hour - time.Duration(pick(600, key.Service))*time.Minute)
		completedAt := failedAt.Add(2 * time.Hour)
		failedID := fmt.Sprintf("devseed/%s/failed", key.ID())
		completedID := fmt.Sprintf("devseed/%s/completed", key.ID())
		if _, err := st.InsertDeploymentIfNew(ctx, store.DeploymentRecord{Key: key, DeploymentID: failedID, Revision: 2, Kind: "deploy", StartedAt: failedAt}); err != nil {
			return err
		}
		if _, err := st.FinishDeployment(ctx, key, failedID, "failed", failedAt.Add(time.Duration(4+i)*time.Minute),
			&store.FailureInfo{Group: seed.group, Detail: seed.detail}); err != nil {
			return err
		}
		finished := completedAt.Add(6 * time.Minute)
		if _, err := st.InsertDeploymentIfNew(ctx, store.DeploymentRecord{
			Key: key, DeploymentID: completedID, Revision: 3, Kind: "deploy", StartedAt: completedAt, FinishedAt: &finished, Outcome: "completed",
		}); err != nil {
			return err
		}
	}
	return nil
}
