package api

import (
	"net/http"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/config"
)

// definitionEnvironment is what a task definition answer says about the containers' environment and secrets.
type definitionEnvironment struct {
	Definition struct {
		ContainerDefinitions []struct {
			Environment []struct{ Name, Value string }     `json:"environment"`
			Secrets     []struct{ Name, ValueFrom string } `json:"secrets"`
		} `json:"containerDefinitions"`
	} `json:"definition"`
	EnvironmentRedacted bool `json:"environment_redacted"`
}

func (d definitionEnvironment) counts() (env, secrets int) {
	for _, c := range d.Definition.ContainerDefinitions {
		env += len(c.Environment)
		secrets += len(c.Secrets)
	}
	return env, secrets
}

// tokenEnvServer is a polled estate with tokens on and the redaction switch set as asked.
func tokenEnvServer(t *testing.T, redact bool) (token, session *apiClient) {
	t.Helper()
	srv, _, fk, st, _ := newServerCustomAPI(t, true, withTokens(func(cfg *config.Config) { cfg.TokenRedactEnv = redact }, nil))
	seedAccounts(t, st)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	return tokenClient(t, srv, st, "sam"), login(t, srv, "sam", testPassword)
}

func readDefinition(t *testing.T, c *apiClient, path string) definitionEnvironment {
	t.Helper()
	resp := c.get(path)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d", path, resp.StatusCode)
	}
	var out definitionEnvironment
	decode(t, resp, &out)
	return out
}

func TestTaskDefinition_TokenSeesValuesByDefault(t *testing.T) {
	token, _ := tokenEnvServer(t, false)
	out := readDefinition(t, token, "/api/services/"+paymentsID()+"/task-definition")
	if env, _ := out.counts(); env == 0 {
		t.Fatal("the fixture has no environment variables; the test proves nothing")
	}
	if out.EnvironmentRedacted {
		t.Error("environment_redacted is true while the switch is off")
	}
	for _, c := range out.Definition.ContainerDefinitions {
		for _, e := range c.Environment {
			if e.Value == "[redacted]" || e.Value == "" {
				t.Errorf("%s = %q, want the real value (the default shows values)", e.Name, e.Value)
			}
		}
	}
}

// authServiceID is the mockup service that has both environment variables and secrets.
func authServiceID() string {
	return serviceID(sandboxAccountID, "us-east-1", "core-services", "auth-service")
}

func TestTaskDefinition_TokenRedactedWhenSwitchOn(t *testing.T) {
	token, session := tokenEnvServer(t, true)
	path := "/api/services/" + authServiceID() + "/task-definition"
	real := readDefinition(t, session, path)
	out := readDefinition(t, token, path)
	wantEnv, wantSecrets := real.counts()
	gotEnv, gotSecrets := out.counts()
	if wantEnv == 0 || wantSecrets == 0 {
		t.Fatalf("fixture has %d environment variables and %d secrets, need both", wantEnv, wantSecrets)
	}
	if gotEnv != wantEnv || gotSecrets != wantSecrets || !out.EnvironmentRedacted {
		t.Fatalf("env %d/%d secrets %d/%d redacted=%v", gotEnv, wantEnv, gotSecrets, wantSecrets, out.EnvironmentRedacted)
	}
	for i, c := range out.Definition.ContainerDefinitions {
		for j, e := range c.Environment {
			if e.Value != "[redacted]" {
				t.Errorf("%s = %q, want [redacted]", e.Name, e.Value)
			}
			if e.Name != real.Definition.ContainerDefinitions[i].Environment[j].Name {
				t.Errorf("name changed: %q", e.Name)
			}
		}
		for j, s := range c.Secrets {
			if s != real.Definition.ContainerDefinitions[i].Secrets[j] {
				t.Errorf("secret reference changed: %+v", s)
			}
		}
	}
}

func TestTaskDefinition_SessionNeverRedacted(t *testing.T) {
	_, session := tokenEnvServer(t, true)
	out := readDefinition(t, session, "/api/services/"+paymentsID()+"/task-definition")
	if out.EnvironmentRedacted {
		t.Error("a session answer is flagged redacted")
	}
	for _, c := range out.Definition.ContainerDefinitions {
		for _, e := range c.Environment {
			if e.Value == "[redacted]" {
				t.Errorf("%s is redacted for a session", e.Name)
			}
		}
	}
}

func TestRevision_TokenRedactedWhenSwitchOn(t *testing.T) {
	token, session := tokenEnvServer(t, true)
	path := "/api/services/" + paymentsID() + "/revisions/42"
	real := readDefinition(t, session, path)
	if env, _ := real.counts(); env == 0 {
		t.Fatal("the revision has no environment variables; the test proves nothing")
	}
	if real.EnvironmentRedacted {
		t.Error("session revision flagged redacted")
	}
	out := readDefinition(t, token, path)
	if !out.EnvironmentRedacted {
		t.Error("environment_redacted is false for a token with the switch on")
	}
	for _, c := range out.Definition.ContainerDefinitions {
		for _, e := range c.Environment {
			if e.Value != "[redacted]" {
				t.Errorf("%s = %q, want [redacted]", e.Name, e.Value)
			}
		}
	}
	// With the switch off the revision shows values to a token.
	tokenOff, _ := tokenEnvServer(t, false)
	plain := readDefinition(t, tokenOff, path)
	for _, c := range plain.Definition.ContainerDefinitions {
		for _, e := range c.Environment {
			if e.Value == "[redacted]" {
				t.Errorf("%s redacted with the switch off", e.Name)
			}
		}
	}
}

func TestRedactEnvironment_DoesNotModifyItsInput(t *testing.T) {
	in := map[string]any{
		"family": "f",
		"containerDefinitions": []any{
			map[string]any{
				"name":        "app",
				"environment": []any{map[string]any{"name": "A", "value": "1"}},
				"secrets":     []any{map[string]any{"name": "S", "valueFrom": "arn:x"}},
			},
		},
	}
	out, flagged := redactEnvironment(in, redactAll)
	if !flagged {
		t.Error("not flagged")
	}
	entry := in["containerDefinitions"].([]any)[0].(map[string]any)["environment"].([]any)[0].(map[string]any)
	if entry["value"] != "1" {
		t.Fatalf("input modified: %v", entry)
	}
	got := out["containerDefinitions"].([]any)[0].(map[string]any)
	if got["environment"].([]any)[0].(map[string]any)["value"] != "[redacted]" {
		t.Errorf("output not redacted: %v", got)
	}
	if got["secrets"].([]any)[0].(map[string]any)["valueFrom"] != "arn:x" {
		t.Errorf("secrets changed: %v", got["secrets"])
	}
	if out["family"] != "f" {
		t.Error("other fields lost")
	}
}
