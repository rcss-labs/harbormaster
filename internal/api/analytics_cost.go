package api

import (
	"hash/fnv"
	"maps"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/cost"
	"github.com/rcss-labs/harbormaster/internal/deploy"
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
		facts, err = s.Store.AnalyticsCostServices(gctx, sc.Filter, sc.Vis, history.from, s.now().AddDate(0, 0, -cost.SpotLookbackDays))
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
		PricesAsOf: cost.PricesAsOf, SpotPricesAsOf: cost.SpotPricesAsOf,
		Rightsizing: CostRightsizingDTO{
			State:              s.rightsizingState(),
			MinHistoryDays:     cost.MinHistoryDays,
			DefaultHeadroomPct: cost.DefaultHeadroomPct,
			Services:           []CostServiceDTO{},
		},
		ARM64: CostARM64DTO{CheckEnabled: s.Cfg.ARM64.Check, Services: []CostARM64ServiceDTO{}},
		Spot:  CostSpotDTO{LookbackDays: cost.SpotLookbackDays, MinTasks: cost.SpotMinTasks, Services: []CostSpotServiceDTO{}},
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
		dto.Spot.Services = append(dto.Spot.Services, spotServiceDTO(sc, fact, totals, tags))

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
	sortSpotServices(dto.Spot.Services)
	arm64, err := s.arm64Section(ctx, facts, spend, tags, accountMap, sc.Subject)
	if err != nil {
		s.fail(w, err)
		return
	}
	dto.ARM64 = arm64
	dto.KPIs.UnpricedRegions = slices.Sorted(maps.Keys(unpricedRegions))
	dto.ByCluster, dto.ByAccount, dto.ByTag = byCluster.sorted(), byAccount.sorted(), byTag.sorted()
	writeJSON(w, http.StatusOK, dto)
}

func (s *Server) costSources() []AnalyticsSourceDTO {
	latest := "latest poll"
	return []AnalyticsSourceDTO{
		{Table: "tasks", Reads: "Running tasks: CPU, memory and capacity provider of each task, priced at list rates (on demand) or Spot prices", Kept: latest},
		{Table: "services", Reads: "The services you can see and their current task definition", Kept: latest},
		{Table: "task_definitions", Reads: "Task definition size, CPU architecture and container minimums", Kept: latest},
		prunedTableSource("service_util_daily",
			"Daily CPU and memory use as a share of what each service reserves, for the rightsizing suggestions",
			s.Cfg.Retention.UtilDaily, "HM_RETENTION_UTIL_DAILY"),
		{Table: "image_refs", Reads: "Which CPU architectures the images of x86 Fargate services support, read in the background from ECR and three public registries, for the ARM64 suggestions", Kept: "7 days"},
		prunedTableSource("operation_log",
			"Terminal sessions opened through Harbormaster in the last 7 days, for the Fargate Spot suggestions. Sessions opened with the AWS CLI are not recorded",
			s.Cfg.Retention.OperationLog, "HM_RETENTION_OPERATION_LOG"),
		prunedTableSource("tasks",
			"Stopped tasks of the last 7 days: deployment and scale-in stops that ended in exit 137 say a service may not stop cleanly on SIGTERM",
			s.Cfg.Retention.StoppedTasks, "HM_RETENTION_STOPPED_TASKS"),
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
	unpriced             int               // all tasks without a price
	notFargate           int               // unpriced because they are not Fargate tasks with a numeric size
	noRateTasks          int               // Fargate tasks in a region or architecture without a rate
	taskList             []cost.PricedTask // the priced running tasks, for the ARM64 saving
}

func (t *serviceSpend) tasks() int { return t.priced + t.unpriced }

// tallyServiceSpend prices each running task of a visible service at its own size, so a rollout between two
// task definition sizes is correct. Windows tasks are unpriced because the table holds Linux rates only. Standalone tasks (no service) are skipped: how long they run is unknown, so a
// month of hours would overstate them. Tasks whose service is not among the visible facts are skipped too.
func tallyServiceSpend(tasks []store.CapacityTaskFact, facts []store.CostServiceFact) map[model.ServiceKey]*serviceSpend {
	type platform struct {
		architecture string
		windows      bool
	}
	platforms := make(map[model.ServiceKey]platform, len(facts))
	for _, fact := range facts {
		platforms[fact.Key] = platform{architecture: fact.Architecture, windows: cost.IsWindows(fact.OSFamily)}
	}
	out := map[model.ServiceKey]*serviceSpend{}
	for _, task := range tasks {
		if task.ServiceName == "" {
			continue
		}
		key := model.ServiceKey{ClusterKey: task.Cluster, Service: task.ServiceName}
		service, visible := platforms[key]
		if !visible {
			continue
		}
		totals := out[key]
		if totals == nil {
			totals = &serviceSpend{}
			out[key] = totals
		}
		class := cost.Classify(task.CapacityProvider, task.LaunchType, task.CPU, task.Memory)
		if class == cost.Unpriced || service.windows {
			totals.unpriced++
			totals.notFargate++
			continue
		}
		rate, ok := cost.RateForClass(task.Cluster.Region, service.architecture, class)
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
		totals.taskList = append(totals.taskList, cost.PricedTask{Class: class, CPUUnits: cpu, MemoryMiB: memory})
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
			s.warnUnreadableFloors(fact, err)
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

// warnUnreadableFloors logs an unparsable container definition once per service and definition per process; the
// endpoint is polled, so logging on every request would repeat the same line. The contents are never logged.
func (s *Server) warnUnreadableFloors(fact store.CostServiceFact, err error) {
	hash := fnv.New64a()
	hash.Write(fact.ContainerDefinitions) //nolint:errcheck // hash writes never fail
	key := fact.Key.ID() + "/" + strconv.FormatUint(hash.Sum64(), 16)
	if s.costWarned != nil {
		if !s.costWarned.add(key) {
			return
		}
	}
	s.Log.Warn("cost: cannot read container minimums", "service", fact.Key.ID(), "err", err)
}

// spotServiceDTO evaluates one service for Fargate Spot from the polled facts. Nothing here calls AWS.
func spotServiceDTO(sc scope, fact store.CostServiceFact, totals *serviceSpend, tags map[authz.TagID]TagDTO) CostSpotServiceDTO {
	result := cost.EvaluateSpot(cost.SpotInput{
		Region: fact.Key.Region, Architecture: fact.Architecture, OSFamily: fact.OSFamily,
		RequiresFargate: fact.RequiresFargate, ControllerSupported: fact.Controller == "ECS",
		Strategy: fact.StrategyProviders, LaunchType: fact.LaunchType,
		Desired: fact.Desired, OnDemandTasks: totals.priced - totals.spot, SpotTasks: totals.spot, UnpricedTasks: totals.notFargate,
		CPUUnits: valueOrZeroInt(fact.CPUUnits), MemoryMiB: valueOrZeroInt(fact.MemoryMiB),
		NonProduction: fact.NonProduction, Untagged: !fact.TagExplicit, SpotWeightZero: fact.SpotWeightZero, ClusterHasSpot: slices.Contains(fact.ClusterProviders, "FARGATE_SPOT"),
		TerminalSessions7d: fact.TerminalSessions7d, ExpectedStops7d: fact.ExpectedStops7d, KilledStops7d: fact.KilledStops7d,
		StopTimeoutSeconds: fact.StopTimeoutSeconds,
	})
	dto := CostSpotServiceDTO{
		ServiceID: fact.Key.ID(), Service: fact.Key.Service, Cluster: fact.Key.Cluster, Region: fact.Key.Region,
		Tag: inheritedTag(tags, fact.TagID, fact.TagInherited), Status: string(result.Status),
		DesiredTasks: fact.Desired, OnDemandTasks: totals.priced - totals.spot, CPUUnits: fact.CPUUnits, MemoryMiB: fact.MemoryMiB,
		OnDemandBase: result.OnDemandBase, SpotTasks: result.SpotTasks,
		TerminalSessions: fact.TerminalSessions7d, ExpectedStops: fact.ExpectedStops7d, KilledStops: fact.KilledStops7d,
		StopEvidence: string(result.StopEvidence), StopTimeoutSeconds: fact.StopTimeoutSeconds,
		Caution: result.Caution, AutoScaled: fact.AutoScaled, CanDeploy: authz.Can(sc.Subject, authz.Deploy, fact.TagID),
	}
	if result.Status == cost.SpotUnsupportedController {
		dto.ControllerName = deploy.ControllerNameOf(fact.Controller)
	}
	if result.Status == cost.SpotCandidate || result.Status == cost.SpotNoClusterSpot {
		saving := result.SavingMonthly
		dto.SavingMonthly = &saving
	}
	return dto
}

// sortSpotServices puts candidates first by saving, then services whose cluster only lacks FARGATE_SPOT by the
// saving they would have, then everything else by name.
func sortSpotServices(services []CostSpotServiceDTO) {
	rank := func(svc CostSpotServiceDTO) int {
		switch svc.Status {
		case string(cost.SpotCandidate):
			return 0
		case string(cost.SpotNoClusterSpot):
			return 1
		}
		return 2
	}
	sort.SliceStable(services, func(i, j int) bool {
		a, b := services[i], services[j]
		if rank(a) != rank(b) {
			return rank(a) < rank(b)
		}
		if rank(a) < 2 && *a.SavingMonthly != *b.SavingMonthly {
			return *a.SavingMonthly > *b.SavingMonthly
		}
		if a.Service != b.Service {
			return a.Service < b.Service
		}
		return a.ServiceID < b.ServiceID
	})
}

// maxWarnedKeys bounds warnedSet. Keys include a hash of the container definitions, so a service whose definition
// keeps changing would otherwise add a key per revision for the life of the process.
const maxWarnedKeys = 4096

// warnedSet remembers which warnings were already logged. When it fills up it starts over, which can repeat a warning
// once; that is cheaper than growing without a bound.
type warnedSet struct {
	mu   sync.Mutex
	keys map[string]struct{}
}

// add records key and reports whether it was new.
func (w *warnedSet) add(key string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, seen := w.keys[key]; seen {
		return false
	}
	if w.keys == nil || len(w.keys) >= maxWarnedKeys {
		w.keys = make(map[string]struct{})
	}
	w.keys[key] = struct{}{}
	return true
}
