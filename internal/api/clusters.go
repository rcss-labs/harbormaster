package api

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/status"
	"github.com/rcss-labs/harbormaster/internal/store"
)

const (
	historyWindow   = 72 * time.Hour
	stopsHours      = 24
	deploymentsDays = 14
)

// estateView is the visible part of the polled estate with statuses already derived.
// It is the shared input of the overview, cluster list and cluster detail handlers.
type estateView struct {
	clusters          []store.ClusterRow
	services          []store.ServiceRow
	runningTasks      []store.TaskRow // all running tasks, standalone included
	tasksByService    map[model.ServiceKey][]model.Task
	statusByService   map[model.ServiceKey]model.Status
	servicesByCluster map[model.ClusterKey][]store.ServiceRow
	tasksByCluster    map[model.ClusterKey][]store.TaskRow
}

// loadEstate reads the services and running tasks visible under vis (clusterName narrows services) and derives service statuses.
func (s *Server) loadEstate(ctx context.Context, f store.TargetFilter, vis store.Visibility, clusterName string, clusters []store.ClusterRow) (*estateView, error) {
	services, err := s.Store.ListServices(ctx, f, clusterName, vis)
	if err != nil {
		return nil, err
	}
	tasks, err := s.Store.RunningTasks(ctx, f, vis)
	if err != nil {
		return nil, err
	}
	view := &estateView{
		clusters: clusters, services: services, runningTasks: tasks,
		tasksByService:    map[model.ServiceKey][]model.Task{},
		statusByService:   map[model.ServiceKey]model.Status{},
		servicesByCluster: map[model.ClusterKey][]store.ServiceRow{},
		tasksByCluster:    map[model.ClusterKey][]store.TaskRow{},
	}
	for _, t := range tasks {
		view.tasksByCluster[t.Cluster] = append(view.tasksByCluster[t.Cluster], t)
		if t.ServiceName == "" {
			continue
		}
		key := model.ServiceKey{ClusterKey: t.Cluster, Service: t.ServiceName}
		view.tasksByService[key] = append(view.tasksByService[key], t.Task)
	}
	for _, svc := range services {
		view.statusByService[svc.Key] = status.Service(status.Inputs{Service: svc.Service, RunningTasks: view.tasksByService[svc.Key]})
		view.servicesByCluster[svc.Key.ClusterKey] = append(view.servicesByCluster[svc.Key.ClusterKey], svc)
	}
	return view, nil
}

func (v *estateView) clusterStatus(k model.ClusterKey) model.ClusterStatus {
	statuses := make([]model.Status, 0, len(v.servicesByCluster[k]))
	for _, svc := range v.servicesByCluster[k] {
		statuses = append(statuses, v.statusByService[svc.Key])
	}
	return status.Cluster(statuses)
}

func (v *estateView) serviceRef(k model.ServiceKey) ServiceRef {
	return ServiceRef{ID: k.ID(), Name: k.Service, Status: v.statusByService[k]}
}

// rollingOutRevision returns the revision being rolled out: the PRIMARY deployment's revision
// while it is IN_PROGRESS and an older deployment still exists.
func rollingOutRevision(svc model.Service) (int, bool) {
	if len(svc.Deployments) < 2 {
		return 0, false
	}
	for _, d := range svc.Deployments {
		if d.Status == "PRIMARY" && d.RolloutState == "IN_PROGRESS" {
			return d.Revision, true
		}
	}
	return 0, false
}

func isRollingOut(svc model.Service, t model.Task) bool {
	rev, ok := rollingOutRevision(svc)
	return ok && t.Revision == rev
}

// tagDTOs maps tag ids to their DTO (Inherited unset).
func (s *Server) tagDTOs(ctx context.Context) (map[authz.TagID]TagDTO, error) {
	tags, err := s.Store.ListTags(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[authz.TagID]TagDTO, len(tags))
	for _, t := range tags {
		out[t.ID] = TagDTO{ID: int64(t.ID), Name: t.Name, Color: t.Color, Builtin: t.Builtin}
	}
	return out, nil
}

func inheritedTag(tags map[authz.TagID]TagDTO, id authz.TagID, inherited bool) TagDTO {
	tag := tags[id]
	tag.Inherited = inherited
	return tag
}

// reserved sums vCPU and GiB over tasks and counts the spot ones; unparsable sizes count as zero.
func reserved(tasks []store.TaskRow) (vcpu, gib float64, spot int) {
	for _, t := range tasks {
		cpu, _ := strconv.Atoi(t.CPU)
		mem, _ := strconv.Atoi(t.Memory)
		vcpu += float64(cpu) / 1024
		gib += float64(mem) / 1024
		if t.CapacityProvider == "FARGATE_SPOT" {
			spot++
		}
	}
	return vcpu, gib, spot
}

var clusterStatusRank = map[model.ClusterStatus]int{
	model.ClusterNeedsAttention: 0, model.ClusterDegraded: 1, model.ClusterDeploying: 2, model.ClusterHealthy: 3,
}

func (s *Server) clusterCard(row store.ClusterRow, view *estateView, tags map[authz.TagID]TagDTO, accountNames map[string]string, lastChange map[model.ClusterKey]time.Time) ClusterCardDTO {
	card := ClusterCardDTO{
		ID: row.Key.ID(), Name: row.Key.Cluster,
		Tag:       inheritedTag(tags, row.TagID, !row.TagSet),
		Status:    view.clusterStatus(row.Key),
		AccountID: row.Key.AccountID, AccountName: accountNames[row.Key.AccountID], Region: row.Key.Region,
		Services:          []ServiceRef{},
		ContainerInsights: row.ContainerInsights,
	}
	for _, svc := range view.servicesByCluster[row.Key] {
		card.Services = append(card.Services, view.serviceRef(svc.Key))
		card.Running += svc.Running
		card.Desired += svc.Desired
	}
	card.ReservedVCPU, card.ReservedGiB, card.SpotTasks = reserved(view.tasksByCluster[row.Key])
	if at, ok := lastChange[row.Key]; ok {
		card.LastChangeAt = &at
	}
	return card
}

func (s *Server) accountNames(ctx context.Context) (map[string]string, error) {
	accounts, err := s.Store.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}
	names := make(map[string]string, len(accounts))
	for _, a := range accounts {
		names[a.AccountID] = a.Name
	}
	return names, nil
}

// listClusters answers GET /api/clusters?account=&region=&limit=&cursor=.
func (s *Server) listClusters(w http.ResponseWriter, r *http.Request) {
	sc, err := s.scopeFrom(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	limit, offset, err := pageFrom(r, s.Cfg)
	if err != nil {
		s.fail(w, err)
		return
	}
	ctx := r.Context()
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
	tags, err := s.tagDTOs(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	names, err := s.accountNames(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	lastChange, err := s.Store.LastEventAtByCluster(ctx, sc.Filter, sc.Vis)
	if err != nil {
		s.fail(w, err)
		return
	}
	polledAt, err := s.Store.PolledAt(ctx, sc.Filter)
	if err != nil {
		s.fail(w, err)
		return
	}

	cards := make([]ClusterCardDTO, 0, len(rows))
	for _, row := range rows {
		cards = append(cards, s.clusterCard(row, view, tags, names, lastChange))
	}
	sort.SliceStable(cards, func(i, j int) bool {
		a, b := cards[i], cards[j]
		if clusterStatusRank[a.Status] != clusterStatusRank[b.Status] {
			return clusterStatusRank[a.Status] < clusterStatusRank[b.Status]
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.ID < b.ID
	})
	if offset > len(cards) {
		offset = len(cards)
	}
	end := min(offset+limit, len(cards))
	page := cards[offset:end]
	writeJSON(w, http.StatusOK, ListResponse[ClusterCardDTO]{Items: page, Next: nextCursor(limit, offset, len(page)), PolledAt: polledAt})
}

// getCluster answers GET /api/clusters/{clusterId}; invisible, unknown and malformed ids are all 404.
func (s *Server) getCluster(w http.ResponseWriter, r *http.Request) {
	key, err := model.ParseClusterID(r.PathValue("clusterId"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Not found.")
		return
	}
	sc, err := s.scopeFrom(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	ctx := r.Context()
	row, err := s.Store.GetCluster(ctx, key, sc.Vis)
	if err != nil {
		s.fail(w, err)
		return
	}
	filter := store.TargetFilter{AccountID: key.AccountID, Region: key.Region}
	view, err := s.loadEstate(ctx, filter, sc.Vis, key.Cluster, []store.ClusterRow{row})
	if err != nil {
		s.fail(w, err)
		return
	}
	tags, err := s.tagDTOs(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	names, err := s.accountNames(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	lastChange, err := s.Store.LastEventAtByCluster(ctx, filter, sc.Vis)
	if err != nil {
		s.fail(w, err)
		return
	}
	polledAt, err := s.Store.PolledAt(ctx, filter)
	if err != nil {
		s.fail(w, err)
		return
	}
	now := s.now()
	history, err := s.Store.CountsHistory(ctx, filter, key.Cluster, "", sc.Vis, now.Add(-historyWindow))
	if err != nil {
		s.fail(w, err)
		return
	}
	stops, err := s.Store.StopsPerHour(ctx, filter, key.Cluster, sc.Vis, stopsWindowStart(now))
	if err != nil {
		s.fail(w, err)
		return
	}

	dto := ClusterDTO{
		ClusterCardDTO:  s.clusterCard(row, view, tags, names, lastChange),
		ARN:             row.ARN,
		ConsoleURL:      "https://" + key.Region + ".console.aws.amazon.com/ecs/v2/clusters/" + key.Cluster,
		TasksHistory72h: pointsOf(history),
		StopsPerHour:    fillStopBuckets(stops, now),
		ServicesDetail:  []ClusterServiceDTO{},
		PolledAt:        polledAt,
	}
	_, _, spot := reserved(view.tasksByCluster[key])
	dto.FargateTasks = len(view.tasksByCluster[key]) - spot

	definitions := map[string]model.TaskDefinitionRef{}
	definition := func(arn string) model.TaskDefinitionRef {
		if td, ok := definitions[arn]; ok {
			return td
		}
		td, err := s.Store.GetTaskDefinition(ctx, key.AccountID, key.Region, arn)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			s.log().Warn("get task definition", "arn", arn, "error", err)
		}
		definitions[arn] = td
		return td
	}
	for _, svc := range view.servicesByCluster[key] {
		td := definition(svc.TaskDefinitionARN)
		detail := ClusterServiceDTO{
			ID: svc.Key.ID(), Name: svc.Key.Service,
			Tag:    inheritedTag(tags, svc.EffectiveTagID, svc.TagInherited),
			Status: view.statusByService[svc.Key],
			Family: svc.Family, Revision: svc.Revision, ImageTag: td.ImageTag(svc.Key.Service),
			Running: svc.Running, Desired: svc.Desired, CPU: td.CPU, Memory: td.Memory,
			Slots: []SlotDTO{},
		}
		for _, t := range view.tasksByService[svc.Key] {
			health := t.HealthStatus
			if health == "" {
				health = "UNKNOWN"
			}
			detail.Slots = append(detail.Slots, SlotDTO{
				TaskID: model.TaskID(t.Cluster.AccountID, t.Cluster.Region, t.ARN), ShortID: t.ShortID(),
				PrivateIP: t.PrivateIP, Health: health, RollingOut: isRollingOut(svc.Service, t),
			})
		}
		events, err := s.Store.ServiceEvents(ctx, svc.Key, 1, 0)
		if err != nil {
			s.fail(w, err)
			return
		}
		if len(events) > 0 {
			at := events[0].CreatedAt
			detail.LastChangeAt = &at
		}
		dto.ServicesDetail = append(dto.ServicesDetail, detail)
	}
	writeJSON(w, http.StatusOK, dto)
}

func pointsOf(history []store.CountsPoint) []PointDTO {
	points := make([]PointDTO, 0, len(history))
	for _, p := range history {
		points = append(points, PointDTO{T: p.TS, Running: p.Running, Desired: p.Desired})
	}
	return points
}

// stopsWindowStart is the first hour of the 24 hourly buckets that end at the current hour.
func stopsWindowStart(now time.Time) time.Time {
	return now.UTC().Truncate(time.Hour).Add(-(stopsHours - 1) * time.Hour)
}

// fillStopBuckets returns 24 hourly buckets ending at the current hour, zero where nothing stopped.
func fillStopBuckets(stops []store.StopBucket, now time.Time) []StopBucketDTO {
	byHour := make(map[int64]store.StopBucket, len(stops))
	for _, b := range stops {
		byHour[b.Hour.UTC().Truncate(time.Hour).Unix()] = b
	}
	start := stopsWindowStart(now)
	out := make([]StopBucketDTO, 0, stopsHours)
	for i := 0; i < stopsHours; i++ {
		hour := start.Add(time.Duration(i) * time.Hour)
		b := byHour[hour.Unix()]
		out = append(out, StopBucketDTO{Hour: hour, Normal: b.Normal, Error: b.Error})
	}
	return out
}
