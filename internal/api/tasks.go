package api

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/status"
	"github.com/rcss-labs/harbormaster/internal/store"
)

const (
	defaultTaskFilter = "problems"
	taskSummaryWindow = 72 * time.Hour
)

var taskFilters = []string{"problems", "running", "stopped", "all"}

func validTaskFilter(f string) bool {
	for _, known := range taskFilters {
		if f == known {
			return true
		}
	}
	return false
}

// listTasks answers GET /api/tasks?account=&region=&filter=&q=&cursor=&limit=.
func (s *Server) listTasks(w http.ResponseWriter, r *http.Request) {
	sc, err := s.scopeFrom(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	out, err := s.tasksList(r.Context(), r, sc, store.TaskQuery{TargetFilter: sc.Filter, Vis: sc.Vis})
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// listServiceTasks answers GET /api/services/{serviceId}/tasks; invisible services are 404.
func (s *Server) listServiceTasks(w http.ResponseWriter, r *http.Request) {
	key, err := serviceKeyFrom(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	sc, err := s.scopeFrom(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	if _, err := s.Store.GetServiceView(r.Context(), key, sc.Vis); err != nil {
		s.fail(w, err)
		return
	}
	base := store.TaskQuery{
		TargetFilter: store.TargetFilter{AccountID: key.AccountID, Region: key.Region},
		Vis:          sc.Vis, Cluster: key.Cluster, Service: key.Service,
	}
	out, err := s.tasksList(r.Context(), r, sc, base)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// tasksList builds the task list body. base carries the scope (target, visibility, cluster, service).
func (s *Server) tasksList(ctx context.Context, r *http.Request, sc scope, base store.TaskQuery) (TasksListDTO, error) {
	query := r.URL.Query()
	filter := query.Get("filter")
	if filter == "" {
		filter = defaultTaskFilter
	}
	if !validTaskFilter(filter) {
		return TasksListDTO{}, invalid("filter must be one of problems, running, stopped or all.")
	}
	limit, offset, err := pageFrom(r, s.Cfg)
	if err != nil {
		return TasksListDTO{}, err
	}
	base.Q = strings.TrimSpace(query.Get("q"))

	page := base
	page.Filter, page.Limit, page.Offset = filter, limit, offset
	rows, total, err := s.Store.ListTasks(ctx, page)
	if err != nil {
		return TasksListDTO{}, err
	}

	var counts TaskCountsDTO
	for _, f := range taskFilters {
		n := total
		if f != filter {
			probe := base
			probe.Filter, probe.Limit = f, 1
			if _, n, err = s.Store.ListTasks(ctx, probe); err != nil {
				return TasksListDTO{}, err
			}
		}
		switch f {
		case "problems":
			counts.Problems = n
		case "running":
			counts.Running = n
		case "stopped":
			counts.Stopped = n
		default:
			counts.All = n
		}
	}

	view, err := s.loadEstate(ctx, base.TargetFilter, sc.Vis, base.Cluster, nil)
	if err != nil {
		return TasksListDTO{}, err
	}
	now := s.now()
	summary, err := s.taskSummaryDTO(ctx, sc, base, now.Add(-taskSummaryWindow))
	if err != nil {
		return TasksListDTO{}, err
	}
	polledAt, err := s.Store.PolledAt(ctx, base.TargetFilter)
	if err != nil {
		return TasksListDTO{}, err
	}

	items := make([]TaskDTO, 0, len(rows))
	for _, row := range rows {
		items = append(items, taskDTO(sc, view, row))
	}
	var next *string
	if offset+len(rows) < total {
		next = nextCursor(limit, offset, len(rows))
	}
	return TasksListDTO{Items: items, Next: next, PolledAt: polledAt, Counts: counts, Summary: summary}, nil
}

func taskDTO(sc scope, view *estateView, row store.TaskRow) TaskDTO {
	tag := authz.EffectiveTaskTag(sc.Tags, row.Cluster, row.ServiceName)
	capacity := row.CapacityProvider
	if capacity == "" {
		capacity = row.LaunchType
	}
	dto := TaskDTO{
		ID: model.TaskID(row.Cluster.AccountID, row.Cluster.Region, row.ARN), ShortID: row.ShortID(), ARN: row.ARN,
		Cluster: ClusterRef{ID: row.Cluster.ID(), Name: row.Cluster.Cluster},
		Group:   row.Group, StartedBy: row.StartedBy, Status: row.LastStatus, ExitCode: row.ExitCode,
		Health: healthOf(row.Task), Family: row.Family, Revision: row.Revision,
		PrivateIP: row.PrivateIP, SubnetID: row.SubnetID, AZ: row.AvailabilityZone, Capacity: capacity,
		CPU: row.CPU, Memory: row.Memory, StartedAt: row.StartedAt, StoppedAt: row.StoppedAt,
		StopCode: row.StopCode, StoppedReason: row.StoppedReason, StopReasonGroup: row.StopReasonGroup,
		ExecEnabled: row.ServiceEnableExec && execAgentRunning(row.Task),
		Can:         TaskCanDTO{Operate: authz.Can(sc.Subject, authz.Operate, tag), Terminal: authz.Can(sc.Subject, authz.Terminal, tag)},
	}
	if row.ServiceName != "" {
		ref := view.serviceRef(model.ServiceKey{ClusterKey: row.Cluster, Service: row.ServiceName})
		dto.Service = &ref
	}
	return dto
}

// taskSummaryDTO aggregates the visible estate, or only the scoped service when one is given.
func (s *Server) taskSummaryDTO(ctx context.Context, sc scope, base store.TaskQuery, since time.Time) (TaskSummaryDTO, error) {
	query := store.TaskSummaryQuery{TargetFilter: base.TargetFilter, Vis: sc.Vis, Cluster: base.Cluster, Service: base.Service, Since: since}
	summary, err := s.Store.TaskSummaryFor(ctx, query)
	if err != nil {
		return TaskSummaryDTO{}, err
	}

	out := TaskSummaryDTO{
		ByStatus: TaskStatusSummaryDTO{
			RunningHealthy: summary.RunningHealthy, RunningUnhealthy: summary.RunningUnhealthy,
			StoppedError: summary.StoppedError, StoppedNormal: summary.StoppedNormal,
		},
		ByAZ: []AZCountDTO{}, ByCluster: []ClusterCapacityDTO{}, StopReasons: []StopReasonDTO{},
	}
	for az, n := range summary.ByAZ {
		out.ByAZ = append(out.ByAZ, AZCountDTO{AZ: az, Count: n})
	}
	sort.Slice(out.ByAZ, func(i, j int) bool { return out.ByAZ[i].AZ < out.ByAZ[j].AZ })
	for k, c := range summary.ByCluster {
		out.ByCluster = append(out.ByCluster, ClusterCapacityDTO{ID: k.ID(), Name: k.Cluster, Fargate: c.Fargate, Spot: c.Spot})
	}
	sort.Slice(out.ByCluster, func(i, j int) bool {
		a, b := out.ByCluster[i], out.ByCluster[j]
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.ID < b.ID
	})
	for _, group := range status.StopReasonGroups {
		if n := summary.StopReasons[group]; n > 0 {
			out.StopReasons = append(out.StopReasons, StopReasonDTO{Reason: group, Count: n})
		}
	}
	return out, nil
}
