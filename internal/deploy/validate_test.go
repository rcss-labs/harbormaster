package deploy

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const testAccount = "482109375512"

// cloneDefinition deep-copies a definition through JSON, which also makes numbers float64
// exactly like a draft decoded from an API request.
func cloneDefinition(t *testing.T, def Definition) Definition {
	t.Helper()
	raw, err := json.Marshal(def)
	if err != nil {
		t.Fatal(err)
	}
	var out Definition
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func validBase(t *testing.T) Definition {
	t.Helper()
	return cloneDefinition(t, Definition{
		"family":                  "payments-api",
		"taskRoleArn":             "arn:aws:iam::482109375512:role/payments-api-task",
		"executionRoleArn":        "arn:aws:iam::482109375512:role/payments-api-execution",
		"requiresCompatibilities": []any{"FARGATE"},
		"cpu":                     "512",
		"memory":                  "1024",
		"containerDefinitions": []any{
			map[string]any{
				"name":      "payments-api",
				"image":     "482109375512.dkr.ecr.us-east-1.amazonaws.com/payments-api:1.0.0",
				"essential": true,
				"secrets": []any{
					map[string]any{"name": "DB_PASS", "valueFrom": "arn:aws:ssm:us-east-1:482109375512:parameter/payments/DB_PASS"},
					map[string]any{"name": "API_KEY", "valueFrom": "arn:aws:secretsmanager:us-east-1:482109375512:secret:payments/api-key-AbCdEf"},
				},
			},
			map[string]any{"name": "sidecar", "image": "envoy:v1", "essential": false},
		},
	})
}

func firstContainer(draft Definition) map[string]any {
	return draft["containerDefinitions"].([]any)[0].(map[string]any)
}

func TestValidate(t *testing.T) {
	const (
		ssmNew  = "arn:aws:ssm:us-east-1:482109375512:parameter/payments/NEW"
		ssmFrom = "arn:aws:ssm:us-east-1:482109375512:parameter/other/X"
		ssmAcct = "arn:aws:ssm:us-east-1:111111111111:parameter/payments/NEW"
		smOther = "arn:aws:secretsmanager:us-east-1:482109375512:secret:elsewhere/creds-QqQqQq"
		smNew   = "arn:aws:secretsmanager:us-east-1:482109375512:secret:payments/other-ZzZzZz"
	)
	const (
		s3Old       = "arn:aws:s3:::payments-config/old.env"
		s3New       = "arn:aws:s3:::attacker-bucket/stolen.env"
		envFilesMsg = "Changing environment files needs a global admin"
		fsxMsg      = "Changing FSx credentials needs a global admin"
		firelensMsg = "Changing the FireLens config file needs a global admin"
		credSpecMsg = "Changing credential specs needs a global admin"
	)
	addEnvFile := func(arn string) func(Definition) {
		return func(d Definition) {
			firstContainer(d)["environmentFiles"] = []any{map[string]any{"type": "s3", "value": arn}}
		}
	}
	addFsx := func(parameter string) func(Definition) {
		return func(d Definition) {
			d["volumes"] = []any{map[string]any{"name": "share", "fsxWindowsFileServerVolumeConfiguration": map[string]any{
				"fileSystemId": "fs-0123456789abcdef0", "rootDirectory": "share",
				"authorizationConfig": map[string]any{"credentialsParameter": parameter, "domain": "corp.example.com"},
			}}}
		}
	}
	addFirelens := func(arn string) func(Definition) {
		return func(d Definition) {
			firstContainer(d)["firelensConfiguration"] = map[string]any{"type": "fluentbit",
				"options": map[string]any{"config-file-type": "s3", "config-file-value": arn}}
		}
	}
	addCredSpec := func(spec string) func(Definition) {
		return func(d Definition) { firstContainer(d)["credentialSpecs"] = []any{spec} }
	}
	addSecret := func(valueFrom string) func(Definition) {
		return func(d Definition) {
			c := firstContainer(d)
			c["secrets"] = append(c["secrets"].([]any), map[string]any{"name": "NEW", "valueFrom": valueFrom})
		}
	}
	addLogSecret := func(valueFrom string) func(Definition) {
		return func(d Definition) {
			firstContainer(d)["logConfiguration"] = map[string]any{
				"logDriver":     "splunk",
				"secretOptions": []any{map[string]any{"name": "token", "valueFrom": valueFrom}},
			}
		}
	}
	fargate := func(cpu, memory any) func(Definition) {
		return func(d Definition) { d["cpu"], d["memory"] = cpu, memory }
	}
	tests := []struct {
		name    string
		base    func(Definition) // optional edit of the deployed definition
		mutate  func(Definition)
		admin   bool
		wantErr string
		check   func(*testing.T, Definition)
	}{
		{name: "unchanged draft", mutate: func(Definition) {}},
		{name: "family changed", mutate: func(d Definition) { d["family"] = "other" }, wantErr: "Use Terraform to create a new family"},
		{name: "no essential container", mutate: func(d Definition) {
			firstContainer(d)["essential"] = false
		}, wantErr: "At least one container must be essential"},
		{name: "missing essential counts as essential", mutate: func(d Definition) {
			delete(firstContainer(d), "essential")
		}},
		{name: "fargate 512/512", mutate: fargate("512", "512"), wantErr: "512 CPU with 512 MiB is not a valid Fargate combination"},
		{name: "fargate 256/512", mutate: fargate("256", "512")},
		{name: "fargate 8192/20480", mutate: fargate("8192", "20480")},
		{name: "fargate 8192/18432 invalid", mutate: fargate("8192", "18432"), wantErr: "8192 CPU with 18432 MiB is not a valid Fargate combination"},
		{name: "fargate 8192/16384", mutate: fargate("8192", "16384")},
		{name: "ec2 only without cpu", mutate: func(d Definition) {
			d["requiresCompatibilities"] = []any{"EC2"}
			delete(d, "cpu")
			delete(d, "memory")
		}},
		{name: "numeric cpu and memory are normalized", mutate: fargate(float64(512), float64(1024)), check: func(t *testing.T, d Definition) {
			if d["cpu"] != "512" || d["memory"] != "1024" {
				t.Fatalf("cpu/memory = %#v/%#v, want strings", d["cpu"], d["memory"])
			}
		}},
		{name: "numeric int cpu and memory are normalized", mutate: fargate(512, 1024), check: func(t *testing.T, d Definition) {
			if d["cpu"] != "512" || d["memory"] != "1024" {
				t.Fatalf("cpu/memory = %#v/%#v, want strings", d["cpu"], d["memory"])
			}
		}},
		{name: "non-admin changes task role", mutate: func(d Definition) { d["taskRoleArn"] = "arn:aws:iam::482109375512:role/other" }, wantErr: "Changing roles needs a global admin"},
		{name: "non-admin changes execution role", mutate: func(d Definition) { d["executionRoleArn"] = "arn:aws:iam::482109375512:role/other" }, wantErr: "Changing roles needs a global admin"},
		{name: "admin changes task role", admin: true, mutate: func(d Definition) { d["taskRoleArn"] = "arn:aws:iam::482109375512:role/other" }},
		{name: "secret in existing ssm prefix", mutate: addSecret(ssmNew)},
		{name: "secret in existing secretsmanager prefix", mutate: addSecret(smNew)},
		{name: "secret in other prefix", mutate: addSecret(ssmFrom), wantErr: "Changing secrets outside the existing prefixes needs a global admin"},
		{name: "secret in other account", mutate: addSecret(ssmAcct), wantErr: "Changing secrets outside the existing prefixes needs a global admin"},
		{name: "unparseable secret reference", mutate: addSecret("not-an-arn"), wantErr: "Changing secrets outside the existing prefixes needs a global admin"},
		{name: "non-admin adds out-of-prefix secretOptions", mutate: addLogSecret(ssmFrom), wantErr: "Changing secrets outside the existing prefixes needs a global admin"},
		{name: "admin adds out-of-prefix secretOptions", admin: true, mutate: addLogSecret(ssmFrom)},
		{name: "non-admin adds in-prefix secretOptions", mutate: addLogSecret(ssmNew)},
		{name: "non-admin adds out-of-prefix repositoryCredentials", mutate: func(d Definition) {
			firstContainer(d)["repositoryCredentials"] = map[string]any{"credentialsParameter": smOther}
		}, wantErr: "Changing secrets outside the existing prefixes needs a global admin"},
		{name: "non-admin adds environmentFiles", mutate: addEnvFile(s3New), wantErr: envFilesMsg},
		{name: "non-admin changes environmentFiles", base: addEnvFile(s3Old), mutate: addEnvFile(s3New), wantErr: envFilesMsg},
		{name: "non-admin keeps environmentFiles", base: addEnvFile(s3Old), mutate: addEnvFile(s3Old)},
		{name: "admin adds environmentFiles", admin: true, mutate: addEnvFile(s3New)},
		{name: "non-admin adds fsx credentialsParameter", mutate: addFsx(ssmNew), wantErr: fsxMsg},
		{name: "non-admin changes fsx credentialsParameter", base: addFsx(ssmFrom), mutate: addFsx(ssmNew), wantErr: fsxMsg},
		{name: "non-admin keeps fsx credentialsParameter", base: addFsx(ssmFrom), mutate: addFsx(ssmFrom)},
		{name: "admin adds fsx credentialsParameter", admin: true, mutate: addFsx(ssmNew)},
		{name: "non-admin adds firelens s3 config", mutate: addFirelens(s3New), wantErr: firelensMsg},
		{name: "admin adds firelens s3 config", admin: true, mutate: addFirelens(s3New)},
		{name: "non-admin adds credentialSpecs", mutate: addCredSpec("credentialspec:" + s3New), wantErr: credSpecMsg},
		{name: "admin adds credentialSpecs", admin: true, mutate: addCredSpec("credentialspec:" + s3New)},
		{name: "non-integral cpu", mutate: fargate(512.7, float64(1024)), wantErr: "Task definition JSON is invalid: cpu must be a whole number"},
		{name: "admin adds secret in other prefix", admin: true, mutate: addSecret(ssmFrom)},
		{name: "secret removed", mutate: func(d Definition) {
			c := firstContainer(d)
			c["secrets"] = c["secrets"].([]any)[:1]
		}},
		{name: "missing containerDefinitions", mutate: func(d Definition) { delete(d, "containerDefinitions") }, wantErr: "Task definition JSON is invalid: containerDefinitions is required"},
		{name: "container without image", mutate: func(d Definition) { delete(firstContainer(d), "image") }, wantErr: "Task definition JSON is invalid: containerDefinitions[0] needs a name and an image"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			base := validBase(t)
			if tc.base != nil {
				tc.base(base)
			}
			draft := cloneDefinition(t, base)
			tc.mutate(draft)
			err := Validate(base, draft, tc.admin, testAccount)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
			} else {
				var validation *ValidationError
				if !errors.As(err, &validation) || validation.Message != tc.wantErr {
					t.Fatalf("Validate() = %v, want ValidationError %q", err, tc.wantErr)
				}
			}
			if tc.check != nil {
				tc.check(t, draft)
			}
		})
	}
}

func TestValidate_BaseWithoutSecretsRejectsNewSecretForNonAdmin(t *testing.T) {
	base := validBase(t)
	delete(firstContainer(base), "secrets")
	draft := cloneDefinition(t, base)
	firstContainer(draft)["secrets"] = []any{map[string]any{"name": "X", "valueFrom": "arn:aws:ssm:us-east-1:482109375512:parameter/payments/X"}}

	err := Validate(base, draft, false, testAccount)
	var validation *ValidationError
	if !errors.As(err, &validation) || validation.Message != "Changing secrets outside the existing prefixes needs a global admin" {
		t.Fatalf("non-admin: %v", err)
	}
	if err := Validate(base, draft, true, testAccount); err != nil {
		t.Fatalf("admin: %v", err)
	}
}

func TestValidate_FixtureUnchanged(t *testing.T) {
	base := convertFixture(t, "describe_payments_api.json")
	draft := cloneDefinition(t, base)
	if err := Validate(base, draft, false, testAccount); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

func TestFargateValid_Table(t *testing.T) {
	valid := map[int][]int{
		256:   {512, 1024, 2048},
		512:   {1024, 2048, 3072, 4096},
		1024:  {2048, 3072, 4096, 5120, 6144, 7168, 8192},
		2048:  {4096, 8192, 12288, 16384},
		4096:  {8192, 16384, 30720},
		8192:  {16384, 20480, 24576, 61440},
		16384: {32768, 40960, 122880},
	}
	for cpu, memories := range valid {
		for _, memory := range memories {
			if !FargateValid(cpu, memory) {
				t.Errorf("FargateValid(%d, %d) = false, want true", cpu, memory)
			}
		}
	}
	invalid := [][2]int{
		{256, 256}, {256, 1536}, {256, 4096}, {512, 512}, {512, 4097}, {512, 5120},
		{1024, 1024}, {1024, 9216}, {2048, 2048}, {2048, 17408},
		{4096, 4096}, {4096, 31744}, {8192, 12288}, {8192, 18432}, {8192, 65536},
		{16384, 16384}, {16384, 36864}, {16384, 131072}, {128, 512}, {0, 0},
	}
	for _, pair := range invalid {
		if FargateValid(pair[0], pair[1]) {
			t.Errorf("FargateValid(%d, %d) = true, want false", pair[0], pair[1])
		}
	}
}

// bypassVector edits a draft so one of the non-admin locks (roles, secrets) is attacked
// through a key whose spelling differs only in letter case from the real field name.
type bypassVector struct {
	name string
	// duplicate adds the folded key next to the untouched camelCase one (last key wins in encoding/json).
	duplicate func(Definition)
	// lone replaces the camelCase key by the folded one, with an attacker value.
	lone func(Definition)
}

const attackerSecret = "arn:aws:ssm:us-east-1:482109375512:parameter/stolen/KEY"

func bypassVectors() []bypassVector {
	return []bypassVector{
		{"taskRoleArn",
			func(d Definition) { d["taskrolearn"] = "arn:aws:iam::482109375512:role/prod-admin" },
			func(d Definition) {
				delete(d, "taskRoleArn")
				d["taskrolearn"] = "arn:aws:iam::482109375512:role/prod-admin"
			}},
		{"executionRoleArn",
			func(d Definition) { d["EXECUTIONROLEARN"] = "arn:aws:iam::482109375512:role/prod-admin" },
			func(d Definition) {
				delete(d, "executionRoleArn")
				d["EXECUTIONROLEARN"] = "arn:aws:iam::482109375512:role/prod-admin"
			}},
		{"secrets valueFrom",
			func(d Definition) {
				firstContainer(d)["secrets"] = []any{map[string]any{"name": "X", "valueFrom": "arn:aws:ssm:us-east-1:482109375512:parameter/payments/DB_PASS", "valuefrom": attackerSecret}}
			},
			func(d Definition) {
				firstContainer(d)["secrets"] = []any{map[string]any{"name": "X", "valuefrom": attackerSecret}}
			}},
		{"logConfiguration secretOptions",
			func(d Definition) {
				firstContainer(d)["logConfiguration"] = map[string]any{"logDriver": "splunk", "secretOptions": []any{
					map[string]any{"name": "token", "valueFrom": "arn:aws:ssm:us-east-1:482109375512:parameter/payments/DB_PASS", "VALUEFROM": attackerSecret}}}
			},
			func(d Definition) {
				firstContainer(d)["logConfiguration"] = map[string]any{"logDriver": "splunk", "secretoptions": []any{
					map[string]any{"name": "token", "valuefrom": attackerSecret}}}
			}},
		{"repositoryCredentials",
			func(d Definition) {
				firstContainer(d)["repositoryCredentials"] = map[string]any{
					"credentialsParameter": "arn:aws:secretsmanager:us-east-1:482109375512:secret:payments/api-key-AbCdEf",
					"credentialsparameter": attackerSecret}
			},
			func(d Definition) {
				firstContainer(d)["repositoryCredentials"] = map[string]any{"credentialsparameter": attackerSecret}
			}},
		{"containerDefinitions swapped wholesale",
			func(d Definition) {
				d["containerdefinitions"] = evilContainers(d)
			},
			func(d Definition) {
				evil := evilContainers(d)
				delete(d, "containerDefinitions")
				d["containerdefinitions"] = evil
			}},
		{"secrets (long s)",
			func(d Definition) {
				firstContainer(d)["ſecrets"] = []any{map[string]any{"name": "X", "valueFrom": attackerSecret}}
			},
			func(d Definition) {
				delete(firstContainer(d), "secrets")
				firstContainer(d)["ſecrets"] = []any{map[string]any{"name": "X", "valueFrom": attackerSecret}}
			}},
	}
}

// evilContainers copies d's containers and gives the first one an attacker-controlled secret.
func evilContainers(d Definition) []any {
	raw, _ := json.Marshal(d["containerDefinitions"])
	var containers []any
	_ = json.Unmarshal(raw, &containers)
	containers[0].(map[string]any)["secrets"] = []any{map[string]any{"name": "X", "valueFrom": attackerSecret}}
	return containers
}

// Case-folded duplicate keys are rejected at the boundary: encoding/json would let the last
// spelling silently override the one the validator looked at.
func TestValidate_FoldedDuplicateKeysAreRejected(t *testing.T) {
	for _, vector := range bypassVectors() {
		t.Run(vector.name, func(t *testing.T) {
			base := validBase(t)
			draft := cloneDefinition(t, base)
			vector.duplicate(draft)
			for _, admin := range []bool{false, true} {
				err := Validate(base, draft, admin, testAccount)
				var validation *ValidationError
				if !errors.As(err, &validation) || !strings.Contains(validation.Message, "letter case") {
					t.Fatalf("admin=%v: Validate() = %v, want a duplicate-key ValidationError", admin, err)
				}
			}
		})
	}
}

// A single case-folded key is decoded into the real field, so the locks must judge the
// decoded value: validating the exact-spelling keys alone would see nothing here.
func TestValidate_FoldedKeyIsJudgedAsDecoded(t *testing.T) {
	for _, vector := range bypassVectors() {
		t.Run(vector.name, func(t *testing.T) {
			base := validBase(t)
			// No roles and no secrets in the base: nothing to compare the exact keys against.
			delete(base, "taskRoleArn")
			delete(base, "executionRoleArn")
			delete(firstContainer(base), "secrets")
			draft := cloneDefinition(t, base)
			vector.lone(draft)
			err := Validate(base, draft, false, testAccount)
			var validation *ValidationError
			if !errors.As(err, &validation) ||
				(validation.Message != "Changing roles needs a global admin" &&
					validation.Message != "Changing secrets outside the existing prefixes needs a global admin") {
				t.Fatalf("Validate() = %v, want the roles/secrets lock", err)
			}
		})
	}
}

func TestValidate_UnknownKeysAreRejected(t *testing.T) {
	for name, mutate := range map[string]func(Definition){
		"top level": func(d Definition) { d["taskRoleARN2"] = "x" },
		"read only": func(d Definition) { d["revision"] = 3 },
		"nested":    func(d Definition) { firstContainer(d)["imge"] = "typo" },
	} {
		t.Run(name, func(t *testing.T) {
			base := validBase(t)
			draft := cloneDefinition(t, base)
			mutate(draft)
			err := Validate(base, draft, true, testAccount)
			var validation *ValidationError
			if !errors.As(err, &validation) || !strings.Contains(validation.Message, "unknown key") {
				t.Fatalf("Validate() = %v, want an unknown-key ValidationError", err)
			}
		})
	}
}

// Map-valued fields hold user data whose keys are case sensitive; two spellings are legitimate there.
func TestValidate_CaseDifferingMapKeysAreAllowed(t *testing.T) {
	base := validBase(t)
	draft := cloneDefinition(t, base)
	firstContainer(draft)["dockerLabels"] = map[string]any{"Env": "a", "env": "b"}
	if err := Validate(base, draft, false, testAccount); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}
