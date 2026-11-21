package cost

import (
	"math"
	"testing"
)

func TestRateFor_MonthlyByRegionAndArchitecture(t *testing.T) {
	cases := []struct {
		name         string
		region       string
		architecture string
		cpuUnits     int
		memoryMiB    int
		want         float64
	}{
		{"us-east-1 x86 512/1024", "us-east-1", "X86_64", 512, 1024, 18.02},
		{"eu-central-1 ARM 1024/2048", "eu-central-1", "ARM64", 1024, 2048, 33.16},
		{"eu-west-1 ARM 256/512", "eu-west-1", "ARM64", 256, 512, 7.21},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rate, ok := RateFor(tc.region, tc.architecture)
			if !ok {
				t.Fatalf("RateFor(%q, %q) has no price", tc.region, tc.architecture)
			}
			got := rate.Monthly(tc.cpuUnits, tc.memoryMiB)
			if math.Abs(got-tc.want) > 0.01 {
				t.Fatalf("Monthly(%d, %d) = %.4f, want %.2f", tc.cpuUnits, tc.memoryMiB, got, tc.want)
			}
		})
	}
}

func TestRateFor_UnknownRegionOrArchitectureHasNoPrice(t *testing.T) {
	cases := []struct {
		name         string
		region       string
		architecture string
	}{
		{"unpriced region", "ap-south-1", "X86_64"},
		{"unpriced architecture", "us-east-1", "RISCV"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := RateFor(tc.region, tc.architecture); ok {
				t.Fatalf("RateFor(%q, %q) returned a price, want none", tc.region, tc.architecture)
			}
		})
	}
}

func TestSpotRateFor_MonthlyByRegionAndArchitecture(t *testing.T) {
	cases := []struct {
		name         string
		region       string
		architecture string
		cpuUnits     int
		memoryMiB    int
		want         float64
	}{
		{"us-east-1 x86 512/1024", "us-east-1", "X86_64", 512, 1024, 6.60},
		{"us-east-1 ARM 512/1024", "us-east-1", "ARM64", 512, 1024, 5.28},
		{"eu-west-1 x86 256/512", "eu-west-1", "", 256, 512, 2.71},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rate, ok := SpotRateFor(tc.region, tc.architecture)
			if !ok {
				t.Fatalf("SpotRateFor(%q, %q) has no price", tc.region, tc.architecture)
			}
			got := rate.Monthly(tc.cpuUnits, tc.memoryMiB)
			if math.Abs(got-tc.want) > 0.01 {
				t.Fatalf("Monthly(%d, %d) = %.4f, want %.2f", tc.cpuUnits, tc.memoryMiB, got, tc.want)
			}
		})
	}
}

func TestRateForClass_SpotNeverFallsBackToList(t *testing.T) {
	// A region that has a list price but no Spot price.
	saved := fargateSpotRates["eu-west-1"]
	delete(fargateSpotRates, "eu-west-1")
	defer func() { fargateSpotRates["eu-west-1"] = saved }()

	if _, ok := RateForClass("eu-west-1", "X86_64", OnDemand); !ok {
		t.Fatal("on-demand task in a listed region must be priced")
	}
	if _, ok := RateForClass("eu-west-1", "X86_64", Spot); ok {
		t.Fatal("Spot task without a Spot price must not borrow the list price")
	}
	if _, ok := RateForClass("us-east-1", "X86_64", Unpriced); ok {
		t.Fatal("unpriced class must have no rate")
	}
	if _, ok := RateForClass("us-east-1", "RISCV", Spot); ok {
		t.Fatal("unknown architecture must have no rate")
	}
}
