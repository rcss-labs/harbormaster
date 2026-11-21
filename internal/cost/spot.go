package cost

import "slices"

// SpotStatus says whether a service can move to Fargate Spot, or the first reason it cannot.
type SpotStatus string

const (
	SpotCandidate             SpotStatus = "candidate"
	SpotNoClusterSpot         SpotStatus = "no_cluster_spot"
	SpotProduction            SpotStatus = "production"
	SpotSingleTask            SpotStatus = "single_task"
	SpotAlreadySpot           SpotStatus = "already_spot"
	SpotCustomCapacity        SpotStatus = "custom_capacity"
	SpotUnsupportedController SpotStatus = "unsupported_controller"
	SpotNotRunning            SpotStatus = "not_running"
	SpotNoPrice               SpotStatus = "no_price"
	SpotWindows               SpotStatus = "windows"
	SpotNotFargate            SpotStatus = "not_fargate"
)

// StopEvidence is what the last days of deployment and scale-in stops say about how the service handles SIGTERM.
type StopEvidence string

const (
	// StopGraceful: there were stops to judge and none ended in exit 137.
	StopGraceful StopEvidence = "graceful"
	// StopKilled: at least one such stop ended in exit 137 (the task was killed after the stop timeout).
	StopKilled StopEvidence = "killed"
	// StopUnknown: no deployment or scale-in stop in the window, so nothing can be judged.
	StopUnknown StopEvidence = "unknown"
)

// Spot advisor defaults.
const (
	SpotMinTasks     = 2
	SpotOnDemandBase = 1
	SpotLookbackDays = 7
)

// SpotInput is everything EvaluateSpot needs about one service.
type SpotInput struct {
	Region, Architecture, OSFamily string
	RequiresFargate                bool
	ControllerSupported            bool
	Strategy                       []string // providers in the service's capacity provider strategy; empty = a launch type
	LaunchType                     string
	Desired                        int
	OnDemandTasks, SpotTasks       int
	UnpricedTasks                  int
	CPUUnits, MemoryMiB            int
	NonProduction, ClusterHasSpot  bool
	TerminalSessions7d             int
	ExpectedStops7d, KilledStops7d int  // deployment and scale-in stops; killed is exit 137 among them
	StopTimeoutSeconds             *int // highest container stopTimeout; nil = the default. Information only.
}

// SpotResult is the verdict and, for a candidate, the proposal.
type SpotResult struct {
	Status SpotStatus
	// OnDemandBase and SpotTasks are the proposal; zero unless the status is candidate or no_cluster_spot.
	OnDemandBase, SpotTasks int
	SavingMonthly           float64
	StopEvidence            StopEvidence
	// Caution is set by terminal sessions or killed stops. It never hides a service; the UI leaves it unticked.
	Caution bool
}

// EvaluateSpot applies the hard gates in order and reports the first that fails. The soft signals (terminal
// sessions, SIGTERM evidence) are reported on every result and never change the status.
func EvaluateSpot(in SpotInput) SpotResult {
	result := SpotResult{Status: gateSpot(in), StopEvidence: stopEvidence(in)}
	result.Caution = in.TerminalSessions7d > 0 || result.StopEvidence == StopKilled
	if result.Status != SpotCandidate && result.Status != SpotNoClusterSpot {
		return result
	}
	// The proposal keeps one on-demand task; everything above it moves to Spot. Both rates exist: gateSpot checked.
	list, _ := RateFor(in.Region, in.Architecture)
	spot, _ := SpotRateFor(in.Region, in.Architecture)
	result.OnDemandBase = SpotOnDemandBase
	result.SpotTasks = in.Desired - SpotOnDemandBase
	result.SavingMonthly = float64(result.SpotTasks) * (list.Monthly(in.CPUUnits, in.MemoryMiB) - spot.Monthly(in.CPUUnits, in.MemoryMiB))
	return result
}

// gateSpot is the first failing hard gate, in the order the statuses are listed from the bottom up.
func gateSpot(in SpotInput) SpotStatus {
	if !in.RequiresFargate || in.UnpricedTasks > 0 {
		return SpotNotFargate
	}
	if IsWindows(in.OSFamily) {
		return SpotWindows
	}
	_, hasList := RateFor(in.Region, in.Architecture)
	_, hasSpot := SpotRateFor(in.Region, in.Architecture)
	if !hasList || !hasSpot {
		return SpotNoPrice
	}
	if in.OnDemandTasks == 0 {
		return SpotNotRunning
	}
	if !in.ControllerSupported {
		return SpotUnsupportedController
	}
	if slices.ContainsFunc(in.Strategy, func(provider string) bool { return provider != "FARGATE" && provider != "FARGATE_SPOT" }) {
		return SpotCustomCapacity
	}
	if slices.Contains(in.Strategy, "FARGATE_SPOT") || in.SpotTasks > 0 {
		return SpotAlreadySpot
	}
	if !in.NonProduction {
		return SpotProduction
	}
	if in.Desired < SpotMinTasks {
		return SpotSingleTask
	}
	if !in.ClusterHasSpot {
		return SpotNoClusterSpot
	}
	return SpotCandidate
}

// stopEvidence reads deployment and scale-in stops: they are the stops a service sees when its tasks get SIGTERM.
// Exit 137 means the container did not stop within its stop timeout and was killed.
func stopEvidence(in SpotInput) StopEvidence {
	switch {
	case in.ExpectedStops7d == 0:
		return StopUnknown
	case in.KilledStops7d > 0:
		return StopKilled
	default:
		return StopGraceful
	}
}
