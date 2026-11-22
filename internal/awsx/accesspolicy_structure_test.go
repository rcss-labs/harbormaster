package awsx_test

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// These tests read terraform/modules/harbormaster-access/main.tf as text. They do not parse HCL fully: they match
// braces, which is enough for the shapes this file uses (statement and dynamic "statement" blocks, quoted action lists).
// They need no database, so they run everywhere.

func accessModuleSource(t *testing.T) string {
	t.Helper()
	src, err := os.ReadFile(accessModuleMain)
	if err != nil {
		t.Fatal(err)
	}
	return string(src)
}

// braceBlock returns the text from the first "{" at or after start up to its matching "}", and the index after it.
func braceBlock(t *testing.T, src string, start int) (string, int) {
	t.Helper()
	open := strings.Index(src[start:], "{")
	if open < 0 {
		t.Fatalf("no block after offset %d", start)
	}
	depth := 0
	for i := start + open; i < len(src); i++ {
		switch src[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[start+open : i+1], i + 1
			}
		}
	}
	t.Fatalf("unbalanced braces after offset %d", start)
	return "", 0
}

// policyDocument returns the body of data "aws_iam_policy_document" "<name>".
func policyDocument(t *testing.T, src, name string) string {
	t.Helper()
	header := `data "aws_iam_policy_document" "` + name + `"`
	at := strings.Index(src, header)
	if at < 0 {
		t.Fatalf("policy document %q not found in %s", name, accessModuleMain)
	}
	body, _ := braceBlock(t, src, at)
	return body
}

// statements returns each top-level statement or dynamic "statement" block of a document body.
func statements(t *testing.T, doc string) []string {
	t.Helper()
	inner := doc[1 : len(doc)-1]
	var out []string
	start := regexp.MustCompile(`(?m)^\s*(dynamic "statement"|statement)\s*\{`)
	for pos := 0; ; {
		loc := start.FindStringIndex(inner[pos:])
		if loc == nil {
			return out
		}
		from := pos + loc[0]
		block, end := braceBlock(t, inner, from)
		// The header before the brace is kept so a dynamic statement is recognisable.
		out = append(out, inner[from:from+strings.Index(inner[from:], "{")]+block)
		pos = end
	}
}

func stripComments(s string) string {
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "#") {
			continue
		}
		lines = append(lines, l)
	}
	return strings.Join(lines, "\n")
}

var quotedAction = regexp.MustCompile(`"([a-z0-9-]+:[A-Za-z*]+)"`)

func actionsOf(statement string) []string {
	var out []string
	for _, line := range strings.Split(stripComments(statement), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "variable") {
			continue
		}
		for _, m := range quotedAction.FindAllStringSubmatch(line, -1) {
			out = append(out, m[1])
		}
	}
	return out
}

func TestAccessPolicies_StatementsAreAllowOnlyAndInsideTheMarkers(t *testing.T) {
	src := accessModuleSource(t)
	for _, role := range []string{"poller", "coordinator"} {
		doc := stripComments(policyDocument(t, src, role))
		for _, forbidden := range []string{"not_actions", "not_resources", "not_principals"} {
			if strings.Contains(stripComments(doc), forbidden) {
				t.Errorf("%s document uses %s", role, forbidden)
			}
		}
		stmts := statements(t, doc)
		if len(stmts) == 0 {
			t.Fatalf("%s document has no statements", role)
		}
		var all []string
		for _, s := range stmts {
			if !regexp.MustCompile(`effect\s*=\s*"Allow"`).MatchString(s) {
				t.Errorf("%s: every statement must say effect = \"Allow\":\n%s", role, s)
			}
			all = append(all, actionsOf(s)...)
		}
		// Every action in the document is one the policy test reads from between the markers.
		marked := policyActions(t, role)
		if strings.Join(all, ",") != strings.Join(marked, ",") {
			t.Errorf("%s: actions in the document %v differ from the actions between the policy markers %v", role, all, marked)
		}
		// Nothing but statements in the document: no stray data between them.
		rest := stripComments(doc[1 : len(doc)-1])
		for _, s := range stmts {
			rest = strings.Replace(rest, s, "", 1)
		}
		if strings.TrimSpace(rest) != "" {
			t.Errorf("%s: the document has content outside its statements:\n%s", role, rest)
		}
	}
}

func TestAccessPolicies_ConditionsMatchTheServiceTheyGuard(t *testing.T) {
	src := accessModuleSource(t)
	namespace := regexp.MustCompile(`variable\s*=\s*"application-autoscaling:service-namespace"`)
	for _, s := range statements(t, stripComments(policyDocument(t, src, "coordinator"))) {
		hasNamespace := namespace.MatchString(s)
		for _, a := range actionsOf(s) {
			isScaling := strings.HasPrefix(a, "application-autoscaling:") && !strings.HasPrefix(a, "application-autoscaling:Describe")
			// ECS, ECR, CloudWatch and IAM requests do not carry the autoscaling namespace key: a condition on it
			// would deny them.
			if hasNamespace && !strings.HasPrefix(a, "application-autoscaling:") {
				t.Errorf("%s sits under the application-autoscaling:service-namespace condition, which its requests do not carry (AccessDenied):\n%s", a, s)
			}
			if isScaling && !hasNamespace {
				t.Errorf("%s must be limited to the ecs namespace:\n%s", a, s)
			}
		}
	}
}

func TestAccessPolicies_CoordinatorWritesStayBehindReadOnly(t *testing.T) {
	src := accessModuleSource(t)
	writes := map[string]bool{
		"ecs:UpdateService": true, "iam:CreateServiceLinkedRole": true,
		"application-autoscaling:RegisterScalableTarget": true, "application-autoscaling:DeregisterScalableTarget": true,
		"application-autoscaling:PutScheduledAction": true, "application-autoscaling:DeleteScheduledAction": true,
	}
	gate := regexp.MustCompile(`for_each\s*=\s*var\.read_only\s*\?\s*\[\]\s*:\s*\[1\]`)
	seen := map[string]bool{}
	for _, s := range statements(t, stripComments(policyDocument(t, src, "coordinator"))) {
		for _, a := range actionsOf(s) {
			if !writes[a] {
				continue
			}
			seen[a] = true
			if !strings.HasPrefix(strings.TrimSpace(s), `dynamic "statement"`) || !gate.MatchString(s) {
				t.Errorf("%s is a write and must be in a dynamic statement gated by var.read_only:\n%s", a, s)
			}
		}
	}
	for a := range writes {
		if !seen[a] {
			t.Errorf("expected coordinator write %s in a gated statement", a)
		}
	}
}

func TestAccessPolicies_TrustNamesOnlyTheIntendedPrincipalWithExternalID(t *testing.T) {
	src := accessModuleSource(t)
	cases := []struct{ doc, identifiers string }{
		{"trust", "compact([var.harbormaster_task_role_arn, var.web_task_role_arn])"},
		{"poller_trust", "[var.poller_task_role_arn]"},
		{"coordinator_trust", "[var.coordinator_task_role_arn]"},
	}
	for _, c := range cases {
		doc := stripComments(policyDocument(t, src, c.doc))
		stmts := statements(t, doc)
		if len(stmts) != 1 {
			t.Errorf("%s: want exactly one statement, got %d", c.doc, len(stmts))
			continue
		}
		s := stmts[0]
		if !regexp.MustCompile(`effect\s*=\s*"Allow"`).MatchString(s) {
			t.Errorf("%s: statement must be Allow", c.doc)
		}
		if got := actionsOf(s); len(got) != 1 || got[0] != "sts:AssumeRole" {
			t.Errorf("%s: actions = %v, want only sts:AssumeRole", c.doc, got)
		}
		ids := regexp.MustCompile(`identifiers\s*=\s*(.+)`).FindAllStringSubmatch(s, -1)
		if len(ids) != 1 || strings.TrimSpace(ids[0][1]) != c.identifiers {
			t.Errorf("%s: principal identifiers = %v, want exactly %s", c.doc, ids, c.identifiers)
		}
		if !regexp.MustCompile(`type\s*=\s*"AWS"`).MatchString(s) || strings.Count(s, "principals {") != 1 {
			t.Errorf("%s: want one AWS principal block", c.doc)
		}
		cond := regexp.MustCompile(`test\s*=\s*"StringEquals"\s*\n\s*variable\s*=\s*"sts:ExternalId"\s*\n\s*values\s*=\s*\[var\.external_id\]`)
		if !cond.MatchString(s) {
			t.Errorf("%s: must require sts:ExternalId = var.external_id with StringEquals", c.doc)
		}
	}
}
