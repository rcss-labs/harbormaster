package cost

import "strings"

// ARM64Status says whether a service can move to ARM64, or the first reason it cannot be evaluated.
type ARM64Status string

const (
	ARM64Ready        ARM64Status = "ready"
	ARM64Blocked      ARM64Status = "blocked"
	ARM64Unknown      ARM64Status = "unknown"
	ARM64AlreadyARM64 ARM64Status = "already_arm64"
	ARM64NotFargate   ARM64Status = "not_fargate"
	ARM64Windows      ARM64Status = "windows"
	ARM64NoPrice      ARM64Status = "no_price"
	ARM64NotRunning   ARM64Status = "not_running"
)

// ImageFacts is what the platform check stored about one image; it mirrors store.ImagePlatforms without importing store.
// The zero value is an image that is not tracked yet.
type ImageFacts struct {
	Status, Detail, Digest string
	Known, IsIndex         bool
	Platforms              []string
}

// ImageCheck is the verdict on one container image. State is arm64, no_arm64 or unchecked; Reason says why for the last two.
type ImageCheck struct{ Container, Image, State, Reason, Detail string }

// PricedTask is one running priced task.
type PricedTask struct {
	Class               TaskClass
	CPUUnits, MemoryMiB int
}

// ARM64Input is everything EvaluateARM64 needs about one service.
type ARM64Input struct {
	Region, Architecture, OSFamily string
	RequiresFargate                bool
	UnpricedTasks                  int
	Tasks                          []PricedTask
	Images                         []ImageCheck
}

// ARM64Result is the verdict, the saving and the images that keep a service from being ready.
type ARM64Result struct {
	Status        ARM64Status
	SavingMonthly float64
	Blockers      []ImageCheck
}

// CheckImage judges one image from what the check stored. Only a manifest that is an index listing linux/arm64 passes. A
// stored single manifest on an x86 service is an amd64 image. Anything not positively known, whether pending, refused,
// unreadable or in a registry that is not checked, is unchecked, never ready.
func CheckImage(container, image string, p ImageFacts) ImageCheck {
	check := ImageCheck{Container: container, Image: image}
	switch {
	case p.Status == "ok" && p.Known && p.IsIndex:
		if hasLinuxARM64(p.Platforms) {
			check.State = "arm64"
		} else {
			check.State, check.Reason = "no_arm64", "no_arm64_in_index"
		}
	case p.Status == "ok" && p.Known:
		check.State, check.Reason = "no_arm64", "single_architecture"
	default:
		check.State, check.Reason = "unchecked", uncheckedReason(p.Status)
		if p.Status == "no_permission" || p.Status == "not_connected" {
			check.Detail = p.Detail
		}
	}
	return check
}

// hasLinuxARM64 is true for linux/arm64 with or without a variant; another OS never counts.
func hasLinuxARM64(platforms []string) bool {
	for _, platform := range platforms {
		if platform == "linux/arm64" || strings.HasPrefix(platform, "linux/arm64/") {
			return true
		}
	}
	return false
}

// uncheckedReason maps a stored status to the reason shown. A tracked image that resolved but has no stored manifest yet
// is still pending, and a status the code does not know is an error.
func uncheckedReason(status string) string {
	switch status {
	case "", "pending", "ok":
		return "pending"
	case "registry_not_checked", "no_permission", "not_connected", "not_found", "denied":
		return status
	default:
		return "error"
	}
}

// ImagesReadiness reduces image verdicts to one: blocked when any image is known to lack arm64 (naming those), else unknown
// when any is unchecked (naming those), else ready. No images at all is unknown: nothing was shown to work.
func ImagesReadiness(images []ImageCheck) (ARM64Status, []ImageCheck) {
	var blocked, unchecked []ImageCheck
	for _, image := range images {
		switch image.State {
		case "no_arm64":
			blocked = append(blocked, image)
		case "unchecked":
			unchecked = append(unchecked, image)
		}
	}
	switch {
	case len(blocked) > 0:
		return ARM64Blocked, blocked
	case len(unchecked) > 0, len(images) == 0:
		return ARM64Unknown, unchecked
	}
	return ARM64Ready, nil
}

// EvaluateARM64 applies the gates in order (already ARM64, not Fargate, Windows, no price, not running), then judges the
// images. The saving, per running task in its own class, is computed for ready, blocked and unknown so a blocked service
// shows what is at stake.
func EvaluateARM64(in ARM64Input) ARM64Result {
	switch {
	case in.Architecture == "ARM64":
		return ARM64Result{Status: ARM64AlreadyARM64}
	case !in.RequiresFargate || in.UnpricedTasks > 0:
		return ARM64Result{Status: ARM64NotFargate}
	case IsWindows(in.OSFamily):
		return ARM64Result{Status: ARM64Windows}
	}
	if _, ok := RateFor(in.Region, "ARM64"); !ok {
		return ARM64Result{Status: ARM64NoPrice}
	}
	var saving float64
	for _, task := range in.Tasks {
		x86, okX86 := RateForClass(in.Region, "X86_64", task.Class)
		arm, okARM := RateForClass(in.Region, "ARM64", task.Class)
		if !okX86 || !okARM {
			return ARM64Result{Status: ARM64NoPrice}
		}
		saving += x86.Monthly(task.CPUUnits, task.MemoryMiB) - arm.Monthly(task.CPUUnits, task.MemoryMiB)
	}
	if len(in.Tasks) == 0 {
		return ARM64Result{Status: ARM64NotRunning}
	}
	status, blockers := ImagesReadiness(in.Images)
	return ARM64Result{Status: status, SavingMonthly: saving, Blockers: blockers}
}
