package deploy

import (
	"encoding/json"
	"errors"
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
		smNew   = "arn:aws:secretsmanager:us-east-1:482109375512:secret:payments/other-ZzZzZz"
	)
	addSecret := func(valueFrom string) func(Definition) {
		return func(d Definition) {
			c := firstContainer(d)
			c["secrets"] = append(c["secrets"].([]any), map[string]any{"name": "NEW", "valueFrom": valueFrom})
		}
	}
	fargate := func(cpu, memory any) func(Definition) {
		return func(d Definition) { d["cpu"], d["memory"] = cpu, memory }
	}
	tests := []struct {
		name    string
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
		{256, 256}, {256, 4096}, {512, 512}, {512, 4097}, {512, 5120},
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
