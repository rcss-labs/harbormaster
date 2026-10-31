package api

import (
	"net/http"
	"sort"
	"time"

	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/status"
	"github.com/rcss-labs/harbormaster/internal/store"
)

const (
	overviewEvents    = 10
	stopReasonsWindow = 72 * time.Hour
)

// overview answers GET /api/overview?account=&region=.
func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	sc, err := s.scopeFrom(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	ctx := r.Context()
	now := s.now()
	rows, err := s.Store.ListClusters(ctx, sc.Filter, sc.Vis)
	if err != nil {
		s.fail(w, err)
		return
	}
	view, err := s.loadEstate(ctx, sc.Filter, sc.Vis, "", rows)
	if err != nil {
		s.fail(w, err)
		return
	}

	out := OverviewDTO{
		Clusters:    []OverviewClusterDTO{},
		PerCluster:  []PerClusterDTO{},
		Services:    OverviewServicesDTO{ByStatus: map[model.Status]int{}},
		Tasks:       OverviewTasksDTO{},
		StopReasons: []StopReasonDTO{},
		Events:      []EventDTO{},
	}
	accounts, regions := map[string]bool{}, map[string]bool{}
	for _, row := range rows {
		accounts[row.Key.AccountID] = true
		regions[row.Key.Region] = true
		out.Clusters = append(out.Clusters, OverviewClusterDTO{ID: row.Key.ID(), Name: row.Key.Cluster, Status: view.clusterStatus(row.Key)})
		per := PerClusterDTO{ID: row.Key.ID(), Name: row.Key.Cluster}
		for _, svc := range view.servicesByCluster[row.Key] {
			per.Running += svc.Running
			per.Desired += svc.Desired
		}
		out.PerCluster = append(out.PerCluster, per)
	}
	out.Accounts, out.Regions = len(accounts), len(regions)

	rollingOut := 0
	for _, svc := range view.services {
		out.Services.Total++
		out.Services.ByStatus[view.statusByService[svc.Key]]++
		out.Tasks.Running += svc.Running
		out.Tasks.Desired += svc.Desired
		out.Tasks.Pending += svc.Pending
		for _, t := range view.tasksByService[svc.Key] {
			if t.HealthStatus == "UNHEALTHY" {
				out.Tasks.Unhealthy++
			}
			if isRollingOut(svc.Service, t) {
				rollingOut++
			}
		}
	}
	out.TaskHealth = TaskHealthDTO{
		Healthy:    max(out.Tasks.Running-out.Tasks.Unhealthy-rollingOut, 0),
		Unhealthy:  out.Tasks.Unhealthy,
		RollingOut: rollingOut,
		Missing:    max(out.Tasks.Desired-out.Tasks.Running, 0),
	}

	if out.PolledAt, err = s.Store.PolledAt(ctx, sc.Filter); err != nil {
		s.fail(w, err)
		return
	}
	history, err := s.Store.CountsHistory(ctx, sc.Filter, "", "", sc.Vis, now.Add(-historyWindow))
	if err != nil {
		s.fail(w, err)
		return
	}
	out.Tasks.History72h = pointsOf(history)

	stops, err := s.Store.StopsPerHour(ctx, sc.Filter, "", sc.Vis, stopsWindowStart(now))
	if err != nil {
		s.fail(w, err)
		return
	}
	out.StopsPerHour = fillStopBuckets(stops, now)

	days, err := s.Store.DeploymentsPerDay(ctx, sc.Filter, sc.Vis, deploymentsWindowStart(now))
	if err != nil {
		s.fail(w, err)
		return
	}
	out.DeploymentsPerDay = fillDeployDays(days, now)

	summary, err := s.Store.TaskSummary(ctx, sc.Filter, sc.Vis, now.Add(-stopReasonsWindow))
	if err != nil {
		s.fail(w, err)
		return
	}
	for _, group := range status.StopReasonGroups {
		if n := summary.StopReasons[group]; n > 0 {
			out.StopReasons = append(out.StopReasons, StopReasonDTO{Reason: group, Count: n})
		}
	}

	if out.Events, err = s.recentEvents(r, sc, view); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func deploymentsWindowStart(now time.Time) time.Time {
	return now.UTC().Truncate(24*time.Hour).AddDate(0, 0, -(deploymentsDays - 1))
}

// fillDeployDays returns 14 UTC-day buckets ending today, zero where nothing was deployed.
func fillDeployDays(days []store.DeployDay, now time.Time) []DeployDayDTO {
	byDay := make(map[int64]store.DeployDay, len(days))
	for _, d := range days {
		byDay[d.Day.UTC().Truncate(24*time.Hour).Unix()] = d
	}
	start := deploymentsWindowStart(now)
	out := make([]DeployDayDTO, 0, deploymentsDays)
	for i := 0; i < deploymentsDays; i++ {
		day := start.AddDate(0, 0, i)
		d := byDay[day.Unix()]
		out = append(out, DeployDayDTO{Day: day, Deploy: d.Deploy, Restart: d.Restart, Failed: d.Failed})
	}
	return out
}

// recentEvents merges ECS service events and Harbormaster operations, newest first, capped at 10.
func (s *Server) recentEvents(r *http.Request, sc scope, view *estateView) ([]EventDTO, error) {
	ctx := r.Context()
	ecsEvents, err := s.Store.RecentServiceEvents(ctx, sc.Filter, sc.Vis, overviewEvents)
	if err != nil {
		return nil, err
	}
	operations, err := s.Store.RecentOperations(ctx, sc.Filter, sc.Vis, overviewEvents)
	if err != nil {
		return nil, err
	}
	events := make([]EventDTO, 0, len(ecsEvents)+len(operations))
	for _, e := range ecsEvents {
		events = append(events, eventDTO(view, e.CreatedAt, "ecs", "ECS", e.Key, e.Message))
	}
	for _, op := range operations {
		events = append(events, eventDTO(view, op.TS, "harbormaster", op.Actor, op.Key, op.Text))
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].TS.After(events[j].TS) })
	if len(events) > overviewEvents {
		events = events[:overviewEvents]
	}
	return events, nil
}

func eventDTO(view *estateView, ts time.Time, source, actor string, key model.ServiceKey, text string) EventDTO {
	e := EventDTO{
		TS: ts, Source: source, Actor: actor, Text: text,
		Cluster: ClusterRef{ID: key.ClusterKey.ID(), Name: key.Cluster},
	}
	if key.Service != "" {
		e.Service = view.serviceRef(key)
	}
	return e
}
