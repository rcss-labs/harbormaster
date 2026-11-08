package api

import "time"

// CostDTO is GET /api/analytics/cost. Spend comes from the visible running tasks and services only, never from
// a whole-cluster rollup, so a caller's totals add up to the rows they can see.
type CostDTO struct {
	Tab      string               `json:"tab"` // "cost"
	Window   AnalyticsWindowDTO   `json:"window"`
	PolledAt time.Time            `json:"polled_at"`
	Sources  []AnalyticsSourceDTO `json:"sources"`

	PricesAsOf  string             `json:"prices_as_of"` // list price publication date, YYYY-MM-DD
	KPIs        CostKPIsDTO        `json:"kpis"`
	ByCluster   []CostGroupDTO     `json:"by_cluster"`
	ByAccount   []CostGroupDTO     `json:"by_account"`
	ByTag       []CostGroupDTO     `json:"by_tag"`
	Rightsizing CostRightsizingDTO `json:"rightsizing"`
}

// CostKPIsDTO totals the priced running tasks. Spot tasks are priced at the on-demand list rate and also
// reported separately; tasks without a Fargate price are counted but never given a dollar figure.
type CostKPIsDTO struct {
	MonthlyTotal    float64  `json:"monthly_total"`
	SpotMonthly     float64  `json:"spot_monthly"`
	PricedTasks     int      `json:"priced_tasks"`
	SpotTasks       int      `json:"spot_tasks"`
	UnpricedTasks   int      `json:"unpriced_tasks"`
	UnpricedRegions []string `json:"unpriced_regions"` // regions with Fargate tasks but no price
}

// CostGroupDTO is the spend of one cluster, account or tag.
type CostGroupDTO struct {
	Key           string  `json:"key"`
	Label         string  `json:"label"`
	Monthly       float64 `json:"monthly"`
	SpotMonthly   float64 `json:"spot_monthly"`
	UnpricedTasks int     `json:"unpriced_tasks"`
}

// CostRightsizingDTO lists every visible service (not capped: the KPIs sum them).
type CostRightsizingDTO struct {
	State              string           `json:"state"` // ok|rollup_off|retention_short
	MinHistoryDays     int              `json:"min_history_days"`
	DefaultHeadroomPct int              `json:"default_headroom_pct"`
	Services           []CostServiceDTO `json:"services"`
}

// CostServiceDTO is one service's spend and, when its status is ok, the headroom ladder.
type CostServiceDTO struct {
	ServiceID    string        `json:"service_id"`
	Service      string        `json:"service"`
	Cluster      string        `json:"cluster"`
	Region       string        `json:"region"`
	Architecture string        `json:"architecture"`
	Tag          TagDTO        `json:"tag"`
	Tasks        int           `json:"tasks"`
	SpotTasks    int           `json:"spot_tasks"`
	CPUUnits     *int          `json:"cpu_units"`
	MemoryMiB    *int          `json:"memory_mib"`
	CPUUsedShare *float64      `json:"cpu_used_share"` // 0..1 of the current size; may exceed 1 after a downsize
	MemUsedShare *float64      `json:"mem_used_share"`
	Monthly      float64       `json:"monthly"`
	Status       string        `json:"status"`
	HistoryDays  int           `json:"history_days"`
	AutoScaled   bool          `json:"auto_scaled"`
	RecentOOM    bool          `json:"recent_oom"`
	CanEdit      bool          `json:"can_edit"`
	Steps        []CostStepDTO `json:"steps"` // never null
}

// CostStepDTO applies from FromHeadroomPct up to the next step; a nil suggestion means no worthwhile saving.
type CostStepDTO struct {
	FromHeadroomPct int                `json:"from_headroom_pct"`
	Suggestion      *CostSuggestionDTO `json:"suggestion"`
}

// CostSuggestionDTO is a smaller valid Fargate size and its monthly saving across the service's running tasks.
type CostSuggestionDTO struct {
	CPUUnits            int     `json:"cpu_units"`
	MemoryMiB           int     `json:"memory_mib"`
	SavingMonthly       float64 `json:"saving_monthly"`
	LimitedByContainers bool    `json:"limited_by_containers"`
}
