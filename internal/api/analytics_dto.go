package api

import "time"

// Wire types for GET /api/analytics/{delivery,reliability,capacity,operations}. The contract is
// docs/analytics_design/PLAN.md section 1; web/src/api/analytics.ts mirrors these types.
//
// Every slice is non-nil on the wire: build responses with the new<Tab>DTO constructors and fill them in.
// The generic wrappers (ProposedDTO, TopTableDTO) work with the contract reflection in contract_test.go,
// so no per-instantiation named types are needed.

// AnalyticsWindowDTO reports the time window a tab actually used.
type AnalyticsWindowDTO struct {
	Requested    string    `json:"requested"`
	Used         string    `json:"used"`
	Clamped      bool      `json:"clamped"`
	RangeApplies bool      `json:"range_applies"` // false on capacity
	Bucket       string    `json:"bucket"`        // "1h", "4h", "1d" or "2d"
	From         time.Time `json:"from"`
	To           time.Time `json:"to"`
	Retention    string    `json:"retention"` // governing table's retention, e.g. "168h"; "" when kept forever
}

// AnalyticsSourceDTO is one row of the "Data used on this page" table.
type AnalyticsSourceDTO struct {
	Table      string  `json:"table"`
	Reads      string  `json:"reads"`
	Kept       string  `json:"kept"`
	Setting    *string `json:"setting"`
	WindowUsed *string `json:"window_used"` // set when this table's window differs from the tab's
}

// SeriesDTO is a bucketed time series. Every line has one value per timestamp.
type SeriesDTO struct {
	Timestamps []time.Time     `json:"timestamps"`
	Series     []SeriesLineDTO `json:"series"`
}

// SeriesLineDTO is one line of a SeriesDTO. A nil value means no sample; counts are zero-filled instead.
type SeriesLineDTO struct {
	Key      string     `json:"key"`
	Label    string     `json:"label"`
	Expected bool       `json:"expected"` // grey styling for expected events (e.g. deployment stops)
	Values   []*float64 `json:"values"`
}

// DonutDTO is a total split into slices.
type DonutDTO struct {
	Total  float64         `json:"total"`
	Slices []DonutSliceDTO `json:"slices"`
}

// DonutSliceDTO is one slice of a DonutDTO.
type DonutSliceDTO struct {
	Key   string  `json:"key"`
	Label string  `json:"label"`
	Value float64 `json:"value"`
}

// NeedsDTO says which new data a proposed panel is waiting for.
type NeedsDTO struct {
	Table string `json:"table"`
	Size  string `json:"size"`
	How   string `json:"how"`
}

// ProposedDTO wraps a panel that needs a phase 2 rollup. Data is nil until Available.
type ProposedDTO[T any] struct {
	Available bool       `json:"available"`
	DataSince *time.Time `json:"data_since"` // earliest day in the rollup table once it has data
	Needs     *NeedsDTO  `json:"needs"`      // set while unavailable
	Data      *T         `json:"data"`
}

// TopTableDTO is a capped table: Rows holds at most 50 rows, Total the size of the matching population.
type TopTableDTO[R any] struct {
	Rows  []R `json:"rows"`
	Total int `json:"total"`
}

// ---- delivery ----------------------------------------------------------------------------------

// DeliveryKPIsDTO holds the delivery headline numbers. Pointers are nil when undefined (e.g. no rollouts).
type DeliveryKPIsDTO struct {
	Rollouts          int      `json:"rollouts"`
	RolloutsPerDay    float64  `json:"rollouts_per_day"`
	ChangeFailureRate *float64 `json:"change_failure_rate"` // 0..1
	MedianRolloutS    *float64 `json:"median_rollout_s"`
	P90RolloutS       *float64 `json:"p90_rollout_s"`
	TimeToRecoverS    *float64 `json:"time_to_recover_s"`
	Rollbacks         int      `json:"rollbacks"`
	Superseded        int      `json:"superseded"`
}

// DeliveryServiceRowDTO is a row of the services-by-change-failure-rate table.
type DeliveryServiceRowDTO struct {
	ServiceID    string     `json:"service_id"`
	Service      string     `json:"service"`
	Cluster      string     `json:"cluster"`
	AccountID    string     `json:"account_id"`
	Tag          TagDTO     `json:"tag"`
	Rollouts     int        `json:"rollouts"`
	Failed       int        `json:"failed"`
	FailureRate  float64    `json:"failure_rate"`
	MedianS      *float64   `json:"median_s"`
	RecoverS     *float64   `json:"recover_s"`
	LastFailedAt *time.Time `json:"last_failed_at"`
}

// FailedRolloutRowDTO is a row of the recent-failed-rollouts table (needs deployment_history.failure_reason).
type FailedRolloutRowDTO struct {
	At           time.Time `json:"at"`
	ServiceID    string    `json:"service_id"`
	Service      string    `json:"service"`
	Cluster      string    `json:"cluster"`
	FromRevision *int      `json:"from_revision"`
	ToRevision   int       `json:"to_revision"`
	Reason       string    `json:"reason"`
	Detail       string    `json:"detail"`
	RanForS      *float64  `json:"ran_for_s"`
}

// DeliveryDTO is the body of GET /api/analytics/delivery.
type DeliveryDTO struct {
	Tab      string               `json:"tab"`
	Window   AnalyticsWindowDTO   `json:"window"`
	Sources  []AnalyticsSourceDTO `json:"sources"`
	PolledAt time.Time            `json:"polled_at"`

	KPIs        DeliveryKPIsDTO                    `json:"kpis"`
	Rollouts    SeriesDTO                          `json:"rollouts"`     // completed|failed|superseded, stacked
	FailureRate SeriesDTO                          `json:"failure_rate"` // key failure_rate, percent
	Duration    SeriesDTO                          `json:"duration"`     // median|p90, seconds
	Services    TopTableDTO[DeliveryServiceRowDTO] `json:"services"`

	FailureReasons ProposedDTO[SeriesDTO]                        `json:"failure_reasons"`
	RecentFailures ProposedDTO[TopTableDTO[FailedRolloutRowDTO]] `json:"recent_failures"`
}

// ---- reliability -------------------------------------------------------------------------------

// ReliabilityKPIsDTO holds the reliability headline numbers.
type ReliabilityKPIsDTO struct {
	Availability        *float64 `json:"availability"` // 0..1
	MinutesBelowDesired float64  `json:"minutes_below_desired"`
	ServicesBelow       int      `json:"services_below"`
	TaskStops           int      `json:"task_stops"`
	UnexpectedStops     int      `json:"unexpected_stops"`
	OutOfMemory         int      `json:"out_of_memory"`
	PlacementFailures   int      `json:"placement_failures"`
}

// ReliabilityServiceRowDTO is a row of the least-available-services table.
type ReliabilityServiceRowDTO struct {
	ServiceID       string  `json:"service_id"`
	Service         string  `json:"service"`
	Cluster         string  `json:"cluster"`
	Tag             TagDTO  `json:"tag"`
	Availability    float64 `json:"availability"`
	MinutesBelow    float64 `json:"minutes_below"`
	UnexpectedStops int     `json:"unexpected_stops"`
	TopReason       *string `json:"top_reason"`
}

// AvailabilityTrendRowDTO is a row of the services-getting-less-available table (needs service_availability_daily).
type AvailabilityTrendRowDTO struct {
	ServiceID string  `json:"service_id"`
	Service   string  `json:"service"`
	Cluster   string  `json:"cluster"`
	Tag       TagDTO  `json:"tag"`
	Previous  float64 `json:"previous"`
	Current   float64 `json:"current"`
	ChangePts float64 `json:"change_pts"`
}

// ReliabilityDTO is the body of GET /api/analytics/reliability.
type ReliabilityDTO struct {
	Tab      string               `json:"tab"`
	Window   AnalyticsWindowDTO   `json:"window"`
	Sources  []AnalyticsSourceDTO `json:"sources"`
	PolledAt time.Time            `json:"polled_at"`

	KPIs             ReliabilityKPIsDTO                    `json:"kpis"`
	Availability     SeriesDTO                             `json:"availability"`
	ServicesBelow    SeriesDTO                             `json:"services_below"`
	StopsByReason    SeriesDTO                             `json:"stops_by_reason"`
	UnexpectedByZone SeriesDTO                             `json:"unexpected_by_zone"`
	ExitCodes        SeriesDTO                             `json:"exit_codes"`
	LeastAvailable   TopTableDTO[ReliabilityServiceRowDTO] `json:"least_available"`

	Availability90d ProposedDTO[SeriesDTO]                            `json:"availability_90d"`
	BelowDesired90d ProposedDTO[SeriesDTO]                            `json:"below_desired_90d"`
	GettingWorse    ProposedDTO[TopTableDTO[AvailabilityTrendRowDTO]] `json:"getting_worse"`
}

// ---- capacity ----------------------------------------------------------------------------------

// CapacityKPIsDTO holds the capacity headline numbers.
type CapacityKPIsDTO struct {
	ReservedVCPU   float64  `json:"reserved_vcpu"`
	ReservedMemGiB float64  `json:"reserved_mem_gib"`
	RunningTasks   int      `json:"running_tasks"`
	Services       int      `json:"services"`
	SpotShare      *float64 `json:"spot_share"` // 0..1
	AtScalingMax   int      `json:"at_scaling_max"`
	SubnetsLowIPs  int      `json:"subnets_low_ips"`
}

// CapacityClusterRowDTO is a row of the clusters-by-reserved-vCPU table.
type CapacityClusterRowDTO struct {
	ClusterID   string  `json:"cluster_id"`
	Cluster     string  `json:"cluster"`
	AccountID   string  `json:"account_id"`
	AccountName string  `json:"account_name"`
	Tag         TagDTO  `json:"tag"`
	Tasks       int     `json:"tasks"`
	VCPU        float64 `json:"vcpu"`
	MemGiB      float64 `json:"mem_gib"`
	Share       float64 `json:"share"`
	SpotShare   float64 `json:"spot_share"`
}

// AtMaxRowDTO is a service sitting at its auto-scaling maximum.
type AtMaxRowDTO struct {
	ServiceID   string     `json:"service_id"`
	Service     string     `json:"service"`
	Cluster     string     `json:"cluster"`
	Desired     int        `json:"desired"`
	MaxCapacity int        `json:"max_capacity"`
	AtMaxSince  *time.Time `json:"at_max_since"` // nil when counts history cannot say
}

// SubnetRowDTO is a subnet that is low on free IP addresses.
type SubnetRowDTO struct {
	SubnetID     string  `json:"subnet_id"`
	Name         string  `json:"name"`
	AZ           string  `json:"az"`
	AccountID    string  `json:"account_id"`
	Region       string  `json:"region"`
	AvailableIPs int     `json:"available_ips"`
	TotalIPs     int     `json:"total_ips"`
	UsedPct      float64 `json:"used_pct"`
}

// OverReservedRowDTO is a row of the most-over-reserved-services table (needs service_util_daily).
// The plan left its fields open; these compare reservation to p95 use.
type OverReservedRowDTO struct {
	ServiceID      string  `json:"service_id"`
	Service        string  `json:"service"`
	Cluster        string  `json:"cluster"`
	Tag            TagDTO  `json:"tag"`
	ReservedVCPU   float64 `json:"reserved_vcpu"`
	UsedP95VCPU    float64 `json:"used_p95_vcpu"`
	ReservedMemGiB float64 `json:"reserved_mem_gib"`
	UsedP95MemGiB  float64 `json:"used_p95_mem_gib"`
	WastedVCPU     float64 `json:"wasted_vcpu"`
}

// CapacityDTO is the body of GET /api/analytics/capacity (window.used is 7d and range_applies false).
type CapacityDTO struct {
	Tab      string               `json:"tab"`
	Window   AnalyticsWindowDTO   `json:"window"`
	Sources  []AnalyticsSourceDTO `json:"sources"`
	PolledAt time.Time            `json:"polled_at"`

	KPIs                    CapacityKPIsDTO                    `json:"kpis"`
	ReservedVCPU7d          SeriesDTO                          `json:"reserved_vcpu_7d"` // one line per tag, stacked
	VCPUByAccount           DonutDTO                           `json:"vcpu_by_account"`
	TasksByCapacityProvider DonutDTO                           `json:"tasks_by_capacity_provider"`
	Clusters                TopTableDTO[CapacityClusterRowDTO] `json:"clusters"`
	AtScalingMax            []AtMaxRowDTO                      `json:"at_scaling_max"`
	SubnetsLow              []SubnetRowDTO                     `json:"subnets_low"`

	ReservedVsUsed90d ProposedDTO[SeriesDTO]                       `json:"reserved_vs_used_90d"`
	SpotShare90d      ProposedDTO[SeriesDTO]                       `json:"spot_share_90d"`
	Memory90d         ProposedDTO[SeriesDTO]                       `json:"memory_90d"`
	OverReserved      ProposedDTO[TopTableDTO[OverReservedRowDTO]] `json:"over_reserved"`
}

// ---- operations --------------------------------------------------------------------------------

// OperationsKPIsDTO holds the operations headline numbers.
type OperationsKPIsDTO struct {
	Operations          int `json:"operations"`
	People              int `json:"people"`
	RevisionsRegistered int `json:"revisions_registered"`
	ScaleChanges        int `json:"scale_changes"`
	Restarts            int `json:"restarts"`
	TasksStopped        int `json:"tasks_stopped"`
	TerminalSessions    int `json:"terminal_sessions"`
}

// OperatorRowDTO is a row of the who-operates table.
type OperatorRowDTO struct {
	Username string    `json:"username"`
	Total    int       `json:"total"`
	Deploy   int       `json:"deploy"`
	Restart  int       `json:"restart"`
	Scale    int       `json:"scale"`
	Terminal int       `json:"terminal"`
	LastAt   time.Time `json:"last_at"`
}

// RestartedServiceRowDTO is a row of the most-restarted-services table.
type RestartedServiceRowDTO struct {
	ServiceID string    `json:"service_id"`
	Service   string    `json:"service"`
	Cluster   string    `json:"cluster"`
	Tag       TagDTO    `json:"tag"`
	Restarts  int       `json:"restarts"`
	People    int       `json:"people"`
	LastAt    time.Time `json:"last_at"`
}

// OperationsDTO is the body of GET /api/analytics/operations.
type OperationsDTO struct {
	Tab      string               `json:"tab"`
	Window   AnalyticsWindowDTO   `json:"window"`
	Sources  []AnalyticsSourceDTO `json:"sources"`
	PolledAt time.Time            `json:"polled_at"`

	KPIs                  OperationsKPIsDTO                   `json:"kpis"`
	ByType                SeriesDTO                           `json:"by_type"`  // deploy|rollback|task_definition|scale|restart|stop_task|terminal
	HandsOn               SeriesDTO                           `json:"hands_on"` // restart|stop_task|terminal
	RestartsPer100Deploys SeriesDTO                           `json:"restarts_per_100_deploys"`
	Who                   TopTableDTO[OperatorRowDTO]         `json:"who"`
	MostRestarted         TopTableDTO[RestartedServiceRowDTO] `json:"most_restarted"`
}

// ---- constructors ------------------------------------------------------------------------------

func newSeriesDTO() SeriesDTO {
	return SeriesDTO{Timestamps: []time.Time{}, Series: []SeriesLineDTO{}}
}

func newDonutDTO() DonutDTO { return DonutDTO{Slices: []DonutSliceDTO{}} }

func newTopTableDTO[R any]() TopTableDTO[R] { return TopTableDTO[R]{Rows: []R{}} }

// newUnavailable is a proposed panel whose rollup has no data yet.
func newUnavailable[T any](table, size, how string) ProposedDTO[T] {
	return ProposedDTO[T]{Needs: &NeedsDTO{Table: table, Size: size, How: how}}
}

const (
	needsAvailabilityDaily = "service_availability_daily"
	needsCapacityDaily     = "cluster_capacity_daily"
	needsUtilDaily         = "service_util_daily"
	needsFailureReason     = "deployment_history.failure_reason"
)

func newDeliveryDTO() DeliveryDTO {
	return DeliveryDTO{
		Sources:     []AnalyticsSourceDTO{},
		Rollouts:    newSeriesDTO(),
		FailureRate: newSeriesDTO(),
		Duration:    newSeriesDTO(),
		Services:    newTopTableDTO[DeliveryServiceRowDTO](),
		FailureReasons: newUnavailable[SeriesDTO](needsFailureReason, "< 1 MB",
			"The outcome logger records the circuit-breaker event or the last stopped task's reason on each failed rollout."),
		RecentFailures: newUnavailable[TopTableDTO[FailedRolloutRowDTO]](needsFailureReason, "< 1 MB",
			"The outcome logger records the circuit-breaker event or the last stopped task's reason on each failed rollout."),
	}
}

func newReliabilityDTO() ReliabilityDTO {
	const how = "A nightly rollup of service_counts_history and stopped tasks, run before retention deletes them."
	return ReliabilityDTO{
		Sources:          []AnalyticsSourceDTO{},
		Availability:     newSeriesDTO(),
		ServicesBelow:    newSeriesDTO(),
		StopsByReason:    newSeriesDTO(),
		UnexpectedByZone: newSeriesDTO(),
		ExitCodes:        newSeriesDTO(),
		LeastAvailable:   newTopTableDTO[ReliabilityServiceRowDTO](),
		Availability90d:  newUnavailable[SeriesDTO](needsAvailabilityDaily, "~35 MB a year", how),
		BelowDesired90d:  newUnavailable[SeriesDTO](needsAvailabilityDaily, "~35 MB a year", how),
		GettingWorse:     newUnavailable[TopTableDTO[AvailabilityTrendRowDTO]](needsAvailabilityDaily, "~35 MB a year", how),
	}
}

func newCapacityDTO() CapacityDTO {
	const capacityHow = "Running tasks sampled on the counts-history poll and rolled up daily per cluster."
	const utilHow = "One CloudWatch GetMetricData batch per account and region a day."
	return CapacityDTO{
		Sources:                 []AnalyticsSourceDTO{},
		ReservedVCPU7d:          newSeriesDTO(),
		VCPUByAccount:           newDonutDTO(),
		TasksByCapacityProvider: newDonutDTO(),
		Clusters:                newTopTableDTO[CapacityClusterRowDTO](),
		AtScalingMax:            []AtMaxRowDTO{},
		SubnetsLow:              []SubnetRowDTO{},
		ReservedVsUsed90d:       newUnavailable[SeriesDTO](needsCapacityDaily, "~5 MB a year", capacityHow),
		SpotShare90d:            newUnavailable[SeriesDTO](needsCapacityDaily, "~5 MB a year", capacityHow),
		Memory90d:               newUnavailable[SeriesDTO](needsUtilDaily, "~8 MB for 90 days", utilHow),
		OverReserved:            newUnavailable[TopTableDTO[OverReservedRowDTO]](needsUtilDaily, "~8 MB for 90 days", utilHow),
	}
}

func newOperationsDTO() OperationsDTO {
	return OperationsDTO{
		Sources:               []AnalyticsSourceDTO{},
		ByType:                newSeriesDTO(),
		HandsOn:               newSeriesDTO(),
		RestartsPer100Deploys: newSeriesDTO(),
		Who:                   newTopTableDTO[OperatorRowDTO](),
		MostRestarted:         newTopTableDTO[RestartedServiceRowDTO](),
	}
}
