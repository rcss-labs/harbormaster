package api

import (
	"sort"
	"time"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// The Proposed panels that read the daily rollup tables (docs/analytics_design/PLAN.md section 4). They turn on
// when their table has data: a panel is ProposedDTO{Available: true, DataSince: first day, Data: ...}. Days before
// the rollup began stay null so the chart shows where the data starts.

// dailyPanelDays is the length of the "last 90 days" panels. They are independent of ?range=.
const dailyPanelDays = 90

// dailyWindow is dailyPanelDays UTC calendar days ending with today (today is partial, or empty for tables
// written by a nightly rollup).
type dailyWindow struct{ from time.Time }

func newDailyWindow(now time.Time) dailyWindow {
	y, m, d := now.UTC().Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	return dailyWindow{from: today.AddDate(0, 0, -(dailyPanelDays - 1))}
}

// to is the exclusive end: midnight after today.
func (d dailyWindow) to() time.Time { return d.from.AddDate(0, 0, dailyPanelDays) }

func (d dailyWindow) series() SeriesDTO {
	stamps := make([]time.Time, dailyPanelDays)
	for i := range stamps {
		stamps[i] = d.from.AddDate(0, 0, i)
	}
	return SeriesDTO{Timestamps: stamps, Series: []SeriesLineDTO{}}
}

// index is the position of day in the window, or -1 outside it.
func (d dailyWindow) index(day time.Time) int {
	if day.Before(d.from) || !day.Before(d.to()) {
		return -1
	}
	return int(day.Sub(d.from).Round(time.Hour) / (24 * time.Hour))
}

// line is a series line that stays null until a day is set.
func (d dailyWindow) line(key, label string) SeriesLineDTO {
	return SeriesLineDTO{Key: key, Label: label, Values: make([]*float64, dailyPanelDays)}
}

// availablePanel wraps data of a proposed panel whose rollup has rows since the given day.
func availablePanel[T any](since *time.Time, data T) ProposedDTO[T] {
	return ProposedDTO[T]{Available: true, DataSince: since, Data: &data}
}

// fillReliabilityDaily turns on the three availability-rollup panels.
func fillReliabilityDaily(dto *ReliabilityDTO, since *time.Time, w dailyWindow, points []store.DailyAvailabilityPoint, trend []store.AvailabilityTrendFact, tags map[authz.TagID]TagDTO) {
	availability := w.line("availability", "Availability")
	below := w.line("minutes_below", "Minutes below desired")
	for _, p := range points {
		index := w.index(p.Day)
		if index < 0 {
			continue
		}
		if p.Samples > 0 {
			availability.set(index, 100*float64(p.OK)/float64(p.Samples))
		}
		below.set(index, float64(p.MinutesBelow))
	}
	availabilitySeries := w.series()
	availabilitySeries.Series = append(availabilitySeries.Series, availability)
	belowSeries := w.series()
	belowSeries.Series = append(belowSeries.Series, below)
	dto.Availability90d = availablePanel(since, availabilitySeries)
	dto.BelowDesired90d = availablePanel(since, belowSeries)
	dto.GettingWorse = availablePanel(since, gettingWorseTable(trend, tags))
}

// gettingWorseTable keeps the services whose availability fell between the two 30-day periods, biggest drop
// first. previous and current are 0..1 fractions; change_pts is current - previous in percentage points.
func gettingWorseTable(trend []store.AvailabilityTrendFact, tags map[authz.TagID]TagDTO) TopTableDTO[AvailabilityTrendRowDTO] {
	table := newTopTableDTO[AvailabilityTrendRowDTO]()
	for _, fact := range trend {
		change := 100 * (fact.Current - fact.Previous)
		if change >= 0 {
			continue
		}
		table.Rows = append(table.Rows, AvailabilityTrendRowDTO{
			ServiceID: fact.Key.ID(), Service: fact.Key.Service, Cluster: fact.Key.Cluster,
			Tag:      inheritedTag(tags, authz.TagID(fact.TagID), fact.TagInherited),
			Previous: fact.Previous, Current: fact.Current, ChangePts: change,
		})
	}
	sort.Slice(table.Rows, func(i, j int) bool {
		a, b := table.Rows[i], table.Rows[j]
		if a.ChangePts != b.ChangePts {
			return a.ChangePts < b.ChangePts
		}
		return a.ServiceID < b.ServiceID
	})
	table.Total = len(table.Rows)
	table.Rows = table.Rows[:min(len(table.Rows), topTableRows)]
	return table
}

func valueOrNil(p *float64) *float64 {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// capacityDailyInputs are the rows read from the capacity and utilisation rollups; a nil since means the table is empty.
type capacityDailyInputs struct {
	capacitySince, utilSince *time.Time
	capacity                 []store.CapacityDailyPoint
	util                     []store.UtilDailyPoint
	overReserved             []store.OverReservedFact
}

// fillCapacityDaily turns on the Capacity panels whose rollup has data. Reserved comes from cluster_capacity_daily
// (daily average of the sampled running tasks), use from service_util_daily.
func fillCapacityDaily(dto *CapacityDTO, w dailyWindow, in capacityDailyInputs, tags map[authz.TagID]TagDTO) {
	reservedVCPU, reservedMem := w.line("reserved", "Reserved"), w.line("reserved", "Reserved")
	spot := w.line("spot_share", "Spot share")
	for _, p := range in.capacity {
		index := w.index(p.Day)
		if index < 0 {
			continue
		}
		reservedVCPU.set(index, p.VCPU)
		reservedMem.set(index, p.MemGiB)
		if p.Tasks > 0 {
			spot.set(index, 100*p.Spot/p.Tasks)
		}
	}
	usedP95, usedAvg := w.line("used_p95", "Used p95"), w.line("used_avg", "Used avg")
	memP95 := w.line("used_p95", "Used p95")
	for _, p := range in.util {
		index := w.index(p.Day)
		if index < 0 {
			continue
		}
		usedP95.Values[index], usedAvg.Values[index], memP95.Values[index] = valueOrNil(p.CPUP95VCPU), valueOrNil(p.CPUAvgVCPU), valueOrNil(p.MemP95GiB)
	}

	if in.capacitySince != nil {
		reserved := w.series()
		reserved.Series = append(reserved.Series, reservedVCPU, usedP95, usedAvg)
		dto.ReservedVsUsed90d = availablePanel(in.capacitySince, reserved)
		spotSeries := w.series()
		spotSeries.Series = append(spotSeries.Series, spot)
		dto.SpotShare90d = availablePanel(in.capacitySince, spotSeries)
	}
	if in.utilSince != nil {
		memory := w.series()
		memory.Series = append(memory.Series, reservedMem, memP95)
		dto.Memory90d = availablePanel(in.utilSince, memory)
		dto.OverReserved = availablePanel(in.utilSince, overReservedTable(in.overReserved, tags))
	}
}

// overReservedTable ranks services by unused vCPU: (reserved - p95 use) per task x running tasks.
func overReservedTable(facts []store.OverReservedFact, tags map[authz.TagID]TagDTO) TopTableDTO[OverReservedRowDTO] {
	table := newTopTableDTO[OverReservedRowDTO]()
	for _, fact := range facts {
		wasted := (fact.ReservedVCPU - fact.UsedP95VCPU) * float64(fact.Running)
		if wasted <= 0 {
			continue
		}
		table.Rows = append(table.Rows, OverReservedRowDTO{
			ServiceID: fact.Key.ID(), Service: fact.Key.Service, Cluster: fact.Key.Cluster,
			Tag:          inheritedTag(tags, authz.TagID(fact.TagID), fact.TagInherited),
			ReservedVCPU: fact.ReservedVCPU, UsedP95VCPU: fact.UsedP95VCPU,
			ReservedMemGiB: fact.ReservedMemGiB, UsedP95MemGiB: fact.UsedP95MemGiB, WastedVCPU: wasted,
		})
	}
	sort.Slice(table.Rows, func(i, j int) bool {
		a, b := table.Rows[i], table.Rows[j]
		if a.WastedVCPU != b.WastedVCPU {
			return a.WastedVCPU > b.WastedVCPU
		}
		return a.ServiceID < b.ServiceID
	})
	table.Total = len(table.Rows)
	table.Rows = table.Rows[:min(len(table.Rows), topTableRows)]
	return table
}

// failureReasons lists the stored failure_reason groups in display order with their labels.
var failureReasons = []struct{ key, label string }{
	{"circuit_breaker", "Circuit breaker"},
	{"health_check", "Health check"},
	{"failed_to_start", "Failed to start"},
	{"out_of_memory", "Out of memory"},
	{"stopped_by_user", "Stopped by user"},
	{"other", "Other"},
}

// hasFailureReason is true when a failed rollout in the window carries a recorded reason.
func hasFailureReason(facts []store.DeploymentFact) bool {
	for _, fact := range facts {
		if fact.Outcome == "failed" && fact.FailureReason != "" {
			return true
		}
	}
	return false
}

// failureReasonsSeries counts failed rollouts per bucket, one zero-filled line per reason seen in the window.
func failureReasonsSeries(facts []store.DeploymentFact, w windowSpec) SeriesDTO {
	series := newBucketedSeries(w)
	counts := map[string]*SeriesLineDTO{}
	for _, fact := range facts {
		if fact.Outcome != "failed" || fact.FailureReason == "" || bucketIndex(fact.StartedAt, w) < 0 {
			continue
		}
		line := counts[fact.FailureReason]
		if line == nil {
			label := fact.FailureReason
			for _, known := range failureReasons {
				if known.key == fact.FailureReason {
					label = known.label
				}
			}
			created := zeroLine(w, fact.FailureReason, label, false)
			line = &created
			counts[fact.FailureReason] = line
		}
		line.add(bucketIndex(fact.StartedAt, w), 1)
	}
	for _, known := range failureReasons {
		if line := counts[known.key]; line != nil {
			series.Series = append(series.Series, *line)
			delete(counts, known.key)
		}
	}
	rest := make([]string, 0, len(counts))
	for key := range counts {
		rest = append(rest, key)
	}
	sort.Strings(rest)
	for _, key := range rest {
		series.Series = append(series.Series, *counts[key])
	}
	return series
}

// recentFailuresTable maps the store's failed rollouts to rows.
func recentFailuresTable(facts []store.FailedRolloutFact, total int) TopTableDTO[FailedRolloutRowDTO] {
	table := newTopTableDTO[FailedRolloutRowDTO]()
	table.Total = total
	for _, fact := range facts {
		row := FailedRolloutRowDTO{
			At: fact.At, ServiceID: fact.Key.ID(), Service: fact.Key.Service, Cluster: fact.Key.Cluster,
			FromRevision: fact.FromRevision, ToRevision: fact.ToRevision, Reason: fact.Reason, Detail: fact.Detail,
		}
		if fact.RanFor != nil {
			seconds := fact.RanFor.Seconds()
			row.RanForS = &seconds
		}
		table.Rows = append(table.Rows, row)
	}
	return table
}
