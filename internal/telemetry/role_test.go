package telemetry

import (
	"strings"
	"testing"
)

func TestPrometheus_RoleInfo(t *testing.T) {
	r := New()
	if out := render(t, r); strings.Contains(out, "harbormaster_role_info{") {
		t.Fatalf("role_info has a sample before SetRoleInfo:\n%s", out)
	}
	r.SetRoleInfo("poller", "v1.2.3")
	out := render(t, r)
	for _, want := range []string{
		"# TYPE harbormaster_role_info gauge",
		`harbormaster_role_info{role="poller",version="v1.2.3"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}
