package api

import (
	"cmp"
	"math"
	"net/http"
	"slices"
	"sort"
	"time"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// deliveryOpsRowCap caps every analytics table; Total still reports the whole population.
const deliveryOpsRowCap = 50

// deliveryServiceStat is what the Delivery tab knows about one service in the window.
type deliveryServiceStat struct {
	key        model.ServiceKey
	tagID      int64
	inherited  bool
	rollouts   int
	failed     int
	durations  []float64 // seconds, completed rollouts
	recoveries []float64 // seconds, failure finish to the next completed rollout's finish
	lastFailed *time.Time
}

// deliveryAggregate is the pure result of aggregating deployment facts; the handler only adds tags and sources.
type deliveryAggregate struct {
	kpis        DeliveryKPIsDTO
	rollouts    SeriesDTO
	failureRate SeriesDTO
	duration    SeriesDTO
	services    []deliveryServiceStat
}

// interpolatedPercentile is percentile_cont: p in [0,1], linear interpolation between the closest ranks.
// values need not be sorted; it panics-free returns 0 for an empty slice (callers check first).
func interpolatedPercentile(values []float64, p float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := slices.Clone(values)
	slices.Sort(sorted)
	position := p * float64(len(sorted)-1)
	lower := int(math.Floor(position))
	upper := int(math.Ceil(position))
	return sorted[lower] + (sorted[upper]-sorted[lower])*(position-float64(lower))
}

// optionalPercentile is nil for no values.
func optionalPercentile(values []float64, p float64) *float64 {
	if len(values) == 0 {
		return nil
	}
	v := interpolatedPercentile(values, p)
	return &v
}

// aggregateDelivery computes every Delivery number from the raw rollouts started in the window.
//
//   - Change failure rate is failed / (completed + failed); open and superseded rollouts are not a verdict.
//   - A rollback is a deploy whose revision is lower than the previous deploy or restart of the same
//     service inside the window (the first rollout of a service has nothing to compare with).
//   - Time to recover is measured per failed rollout to the finish of the next completed rollout of the
//     same service that started after the failure finished. Only failures in the window count and the
//     recovery must also be in the window.
func aggregateDelivery(facts []store.DeploymentFact, w windowSpec) deliveryAggregate {
	out := deliveryAggregate{
		rollouts:    newBucketedSeries(w),
		failureRate: newBucketedSeries(w),
		duration:    newBucketedSeries(w),
	}
	completedLine := zeroLine(w, "completed", "Completed", false)
	failedLine := zeroLine(w, "failed", "Failed", false)
	supersededLine := zeroLine(w, "superseded", "Superseded", false)
	rateLine := nullLine(w, "failure_rate", "Change failure rate")
	medianLine := nullLine(w, "median", "Median")
	p90Line := nullLine(w, "p90", "90th percentile")

	byService := map[model.ServiceKey][]store.DeploymentFact{}
	var order []model.ServiceKey
	for _, fact := range facts {
		if bucketIndex(fact.StartedAt, w) < 0 {
			continue
		}
		if _, seen := byService[fact.Key]; !seen {
			order = append(order, fact.Key)
		}
		byService[fact.Key] = append(byService[fact.Key], fact)
	}

	bucketCompleted := make([]int, w.buckets)
	bucketFailed := make([]int, w.buckets)
	bucketDurations := make([][]float64, w.buckets)
	var allDurations, allRecoveries []float64
	completed, failed := 0, 0

	for _, key := range order {
		group := byService[key]
		slices.SortStableFunc(group, func(a, b store.DeploymentFact) int { return a.StartedAt.Compare(b.StartedAt) })
		stat := deliveryServiceStat{key: key, tagID: group[0].TagID, inherited: group[0].TagInherited}
		previousRevision := 0
		for i, fact := range group {
			index := bucketIndex(fact.StartedAt, w)
			stat.rollouts++
			out.kpis.Rollouts++

			switch fact.Outcome {
			case "completed":
				completed++
				bucketCompleted[index]++
				completedLine.add(index, 1)
				if fact.FinishedAt != nil {
					seconds := fact.FinishedAt.Sub(fact.StartedAt).Seconds()
					stat.durations = append(stat.durations, seconds)
					allDurations = append(allDurations, seconds)
					bucketDurations[index] = append(bucketDurations[index], seconds)
				}
			case "failed":
				failed++
				stat.failed++
				bucketFailed[index]++
				failedLine.add(index, 1)
				if fact.FinishedAt != nil && (stat.lastFailed == nil || fact.FinishedAt.After(*stat.lastFailed)) {
					finished := *fact.FinishedAt
					stat.lastFailed = &finished
				}
				if recovery, ok := recoverySeconds(fact, group[i+1:]); ok {
					stat.recoveries = append(stat.recoveries, recovery)
					allRecoveries = append(allRecoveries, recovery)
				}
			case "superseded":
				out.kpis.Superseded++
				supersededLine.add(index, 1)
			}

			if fact.Kind == "deploy" && fact.Revision > 0 && previousRevision > 0 && fact.Revision < previousRevision {
				out.kpis.Rollbacks++
			}
			if (fact.Kind == "deploy" || fact.Kind == "restart") && fact.Revision > 0 {
				previousRevision = fact.Revision
			}
		}
		out.services = append(out.services, stat)
	}

	for i := range w.buckets {
		if verdicts := bucketCompleted[i] + bucketFailed[i]; verdicts > 0 {
			rateLine.set(i, 100*float64(bucketFailed[i])/float64(verdicts))
		}
		if len(bucketDurations[i]) > 0 {
			medianLine.set(i, interpolatedPercentile(bucketDurations[i], 0.5))
			p90Line.set(i, interpolatedPercentile(bucketDurations[i], 0.9))
		}
	}
	out.rollouts.Series = []SeriesLineDTO{completedLine, failedLine, supersededLine}
	out.failureRate.Series = []SeriesLineDTO{rateLine}
	out.duration.Series = []SeriesLineDTO{medianLine, p90Line}

	out.kpis.RolloutsPerDay = float64(out.kpis.Rollouts) / (w.to.Sub(w.from).Hours() / 24)
	if completed+failed > 0 {
		rate := float64(failed) / float64(completed+failed)
		out.kpis.ChangeFailureRate = &rate
	}
	out.kpis.MedianRolloutS = optionalPercentile(allDurations, 0.5)
	out.kpis.P90RolloutS = optionalPercentile(allDurations, 0.9)
	out.kpis.TimeToRecoverS = optionalPercentile(allRecoveries, 0.5)
	return out
}

// recoverySeconds finds the first completed rollout among later (ordered by start) that started after the
// failed rollout finished, and returns the time between the two finishes.
func recoverySeconds(failedRollout store.DeploymentFact, later []store.DeploymentFact) (float64, bool) {
	if failedRollout.FinishedAt == nil {
		return 0, false
	}
	for _, next := range later {
		if next.Outcome == "completed" && next.FinishedAt != nil && next.StartedAt.After(*failedRollout.FinishedAt) {
			return next.FinishedAt.Sub(*failedRollout.FinishedAt).Seconds(), true
		}
	}
	return 0, false
}

// deliveryServiceRows lists services with at least one failed rollout, worst failure rate first. Total is
// the number of services that had any rollout in the window.
func deliveryServiceRows(stats []deliveryServiceStat, tags map[authz.TagID]TagDTO) TopTableDTO[DeliveryServiceRowDTO] {
	table := newTopTableDTO[DeliveryServiceRowDTO]()
	table.Total = len(stats)
	for _, stat := range stats {
		if stat.failed == 0 {
			continue
		}
		table.Rows = append(table.Rows, DeliveryServiceRowDTO{
			ServiceID:    stat.key.ID(),
			Service:      stat.key.Service,
			Cluster:      stat.key.Cluster,
			AccountID:    stat.key.AccountID,
			Tag:          inheritedTag(tags, authz.TagID(stat.tagID), stat.inherited),
			Rollouts:     stat.rollouts,
			Failed:       stat.failed,
			FailureRate:  float64(stat.failed) / float64(stat.rollouts),
			MedianS:      optionalPercentile(stat.durations, 0.5),
			RecoverS:     optionalPercentile(stat.recoveries, 0.5),
			LastFailedAt: stat.lastFailed,
		})
	}
	sort.SliceStable(table.Rows, func(i, j int) bool {
		a, b := table.Rows[i], table.Rows[j]
		if a.FailureRate != b.FailureRate {
			return a.FailureRate > b.FailureRate
		}
		if a.Failed != b.Failed {
			return a.Failed > b.Failed
		}
		return cmp.Or(cmp.Compare(a.Cluster, b.Cluster), cmp.Compare(a.Service, b.Service)) < 0
	})
	if len(table.Rows) > deliveryOpsRowCap {
		table.Rows = table.Rows[:deliveryOpsRowCap]
	}
	return table
}

// analyticsDelivery answers GET /api/analytics/delivery?range=&account=&region=&tag=.
func (s *Server) analyticsDelivery(w http.ResponseWriter, r *http.Request) {
	sc, err := s.scopeFrom(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	requested, err := analyticsRangeFrom(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	ctx := r.Context()
	retention := s.Cfg.Retention.DeploymentHistory
	window := clampWindow(requested, retention, s.now())

	facts, err := s.Store.AnalyticsDeployments(ctx, sc.Filter, sc.Vis, window.from, window.to)
	if err != nil {
		s.fail(w, err)
		return
	}
	tags, err := s.tagDTOs(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	polledAt, err := s.Store.PolledAt(ctx, sc.Filter)
	if err != nil {
		s.fail(w, err)
		return
	}

	aggregate := aggregateDelivery(facts, window)
	out := newDeliveryDTO()
	out.Tab = "delivery"
	out.Window = window.dto(retention, true)
	out.Sources = []AnalyticsSourceDTO{prunedTableSource("deployment_history",
		"Rollouts (deploy, restart and scale) started in the window and how each ended.",
		retention, "HM_RETENTION_DEPLOYMENT_HISTORY")}
	out.PolledAt = polledAt
	out.KPIs = aggregate.kpis
	out.Rollouts = aggregate.rollouts
	out.FailureRate = aggregate.failureRate
	out.Duration = aggregate.duration
	out.Services = deliveryServiceRows(aggregate.services, tags)
	writeJSON(w, http.StatusOK, out)
}

// prunedTableSource is a "Data used on this page" row for a table pruned by a retention setting.
func prunedTableSource(table, reads string, retention time.Duration, setting string) AnalyticsSourceDTO {
	kept := formatRetention(retention)
	if kept == "" {
		kept = "forever"
	}
	return AnalyticsSourceDTO{Table: table, Reads: reads, Kept: kept, Setting: &setting}
}
