package api

import (
	"net/http"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// operationKinds are the kinds the Operations tab counts, in stacking order.
var operationKinds = []struct{ key, label string }{
	{"deploy", "Deploy"},
	{"rollback", "Rollback"},
	{"task_definition", "New revision"},
	{"scale", "Scale"},
	{"restart", "Restart"},
	{"stop_task", "Stop task"},
	{"terminal", "Terminal"},
}

// handsOnKinds are the manual interventions charted on their own.
var handsOnKinds = []string{"restart", "stop_task", "terminal"}

// operationsAggregate is the pure part of the Operations tab: KPIs and the three series.
type operationsAggregate struct {
	kpis           OperationsKPIsDTO
	byType         SeriesDTO
	handsOn        SeriesDTO
	restartsPer100 SeriesDTO
}

// aggregateOperations folds per-bucket counts into the KPIs and series. People is the number of distinct
// operators; restarts per 100 deploys counts rollbacks as deploys and is null in buckets without any.
func aggregateOperations(buckets []store.OperationBucketCount, operators []store.OperatorCounts, w windowSpec) operationsAggregate {
	out := operationsAggregate{byType: newBucketedSeries(w), handsOn: newBucketedSeries(w), restartsPer100: newBucketedSeries(w)}
	lines := map[string]*SeriesLineDTO{}
	out.byType.Series = make([]SeriesLineDTO, 0, len(operationKinds))
	for _, kind := range operationKinds {
		out.byType.Series = append(out.byType.Series, zeroLine(w, kind.key, kind.label, false))
	}
	for i := range out.byType.Series {
		lines[out.byType.Series[i].Key] = &out.byType.Series[i]
	}
	handsOn := map[string]*SeriesLineDTO{}
	for _, kind := range handsOnKinds {
		label := lines[kind].Label
		out.handsOn.Series = append(out.handsOn.Series, zeroLine(w, kind, label, false))
	}
	for i := range out.handsOn.Series {
		handsOn[out.handsOn.Series[i].Key] = &out.handsOn.Series[i]
	}

	deploys := make([]float64, w.buckets)
	restarts := make([]float64, w.buckets)
	totals := map[string]int{}
	for _, c := range buckets {
		index := bucketIndex(c.Bucket, w)
		line, known := lines[c.Kind]
		if index < 0 || !known {
			continue
		}
		n := float64(c.Count)
		line.add(index, n)
		if hands, ok := handsOn[c.Kind]; ok {
			hands.add(index, n)
		}
		totals[c.Kind] += c.Count
		out.kpis.Operations += c.Count
		switch c.Kind {
		case "deploy", "rollback":
			deploys[index] += n
		case "restart":
			restarts[index] += n
		}
	}

	ratio := nullLine(w, "restarts_per_100_deploys", "Restarts per 100 deploys")
	for i := range w.buckets {
		if deploys[i] > 0 {
			ratio.set(i, 100*restarts[i]/deploys[i])
		}
	}
	out.restartsPer100.Series = []SeriesLineDTO{ratio}

	out.kpis.People = len(operators)
	out.kpis.RevisionsRegistered = totals["task_definition"]
	out.kpis.ScaleChanges = totals["scale"]
	out.kpis.Restarts = totals["restart"]
	out.kpis.TasksStopped = totals["stop_task"]
	out.kpis.TerminalSessions = totals["terminal"]
	return out
}

// operatorRows renders the who-operates table; Deploy includes rollbacks. Operators arrive busiest first.
func operatorRows(operators []store.OperatorCounts) TopTableDTO[OperatorRowDTO] {
	table := newTopTableDTO[OperatorRowDTO]()
	table.Total = len(operators)
	for _, op := range operators {
		if len(table.Rows) == deliveryOpsRowCap {
			break
		}
		total := 0
		for _, n := range op.Counts {
			total += n
		}
		table.Rows = append(table.Rows, OperatorRowDTO{
			Username: op.Username, Total: total,
			Deploy:   op.Counts["deploy"] + op.Counts["rollback"],
			Restart:  op.Counts["restart"],
			Scale:    op.Counts["scale"],
			Terminal: op.Counts["terminal"],
			LastAt:   op.LastAt,
		})
	}
	return table
}

func restartedRows(services []store.RestartedService, tags map[authz.TagID]TagDTO) TopTableDTO[RestartedServiceRowDTO] {
	table := newTopTableDTO[RestartedServiceRowDTO]()
	table.Total = len(services)
	for _, svc := range services {
		if len(table.Rows) == deliveryOpsRowCap {
			break
		}
		table.Rows = append(table.Rows, RestartedServiceRowDTO{
			ServiceID: svc.Key.ID(), Service: svc.Key.Service, Cluster: svc.Key.Cluster,
			Tag:      inheritedTag(tags, authz.TagID(svc.TagID), svc.TagInherited),
			Restarts: svc.Restarts, People: svc.People, LastAt: svc.LastAt,
		})
	}
	return table
}

// analyticsOperations answers GET /api/analytics/operations?range=&account=&region=&tag=.
func (s *Server) analyticsOperations(w http.ResponseWriter, r *http.Request) {
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
	retention := s.Cfg.Retention.OperationLog
	window := clampWindow(requested, retention, s.now())

	buckets, err := s.Store.AnalyticsOperations(ctx, sc.Filter, sc.Vis, window.from, window.to, window.bucket)
	if err != nil {
		s.fail(w, err)
		return
	}
	operators, err := s.Store.AnalyticsOperators(ctx, sc.Filter, sc.Vis, window.from, window.to)
	if err != nil {
		s.fail(w, err)
		return
	}
	restarted, err := s.Store.AnalyticsRestartedServices(ctx, sc.Filter, sc.Vis, window.from, window.to)
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

	aggregate := aggregateOperations(buckets, operators, window)
	out := newOperationsDTO()
	out.Tab = "operations"
	out.Window = window.dto(retention, true)
	out.Sources = []AnalyticsSourceDTO{
		prunedTableSource("operation_log", "Changes made from Harbormaster, by kind, person and service.", retention, "HM_RETENTION_OPERATION_LOG"),
		{Table: "revision_records", Reads: "Cross-check only: it cannot be scoped to a service, so revisions registered are counted from operation_log.", Kept: "forever"},
	}
	out.PolledAt = polledAt
	out.KPIs = aggregate.kpis
	out.ByType = aggregate.byType
	out.HandsOn = aggregate.handsOn
	out.RestartsPer100Deploys = aggregate.restartsPer100
	out.Who = operatorRows(operators)
	out.MostRestarted = restartedRows(restarted, tags)
	writeJSON(w, http.StatusOK, out)
}
