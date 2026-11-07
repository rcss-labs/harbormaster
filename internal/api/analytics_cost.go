package api

import (
	"net/http"
	"sort"
	"strconv"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/cost"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

const defaultArchitecture = "X86_64"

// analyticsCost answers GET /api/analytics/cost. ?range= is validated but otherwise ignored. Spend is summed
// from the caller's visible running tasks and services, so totals never include what the caller cannot see; the
// cluster_capacity_daily rollup covers whole clusters and is deliberately not used.
func (s *Server) analyticsCost(w http.ResponseWriter, r *http.Request) {
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
	retention := s.Cfg.Retention.UtilDaily
	win := clampWindow(Range90d, retention, s.now())
	win.requested, win.clamped = requested, false
	history := newDailyWindow(s.now())

	var (
		tasks      []store.CapacityTaskFact
		facts      []store.CostServiceFact
		tags       map[authz.TagID]TagDTO
		accountMap map[string]string
		polledAt   time.Time
	)
	group, gctx := errgroup.WithContext(ctx)
	group.Go(func() (err error) {
		tasks, err = s.Store.AnalyticsRunningTasks(gctx, sc.Filter, sc.Vis)
		return err
	})
	group.Go(func() (err error) {
		facts, err = s.Store.AnalyticsCostServices(gctx, sc.Filter, sc.Vis, history.from)
		return err
	})
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

	spend := tallyServiceSpend(tasks, facts)
	dto := CostDTO{
		Tab:        "cost",
		Window:     win.dto(retention, false),
		PolledAt:   polledAt,
		Sources:    s.costSources(),
		PricesAsOf: cost.PricesAsOf,
		Rightsizing: CostRightsizingDTO{
			State:              s.rightsizingState(),
			MinHistoryDays:     cost.MinHistoryDays,
			DefaultHeadroomPct: cost.DefaultHeadroomPct,
			Services:           []CostServiceDTO{},
		},
	}
	today := history.from.AddDate(0, 0, dailyPanelDays-1)
	byCluster, byAccount, byTag := groupTotals{}, groupTotals{}, groupTotals{}
	unpricedRegions := map[string]bool{}
	for _, fact := range facts {
		totals := spend[fact.Key]
		if totals == nil {
			totals = &serviceSpend{}
		}
		dto.Rightsizing.Services = append(dto.Rightsizing.Services, s.costServiceDTO(sc, fact, totals, tags, today))

		dto.KPIs.MonthlyTotal += totals.monthly
		dto.KPIs.SpotMonthly += totals.spotMonthly
		dto.KPIs.PricedTasks += totals.priced
		dto.KPIs.SpotTasks += totals.spot
		dto.KPIs.UnpricedTasks += totals.unpriced
		if totals.noRateTasks > 0 {
			unpricedRegions[fact.Key.Region] = true
		}
		if totals.tasks() == 0 {
			continue
		}
		accountLabel := accountMap[fact.Key.AccountID]
		if accountLabel == "" {
			accountLabel = fact.Key.AccountID
		}
		byCluster.add(fact.Key.ClusterKey.ID(), fact.Key.Cluster, totals)
		byAccount.add(fact.Key.AccountID, accountLabel, totals)
		byTag.add(strconv.FormatInt(int64(fact.TagID), 10), tags[fact.TagID].Name, totals)
	}
	dto.KPIs.UnpricedRegions = sortedKeys(unpricedRegions)
	dto.ByCluster, dto.ByAccount, dto.ByTag = byCluster.sorted(), byAccount.sorted(), byTag.sorted()
	writeJSON(w, http.StatusOK, dto)
}

func (s *Server) costSources() []AnalyticsSourceDTO {
	latest := "latest poll"
	return []AnalyticsSourceDTO{
		{Table: "tasks", Reads: "Running tasks: CPU, memory and capacity provider of each task, priced at list rates", Kept: latest},
		{Table: "services", Reads: "The services you can see and their current task definition", Kept: latest},
		{Table: "task_definitions", Reads: "Task definition size, CPU architecture and container minimums", Kept: latest},
		prunedTableSource("service_util_daily",
			"Daily CPU and memory use as a share of what each service reserves, for the rightsizing suggestions",
			s.Cfg.Retention.UtilDaily, "HM_RETENTION_UTIL_DAILY"),
	}
}

// rightsizingState says whether suggestions can exist at all: the utilisation rollup must run and be kept long
// enough to hold the minimum history.
func (s *Server) rightsizingState() string {
	retention := s.Cfg.Retention.UtilDaily
	switch {
	case !s.Cfg.Analytics.Util:
		return "rollup_off"
	case retention > 0 && retention < cost.MinHistoryDays*24*time.Hour:
		return "retention_short"
	}
	return "ok"
}

// serviceSpend is what one service's running tasks cost. A Fargate task in a region without a rate is unpriced
// like any other, but is counted apart (noRateTasks) because it says "no price" rather than "not Fargate".
type serviceSpend struct {
	monthly, spotMonthly float64
	priced, spot         int
	unpriced             int // all tasks without a price
	notFargate           int // unpriced because they are not Fargate tasks with a numeric size
	noRateTasks          int // Fargate tasks in a region or architecture without a rate
}

func (t *serviceSpend) tasks() int { return t.priced + t.unpriced }

// tallyServiceSpend prices each running task of a visible service at its own size, so a rollout between two
// task definition sizes is correct. Standalone tasks (no service) are skipped: how long they run is unknown, so a
// month of hours would overstate them. Tasks whose service is not among the visible facts are skipped too.
func tallyServiceSpend(tasks []store.CapacityTaskFact, facts []store.CostServiceFact) map[model.ServiceKey]*serviceSpend {
	architectures := make(map[model.ServiceKey]string, len(facts))
	for _, fact := range facts {
		architectures[fact.Key] = fact.Architecture
	}
	out := map[model.ServiceKey]*serviceSpend{}
	for _, task := range tasks {
		if task.ServiceName == "" {
			continue
		}
		key := model.ServiceKey{ClusterKey: task.Cluster, Service: task.ServiceName}
		architecture, visible := architectures[key]
		if !visible {
			continue
		}
		totals := out[key]
		if totals == nil {
			totals = &serviceSpend{}
			out[key] = totals
		}
		class := cost.Classify(task.CapacityProvider, task.LaunchType, task.CPU, task.Memory)
		if class == cost.Unpriced {
			totals.unpriced++
			totals.notFargate++
			continue
		}
		rate, ok := cost.RateFor(task.Cluster.Region, architecture)
		if !ok {
			totals.unpriced++
			totals.noRateTasks++
			continue
		}
		cpu, _ := strconv.Atoi(task.CPU)
		memory, _ := strconv.Atoi(task.Memory)
		price := rate.Monthly(cpu, memory)
		totals.monthly += price
		totals.priced++
		if class == cost.Spot {
			totals.spot++
			totals.spotMonthly += price
		}
	}
	return out
}

// costServiceDTO evaluates one service. Services whose container minimums are unknown never get a suggestion,
// because a size below the containers' own CPU and memory would fail to deploy.
func (s *Server) costServiceDTO(sc scope, fact store.CostServiceFact, totals *serviceSpend, tags map[authz.TagID]TagDTO, today time.Time) CostServiceDTO {
	input := cost.Input{
		Region: fact.Key.Region, Architecture: fact.Architecture, OSFamily: fact.OSFamily,
		RequiresFargate: fact.RequiresFargate,
		CPUUnits:        valueOrZeroInt(fact.CPUUnits), MemoryMiB: valueOrZeroInt(fact.MemoryMiB),
		OnDemandTasks: totals.priced - totals.spot, SpotTasks: totals.spot,
		UnpricedTasks: totals.notFargate,
		UsedCPUUnits:  fact.UsedCPUUnits, UsedMemMiB: fact.UsedMemMiB,
		HistoryDays: fact.HistoryDays, LatestDay: fact.LatestDay,
		RecentOOM: fact.RecentOOM, RollupEnabled: s.Cfg.Analytics.Util, Today: today,
	}
	floorsKnown := false
	if fact.ContainerDefinitions != nil {
		if floors, err := cost.ContainerFloors(fact.ContainerDefinitions); err != nil {
			s.Log.Warn("cost: cannot read container minimums", "service", fact.Key.ID(), "err", err)
		} else {
			input.Floors, floorsKnown = floors, true
		}
	}
	status, steps := cost.Evaluate(input)
	if status == cost.StatusOK && !floorsKnown {
		status, steps = cost.StatusNoSize, nil
	}

	architecture := fact.Architecture
	if architecture == "" {
		architecture = defaultArchitecture
	}
	dto := CostServiceDTO{
		ServiceID: fact.Key.ID(), Service: fact.Key.Service, Cluster: fact.Key.Cluster, Region: fact.Key.Region,
		Architecture: architecture, Tag: inheritedTag(tags, fact.TagID, fact.TagInherited),
		Tasks: totals.tasks(), SpotTasks: totals.spot, CPUUnits: fact.CPUUnits, MemoryMiB: fact.MemoryMiB,
		CPUUsedShare: usedShare(fact.UsedCPUUnits, fact.CPUUnits), MemUsedShare: usedShare(fact.UsedMemMiB, fact.MemoryMiB),
		Monthly: totals.monthly, Status: string(status), HistoryDays: fact.HistoryDays,
		AutoScaled: fact.AutoScaled, RecentOOM: fact.RecentOOM,
		CanEdit: authz.Can(sc.Subject, authz.Edit, fact.TagID),
		Steps:   make([]CostStepDTO, 0, len(steps)),
	}
	for _, step := range steps {
		out := CostStepDTO{FromHeadroomPct: step.FromHeadroomPct}
		if step.Suggestion != nil {
			out.Suggestion = &CostSuggestionDTO{
				CPUUnits: step.Suggestion.CPUUnits, MemoryMiB: step.Suggestion.MemoryMiB,
				SavingMonthly: step.Suggestion.SavingMonthly, LimitedByContainers: step.Suggestion.LimitedByContainers,
			}
		}
		dto.Steps = append(dto.Steps, out)
	}
	return dto
}

// usedShare is use as a 0..1 share of the current size; nil when either is unknown or the size is zero.
func usedShare(used *float64, size *int) *float64 {
	if used == nil || size == nil || *size <= 0 {
		return nil
	}
	share := *used / float64(*size)
	return &share
}

func valueOrZeroInt(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

type groupTotals map[string]*CostGroupDTO

// add accumulates one service's spend into the group with the given key.
func (g groupTotals) add(key, label string, spend *serviceSpend) {
	group := g[key]
	if group == nil {
		group = &CostGroupDTO{Key: key, Label: label}
		g[key] = group
	}
	group.Monthly += spend.monthly
	group.SpotMonthly += spend.spotMonthly
	group.UnpricedTasks += spend.unpriced
}

// sorted returns the groups by monthly spend descending, then key; never nil.
func (g groupTotals) sorted() []CostGroupDTO {
	out := make([]CostGroupDTO, 0, len(g))
	for _, group := range g {
		out = append(out, *group)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Monthly != out[j].Monthly {
			return out[i].Monthly > out[j].Monthly
		}
		return out[i].Key < out[j].Key
	})
	return out
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}
