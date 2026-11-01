package network

import "testing"

func TestSubnetIPs(t *testing.T) {
	rows := []struct {
		cidr        string
		available   int
		used, total int
	}{
		{"10.20.1.0/24", 224, 27, 251},
		{"172.31.0.0/20", 4091, 0, 4091},
		{"10.0.0.0/28", 11, 0, 11},
		{"10.0.0.0/24", 300, 0, 251},
		{"10.0.0.0/24", -4, 251, 251},
		{"", 0, 0, 0},
		{"garbage", 5, 0, 0},
	}
	for _, row := range rows {
		used, total := SubnetIPs(row.cidr, row.available)
		if used != row.used || total != row.total {
			t.Errorf("SubnetIPs(%q, %d) = (%d, %d), want (%d, %d)", row.cidr, row.available, used, total, row.used, row.total)
		}
	}
}

func TestShortTaskID(t *testing.T) {
	got := ShortTaskID("arn:aws:ecs:us-east-1:4821:task/core-services/6faba45f1c2d3e4f")
	if got != "6faba45f" {
		t.Errorf("got %q", got)
	}
	if ShortTaskID("abc") != "abc" {
		t.Error("short ids are returned whole")
	}
}
