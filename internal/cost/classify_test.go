package cost

import "testing"

func TestClassify(t *testing.T) {
	cases := []struct {
		name             string
		capacityProvider string
		launchType       string
		cpu              string
		memory           string
		want             TaskClass
	}{
		{"fargate capacity provider", "FARGATE", "", "256", "512", OnDemand},
		{"fargate launch type", "", "FARGATE", "256", "512", OnDemand},
		{"fargate spot", "FARGATE_SPOT", "FARGATE", "256", "512", Spot},
		{"ec2 launch type", "", "EC2", "256", "512", Unpriced},
		{"custom ec2 capacity provider", "my-asg-provider", "EC2", "256", "512", Unpriced},
		{"fargate without size", "FARGATE", "FARGATE", "", "", Unpriced},
		{"external launch type", "", "EXTERNAL", "256", "512", Unpriced},
		{"fargate zero cpu", "FARGATE", "", "0", "512", Unpriced},
		{"fargate zero memory", "FARGATE", "", "256", "0", Unpriced},
		{"fargate negative cpu", "FARGATE", "", "-1", "512", Unpriced},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify(tc.capacityProvider, tc.launchType, tc.cpu, tc.memory)
			if got != tc.want {
				t.Fatalf("Classify(%q, %q, %q, %q) = %v, want %v",
					tc.capacityProvider, tc.launchType, tc.cpu, tc.memory, got, tc.want)
			}
		})
	}
}
