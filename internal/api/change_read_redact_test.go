package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/rcss-labs/harbormaster/internal/apitoken"
	"github.com/rcss-labs/harbormaster/internal/auth"
	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/config"
)

// fakeAccessKey is assembled from two pieces so secret scanners do not flag the test file.
var fakeAccessKey = "AKIA" + "ABCDEFGHIJKLMNOP"

// plantSecretShapes adds one harmless, one secret-named and one secret-valued variable to every auth-service revision.
func plantSecretShapes(f *fake.Fake) {
	f.Estate.Mu.Lock()
	defer f.Estate.Mu.Unlock()
	for i := range f.Estate.TaskDefinitions {
		def := &f.Estate.TaskDefinitions[i].Definition
		if aws.ToString(def.Family) != "auth-service" {
			continue
		}
		main := &def.ContainerDefinitions[0]
		main.Environment = append(main.Environment,
			ecstypes.KeyValuePair{Name: aws.String("DATABASE_HOST"), Value: aws.String("db.internal")},
			ecstypes.KeyValuePair{Name: aws.String("API_KEY"), Value: aws.String("abc")},
			ecstypes.KeyValuePair{Name: aws.String("EXTRA"), Value: aws.String(fakeAccessKey)})
	}
}

// environmentValues maps variable names of every container to their values in one definition answer.
func (d definitionEnvironment) environmentValues() map[string]string {
	out := map[string]string{}
	for _, c := range d.Definition.ContainerDefinitions {
		for _, e := range c.Environment {
			out[e.Name] = e.Value
		}
	}
	return out
}

// requireAllRedacted fails unless got has exactly the variables of real, every one of them [redacted].
func requireAllRedacted(t *testing.T, got, real definitionEnvironment) {
	t.Helper()
	values, want := got.environmentValues(), real.environmentValues()
	if len(want) == 0 || len(values) != len(want) {
		t.Fatalf("got %d variables, the real definition has %d", len(values), len(want))
	}
	for name := range want {
		if value, ok := values[name]; !ok || value != "[redacted]" {
			t.Errorf("%s = %q, want [redacted]", name, value)
		}
	}
}

type redactCase struct {
	redactAll     bool // HM_TOKEN_REDACT_ENV is on
	runTokenFlag  bool // the run token was minted with RedactEnv true (the provider was not lifted)
	personalToken bool // use a personal token instead of a run token
}

// readPlanted answers auth-service's deployed definition (revisionPath empty) or that revision to the client the case
// describes, and the same answer to a session for comparison.
func readPlanted(t *testing.T, c redactCase, revisionPath string) (definitionEnvironment, definitionEnvironment) {
	t.Helper()
	srv, api, fk, st, _ := newServerCustomAPI(t, true, withAgent(withTokens(func(cfg *config.Config) { cfg.TokenRedactEnv = c.redactAll }, nil)))
	seedAccounts(t, st)
	seedMockupTags(t, st)
	plantSecretShapes(fk)
	pollAll(t, st, fk)
	path := "/api/services/" + authServiceID() + "/task-definition"
	if revisionPath != "" {
		path = "/api/services/" + authServiceID() + "/revisions/" + revisionPath
	}
	client := tokenClient(t, srv, st, "sam")
	if !c.personalToken {
		client = runTokenClientRedacting(t, srv, api, st, "sam", c.runTokenFlag)
	}
	return readDefinition(t, client, path), readDefinition(t, login(t, srv, "sam", testPassword), path)
}

func TestTaskDefinition_RunTokenRedactsAllValuesByDefault(t *testing.T) {
	got, real := readPlanted(t, redactCase{runTokenFlag: true}, "")
	if !got.EnvironmentRedacted {
		t.Error("environment_redacted is false")
	}
	requireAllRedacted(t, got, real)
	if _, wantSecrets := real.counts(); wantSecrets == 0 {
		t.Fatal("fixture has no secrets references")
	}
	_, realSecrets := real.counts()
	if _, gotSecrets := got.counts(); gotSecrets != realSecrets {
		t.Error("secret references were dropped")
	}
}

func TestTaskDefinition_RunTokenLiftedStillRedactsSecretShapes(t *testing.T) {
	got, real := readPlanted(t, redactCase{runTokenFlag: false}, "")
	values, wantValues := got.environmentValues(), real.environmentValues()
	if !got.EnvironmentRedacted {
		t.Error("environment_redacted is false although values were replaced")
	}
	if values["DATABASE_HOST"] != "db.internal" {
		t.Errorf("DATABASE_HOST = %q, want it visible", values["DATABASE_HOST"])
	}
	for _, name := range []string{"API_KEY", "EXTRA"} {
		if values[name] != "[redacted]" {
			t.Errorf("%s = %q, want [redacted]", name, values[name])
		}
	}
	for name, value := range values {
		if value != "[redacted]" && value != wantValues[name] {
			t.Errorf("%s = %q, changed from %q", name, value, wantValues[name])
		}
	}
	if values["ISSUER"] != "https://auth.rcss.local" {
		t.Errorf("ISSUER = %q, want its real value in secrets-only mode", values["ISSUER"])
	}
}

// An operator's global switch is never weakened by an admin's per-provider lift.
func TestTaskDefinition_RunTokenLiftIgnoredWhileGlobalSwitchOn(t *testing.T) {
	got, real := readPlanted(t, redactCase{redactAll: true, runTokenFlag: false}, "")
	if !got.EnvironmentRedacted {
		t.Error("environment_redacted is false")
	}
	requireAllRedacted(t, got, real)
}

func TestTaskDefinition_PersonalTokenUnchanged(t *testing.T) {
	got, real := readPlanted(t, redactCase{personalToken: true}, "")
	if got.EnvironmentRedacted || got.environmentValues()["API_KEY"] != "abc" || got.environmentValues()["EXTRA"] != fakeAccessKey {
		t.Errorf("switch off: personal token sees %v redacted=%v, want every real value", got.environmentValues(), got.EnvironmentRedacted)
	}
	if len(got.environmentValues()) != len(real.environmentValues()) {
		t.Error("variables lost")
	}
	on, _ := readPlanted(t, redactCase{personalToken: true, redactAll: true}, "")
	if !on.EnvironmentRedacted || on.environmentValues()["DATABASE_HOST"] != "[redacted]" {
		t.Errorf("switch on: DATABASE_HOST = %q, want [redacted] (all or nothing)", on.environmentValues()["DATABASE_HOST"])
	}
}

func TestRevision_RunTokenRedaction(t *testing.T) {
	all, realRevision := readPlanted(t, redactCase{runTokenFlag: true}, "15")
	if !all.EnvironmentRedacted {
		t.Error("revision: environment_redacted is false")
	}
	requireAllRedacted(t, all, realRevision)
	lifted, _ := readPlanted(t, redactCase{runTokenFlag: false}, "15")
	values := lifted.environmentValues()
	if !lifted.EnvironmentRedacted || values["DATABASE_HOST"] != "db.internal" || values["API_KEY"] != "[redacted]" || values["EXTRA"] != "[redacted]" {
		t.Errorf("revision, lifted: %v redacted=%v", values, lifted.EnvironmentRedacted)
	}
}

func TestRedactionFor_EveryBranch(t *testing.T) {
	cases := []struct {
		name     string
		cred     *auth.Credential
		switchOn bool
		want     envRedaction
	}{
		{"no credential", nil, false, redactAll},
		{"session", &auth.Credential{Kind: auth.CredentialSession}, true, redactNone},
		{"pat, switch off", &auth.Credential{Kind: auth.CredentialToken, TokenKind: apitoken.KindPAT}, false, redactNone},
		{"pat, switch on", &auth.Credential{Kind: auth.CredentialToken, TokenKind: apitoken.KindPAT}, true, redactAll},
		{"oauth access, switch off", &auth.Credential{Kind: auth.CredentialToken, TokenKind: apitoken.KindOAuthAccess}, false, redactNone},
		{"oauth access, switch on", &auth.Credential{Kind: auth.CredentialToken, TokenKind: apitoken.KindOAuthAccess}, true, redactAll},
		{"run, not lifted", &auth.Credential{Kind: auth.CredentialToken, TokenKind: apitoken.KindRun, RedactEnv: true}, false, redactAll},
		{"run, lifted", &auth.Credential{Kind: auth.CredentialToken, TokenKind: apitoken.KindRun}, false, redactSecretShapes},
		{"run, lifted, switch on", &auth.Credential{Kind: auth.CredentialToken, TokenKind: apitoken.KindRun}, true, redactAll},
		{"unknown token kind", &auth.Credential{Kind: auth.CredentialToken, TokenKind: "future"}, false, redactAll},
		{"unknown credential kind", &auth.Credential{Kind: 99}, false, redactAll},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var cred auth.Credential
			if c.cred != nil {
				cred = *c.cred
			}
			if got := redactionMode(cred, c.cred != nil, c.switchOn); got != c.want {
				t.Errorf("mode %d, want %d", got, c.want)
			}
		})
	}
}

func definitionWithEnv(entries ...any) map[string]any {
	return map[string]any{"family": "f", "containerDefinitions": []any{
		map[string]any{"name": "app", "environment": entries},
		map[string]any{"name": "sidecar", "environment": []any{map[string]any{"name": "SIDE", "value": "side-secret-value-1"}}},
	}}
}

func TestRedactEnvironment_SecretShapesByValue(t *testing.T) {
	hidden := []string{
		"Server=db;User Id=app;Password=hunter2",
		"-Xmx512m -Dspring.datasource.password=hunter2",
		`{"password":"hunter2"}`,
		"https://x.example/hook?token=abc123def",
	}
	visible := []string{"db.internal:5432", "-Xmx512m", "eu-west-1",
		"123456789012.dkr.ecr.eu-west-1.amazonaws.com/app:1.2.3", "arn:aws:iam::123456789012:role/app-task"}
	var entries []any
	for i, v := range append(append([]string{}, hidden...), visible...) {
		entries = append(entries, map[string]any{"name": "VAR_" + string(rune('A'+i)), "value": v})
	}
	out, flagged := redactEnvironment(definitionWithEnv(entries...), redactSecretShapes)
	if !flagged {
		t.Error("not flagged")
	}
	got := out["containerDefinitions"].([]any)[0].(map[string]any)["environment"].([]any)
	for i, v := range hidden {
		if got[i].(map[string]any)["value"] != "[redacted]" {
			t.Errorf("%q not hidden", v)
		}
	}
	for i, v := range visible {
		if got[len(hidden)+i].(map[string]any)["value"] != v {
			t.Errorf("%q was hidden", v)
		}
	}
}

// With a mode other than none, a shape the code does not understand is hidden, never passed through.
func TestRedactEnvironment_FailsClosedOnUnexpectedShapes(t *testing.T) {
	const real = "real-value-9f8e7d"
	cases := map[string]map[string]any{
		"containerDefinitions not a list": {"containerDefinitions": map[string]any{"environment": []any{map[string]any{"name": "A", "value": real}}}},
		"environment not a list":          {"containerDefinitions": []any{map[string]any{"name": "a", "environment": map[string]any{"A": real}}}},
		"container not an object":         {"containerDefinitions": []any{real}},
		"entry not an object":             {"containerDefinitions": []any{map[string]any{"environment": []any{real}}}},
		"capitalised keys": {"ContainerDefinitions": []any{map[string]any{"Environment": []any{
			map[string]any{"Name": "API_KEY", "Value": real}}}}},
		"non-string value": {"containerDefinitions": []any{map[string]any{"environment": []any{
			map[string]any{"name": "HOST", "value": map[string]any{"x": real}}}}}},
		"duplicate names": {"containerDefinitions": []any{map[string]any{"environment": []any{
			map[string]any{"name": "HOST", "value": "db.internal"}, map[string]any{"name": "HOST", "value": real + "-API_KEY=x"}}}}},
	}
	for name, def := range cases {
		for _, mode := range []envRedaction{redactAll, redactSecretShapes} {
			out, _ := redactEnvironment(def, mode)
			raw, err := json.Marshal(out)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), real) {
				t.Errorf("%s, mode %d: real value in %s", name, mode, raw)
			}
		}
	}
}
