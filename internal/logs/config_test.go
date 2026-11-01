package logs

import (
	"encoding/json"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/rcss-labs/harbormaster/internal/model"
)

// taskDefinitionRef stores the SDK task definition as the poller does (json.Marshal of the SDK type).
func taskDefinitionRef(t *testing.T, containers ...ecstypes.ContainerDefinition) model.TaskDefinitionRef {
	t.Helper()
	raw, err := json.Marshal(ecstypes.TaskDefinition{Family: aws.String("payments-api"), ContainerDefinitions: containers})
	if err != nil {
		t.Fatal(err)
	}
	return model.TaskDefinitionRef{Family: "payments-api", Raw: raw}
}

func TestConfigsFromTaskDefinition_AwslogsAndFirelens(t *testing.T) {
	td := taskDefinitionRef(t,
		ecstypes.ContainerDefinition{Name: aws.String("payments-api"), LogConfiguration: &ecstypes.LogConfiguration{
			LogDriver: ecstypes.LogDriverAwslogs,
			Options: map[string]string{
				"awslogs-group": "/ecs/core-services/payments-api", "awslogs-region": "us-east-1", "awslogs-stream-prefix": "ecs",
			},
		}},
		ecstypes.ContainerDefinition{Name: aws.String("otel-collector"), LogConfiguration: &ecstypes.LogConfiguration{
			LogDriver: ecstypes.LogDriverAwsfirelens,
		}},
		ecstypes.ContainerDefinition{Name: aws.String("quiet")},
	)
	got, err := ConfigsFromTaskDefinition(td)
	if err != nil {
		t.Fatal(err)
	}
	want := []LogConfig{
		{Container: "payments-api", Driver: "awslogs", Group: "/ecs/core-services/payments-api", Region: "us-east-1", StreamPrefix: "ecs"},
		{Container: "otel-collector", Driver: "awsfirelens"},
		{Container: "quiet"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d configs, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("config %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if !got[0].Supported() || got[1].Supported() || got[2].Supported() {
		t.Errorf("Supported() = %v %v %v, want true false false", got[0].Supported(), got[1].Supported(), got[2].Supported())
	}
}

func TestConfigsFromTaskDefinition_FallsBackToParsedContainers(t *testing.T) {
	td := model.TaskDefinitionRef{Containers: []model.ContainerDef{
		{Name: "api", LogDriver: "awslogs", LogGroup: "/ecs/x/api", LogRegion: "eu-west-1", LogStreamPrefix: "ecs"},
	}}
	got, err := ConfigsFromTaskDefinition(td)
	if err != nil || len(got) != 1 || got[0].Group != "/ecs/x/api" || got[0].Region != "eu-west-1" {
		t.Fatalf("got %+v, err %v", got, err)
	}
}

func TestConfigsFromTaskDefinition_InvalidRaw(t *testing.T) {
	if _, err := ConfigsFromTaskDefinition(model.TaskDefinitionRef{Raw: []byte("not json")}); err == nil {
		t.Fatal("want error for invalid Raw")
	}
}

func TestStreamPrefixes_TrailingSlash(t *testing.T) {
	if got := StreamPrefix(LogConfig{Container: "api", StreamPrefix: "ecs"}); got != "ecs/api/" {
		t.Errorf("with prefix: %q, want ecs/api/", got)
	}
	if got := StreamPrefix(LogConfig{Container: "api"}); got != "api/" {
		t.Errorf("without prefix: %q, want api/", got)
	}
}

func TestParseStream_Table(t *testing.T) {
	tests := []struct{ name, container, taskID string }{
		{"ecs/payments-api/6faba45f99784d2b", "payments-api", "6faba45f99784d2b"},
		{"payments-api/6faba45f99784d2b", "", "6faba45f99784d2b"},
		{"6faba45f99784d2b", "", "6faba45f99784d2b"},
		{"a/b/c/d", "c", "d"},
	}
	for _, tc := range tests {
		c, id := ParseStream(tc.name)
		if c != tc.container || id != tc.taskID {
			t.Errorf("ParseStream(%q) = (%q, %q), want (%q, %q)", tc.name, c, id, tc.container, tc.taskID)
		}
	}
}

func TestFilterPattern_EscapesMetacharacters(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", ""},
		{"   ", ""},
		{`error "db"`, `"error" "\"db\""`},
		{"[warn]", `"[warn]"`},
		{`a\b`, `"a\\b"`},
		{"-x ? * {", `"-x" "?" "*" "{"`},
	}
	for _, tc := range tests {
		if got := FilterPattern(tc.in); got != tc.want {
			t.Errorf("FilterPattern(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}
}
