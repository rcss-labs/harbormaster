package api

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"sort"
	"strconv"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/metrics"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/storage"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// storageMetricsConcurrency bounds how many account x region CloudWatch fetches run at once.
const storageMetricsConcurrency = 6

// metricsUnavailable is the only thing a client learns about a failed CloudWatch read.
const metricsUnavailable = "CloudWatch metrics are unavailable right now."

// efsMetrics is the part of *metrics.Client the storage handler needs; tests inject a failing one.
type efsMetrics interface {
	Fetch(ctx context.Context, t awsx.Target, span metrics.Span, qs []metrics.Query) (*metrics.Result, error)
}

// StoragePointDTO is one sample of a storage series.
type StoragePointDTO struct {
	T time.Time `json:"t"`
	V float64   `json:"v"`
}

// StorageAccessPointDTO is an EFS access point of a file system.
type StorageAccessPointDTO struct {
	AccessPointID string `json:"access_point_id"`
	Path          string `json:"path"`
	UID           *int64 `json:"uid"`
}

// StorageMountDTO is a visible service that mounts a file system.
type StorageMountDTO struct {
	ServiceID     string `json:"service_id"`
	Service       string `json:"service"`
	Cluster       string `json:"cluster"`
	ContainerPath string `json:"container_path"`
	Tasks         int    `json:"tasks"`
}

// StorageItemDTO is one EFS file system on GET /api/storage.
type StorageItemDTO struct {
	FileSystemID      string                  `json:"file_system_id"`
	Name              string                  `json:"name"`
	AccountID         string                  `json:"account_id"`
	Region            string                  `json:"region"`
	VPC               *VPCRefDTO              `json:"vpc"`
	SizeBytes         int64                   `json:"size_bytes"`
	ThroughputMode    string                  `json:"throughput_mode"`
	PerformanceMode   string                  `json:"performance_mode"`
	Mounted           bool                    `json:"mounted"`
	ClientConnections *int                    `json:"client_connections"`
	MountingTasks     int                     `json:"mounting_tasks"`
	AccessPoints      []StorageAccessPointDTO `json:"access_points"`
	MountedBy         []StorageMountDTO       `json:"mounted_by"`
	Size30d           []StoragePointDTO       `json:"size_30d"`
	GrowthPct         *float64                `json:"growth_30d_pct"`
	Throughput24h     []StoragePointDTO       `json:"throughput_24h"`
}

// StorageTotalsDTO sums the file systems listed to the caller.
type StorageTotalsDTO struct {
	SizeBytes  int64 `json:"size_bytes"`
	Mounted    int   `json:"mounted"`
	NotMounted int   `json:"not_mounted"`
}

// StorageDTO is the body of GET /api/storage.
type StorageDTO struct {
	Items        []StorageItemDTO `json:"items"`
	Totals       StorageTotalsDTO `json:"totals"`
	MetricsError string           `json:"metrics_error,omitempty"`
	PolledAt     time.Time        `json:"polled_at"`
}

// storageRoutes registers the plan 05 storage read route.
func (s *Server) storageRoutes(mux *http.ServeMux) {
	s.handle(mux, "GET /api/storage", s.listStorage, routeOptions{})
}

// mountIndexFor maps file system IDs to the caller's visible services that mount them. Only services the
// caller may View count (rule 6), so standalone tasks never make a file system visible.
func (s *Server) mountIndexFor(ctx context.Context, sc scope) (map[string][]storage.MountedBy, error) {
	rows, err := s.Store.ListServices(ctx, sc.Filter, "", sc.Vis)
	if err != nil {
		return nil, err
	}
	tasks, err := s.Store.RunningTasks(ctx, sc.Filter, sc.Vis)
	if err != nil {
		return nil, err
	}
	running := map[model.ServiceKey]int{}
	for _, task := range tasks {
		if task.ServiceName != "" {
			running[model.ServiceKey{ClusterKey: task.Cluster, Service: task.ServiceName}]++
		}
	}
	services := make([]model.Service, 0, len(rows))
	taskDefs := map[string]json.RawMessage{}
	for _, row := range rows {
		services = append(services, row.Service)
		arn := row.TaskDefinitionARN
		if _, done := taskDefs[arn]; done || arn == "" {
			continue
		}
		td, err := s.Store.GetTaskDefinition(ctx, row.Key.AccountID, row.Key.Region, arn)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		taskDefs[arn] = td.Raw
	}
	index := storage.MountIndex(services, taskDefs, running)
	for _, list := range index {
		// Busiest mount first; MountIndex already ordered ties by cluster and service name.
		sort.SliceStable(list, func(i, j int) bool { return list[i].RunningTasks > list[j].RunningTasks })
	}
	return index, nil
}

// listStorage answers GET /api/storage?account=&region=.
func (s *Server) listStorage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sc, err := s.scopeFrom(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	index, err := s.mountIndexFor(ctx, sc)
	if err != nil {
		s.fail(w, err)
		return
	}
	all, err := s.Store.ListFileSystems(ctx, sc.Filter.AccountID, sc.Filter.Region)
	if err != nil {
		s.fail(w, err)
		return
	}
	// File systems that no stored row describes never appear, even if a task definition mounts them.
	// A tag filter narrows an admin to the services it matches, the same rule non-admins get; otherwise
	// the admin would see every file system flagged as unmounted because the mount index is narrowed.
	subject := sc.Subject
	if sc.TagFilter != nil {
		subject.Admin = false
	}
	visible := storage.Visible(subject, all, index)
	// polled_at is only a timestamp, so it may come from every stored row without leaking a file system.
	var polledAt time.Time
	for _, fs := range all {
		if fs.PolledAt.After(polledAt) {
			polledAt = fs.PolledAt
		}
	}
	accessPoints, err := s.Store.ListAccessPoints(ctx, sc.Filter.AccountID, sc.Filter.Region)
	if err != nil {
		s.fail(w, err)
		return
	}
	apsByFS := map[string][]StorageAccessPointDTO{}
	for _, ap := range accessPoints {
		apsByFS[ap.FileSystemID] = append(apsByFS[ap.FileSystemID],
			StorageAccessPointDTO{AccessPointID: ap.AccessPointID, Path: ap.RootPath, UID: ap.PosixUID})
	}
	vpcNames, err := s.vpcNames(ctx, visible)
	if err != nil {
		s.fail(w, err)
		return
	}

	out := StorageDTO{Items: make([]StorageItemDTO, 0, len(visible)), PolledAt: polledAt}
	byTarget := map[awsTarget][]int{} // item positions per account x region
	for _, fs := range visible {
		item := StorageItemDTO{
			FileSystemID: fs.FileSystemID, Name: fs.Name, AccountID: fs.AccountID, Region: fs.Region,
			SizeBytes: fs.SizeBytes, ThroughputMode: fs.ThroughputMode, PerformanceMode: fs.PerformanceMode,
			AccessPoints: apsByFS[fs.FileSystemID], MountedBy: []StorageMountDTO{},
		}
		if item.AccessPoints == nil {
			item.AccessPoints = []StorageAccessPointDTO{}
		}
		if fs.VPCID != "" {
			item.VPC = &VPCRefDTO{VPCID: fs.VPCID, Name: vpcNames[awsTarget{fs.AccountID, fs.Region}][fs.VPCID]}
		}
		for _, mount := range index[fs.FileSystemID] {
			item.MountedBy = append(item.MountedBy, StorageMountDTO{
				ServiceID: mount.ServiceID, Service: mount.ServiceName, Cluster: mount.Cluster,
				ContainerPath: mount.ContainerPath, Tasks: mount.RunningTasks,
			})
			item.MountingTasks += mount.RunningTasks
		}
		item.Mounted = len(item.MountedBy) > 0
		if item.Mounted {
			out.Totals.Mounted++
		} else {
			out.Totals.NotMounted++
		}
		out.Totals.SizeBytes += fs.SizeBytes
		target := awsTarget{fs.AccountID, fs.Region}
		byTarget[target] = append(byTarget[target], len(out.Items))
		out.Items = append(out.Items, item)
	}

	// Targets are fetched concurrently (each makes three CloudWatch reads); every goroutine writes only the
	// items of its own target, so the result order is the fixed item order whatever finishes first.
	targets := make([]awsTarget, 0, len(byTarget))
	for target := range byTarget {
		targets = append(targets, target)
	}
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].accountID != targets[j].accountID {
			return targets[i].accountID < targets[j].accountID
		}
		return targets[i].region < targets[j].region
	})
	var metricsFailed atomic.Bool
	var group errgroup.Group
	group.SetLimit(storageMetricsConcurrency)
	for _, target := range targets {
		positions := byTarget[target]
		group.Go(func() error {
			ids := make([]string, len(positions))
			for i, pos := range positions {
				ids[i] = out.Items[pos].FileSystemID
			}
			series, err := s.fetchEFSSeries(ctx, awsx.Target{AccountID: target.accountID, Region: target.region}, ids)
			if err != nil {
				s.log().Warn("storage metrics", "account", target.accountID, "region", target.region, "error", err)
				metricsFailed.Store(true)
				return nil // one target's failure must not cancel the others
			}
			for i, pos := range positions {
				item := &out.Items[pos]
				item.Size30d, item.Throughput24h = series[i].size, series[i].throughput
				item.GrowthPct, item.ClientConnections = series[i].growth, series[i].connections
			}
			return nil
		})
	}
	_ = group.Wait() // the goroutines never return an error
	if metricsFailed.Load() {
		out.MetricsError = metricsUnavailable
	}
	writeJSON(w, http.StatusOK, out)
}

// vpcNames resolves VPC names for the regions of the listed file systems.
func (s *Server) vpcNames(ctx context.Context, fileSystems []model.FileSystem) (map[awsTarget]map[string]string, error) {
	names := map[awsTarget]map[string]string{}
	for _, fs := range fileSystems {
		target := awsTarget{fs.AccountID, fs.Region}
		if _, done := names[target]; done {
			continue
		}
		vpcs, err := s.Store.ListVPCs(ctx, fs.AccountID, fs.Region)
		if err != nil {
			return nil, err
		}
		names[target] = map[string]string{}
		for _, vpc := range vpcs {
			names[target][vpc.VPCID] = vpc.Name
		}
	}
	return names, nil
}

// efsSeries is the CloudWatch-derived part of one storage item.
type efsSeries struct {
	size, throughput []StoragePointDTO
	growth           *float64
	connections      *int
}

// fetchEFSSeries reads size, throughput and connections for fsIDs of one account x region. Any failed
// read fails the whole group so a page never mixes live and missing series. A stale result is used as is.
func (s *Server) fetchEFSSeries(ctx context.Context, target awsx.Target, fsIDs []string) ([]efsSeries, error) {
	size, err := s.efsFetcher.Fetch(ctx, target, metrics.EFSSizeSpan, metrics.EFSStorageQueries(fsIDs))
	if err != nil {
		return nil, err
	}
	throughput, err := s.efsFetcher.Fetch(ctx, target, metrics.EFSThroughputSpan, metrics.EFSThroughputQueries(fsIDs))
	if err != nil {
		return nil, err
	}
	connections, err := s.efsFetcher.Fetch(ctx, target, metrics.EFSConnectionSpan, metrics.EFSConnectionQueries(fsIDs))
	if err != nil {
		return nil, err
	}
	out := make([]efsSeries, len(fsIDs))
	for i := range fsIDs {
		n := strconv.Itoa(i) // query IDs are index based, see metrics.efsQueries
		sizePoints := size.Points("sz_" + n)
		out[i].size = storagePoints(sizePoints, func(v float64) float64 { return v })
		out[i].throughput = storagePoints(throughput.Points("io_"+n), func(v float64) float64 {
			return metrics.BytesPerPeriodToMiBps(v, throughput.Period)
		})
		if pct, ok := storage.Growth(sizePoints); ok {
			out[i].growth = &pct
		}
		out[i].connections = latestCount(connections.Points("cc_" + n))
	}
	return out, nil
}

// storagePoints drops empty buckets and converts the rest; the result is never nil.
func storagePoints(points []metrics.Point, convert func(float64) float64) []StoragePointDTO {
	out := []StoragePointDTO{}
	for _, p := range points {
		if p.V != nil {
			out = append(out, StoragePointDTO{T: p.T, V: convert(*p.V)})
		}
	}
	return out
}

// latestCount is the newest non-empty point rounded to a whole number, or nil.
func latestCount(points []metrics.Point) *int {
	for i := len(points) - 1; i >= 0; i-- {
		if points[i].V != nil {
			n := int(math.Round(*points[i].V))
			return &n
		}
	}
	return nil
}
