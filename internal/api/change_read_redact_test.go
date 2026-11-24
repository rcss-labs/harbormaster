package api

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

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

type redactCase struct {
	redactAll     bool // HM_TOKEN_REDACT_ENV
	runTokenFlag  bool // the run token's RedactEnv
	personalToken bool
}

// readPlanted answers auth-service's definition (revision empty) or one revision to the client the case describes.
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
	for name, value := range got.environmentValues() {
		if value != "[redacted]" {
			t.Errorf("%s = %q, want [redacted]", name, value)
		}
	}
	if _, wantSecrets := real.counts(); wantSecrets == 0 {
		t.Fatal("fixture has no secrets references")
	}
	if _, gotSecrets := got.counts(); gotSecrets != func() int { _, s := real.counts(); return s }() {
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
	if values["ISSUER"] == "[redacted]" {
		t.Error("a harmless seeded variable was redacted in secrets-only mode")
	}
}

// An operator's global switch is never weakened by an admin's per-provider lift.
func TestTaskDefinition_RunTokenLiftIgnoredWhileGlobalSwitchOn(t *testing.T) {
	got, _ := readPlanted(t, redactCase{redactAll: true, runTokenFlag: false}, "")
	if !got.EnvironmentRedacted {
		t.Error("environment_redacted is false")
	}
	for name, value := range got.environmentValues() {
		if value != "[redacted]" {
			t.Errorf("%s = %q, want [redacted]", name, value)
		}
	}
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
	all, _ := readPlanted(t, redactCase{runTokenFlag: true}, "15")
	if !all.EnvironmentRedacted {
		t.Error("revision: environment_redacted is false")
	}
	for name, value := range all.environmentValues() {
		if value != "[redacted]" {
			t.Errorf("revision %s = %q, want [redacted]", name, value)
		}
	}
	lifted, _ := readPlanted(t, redactCase{runTokenFlag: false}, "15")
	values := lifted.environmentValues()
	if !lifted.EnvironmentRedacted || values["DATABASE_HOST"] != "db.internal" || values["API_KEY"] != "[redacted]" || values["EXTRA"] != "[redacted]" {
		t.Errorf("revision, lifted: %v redacted=%v", values, lifted.EnvironmentRedacted)
	}
}
