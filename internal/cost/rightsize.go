package cost

import (
	"strings"
	"time"

	"github.com/rcss-labs/harbormaster/internal/deploy"
)

const (
	MinHeadroomPct     = 10
	MaxHeadroomPct     = 100
	HeadroomStepPct    = 5
	DefaultHeadroomPct = 30
	MinHistoryDays     = 14
	FreshnessDays      = 3
	MinSavingMonthly   = 0.5
)

// Status explains why a service has, or has no, rightsizing suggestions.
type Status string

const (
	StatusOK           Status = "ok"
	StatusNotFargate   Status = "not_fargate"
	StatusNoPrice      Status = "no_price"
	StatusNotRunning   Status = "not_running"
	StatusNoSize       Status = "no_size"
	StatusNoData       Status = "no_data"
	StatusShortHistory Status = "short_history"
	StatusStale        Status = "stale"
	StatusRollupOff    Status = "rollup_off"
)

// Input is everything Evaluate needs about one service.
type Input struct {
	Region, Architecture, OSFamily string
	RequiresFargate                bool
	CPUUnits, MemoryMiB            int // current task definition; 0 = unknown
	Floors                         Floors
	OnDemandTasks, SpotTasks       int
	UnpricedTasks                  int
	UsedCPUUnits, UsedMemMiB       *float64
	HistoryDays                    int
	LatestDay                      *time.Time
	RecentOOM                      bool
	RollupEnabled                  bool
	Today                          time.Time // UTC midnight
}

// Suggestion is a smaller valid Fargate size and what it saves per month.
type Suggestion struct {
	CPUUnits            int
	MemoryMiB           int
	SavingMonthly       float64
	LimitedByContainers bool
}

// Step applies from FromHeadroomPct up to the next step. A nil Suggestion means
// no worthwhile saving at that headroom.
type Step struct {
	FromHeadroomPct int
	Suggestion      *Suggestion
}

// Evaluate returns the status and, when ok and at least one stop saves money,
// the headroom ladder with neighbouring equal stops merged.
func Evaluate(in Input) (Status, []Step) {
	if status := gate(in); status != StatusOK {
		return status, nil
	}
	rate, _ := RateFor(in.Region, in.Architecture)
	sizes := sortedSizes(rate)
	tasks := float64(in.OnDemandTasks + in.SpotTasks)
	currentCost := rate.Monthly(in.CPUUnits, in.MemoryMiB)

	var steps []Step
	anySuggestion := false
	for headroom := MinHeadroomPct; headroom <= MaxHeadroomPct; headroom += HeadroomStepPct {
		factor := 1 + float64(headroom)/100
		neededCPU := *in.UsedCPUUnits * factor
		neededMem := *in.UsedMemMiB * factor
		oomMem := 0
		if in.RecentOOM {
			oomMem = in.MemoryMiB
		}

		var suggestion *Suggestion
		if best, ok := cheapest(sizes, neededCPU, neededMem, float64(in.Floors.CPUUnits), float64(in.Floors.MemoryMiB), oomMem); ok {
			saving := tasks * (currentCost - rate.Monthly(best[0], best[1]))
			if saving > MinSavingMonthly {
				unconstrained, found := cheapest(sizes, neededCPU, neededMem, 0, 0, oomMem)
				suggestion = &Suggestion{
					CPUUnits:            best[0],
					MemoryMiB:           best[1],
					SavingMonthly:       saving,
					LimitedByContainers: found && rate.Monthly(unconstrained[0], unconstrained[1]) < rate.Monthly(best[0], best[1]),
				}
				anySuggestion = true
			}
		}

		if n := len(steps); n > 0 && sameSuggestion(steps[n-1].Suggestion, suggestion) {
			continue
		}
		steps = append(steps, Step{FromHeadroomPct: headroom, Suggestion: suggestion})
	}
	if !anySuggestion {
		return StatusOK, nil
	}
	return StatusOK, steps
}

// gate applies the status rules in priority order and returns StatusOK when
// the service can be evaluated.
func gate(in Input) Status {
	switch {
	case !in.RollupEnabled:
		return StatusRollupOff
	case !in.RequiresFargate, in.UnpricedTasks > 0, strings.HasPrefix(strings.ToUpper(in.OSFamily), "WINDOWS"):
		return StatusNotFargate
	}
	if _, ok := RateFor(in.Region, in.Architecture); !ok {
		return StatusNoPrice
	}
	switch {
	case in.OnDemandTasks+in.SpotTasks == 0:
		return StatusNotRunning
	case in.CPUUnits <= 0 || in.MemoryMiB <= 0:
		return StatusNoSize
	case in.HistoryDays == 0 || in.UsedCPUUnits == nil || in.UsedMemMiB == nil || *in.UsedMemMiB <= 0 || in.LatestDay == nil:
		return StatusNoData
	case in.HistoryDays < MinHistoryDays:
		return StatusShortHistory
	case in.Today.Sub(*in.LatestDay) > FreshnessDays*24*time.Hour:
		return StatusStale
	}
	return StatusOK
}

// sortedSizes orders sizes by price, then lower CPU, then lower memory, so the
// first fit is the cheapest with deterministic ties.
func sortedSizes(rate Rate) [][2]int {
	sizes := deploy.FargateSizes()
	for i := 1; i < len(sizes); i++ {
		for j := i; j > 0 && sizeLess(rate, sizes[j], sizes[j-1]); j-- {
			sizes[j], sizes[j-1] = sizes[j-1], sizes[j]
		}
	}
	return sizes
}

func sizeLess(rate Rate, a, b [2]int) bool {
	costA, costB := rate.Monthly(a[0], a[1]), rate.Monthly(b[0], b[1])
	if costA != costB {
		return costA < costB
	}
	if a[0] != b[0] {
		return a[0] < b[0]
	}
	return a[1] < b[1]
}

// cheapest returns the first size in price order that covers the needed CPU
// and memory, the container floors and the OOM memory.
func cheapest(sizes [][2]int, neededCPU, neededMem, floorCPU, floorMem float64, oomMem int) ([2]int, bool) {
	cpuNeeded := max(neededCPU, floorCPU)
	memNeeded := max(neededMem, floorMem, float64(oomMem))
	for _, size := range sizes {
		if float64(size[0]) >= cpuNeeded && float64(size[1]) >= memNeeded {
			return size, true
		}
	}
	return [2]int{}, false
}

func sameSuggestion(a, b *Suggestion) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.CPUUnits == b.CPUUnits && a.MemoryMiB == b.MemoryMiB && a.LimitedByContainers == b.LimitedByContainers
}
