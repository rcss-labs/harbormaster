package deploy

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

func loadFixture(t *testing.T, name string) ecstypes.TaskDefinition {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	var td ecstypes.TaskDefinition
	if err := json.Unmarshal(raw, &td); err != nil {
		t.Fatal(err)
	}
	return td
}

func convertFixture(t *testing.T, name string) Definition {
	t.Helper()
	def, err := ToRegisterReady(loadFixture(t, name))
	if err != nil {
		t.Fatal(err)
	}
	return def
}

func containerAt(t *testing.T, def Definition, i int) map[string]any {
	t.Helper()
	return def["containerDefinitions"].([]any)[i].(map[string]any)
}

func TestToRegisterReady_RemovesReadOnlyFields(t *testing.T) {
	def := convertFixture(t, "describe_payments_api.json")
	for _, key := range []string{"taskDefinitionArn", "revision", "status", "requiresAttributes",
		"compatibilities", "registeredAt", "registeredBy", "deregisteredAt"} {
		if _, ok := def[key]; ok {
			t.Errorf("read-only key %q still present", key)
		}
	}
	want := map[string]string{
		"family":           "payments-api",
		"taskRoleArn":      "arn:aws:iam::482109375512:role/payments-api-task",
		"executionRoleArn": "arn:aws:iam::482109375512:role/payments-api-execution",
		"networkMode":      "awsvpc",
		"cpu":              "512",
		"memory":           "1024",
	}
	for key, value := range want {
		if def[key] != value {
			t.Errorf("%s = %v, want %s", key, def[key], value)
		}
	}
	for _, key := range []string{"requiresCompatibilities", "containerDefinitions", "volumes"} {
		if _, ok := def[key]; !ok {
			t.Errorf("missing %s", key)
		}
	}
	if essential := containerAt(t, def, 1)["essential"]; essential != false {
		t.Errorf("explicit essential=false must be kept, got %v", essential)
	}
}

func TestToRegisterReady_PreservesMapKeysAndSecrets(t *testing.T) {
	c := containerAt(t, convertFixture(t, "describe_payments_api.json"), 0)
	options := c["logConfiguration"].(map[string]any)["options"].(map[string]any)
	if options["awslogs-group"] != "/ecs/payments-api" {
		t.Errorf("options = %v", options)
	}
	if c["dockerLabels"].(map[string]any)["Owner"] != "payments-team" {
		t.Errorf("dockerLabels = %v", c["dockerLabels"])
	}
	secret := c["secrets"].([]any)[0].(map[string]any)
	if !strings.HasSuffix(secret["valueFrom"].(string), "secret:payments-api/database-url-aB3dEf") {
		t.Errorf("secret = %v", secret)
	}
	if cmd := c["healthCheck"].(map[string]any)["command"].([]any); cmd[0] != "CMD-SHELL" {
		t.Errorf("command = %v", cmd)
	}
}

func TestToRegisterInput_RoundTrips(t *testing.T) {
	td := loadFixture(t, "describe_payments_api.json")
	def, err := ToRegisterReady(td)
	if err != nil {
		t.Fatal(err)
	}
	tags := []ecstypes.Tag{{Key: aws.String("owner"), Value: aws.String("payments")}}
	in, err := ToRegisterInput(def, tags)
	if err != nil {
		t.Fatal(err)
	}
	if aws.ToString(in.Family) != "payments-api" || aws.ToString(in.Cpu) != "512" || aws.ToString(in.Memory) != "1024" {
		t.Errorf("family/cpu/memory = %v %v %v", aws.ToString(in.Family), aws.ToString(in.Cpu), aws.ToString(in.Memory))
	}
	if len(in.ContainerDefinitions) != len(td.ContainerDefinitions) {
		t.Fatalf("containers = %d", len(in.ContainerDefinitions))
	}
	for i, want := range td.ContainerDefinitions {
		got := in.ContainerDefinitions[i]
		if aws.ToString(got.Name) != aws.ToString(want.Name) || aws.ToString(got.Image) != aws.ToString(want.Image) {
			t.Errorf("container %d = %s/%s", i, aws.ToString(got.Name), aws.ToString(got.Image))
		}
		if len(got.Environment) != len(want.Environment) {
			t.Errorf("container %d environment = %v", i, got.Environment)
		}
		if aws.ToBool(got.Essential) != aws.ToBool(want.Essential) {
			t.Errorf("container %d essential changed", i)
		}
	}
	if in.ContainerDefinitions[0].Environment[0].Name == nil ||
		*in.ContainerDefinitions[0].Environment[0].Name != "APP_ENV" {
		t.Errorf("environment[0] = %v", in.ContainerDefinitions[0].Environment[0])
	}
	if got := aws.ToString(in.Volumes[0].EfsVolumeConfiguration.FileSystemId); got != "fs-0a1b2c3d4e5f60718" {
		t.Errorf("file system id = %s", got)
	}
	if len(in.Tags) != 1 || aws.ToString(in.Tags[0].Key) != "owner" || aws.ToString(in.Tags[0].Value) != "payments" {
		t.Errorf("tags = %v", in.Tags)
	}
}

func TestToRegisterReady_EC2WithoutCPU(t *testing.T) {
	def := convertFixture(t, "describe_ec2_bridge.json")
	for _, key := range []string{"cpu", "memory"} {
		if _, ok := def[key]; ok {
			t.Errorf("%s present for EC2 fixture", key)
		}
	}
	if def["networkMode"] != "bridge" {
		t.Errorf("networkMode = %v", def["networkMode"])
	}
}

func defWithContainers(containers ...map[string]any) Definition {
	list := make([]any, len(containers))
	for i, c := range containers {
		list[i] = c
	}
	return Definition{"containerDefinitions": list}
}

func TestMainContainer_NameMatchElseFirstEssential(t *testing.T) {
	cases := []struct {
		name      string
		service   string
		def       Definition
		wantName  string
		wantIndex int
	}{
		{"name match", "payments-api", defWithContainers(
			map[string]any{"name": "otel-collector", "essential": false},
			map[string]any{"name": "payments-api", "essential": true}), "payments-api", 1},
		{"first essential", "web", defWithContainers(
			map[string]any{"name": "nginx", "essential": true},
			map[string]any{"name": "app", "essential": true}), "nginx", 0},
		{"skips non-essential", "web", defWithContainers(
			map[string]any{"name": "sidecar", "essential": false},
			map[string]any{"name": "app", "essential": true}), "app", 1},
		{"none essential falls back to first", "web", defWithContainers(
			map[string]any{"name": "a", "essential": false}), "a", 0},
		{"no containers", "web", Definition{}, "", -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name, index := MainContainer(tc.def, tc.service)
			if name != tc.wantName || index != tc.wantIndex {
				t.Errorf("got %q,%d want %q,%d", name, index, tc.wantName, tc.wantIndex)
			}
		})
	}
}

func TestImageTag(t *testing.T) {
	def := defWithContainers(
		map[string]any{"name": "a", "image": "482109375512.dkr.ecr.us-east-1.amazonaws.com/payments-api:1.18.2"},
		map[string]any{"name": "b", "image": "nginx"},
		map[string]any{"name": "c", "image": "public.ecr.aws/x/y:v1"},
		map[string]any{"name": "d", "image": "localhost:5000/app"},
	)
	for container, want := range map[string]string{"a": "1.18.2", "b": "", "c": "v1", "d": "", "missing": ""} {
		if got := ImageTag(def, container); got != want {
			t.Errorf("ImageTag(%s) = %q, want %q", container, got, want)
		}
	}
}

func TestParseRevisionARN(t *testing.T) {
	family, revision, err := ParseRevisionARN("arn:aws:ecs:us-east-1:482109375512:task-definition/payments-api:42")
	if err != nil || family != "payments-api" || revision != 42 {
		t.Errorf("got %q %d %v", family, revision, err)
	}
	for _, bad := range []string{
		"arn:aws:ecs:us-east-1:482109375512:cluster/prod",
		"arn:aws:ecs:us-east-1:482109375512:task-definition/payments-api",
		"arn:aws:ecs:us-east-1:482109375512:task-definition/payments-api:x",
		"",
	} {
		if _, _, err := ParseRevisionARN(bad); err == nil {
			t.Errorf("expected error for %q", bad)
		}
	}
}

func TestDefinitionJSON_StableOrder(t *testing.T) {
	out, err := DefinitionJSON(convertFixture(t, "describe_payments_api.json"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	if !strings.HasPrefix(text, "{\n  \"family\"") {
		t.Errorf("family not first / not 2-space indented:\n%s", text[:60])
	}
	order := []string{`"family"`, `"taskRoleArn"`, `"executionRoleArn"`, `"networkMode"`,
		`"requiresCompatibilities"`, `"cpu"`, `"memory"`, `"containerDefinitions"`, `"volumes"`, `"runtimePlatform"`}
	last := -1
	for _, key := range order {
		at := strings.Index(text, "\n  "+key)
		if at < 0 || at < last {
			t.Fatalf("key %s out of order (at %d, previous %d)", key, at, last)
		}
		last = at
	}
}
