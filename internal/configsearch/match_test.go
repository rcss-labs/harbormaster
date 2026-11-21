package configsearch

import (
	"reflect"
	"strconv"
	"testing"
)

func mustParse(t *testing.T, s string) Query {
	t.Helper()
	q, err := Parse(s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return q
}

func sampleDoc() Doc {
	return Doc{
		TaskRoleARN:      "arn:aws:iam::111:role/payments-task",
		ExecutionRoleARN: "arn:aws:iam::111:role/ecs-exec",
		Containers: []Container{
			{
				Name: "api", Image: "111.dkr.ecr.us-east-1.amazonaws.com/payments:2.1.0", Tag: "2.1.0",
				BaseImage: "node:18-alpine", LogGroup: "/ecs/payments",
				Ports: []int{8080}, EnvNames: []string{"PORT", "SUPPORT_EMAIL"}, SecretNames: []string{"API_KEY"},
			},
			{Name: "sidecar", Image: "nginx:1.27", Tag: "1.27", Ports: []int{80}, EnvNames: []string{"NGINX_HOST"}},
		},
	}
}

func matches(t *testing.T, query string, d Doc) bool {
	t.Helper()
	_, ok := Match(mustParse(t, query), d)
	return ok
}

func TestMatch_SubstringExactAndCase(t *testing.T) {
	d := sampleDoc()
	hits, ok := Match(mustParse(t, "env:port"), d)
	if !ok {
		t.Fatal("env:port should match")
	}
	var values []string
	for _, h := range hits {
		values = append(values, h.Value)
	}
	if !reflect.DeepEqual(values, []string{"PORT", "SUPPORT_EMAIL"}) {
		t.Errorf("substring hits = %v", values)
	}
	hits, ok = Match(mustParse(t, `env:"port"`), d)
	if !ok || len(hits) != 1 || hits[0].Value != "PORT" {
		t.Errorf("exact hits = %+v ok=%v", hits, ok)
	}
	if matches(t, `env:"PO"`, d) {
		t.Error("exact must match the whole value")
	}
}

func TestMatch_PortIsExact(t *testing.T) {
	d := sampleDoc()
	if matches(t, "port:808", d) {
		t.Error("port:808 must not match 8080")
	}
	if !matches(t, "port:80", d) {
		t.Error("port:80 matches the sidecar")
	}
	if matches(t, "port:9090", d) {
		t.Error("port:9090 must not match")
	}
	hits, ok := Match(mustParse(t, "8080"), d)
	if !ok || hits[0].Field != "port" || hits[0].Value != "8080" || hits[0].Container != "api" {
		t.Errorf("free text 8080 hits = %+v ok=%v", hits, ok)
	}
}

func TestMatch_PortIsNormalised(t *testing.T) {
	d := sampleDoc()
	for _, q := range []string{"port:080", "port:+80", "ports:0080"} {
		if !matches(t, q, d) {
			t.Errorf("%q should match port 80", q)
		}
	}
	if matches(t, "-port:080", d) {
		t.Error("-port:080 must exclude a service with port 80")
	}
	if !matches(t, "env:PORT -port:0443", d) {
		t.Error("-port:0443 must not exclude a service without port 443")
	}
}

func TestMatch_TermsAreAndedAcrossContainers(t *testing.T) {
	d := sampleDoc()
	if !matches(t, "image:nginx env:PORT secret:API_KEY", d) {
		t.Error("terms in different containers should both hold")
	}
	if matches(t, "image:nginx env:NOPE", d) {
		t.Error("a missing term fails the whole doc")
	}
}

func TestMatch_NegationExcludes(t *testing.T) {
	d := sampleDoc()
	if matches(t, "image:nginx -secret:API_KEY", d) {
		t.Error("negated term present must fail")
	}
	if !matches(t, "image:nginx -secret:OTHER", d) {
		t.Error("negated term absent must hold")
	}
}

func TestMatch_OnlyNegatedTerms(t *testing.T) {
	d := sampleDoc()
	hits, ok := Match(mustParse(t, "-secret:OTHER"), d)
	if !ok || len(hits) != 0 {
		t.Errorf("hits=%+v ok=%v", hits, ok)
	}
	if _, ok := Match(mustParse(t, "-secret:API_KEY"), d); ok {
		t.Error("should fail when the negated value exists")
	}
}

func TestMatch_RoleFields(t *testing.T) {
	d := sampleDoc()
	if !matches(t, "role:payments-task", d) || !matches(t, "role:ecs-exec", d) {
		t.Error("role: matches either ARN")
	}
	if !matches(t, "taskrole:payments-task", d) || matches(t, "taskrole:ecs-exec", d) {
		t.Error("taskrole: only the task role")
	}
	if !matches(t, "execrole:ecs-exec", d) || matches(t, "execrole:payments-task", d) {
		t.Error("execrole: only the execution role")
	}
	hits, _ := Match(mustParse(t, "role:payments-task"), d)
	if len(hits) != 1 || hits[0].Field != "taskrole" || hits[0].Container != "" {
		t.Errorf("hits = %+v", hits)
	}
}

func TestMatch_FieldsAndFreeText(t *testing.T) {
	d := sampleDoc()
	for _, q := range []string{
		"tag:2.1.0", "image:payments", "image:nginx:1.27", "base:node:18", "loggroup:/ecs/pay",
		"secret:api", "payments-task", "node:18", "/ecs/payments", "API_KEY",
	} {
		if !matches(t, q, d) {
			t.Errorf("%q should match", q)
		}
	}
	if matches(t, "sidecar", d) {
		t.Error("free text must not match container names")
	}
	if matches(t, "tag:payments", d) {
		t.Error("tag: matches only the tag")
	}
}

func TestMatch_HitsNameFieldContainerAndValue(t *testing.T) {
	d := sampleDoc()
	hits, ok := Match(mustParse(t, "env:port env:PORT"), d)
	if !ok || len(hits) != 2 {
		t.Fatalf("hits not deduplicated: %+v", hits)
	}
	want := Hit{Field: "env", Container: "api", Value: "PORT"}
	if hits[0] != want {
		t.Errorf("hits[0] = %+v, want %+v", hits[0], want)
	}

	big := Doc{Containers: []Container{{Name: "c"}}}
	for i := 0; i < 30; i++ {
		big.Containers[0].EnvNames = append(big.Containers[0].EnvNames, "VAR_"+strconv.Itoa(i))
	}
	hits, _ = Match(mustParse(t, "env:VAR_"), big)
	if len(hits) != 20 {
		t.Errorf("hits capped at 20, got %d", len(hits))
	}
}

func TestMatch_EmptyQueryMatchesNothing(t *testing.T) {
	hits, ok := Match(Query{}, sampleDoc())
	if ok || len(hits) != 0 {
		t.Errorf("hits=%+v ok=%v", hits, ok)
	}
}
