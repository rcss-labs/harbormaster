package metrics

import (
	"encoding/json"
	"regexp"
	"slices"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/model"
)

var legalID = regexp.MustCompile(`^[a-z][a-zA-Z0-9_]*$`)

func dimMap(q Query) map[string]string {
	m := map[string]string{}
	for _, d := range q.Dimensions {
		m[d.Name] = d.Value
	}
	return m
}

func TestServiceQueries_ShapeAndDimensions(t *testing.T) {
	qs := ServiceQueries("core-services", "payments-api", 300)
	var ids []string
	for _, q := range qs {
		ids = append(ids, q.ID)
		d := dimMap(q)
		if q.Namespace != "ECS/ContainerInsights" || q.Period != 300 || len(d) != 2 ||
			d["ClusterName"] != "core-services" || d["ServiceName"] != "payments-api" {
			t.Errorf("bad query %+v", q)
		}
		if !legalID.MatchString(q.ID) {
			t.Errorf("illegal id %q", q.ID)
		}
	}
	want := []string{"cpu_u", "cpu_r", "mem_u", "mem_r", "net_rx", "net_tx", "tasks"}
	if !slices.Equal(ids, want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
}

func TestClusterAndTaskQueries(t *testing.T) {
	cq := ClusterQueries("core-services", 60)
	if len(cq) != 4 || len(dimMap(cq[0])) != 1 {
		t.Fatalf("cluster queries: %+v", cq)
	}
	tq := TaskQueries("core-services", "payments-api", "ab-12.cd", 60)
	if len(tq) != 4 {
		t.Fatalf("task queries: %d", len(tq))
	}
	for _, q := range tq {
		d := dimMap(q)
		if d["ClusterName"] != "core-services" || d["TaskDefinitionFamily"] != "payments-api" || d["TaskId"] != "ab-12.cd" {
			t.Errorf("dims %+v", q.Dimensions)
		}
		if !legalID.MatchString(q.ID) {
			t.Errorf("illegal id %q", q.ID)
		}
	}
	if tq[0].ID != "cpu_u_ab_12_cd" {
		t.Errorf("id = %q", tq[0].ID)
	}
}

func TestALBQueries_TwoTargetGroups(t *testing.T) {
	qs := ALBQueries([]TargetGroupRef{
		{"arn:aws:elasticloadbalancing:eu-west-1:1234:targetgroup/payments-api-tg/def", "arn:aws:elasticloadbalancing:eu-west-1:1234:loadbalancer/app/core/abc"},
		{"arn:aws:elasticloadbalancing:eu-west-1:1234:targetgroup/other-tg/ghi", "arn:aws:elasticloadbalancing:eu-west-1:1234:loadbalancer/app/core/abc"},
	}, 300)
	if len(qs) != 10 {
		t.Fatalf("len = %d", len(qs))
	}
	byID := map[string]Query{}
	for _, q := range qs {
		byID[q.ID] = q
		if q.Namespace != "AWS/ApplicationELB" || !legalID.MatchString(q.ID) {
			t.Errorf("bad %+v", q)
		}
	}
	d := dimMap(byID["req_0"])
	if d["LoadBalancer"] != "app/core/abc" || d["TargetGroup"] != "targetgroup/payments-api-tg/def" || byID["req_0"].Stat != "Sum" {
		t.Errorf("req_0 = %+v", byID["req_0"])
	}
	if byID["p95_1"].Stat != "p95" || byID["p95_1"].MetricName != "TargetResponseTime" {
		t.Errorf("p95_1 = %+v", byID["p95_1"])
	}
	if byID["e5xx_1"].MetricName != "HTTPCode_Target_5XX_Count" || byID["unhealthy_0"].MetricName != "UnHealthyHostCount" || byID["healthy_0"].Stat != "Average" {
		t.Errorf("unexpected metrics")
	}
}

func TestARNSuffix_Table(t *testing.T) {
	cases := []struct {
		in, want string
		err      bool
	}{
		{"arn:aws:elasticloadbalancing:eu-west-1:1234:targetgroup/x/abc", "targetgroup/x/abc", false},
		{"arn:aws:elasticloadbalancing:eu-west-1:1234:loadbalancer/app/x/abc", "app/x/abc", false},
		{"not-an-arn", "", true},
		{"arn:aws:s3:::bucket", "", true},
		{"", "", true},
	}
	for _, c := range cases {
		got, err := ARNSuffix(c.in)
		if (err != nil) != c.err || got != c.want {
			t.Errorf("ARNSuffix(%q) = %q, %v", c.in, got, err)
		}
	}
}

func TestInsightsMode_FromRawSettings(t *testing.T) {
	cases := []struct {
		name string
		c    model.Cluster
		want string
	}{
		{"lower-case enhanced", model.Cluster{ContainerInsights: true, Raw: json.RawMessage(`{"settings":[{"name":"containerInsights","value":"enhanced"}]}`)}, "enhanced"},
		{"sdk-cased enhanced", model.Cluster{ContainerInsights: true, Raw: json.RawMessage(`{"Settings":[{"Name":"containerInsights","Value":"enhanced"}]}`)}, "enhanced"},
		{"sdk-cased enabled", model.Cluster{ContainerInsights: true, Raw: json.RawMessage(`{"Settings":[{"Name":"containerInsights","Value":"enabled"}]}`)}, "enabled"},
		{"no settings", model.Cluster{Raw: json.RawMessage(`{}`)}, "disabled"},
		{"nil raw", model.Cluster{}, "disabled"},
		{"flag only", model.Cluster{ContainerInsights: true}, "enabled"},
		{"explicit disabled", model.Cluster{Raw: json.RawMessage(`{"Settings":[{"Name":"containerInsights","Value":"disabled"}]}`)}, "disabled"},
		{"bad json", model.Cluster{Raw: json.RawMessage(`{`)}, "disabled"},
	}
	for _, c := range cases {
		if got := InsightsMode(c.c); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestServiceTargetGroups_SkipsUnknownLB(t *testing.T) {
	s := model.Service{LoadBalancers: []model.ServiceLoadBalancer{{TargetGroupARN: "tg1"}, {TargetGroupARN: "tg2"}, {TargetGroupARN: "tg3"}}}
	th := []model.TargetHealth{
		{TargetGroupARN: "tg1", LoadBalancerARN: "lb1"},
		{TargetGroupARN: "tg2"},
		{TargetGroupARN: "other", LoadBalancerARN: "lbx"},
	}
	got := ServiceTargetGroups(s, th)
	want := []TargetGroupRef{{"tg1", "lb1"}}
	if !slices.Equal(got, want) {
		t.Fatalf("got %+v", got)
	}
}
