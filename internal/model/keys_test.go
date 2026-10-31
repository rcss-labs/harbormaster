package model

import (
	"encoding/base64"
	"testing"
)

const testTaskARN = "arn:aws:ecs:us-east-1:482109375512:task/core-services/0123456789abcdef0123456789abcdef"

func TestClusterKeyID_RoundTrip(t *testing.T) {
	key := ClusterKey{AccountID: "482109375512", Region: "us-east-1", Cluster: "core-services"}

	id := key.ID()
	want := base64.RawURLEncoding.EncodeToString([]byte("482109375512|us-east-1|core-services"))
	if id != want {
		t.Fatalf("ID() = %q, want %q", id, want)
	}

	parsed, err := ParseClusterID(id)
	if err != nil {
		t.Fatalf("ParseClusterID(%q) error: %v", id, err)
	}
	if parsed != key {
		t.Fatalf("round trip = %+v, want %+v", parsed, key)
	}
}

func TestServiceKeyID_RoundTrip(t *testing.T) {
	key := ServiceKey{
		ClusterKey: ClusterKey{AccountID: "482109375512", Region: "us-east-1", Cluster: "core-services"},
		Service:    "notifications",
	}

	parsed, err := ParseServiceID(key.ID())
	if err != nil {
		t.Fatalf("ParseServiceID error: %v", err)
	}
	if parsed != key {
		t.Fatalf("round trip = %+v, want %+v", parsed, key)
	}
}

func TestTaskID_RoundTrip(t *testing.T) {
	id := TaskID("482109375512", "us-east-1", testTaskARN)

	accountID, region, taskARN, err := ParseTaskID(id)
	if err != nil {
		t.Fatalf("ParseTaskID error: %v", err)
	}
	if accountID != "482109375512" || region != "us-east-1" || taskARN != testTaskARN {
		t.Fatalf("round trip = (%q, %q, %q)", accountID, region, taskARN)
	}
}

func TestParseClusterID_Rejects(t *testing.T) {
	encode := func(raw string) string { return base64.RawURLEncoding.EncodeToString([]byte(raw)) }

	cases := map[string]string{
		"not base64":    "!!!not-base64!!!",
		"two fields":    encode("482109375512|us-east-1"),
		"four fields":   encode("482109375512|us-east-1|core|extra"),
		"empty account": encode("|us-east-1|core-services"),
		"empty region":  encode("482109375512||core-services"),
		"empty cluster": encode("482109375512|us-east-1|"),
	}

	for name, id := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseClusterID(id); err == nil {
				t.Fatalf("ParseClusterID(%q) returned nil error", id)
			}
		})
	}
}

func TestMainContainer_PrefersNameMatchThenEssential(t *testing.T) {
	td := TaskDefinitionRef{Containers: []ContainerDef{
		{Name: "sidecar", Essential: false},
		{Name: "log-router", Essential: true},
		{Name: "notifications", Essential: false},
	}}
	if got := td.MainContainer("notifications").Name; got != "notifications" {
		t.Errorf("name match: got %q, want notifications", got)
	}
	if got := td.MainContainer("missing").Name; got != "log-router" {
		t.Errorf("first essential fallback: got %q, want log-router", got)
	}

	noEssential := TaskDefinitionRef{Containers: []ContainerDef{{Name: "a"}, {Name: "b"}}}
	if got := noEssential.MainContainer("missing").Name; got != "a" {
		t.Errorf("Containers[0] fallback: got %q, want a", got)
	}

	empty := TaskDefinitionRef{}
	if got := empty.MainContainer("anything"); got.Name != "" || got.Image != "" {
		t.Errorf("no containers: got %+v, want zero value", got)
	}
}

func TestImageTag(t *testing.T) {
	cases := map[string]string{
		"nginx:1.27":                  "1.27",
		"registry.example.com/app:v3": "v3",
		"host:5000/repo":              "",
		"host:5000/repo:2.1":          "2.1",
		"repo":                        "",
	}
	for image, want := range cases {
		td := TaskDefinitionRef{Containers: []ContainerDef{{Name: "app", Image: image, Essential: true}}}
		if got := td.ImageTag("app"); got != want {
			t.Errorf("ImageTag(%q) = %q, want %q", image, got, want)
		}
	}
}

func TestExitCode_PicksNonZero(t *testing.T) {
	code := func(n int) *int { return &n }

	withFailure := Task{Containers: []Container{
		{Name: "a", ExitCode: code(0)},
		{Name: "b", ExitCode: code(137)},
		{Name: "c", ExitCode: code(1)},
	}}
	if got := withFailure.ExitCode(); got == nil || *got != 137 {
		t.Errorf("highest non-zero: got %v, want 137", got)
	}

	allZero := Task{Containers: []Container{{Name: "a", ExitCode: code(0)}, {Name: "b"}}}
	if got := allZero.ExitCode(); got == nil || *got != 0 {
		t.Errorf("first non-nil when all zero: got %v, want 0", got)
	}

	noneSet := Task{Containers: []Container{{Name: "a"}}}
	if got := noneSet.ExitCode(); got != nil {
		t.Errorf("no exit codes: got %d, want nil", *got)
	}
}
