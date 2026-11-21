package cost

import (
	"math"
	"testing"
)

var armImage = ImageFacts{Status: "ok", Known: true, IsIndex: true, Platforms: []string{"linux/amd64", "linux/arm64"}, Digest: "sha256:a"}

func armInput() ARM64Input {
	return ARM64Input{
		Region: "us-east-1", Architecture: "X86_64", OSFamily: "LINUX", RequiresFargate: true,
		Tasks:  []PricedTask{{Class: OnDemand, CPUUnits: 512, MemoryMiB: 1024}},
		Images: []ImageCheck{CheckImage("app", "app:1", armImage)},
	}
}

func TestEvaluateARM64_GateOrder(t *testing.T) {
	cases := []struct {
		name   string
		change func(*ARM64Input)
		want   ARM64Status
	}{
		{"every gate passes", func(*ARM64Input) {}, ARM64Ready},
		{"already ARM64", func(in *ARM64Input) { in.Architecture = "ARM64" }, ARM64AlreadyARM64},
		{"not Fargate", func(in *ARM64Input) { in.RequiresFargate = false }, ARM64NotFargate},
		{"unpriced tasks", func(in *ARM64Input) { in.UnpricedTasks = 1 }, ARM64NotFargate},
		{"windows", func(in *ARM64Input) { in.OSFamily = "WINDOWS_SERVER_2022_CORE" }, ARM64Windows},
		{"no price", func(in *ARM64Input) { in.Region = "ap-south-1" }, ARM64NoPrice},
		{"no Spot price for a Spot task", func(in *ARM64Input) {
			in.Tasks = []PricedTask{{Class: Spot, CPUUnits: 512, MemoryMiB: 1024}}
			in.Region = "ap-south-1"
		}, ARM64NoPrice},
		{"not running", func(in *ARM64Input) { in.Tasks = nil }, ARM64NotRunning},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := armInput()
			tc.change(&in)
			if got := EvaluateARM64(in).Status; got != tc.want {
				t.Fatalf("status = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestEvaluateARM64_ReadyNeedsEveryImageOnArm64(t *testing.T) {
	in := armInput()
	in.Images = append(in.Images, CheckImage("sidecar", "sidecar:1", armImage))
	if got := EvaluateARM64(in).Status; got != ARM64Ready {
		t.Fatalf("status = %q, want ready when both images have an arm64 entry", got)
	}
	in.Images = append(in.Images, CheckImage("late", "late:1", ImageFacts{}))
	if got := EvaluateARM64(in).Status; got == ARM64Ready {
		t.Fatal("a third, unchecked image still left the service ready")
	}
}

func TestEvaluateARM64_BlockedNamesTheImage(t *testing.T) {
	single := ImageFacts{Status: "ok", Known: true, IsIndex: false, Digest: "sha256:b"}
	in := armInput()
	in.Images = []ImageCheck{CheckImage("app", "app:1", armImage), CheckImage("sidecar", "sidecar:2", single)}
	result := EvaluateARM64(in)
	if result.Status != ARM64Blocked || len(result.Blockers) != 1 {
		t.Fatalf("result = %+v, want blocked by one image", result)
	}
	blocker := result.Blockers[0]
	if blocker.Container != "sidecar" || blocker.Image != "sidecar:2" || blocker.State != "no_arm64" || blocker.Reason != "single_architecture" {
		t.Fatalf("blocker = %+v, want the single-architecture sidecar", blocker)
	}
}

func TestEvaluateARM64_UnknownIsNotReady(t *testing.T) {
	cases := []struct {
		name       string
		facts      ImageFacts
		wantReason string
	}{
		{"not tracked yet", ImageFacts{}, "pending"},
		{"pending", ImageFacts{Status: "pending"}, "pending"},
		{"ok without a stored manifest", ImageFacts{Status: "ok", Digest: "sha256:c"}, "pending"},
		{"registry not checked", ImageFacts{Status: "registry_not_checked"}, "registry_not_checked"},
		{"no permission", ImageFacts{Status: "no_permission", Detail: "123456789012"}, "no_permission"},
		{"not connected", ImageFacts{Status: "not_connected", Detail: "123456789012"}, "not_connected"},
		{"not found", ImageFacts{Status: "not_found"}, "not_found"},
		{"denied", ImageFacts{Status: "denied"}, "denied"},
		{"error", ImageFacts{Status: "error"}, "error"},
		{"invalid", ImageFacts{Status: "invalid"}, "error"},
		{"a status the code does not know", ImageFacts{Status: "brand_new"}, "error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := armInput()
			in.Images = []ImageCheck{CheckImage("app", "app:1", tc.facts)}
			result := EvaluateARM64(in)
			if result.Status != ARM64Unknown || len(result.Blockers) != 1 || result.Blockers[0].State != "unchecked" || result.Blockers[0].Reason != tc.wantReason {
				t.Fatalf("result = %+v, want unknown with reason %q (never ready)", result, tc.wantReason)
			}
		})
	}
}

func TestCheckImage_KeepsTheAccountForPermissionAndConnection(t *testing.T) {
	check := CheckImage("app", "app:1", ImageFacts{Status: "no_permission", Detail: "123456789012"})
	if check.Detail != "123456789012" {
		t.Fatalf("detail = %q, want the account id", check.Detail)
	}
}

func TestEvaluateARM64_IndexWithoutArm64Blocks(t *testing.T) {
	in := armInput()
	in.Images = []ImageCheck{CheckImage("app", "app:1", ImageFacts{Status: "ok", Known: true, IsIndex: true, Platforms: []string{"linux/amd64", "linux/386"}})}
	result := EvaluateARM64(in)
	if result.Status != ARM64Blocked || result.Blockers[0].Reason != "no_arm64_in_index" {
		t.Fatalf("result = %+v, want blocked: only a linux/arm64 entry counts", result)
	}
	// A variant still counts; another OS does not.
	in.Images = []ImageCheck{CheckImage("app", "app:1", ImageFacts{Status: "ok", Known: true, IsIndex: true, Platforms: []string{"linux/arm64/v8"}})}
	if got := EvaluateARM64(in).Status; got != ARM64Ready {
		t.Fatalf("status = %q, want ready for linux/arm64/v8", got)
	}
	in.Images = []ImageCheck{CheckImage("app", "app:1", ImageFacts{Status: "ok", Known: true, IsIndex: true, Platforms: []string{"windows/arm64"}})}
	if got := EvaluateARM64(in).Status; got != ARM64Blocked {
		t.Fatalf("status = %q, want blocked for windows/arm64", got)
	}
}

func TestEvaluateARM64_BlockedWinsOverUnknown(t *testing.T) {
	in := armInput()
	in.Images = []ImageCheck{CheckImage("a", "a:1", ImageFacts{}), CheckImage("b", "b:1", ImageFacts{Status: "ok", Known: true, Platforms: nil})}
	result := EvaluateARM64(in)
	if result.Status != ARM64Blocked || len(result.Blockers) != 1 || result.Blockers[0].Container != "b" {
		t.Fatalf("result = %+v, want blocked, naming only the image that is known to lack arm64", result)
	}
}

func TestEvaluateARM64_SavingPerTaskClass(t *testing.T) {
	in := armInput()
	in.Tasks = []PricedTask{{Class: OnDemand, CPUUnits: 512, MemoryMiB: 1024}, {Class: Spot, CPUUnits: 512, MemoryMiB: 1024}}
	want := (18.02 - 14.42) + (6.60 - 5.28)
	if got := EvaluateARM64(in).SavingMonthly; math.Abs(got-want) > 0.01 {
		t.Fatalf("saving = %.4f, want %.4f", got, want)
	}
	// A blocked service still shows what is at stake.
	in.Images = []ImageCheck{CheckImage("app", "app:1", ImageFacts{Status: "ok", Known: true})}
	if got := EvaluateARM64(in).SavingMonthly; math.Abs(got-want) > 0.01 {
		t.Fatalf("blocked saving = %.4f, want %.4f", got, want)
	}
}

func TestImagesReadiness(t *testing.T) {
	status, blockers := ImagesReadiness([]ImageCheck{CheckImage("a", "a:1", armImage)})
	if status != ARM64Ready || len(blockers) != 0 {
		t.Fatalf("readiness = %q, %v", status, blockers)
	}
	// Nothing was checked, so nothing was shown to work: never ready.
	if status, _ := ImagesReadiness(nil); status != ARM64Unknown {
		t.Errorf("no images: readiness = %q, want unknown", status)
	}
}
