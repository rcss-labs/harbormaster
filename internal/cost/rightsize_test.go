package cost

import (
	"math"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/deploy"
)

var testToday = time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)

func floatPtr(value float64) *float64 { return &value }

func baseInput() Input {
	latest := testToday.AddDate(0, 0, -1)
	return Input{
		Region:          "us-east-1",
		Architecture:    "X86_64",
		OSFamily:        "LINUX",
		RequiresFargate: true,
		CPUUnits:        2048,
		MemoryMiB:       4096,
		OnDemandTasks:   1,
		UsedCPUUnits:    floatPtr(450),
		UsedMemMiB:      floatPtr(1229),
		HistoryDays:     30,
		LatestDay:       &latest,
		RollupEnabled:   true,
		Today:           testToday,
	}
}

// suggestionAt returns the suggestion in force at the given headroom percentage.
func suggestionAt(steps []Step, headroom int) *Suggestion {
	var current *Suggestion
	for _, step := range steps {
		if step.FromHeadroomPct > headroom {
			break
		}
		current = step.Suggestion
	}
	return current
}

func TestEvaluate_LadderAcrossHeadroom(t *testing.T) {
	status, steps := Evaluate(baseInput())
	if status != StatusOK {
		t.Fatalf("status = %q, want ok", status)
	}
	wantFrom := []int{10, 15, 70}
	wantSize := [][2]int{{512, 2048}, {1024, 2048}, {1024, 3072}}
	if len(steps) != len(wantFrom) {
		t.Fatalf("steps = %+v, want %d steps", steps, len(wantFrom))
	}
	for i, step := range steps {
		if step.FromHeadroomPct != wantFrom[i] {
			t.Errorf("step %d from = %d, want %d", i, step.FromHeadroomPct, wantFrom[i])
		}
		if step.Suggestion == nil || step.Suggestion.CPUUnits != wantSize[i][0] || step.Suggestion.MemoryMiB != wantSize[i][1] {
			t.Errorf("step %d suggestion = %+v, want %v", i, step.Suggestion, wantSize[i])
		}
	}
	if got := steps[1].Suggestion.SavingMonthly; math.Abs(got-36.04) > 0.01 {
		t.Errorf("saving at 15 = %.4f, want 36.04", got)
	}
}

func TestEvaluate_OnlyValidFargateSizes(t *testing.T) {
	for cpu := 10.0; cpu <= 2000; cpu += 190 {
		for mem := 100.0; mem <= 4000; mem += 370 {
			in := baseInput()
			in.UsedCPUUnits, in.UsedMemMiB = floatPtr(cpu), floatPtr(mem)
			_, steps := Evaluate(in)
			for _, step := range steps {
				if step.Suggestion != nil && !deploy.FargateValid(step.Suggestion.CPUUnits, step.Suggestion.MemoryMiB) {
					t.Fatalf("used %v/%v: invalid size %+v", cpu, mem, step.Suggestion)
				}
			}
		}
	}
}

func TestEvaluate_NeverBelowContainerFloors(t *testing.T) {
	t.Run("memory floor", func(t *testing.T) {
		in := baseInput()
		in.Floors = Floors{MemoryMiB: 3072}
		_, steps := Evaluate(in)
		got := suggestionAt(steps, 30)
		if got == nil || got.CPUUnits != 1024 || got.MemoryMiB != 3072 || !got.LimitedByContainers {
			t.Fatalf("at 30 = %+v, want 1024/3072 limited", got)
		}
	})
	t.Run("cpu floor", func(t *testing.T) {
		in := baseInput()
		in.Floors = Floors{CPUUnits: 1024}
		_, steps := Evaluate(in)
		got := suggestionAt(steps, 10)
		if got == nil || got.CPUUnits != 1024 {
			t.Fatalf("at 10 = %+v, want cpu 1024", got)
		}
	})
	t.Run("floors equal current size", func(t *testing.T) {
		in := baseInput()
		in.Floors = Floors{CPUUnits: 2048, MemoryMiB: 4096}
		status, steps := Evaluate(in)
		if status != StatusOK || steps != nil {
			t.Fatalf("got (%q, %+v), want (ok, nil)", status, steps)
		}
	})
}

func TestEvaluate_RecentOOMKeepsMemory(t *testing.T) {
	in := baseInput()
	in.RecentOOM = true
	_, steps := Evaluate(in)
	got := suggestionAt(steps, 30)
	if got == nil || got.CPUUnits != 1024 || got.MemoryMiB != 4096 {
		t.Fatalf("at 30 = %+v, want 1024/4096", got)
	}
}

func TestEvaluate_HistoryGates(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(in *Input)
		want   Status
	}{
		{"13 days", func(in *Input) { in.HistoryDays = 13 }, StatusShortHistory},
		{"14 days", func(in *Input) { in.HistoryDays = 14 }, StatusOK},
		{"0 days", func(in *Input) { in.HistoryDays = 0 }, StatusNoData},
		{"latest 4 days old", func(in *Input) { d := testToday.AddDate(0, 0, -4); in.LatestDay = &d }, StatusStale},
		{"latest 3 days old", func(in *Input) { d := testToday.AddDate(0, 0, -3); in.LatestDay = &d }, StatusOK},
		{"memory nil", func(in *Input) { in.UsedMemMiB = nil }, StatusNoData},
		{"memory zero", func(in *Input) { in.UsedMemMiB = floatPtr(0) }, StatusNoData},
		{"cpu zero is valid", func(in *Input) { in.UsedCPUUnits = floatPtr(0) }, StatusOK},
		{"latest day nil", func(in *Input) { in.LatestDay = nil }, StatusNoData},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := baseInput()
			tc.mutate(&in)
			if got, _ := Evaluate(in); got != tc.want {
				t.Fatalf("status = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestEvaluate_NonFargateGetsNothing(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(in *Input)
	}{
		{"not fargate", func(in *Input) { in.RequiresFargate = false }},
		{"unpriced task", func(in *Input) { in.UnpricedTasks = 1 }},
		{"windows", func(in *Input) { in.OSFamily = "WINDOWS_SERVER_2022_CORE" }},
		{"windows mixed case", func(in *Input) { in.OSFamily = "Windows_Server_2022_Core" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := baseInput()
			tc.mutate(&in)
			status, steps := Evaluate(in)
			if status != StatusNotFargate || steps != nil {
				t.Fatalf("got (%q, %+v), want (not_fargate, nil)", status, steps)
			}
		})
	}
}

func TestEvaluate_UnknownRegionHasNoPrice(t *testing.T) {
	in := baseInput()
	in.Region = "mars-north-1"
	status, steps := Evaluate(in)
	if status != StatusNoPrice || steps != nil {
		t.Fatalf("got (%q, %+v), want (no_price, nil)", status, steps)
	}
}

func TestEvaluate_RollupOffWins(t *testing.T) {
	in := baseInput()
	in.RollupEnabled = false
	in.RequiresFargate = false
	if status, _ := Evaluate(in); status != StatusRollupOff {
		t.Fatalf("status = %q, want rollup_off", status)
	}
}

func TestEvaluate_NotRunningAndNoSize(t *testing.T) {
	in := baseInput()
	in.OnDemandTasks = 0
	if status, _ := Evaluate(in); status != StatusNotRunning {
		t.Fatalf("status = %q, want not_running", status)
	}
	in = baseInput()
	in.CPUUnits = 0
	if status, _ := Evaluate(in); status != StatusNoSize {
		t.Fatalf("status = %q, want no_size", status)
	}
	in = baseInput()
	in.MemoryMiB = 0
	if status, _ := Evaluate(in); status != StatusNoSize {
		t.Fatalf("status = %q, want no_size for zero memory", status)
	}
}

func TestEvaluate_SpotTasksCountInSaving(t *testing.T) {
	in := baseInput()
	in.SpotTasks = 1
	_, steps := Evaluate(in)
	if got := steps[1].Suggestion.SavingMonthly; math.Abs(got-2*36.04) > 0.02 {
		t.Fatalf("saving = %.4f, want %.4f", got, 2*36.04)
	}
}

func TestEvaluate_ArmUsesArmRates(t *testing.T) {
	in := baseInput()
	in.Architecture = "ARM64"
	_, steps := Evaluate(in)
	if got := steps[1].Suggestion.SavingMonthly; math.Abs(got-28.84) > 0.01 {
		t.Fatalf("saving = %.4f, want 28.84", got)
	}
}

func TestEvaluate_PeggedServiceHasNoSuggestion(t *testing.T) {
	in := baseInput()
	in.UsedCPUUnits, in.UsedMemMiB = floatPtr(2048), floatPtr(4096)
	status, steps := Evaluate(in)
	if status != StatusOK || steps != nil {
		t.Fatalf("got (%q, %+v), want (ok, nil)", status, steps)
	}
}

func TestEvaluate_LimitedFlagChangesStepWhenFloorStopsBinding(t *testing.T) {
	in := baseInput()
	in.Floors = Floors{MemoryMiB: 3072}
	_, steps := Evaluate(in)
	if len(steps) != 3 || steps[1].FromHeadroomPct != 15 || steps[2].FromHeadroomPct != 70 {
		t.Fatalf("steps = %+v, want stops at 10, 15, 70", steps)
	}
	if !steps[1].Suggestion.LimitedByContainers || steps[2].Suggestion.LimitedByContainers {
		t.Fatalf("limited flags = %v then %v, want true then false",
			steps[1].Suggestion.LimitedByContainers, steps[2].Suggestion.LimitedByContainers)
	}
	if steps[2].Suggestion.CPUUnits != 1024 || steps[2].Suggestion.MemoryMiB != 3072 {
		t.Fatalf("step at 70 = %+v, want 1024/3072", steps[2].Suggestion)
	}
}

func TestEvaluate_SuggestionCoversUsageAndFloors(t *testing.T) {
	for _, usedCPU := range []float64{0, 100, 450, 900} {
		for _, usedMem := range []float64{200, 1229, 2500} {
			for _, floors := range []Floors{{}, {CPUUnits: 512}, {MemoryMiB: 3072}, {CPUUnits: 1024, MemoryMiB: 2048}} {
				for _, oom := range []bool{false, true} {
					in := baseInput()
					in.UsedCPUUnits, in.UsedMemMiB = floatPtr(usedCPU), floatPtr(usedMem)
					in.Floors, in.RecentOOM = floors, oom
					_, steps := Evaluate(in)
					for headroom := MinHeadroomPct; headroom <= MaxHeadroomPct; headroom += HeadroomStepPct {
						got := suggestionAt(steps, headroom)
						if got == nil {
							continue
						}
						factor := 1 + float64(headroom)/100
						wantMem := max(usedMem*factor, float64(floors.MemoryMiB))
						if oom {
							wantMem = max(wantMem, float64(in.MemoryMiB))
						}
						wantCPU := max(usedCPU*factor, float64(floors.CPUUnits))
						if float64(got.CPUUnits) < wantCPU || float64(got.MemoryMiB) < wantMem {
							t.Fatalf("used %v/%v floors %+v oom %v at %d: %+v below needed %v/%v",
								usedCPU, usedMem, floors, oom, headroom, got, wantCPU, wantMem)
						}
					}
				}
			}
		}
	}
}
