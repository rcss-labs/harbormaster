package api

import (
	"net/http"
	"sort"
	"strconv"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

const spotProvider = "FARGATE_SPOT"

// analyticsCapacity answers GET /api/analytics/capacity. ?range= is validated but otherwise ignored: the tab
// shows the latest poll plus the last 7 days of counts history.
func (s *Server) analyticsCapacity(w http.ResponseWriter, r *http.Request) {
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
	retention := s.Cfg.Retention.CountsHistory
	win := clampWindow(Range7d, retention, s.now())
	win.requested, win.clamped = requested, false

	daily, err := s.Store.DailyDataSince(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	dailyIn := capacityDailyInputs{capacitySince: daily[store.TableClusterCapacityDaily], utilSince: daily[store.TableServiceUtilDaily]}
	dailyWin := newDailyWindow(s.now())

	var (
		facts      []store.CapacityTaskFact
		clusters   []store.ClusterRow
		atMax      []store.AtMaxRow
		subnets    []store.SubnetLow
		trend      []store.TrendPoint
		services   int
		tags       map[authz.TagID]TagDTO
		accountMap map[string]string
		polledAt   time.Time
	)
	group, gctx := errgroup.WithContext(ctx)
	group.Go(func() (err error) {
		facts, err = s.Store.AnalyticsRunningTasks(gctx, sc.Filter, sc.Vis)
		return err
	})
	group.Go(func() (err error) {
		clusters, err = s.Store.ListClusters(gctx, sc.Filter, sc.Vis)
		return err
	})
	group.Go(func() (err error) {
		atMax, err = s.Store.AnalyticsAtScalingMax(gctx, sc.Filter, sc.Vis)
		return err
	})
	group.Go(func() (err error) {
		subnets, err = s.Store.AnalyticsSubnetsLow(gctx, sc.Filter, sc.Vis)
		return err
	})
	group.Go(func() (err error) {
		trend, err = s.Store.AnalyticsReservedTrend(gctx, sc.Filter, sc.Vis, win.from, win.to, win.bucket)
		return err
	})
	group.Go(func() (err error) {
		services, err = s.Store.AnalyticsServiceCount(gctx, sc.Filter, sc.Vis)
		return err
	})
	if dailyIn.capacitySince != nil {
		group.Go(func() (err error) {
			dailyIn.capacity, err = s.Store.AnalyticsCapacityDaily(gctx, sc.Filter, sc.Vis, dailyWin.from, dailyWin.to())
			return err
		})
	}
	if dailyIn.utilSince != nil {
		group.Go(func() (err error) {
			dailyIn.util, err = s.Store.AnalyticsUtilDaily(gctx, sc.Filter, sc.Vis, dailyWin.from, dailyWin.to())
			return err
		})
		group.Go(func() (err error) {
			dailyIn.overReserved, err = s.Store.AnalyticsOverReserved(gctx, sc.Filter, sc.Vis, dailyWin.from)
			return err
		})
	}
	group.Go(func() (err error) {
		tags, err = s.tagDTOs(gctx)
		return err
	})
	group.Go(func() (err error) {
		accountMap, err = s.accountNames(gctx)
		return err
	})
	group.Go(func() (err error) {
		polledAt, err = s.Store.PolledAt(gctx, sc.Filter)
		return err
	})
	if err := group.Wait(); err != nil {
		s.fail(w, err)
		return
	}

	dto := newCapacityDTO()
	dto.Tab = "capacity"
	dto.Window = win.dto(retention, false)
	dto.PolledAt = polledAt
	dto.Sources = s.capacitySources()
	dto.KPIs.Services = services
	dto.KPIs.AtScalingMax = len(atMax)
	dto.KPIs.SubnetsLowIPs = len(subnets)
	fillCapacityTasks(&dto, facts, clusters, tags, accountMap)
	fillCapacityTrend(&dto, win, trend, tags)
	fillCapacityDaily(&dto, dailyWin, dailyIn, tags)
	dto.Sources = append(dto.Sources,
		prunedTableSource(store.TableClusterCapacityDaily,
			"Daily average reserved vCPU and memory and spot share per cluster, for the 90-day panels",
			s.Cfg.Retention.CapacityDaily, "HM_RETENTION_CAPACITY_DAILY"),
		prunedTableSource(store.TableServiceUtilDaily,
			"Daily CPU and memory use as a share of what each service reserves, for the 90-day panels",
			s.Cfg.Retention.UtilDaily, "HM_RETENTION_UTIL_DAILY"))
	for _, row := range atMax {
		dto.AtScalingMax = append(dto.AtScalingMax, AtMaxRowDTO{
			ServiceID: row.Key.ID(), Service: row.Key.Service, Cluster: row.Key.Cluster,
			Desired: row.Desired, MaxCapacity: row.MaxCapacity, AtMaxSince: row.AtMaxSince,
		})
	}
	for _, row := range subnets {
		dto.SubnetsLow = append(dto.SubnetsLow, SubnetRowDTO{
			SubnetID: row.SubnetID, Name: row.Name, AZ: row.AZ, AccountID: row.AccountID, Region: row.Region,
			AvailableIPs: row.AvailableIPs, TotalIPs: row.TotalIPs, UsedPct: row.UsedPct,
		})
	}
	writeJSON(w, http.StatusOK, dto)
}

func (s *Server) capacitySources() []AnalyticsSourceDTO {
	latest := "latest poll"
	return []AnalyticsSourceDTO{
		{Table: "tasks", Reads: "Running tasks: CPU, memory and capacity provider (task-level sizes only)", Kept: latest},
		{Table: "services", Reads: "Desired counts and subnets", Kept: latest},
		{Table: "scaling", Reads: "Auto-scaling maximum per service", Kept: latest},
		{Table: "subnets", Reads: "Free IP addresses against the CIDR size", Kept: latest},
		{Table: "service_counts_history", Reads: "Running counts for the 7-day reserved vCPU estimate and time at max",
			Kept: keptText(s.Cfg.Retention.CountsHistory), Setting: settingPtr("HM_RETENTION_COUNTS_HISTORY")},
		{Table: "task_definitions", Reads: "Each service's current task definition CPU", Kept: latest},
	}
}

// clusterTotals accumulates the running tasks of one cluster.
type clusterTotals struct {
	tasks, spot int
	vcpu, gib   float64
}

// fillCapacityTasks sets the running-task KPIs, both donuts and the clusters table.
func fillCapacityTasks(dto *CapacityDTO, facts []store.CapacityTaskFact, clusters []store.ClusterRow, tags map[authz.TagID]TagDTO, accountNames map[string]string) {
	byCluster := map[model.ClusterKey]*clusterTotals{}
	vcpuByAccount := map[string]float64{}
	tasksByProvider := map[string]int{}
	var vcpu, gib float64
	var spot int
	for _, fact := range facts {
		taskVCPU, taskGiB := reservedSize(fact.CPU, fact.Memory)
		totals := byCluster[fact.Cluster]
		if totals == nil {
			totals = &clusterTotals{}
			byCluster[fact.Cluster] = totals
		}
		totals.tasks++
		totals.vcpu += taskVCPU
		totals.gib += taskGiB
		vcpu += taskVCPU
		gib += taskGiB
		vcpuByAccount[fact.Cluster.AccountID] += taskVCPU
		provider := fact.CapacityProvider
		if provider == "" {
			provider = fact.LaunchType
		}
		if provider == "" {
			provider = "unknown"
		}
		tasksByProvider[provider]++
		if fact.CapacityProvider == spotProvider {
			totals.spot++
			spot++
		}
	}
	dto.KPIs.ReservedVCPU, dto.KPIs.ReservedMemGiB, dto.KPIs.RunningTasks = vcpu, gib, len(facts)
	if len(facts) > 0 {
		share := float64(spot) / float64(len(facts))
		dto.KPIs.SpotShare = &share
	}

	dto.VCPUByAccount = newDonutDTO()
	dto.VCPUByAccount.Total = vcpu
	for accountID, value := range vcpuByAccount {
		if value > 0 {
			label := accountNames[accountID]
			if label == "" {
				label = accountID
			}
			dto.VCPUByAccount.Slices = append(dto.VCPUByAccount.Slices, DonutSliceDTO{Key: accountID, Label: label, Value: value})
		}
	}
	dto.TasksByCapacityProvider = newDonutDTO()
	dto.TasksByCapacityProvider.Total = float64(len(facts))
	for provider, n := range tasksByProvider {
		dto.TasksByCapacityProvider.Slices = append(dto.TasksByCapacityProvider.Slices, DonutSliceDTO{Key: provider, Label: provider, Value: float64(n)})
	}
	for _, donut := range []*DonutDTO{&dto.VCPUByAccount, &dto.TasksByCapacityProvider} {
		slices := donut.Slices
		sort.Slice(slices, func(i, j int) bool {
			if slices[i].Value != slices[j].Value {
				return slices[i].Value > slices[j].Value
			}
			return slices[i].Key < slices[j].Key
		})
	}

	var rows []CapacityClusterRowDTO
	for _, cluster := range clusters {
		totals := byCluster[cluster.Key]
		if totals == nil {
			continue
		}
		row := CapacityClusterRowDTO{
			ClusterID: cluster.Key.ID(), Cluster: cluster.Key.Cluster, AccountID: cluster.Key.AccountID,
			AccountName: accountNames[cluster.Key.AccountID], Tag: inheritedTag(tags, cluster.TagID, !cluster.TagSet),
			Tasks: totals.tasks, VCPU: totals.vcpu, MemGiB: totals.gib,
			SpotShare: float64(totals.spot) / float64(totals.tasks),
		}
		if vcpu > 0 {
			row.Share = totals.vcpu / vcpu
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].VCPU != rows[j].VCPU {
			return rows[i].VCPU > rows[j].VCPU
		}
		return rows[i].ClusterID < rows[j].ClusterID
	})
	dto.Clusters.Total = len(rows)
	dto.Clusters.Rows = rows[:min(len(rows), topTableRows)]
	if dto.Clusters.Rows == nil {
		dto.Clusters.Rows = []CapacityClusterRowDTO{}
	}
}

// fillCapacityTrend sets the stacked reserved-vCPU series, one zero-filled line per effective tag.
func fillCapacityTrend(dto *CapacityDTO, win windowSpec, points []store.TrendPoint, tags map[authz.TagID]TagDTO) {
	series := newBucketedSeries(win)
	lines := map[authz.TagID]*SeriesLineDTO{}
	order := []authz.TagID{}
	for _, p := range points {
		if lines[p.TagID] == nil {
			lines[p.TagID] = &SeriesLineDTO{}
			order = append(order, p.TagID)
		}
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })
	for _, id := range order {
		line := zeroLine(win, strconv.FormatInt(int64(id), 10), tags[id].Name, false)
		*lines[id] = line
	}
	for _, p := range points {
		lines[p.TagID].add(bucketIndex(p.Bucket, win), p.VCPU)
	}
	for _, id := range order {
		series.Series = append(series.Series, *lines[id])
	}
	dto.ReservedVCPU7d = series
}
