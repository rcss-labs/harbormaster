package cost

import (
	"math"
	"testing"
)

// candidateInput is a Linux Fargate service in us-east-1 that passes every gate: three tasks of 512/1024, a
// non-production tag and a cluster with FARGATE_SPOT.
func candidateInput() SpotInput {
	return SpotInput{
		Region: "us-east-1", Architecture: "X86_64", OSFamily: "LINUX",
		RequiresFargate: true, ControllerSupported: true,
		LaunchType: "FARGATE", Desired: 3, OnDemandTasks: 3,
		CPUUnits: 512, MemoryMiB: 1024,
		NonProduction: true, ClusterHasSpot: true,
	}
}

func TestEvaluateSpot_GateOrder(t *testing.T) {
	cases := []struct {
		name   string
		change func(*SpotInput)
		want   SpotStatus
	}{
		{"all gates pass", func(*SpotInput) {}, SpotCandidate},
		{"not Fargate", func(in *SpotInput) { in.RequiresFargate = false }, SpotNotFargate},
		{"unpriced tasks", func(in *SpotInput) { in.UnpricedTasks = 1 }, SpotNotFargate},
		{"windows", func(in *SpotInput) { in.OSFamily = "WINDOWS_SERVER_2019_CORE" }, SpotWindows},
		{"no list price", func(in *SpotInput) { in.Region = "ap-south-1" }, SpotNoPrice},
		{"unknown task size", func(in *SpotInput) { in.CPUUnits = 0 }, SpotNoPrice},
		{"no running on-demand task", func(in *SpotInput) { in.OnDemandTasks = 0 }, SpotNotRunning},
		{"foreign controller", func(in *SpotInput) { in.ControllerSupported = false }, SpotUnsupportedController},
		{"custom provider", func(in *SpotInput) { in.LaunchType = ""; in.Strategy = []string{"FARGATE", "my-asg"} }, SpotCustomCapacity},
		{"already on Spot", func(in *SpotInput) { in.LaunchType = ""; in.Strategy = []string{"FARGATE", "FARGATE_SPOT"} }, SpotAlreadySpot},
		{"untagged, only the Dev fallback", func(in *SpotInput) { in.Untagged = true }, SpotUntagged},
		{"production tag", func(in *SpotInput) { in.NonProduction = false }, SpotProduction},
		{"single task", func(in *SpotInput) { in.Desired = 1; in.OnDemandTasks = 1 }, SpotSingleTask},
		{"cluster without Spot", func(in *SpotInput) { in.ClusterHasSpot = false }, SpotNoClusterSpot},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := candidateInput()
			tc.change(&in)
			if got := EvaluateSpot(in).Status; got != tc.want {
				t.Fatalf("status = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestEvaluateSpot_ServiceEntirelyOnSpotIsAlreadySpot(t *testing.T) {
	// Every task is on Spot, so there is no running on-demand task; saying so would hide that the service is already there.
	in := candidateInput()
	in.LaunchType = ""
	in.Strategy = []string{"FARGATE_SPOT"}
	in.OnDemandTasks, in.SpotTasks = 0, 3
	if got := EvaluateSpot(in).Status; got != SpotAlreadySpot {
		t.Fatalf("status = %q, want already_spot", got)
	}
}

func TestEvaluateSpot_SpotEntryWithWeightZeroIsNotAlreadySpot(t *testing.T) {
	in := candidateInput()
	in.LaunchType = ""
	in.Strategy = []string{"FARGATE", "FARGATE_SPOT"}
	in.SpotWeightZero = true
	if got := EvaluateSpot(in).Status; got != SpotCandidate {
		t.Fatalf("status = %q, want candidate: a Spot entry with weight 0 places nothing on Spot", got)
	}
}

func TestEvaluateSpot_SavingNeverCountsMoreTasksThanRun(t *testing.T) {
	// Desired 10 but only 2 running (a scale-up in progress, or a service that cannot place its tasks): the saving
	// is for the two that run, so it cannot exceed what the service is shown to spend.
	in := candidateInput()
	in.Desired, in.OnDemandTasks = 10, 2
	result := EvaluateSpot(in)
	want := 1 * (18.02 - 6.60)
	if math.Abs(result.SavingMonthly-want) > 0.01 || result.SpotTasks != 1 {
		t.Fatalf("saving %.2f with %d Spot tasks, want %.2f and 1", result.SavingMonthly, result.SpotTasks, want)
	}
	if spend := 2 * 18.02; result.SavingMonthly > spend {
		t.Fatalf("saving %.2f exceeds the %.2f the service spends", result.SavingMonthly, spend)
	}
}

func TestEvaluateSpot_FirstFailingGateWins(t *testing.T) {
	in := candidateInput()
	in.RequiresFargate = false
	in.NonProduction = false
	in.Desired, in.OnDemandTasks = 1, 1
	if got := EvaluateSpot(in).Status; got != SpotNotFargate {
		t.Fatalf("status = %q, want not_fargate before production and single_task", got)
	}
	in = candidateInput()
	in.NonProduction = false
	in.ClusterHasSpot = false
	if got := EvaluateSpot(in).Status; got != SpotProduction {
		t.Fatalf("status = %q, want production before the cluster check", got)
	}
}

func TestEvaluateSpot_UntaggedServiceIsNotACandidateButAnExplicitDevTagIs(t *testing.T) {
	in := candidateInput()
	in.Untagged = true // the Dev tag is non-production, but this service only fell back to it
	if got := EvaluateSpot(in); got.Status != SpotUntagged || got.SavingMonthly != 0 || got.SpotTasks != 0 {
		t.Fatalf("result = %+v, want untagged with no proposal", got)
	}
	in.Untagged = false // explicitly tagged Dev, which an admin marked non-production
	if got := EvaluateSpot(in).Status; got != SpotCandidate {
		t.Fatalf("status = %q, want candidate", got)
	}
}

func TestEvaluateSpot_UnmarkedTagIsProduction(t *testing.T) {
	// The zero value of the flag is the safe one: a service nobody marked is never suggested.
	in := candidateInput()
	in.NonProduction = false
	result := EvaluateSpot(in)
	if result.Status != SpotProduction || result.SavingMonthly != 0 || result.SpotTasks != 0 {
		t.Fatalf("result = %+v, want production with no proposal", result)
	}
}

func TestEvaluateSpot_SavingKeepsOneOnDemand(t *testing.T) {
	result := EvaluateSpot(candidateInput())
	want := 2 * (18.02 - 6.60)
	if math.Abs(result.SavingMonthly-want) > 0.01 {
		t.Fatalf("saving = %.4f, want %.4f (two of three tasks move)", result.SavingMonthly, want)
	}
	if result.OnDemandBase != 1 || result.SpotTasks != 2 {
		t.Fatalf("proposal = base %d, Spot %d; want base 1, Spot 2", result.OnDemandBase, result.SpotTasks)
	}
}

func TestEvaluateSpot_NoClusterSpotStillCarriesSaving(t *testing.T) {
	in := candidateInput()
	in.ClusterHasSpot = false
	result := EvaluateSpot(in)
	if result.Status != SpotNoClusterSpot || result.OnDemandBase != 1 || result.SpotTasks != 2 || math.Abs(result.SavingMonthly-2*(18.02-6.60)) > 0.01 {
		t.Fatalf("result = %+v, want the proposal and saving of an associated cluster", result)
	}
}

func TestEvaluateSpot_NoProposalForOtherStatuses(t *testing.T) {
	in := candidateInput()
	in.Desired, in.OnDemandTasks = 1, 1
	result := EvaluateSpot(in)
	if result.SavingMonthly != 0 || result.OnDemandBase != 0 || result.SpotTasks != 0 {
		t.Fatalf("result = %+v, want no proposal for a single-task service", result)
	}
}

func TestEvaluateSpot_StopEvidence(t *testing.T) {
	cases := []struct {
		name             string
		expected, killed int
		want             StopEvidence
		caution          bool
	}{
		{"no stops to judge", 0, 0, StopUnknown, false},
		{"stops that all ended cleanly", 3, 0, StopGraceful, false},
		{"one stop was killed", 3, 1, StopKilled, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := candidateInput()
			in.ExpectedStops7d, in.KilledStops7d = tc.expected, tc.killed
			result := EvaluateSpot(in)
			if result.StopEvidence != tc.want || result.Caution != tc.caution || result.Status != SpotCandidate {
				t.Fatalf("result = %+v, want %q, caution %v and still a candidate", result, tc.want, tc.caution)
			}
		})
	}
}

func TestEvaluateSpot_TerminalSessionsAreACautionNotAGate(t *testing.T) {
	in := candidateInput()
	in.TerminalSessions7d = 2
	result := EvaluateSpot(in)
	if result.Status != SpotCandidate || !result.Caution {
		t.Fatalf("result = %+v, want a candidate with a caution", result)
	}
}
