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
