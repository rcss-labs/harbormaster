package store_test

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

const (
	leakedEnvValue  = "v4lue-must-not-leak"
	leakedSecretARN = "arn:aws:secretsmanager:eu-west-1:111111111111:secret:must-not-leak"
)

func configTDARN(family string, revision int) string {
	return "arn:aws:ecs:eu-west-1:" + readAccount + ":task-definition/" + family + ":" + string(rune('0'+revision%10))
}

// putConfigTaskDefinition stores a definition the way the poller does: Raw is json.Marshal of the SDK struct.
func putConfigTaskDefinition(t *testing.T, fx readFixture, family string, revision int, def ecstypes.TaskDefinition) string {
	t.Helper()
	arn := configTDARN(family, revision)
	def.TaskDefinitionArn = aws.String(arn)
	raw, err := json.Marshal(def)
	if err != nil {
		t.Fatal(err)
	}
	err = fx.s.UpsertTaskDefinition(context.Background(), model.TaskDefinitionRef{
		AccountID: readAccount, Region: "eu-west-1", ARN: arn, Family: family, Revision: revision,
		TaskRoleARN: aws.ToString(def.TaskRoleArn), ExecutionRoleARN: aws.ToString(def.ExecutionRoleArn),
		Raw: raw, PolledAt: fx.now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return arn
}

// setConfigService rewrites a fixture service so it points at a task definition and carries deployments.
func setConfigService(t *testing.T, fx readFixture, cluster, name, family string, revision int, primaryARN string, deployments []model.Deployment) {
	t.Helper()
	_, err := fx.s.UpsertServices(context.Background(), []model.Service{{
		Key: serviceKey(cluster, name), Desired: 1, Running: 1, Family: family, Revision: revision,
		TaskDefinitionARN: primaryARN, Deployments: deployments, PolledAt: fx.now,
	}})
	if err != nil {
		t.Fatal(err)
	}
}

func configRowFor(t *testing.T, rows []store.ConfigRow, service string) store.ConfigRow {
	t.Helper()
	for _, r := range rows {
		if r.Key.Service == service {
			return r
		}
	}
	t.Fatalf("no row for service %q in %d rows", service, len(rows))
	return store.ConfigRow{}
}

func configServiceNames(rows []store.ConfigRow) []string {
	names := []string{}
	for _, r := range rows {
		names = append(names, r.Key.Service)
	}
	return names
}

func sampleDefinition() ecstypes.TaskDefinition {
	return ecstypes.TaskDefinition{
		TaskRoleArn:      aws.String("arn:aws:iam::111111111111:role/payments-task"),
		ExecutionRoleArn: aws.String("arn:aws:iam::111111111111:role/ecs-exec"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{
			{
				Name: aws.String("app"), Image: aws.String("111111111111.dkr.ecr.eu-west-1.amazonaws.com/payments:2.1.0"),
				PortMappings: []ecstypes.PortMapping{{ContainerPort: aws.Int32(8080)}, {ContainerPort: aws.Int32(9090)}},
				Environment: []ecstypes.KeyValuePair{
					{Name: aws.String("PORT"), Value: aws.String(leakedEnvValue)},
					{Name: aws.String("NODE_ENV"), Value: aws.String("production")},
				},
				Secrets: []ecstypes.Secret{{Name: aws.String("DATABASE_URL"), ValueFrom: aws.String(leakedSecretARN)}},
				LogConfiguration: &ecstypes.LogConfiguration{
					LogDriver: ecstypes.LogDriverAwslogs,
					Options:   map[string]string{"awslogs-group": "/ecs/payments", "awslogs-region": "eu-west-1"},
				},
			},
			{Name: aws.String("sidecar"), Image: aws.String("nginx:1.27")},
		},
	}
}

func TestConfigSearchRows_ProjectsNames(t *testing.T) {
	fx := newReadFixture(t)
	arn := putConfigTaskDefinition(t, fx, "payments", 3, sampleDefinition())
	setConfigService(t, fx, "core-services", "payments-api", "payments", 3, arn, nil)

	rows, err := fx.s.ConfigSearchRows(context.Background(), store.TargetFilter{}, store.Visibility{Admin: true})
	if err != nil {
		t.Fatal(err)
	}
	row := configRowFor(t, rows, "payments-api")
	if row.Family != "payments" || len(row.Revisions) != 1 {
		t.Fatalf("row = %+v", row)
	}
	rev := row.Revisions[0]
	if !rev.Primary || !rev.Loaded || rev.ARN != arn || rev.Revision != 3 {
		t.Errorf("revision = %+v", rev)
	}
	if rev.TaskRoleARN != "arn:aws:iam::111111111111:role/payments-task" || rev.ExecutionRoleARN != "arn:aws:iam::111111111111:role/ecs-exec" {
		t.Errorf("roles = %q %q", rev.TaskRoleARN, rev.ExecutionRoleARN)
	}
	want := []store.ConfigContainer{
		{Name: "app", Image: "111111111111.dkr.ecr.eu-west-1.amazonaws.com/payments:2.1.0", LogGroup: "/ecs/payments",
			Ports: []int{8080, 9090}, EnvNames: []string{"PORT", "NODE_ENV"}, SecretNames: []string{"DATABASE_URL"}},
		{Name: "sidecar", Image: "nginx:1.27", Ports: []int{}, EnvNames: []string{}, SecretNames: []string{}},
	}
	if !reflect.DeepEqual(rev.Containers, want) {
		t.Errorf("containers = %+v\nwant %+v", rev.Containers, want)
	}
}

func TestConfigSearchRows_NeverReturnsValues(t *testing.T) {
	fx := newReadFixture(t)
	arn := putConfigTaskDefinition(t, fx, "payments", 3, sampleDefinition())
	setConfigService(t, fx, "core-services", "payments-api", "payments", 3, arn, nil)

	rows, err := fx.s.ConfigSearchRows(context.Background(), store.TargetFilter{}, store.Visibility{Admin: true})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	for _, leaked := range []string{leakedEnvValue, leakedSecretARN, "secretsmanager"} {
		if strings.Contains(string(body), leaked) {
			t.Errorf("result contains %q", leaked)
		}
	}
	for _, name := range []string{"PORT", "DATABASE_URL"} {
		if !strings.Contains(string(body), name) {
			t.Errorf("result lacks the name %q", name)
		}
	}
}

func TestConfigSearchRows_OnlyVisibleServices(t *testing.T) {
	fx := newReadFixture(t)
	ctx := context.Background()
	cases := []struct {
		name string
		vis  store.Visibility
		want []string
	}{
		{"dev only", fx.vis(fx.dev), []string{"image-resizer"}},
		{"prod only", fx.vis(fx.prod), []string{"notifications", "payments-api", "web"}},
		{"staging only", fx.vis(fx.staging), []string{}},
		{"admin", store.Visibility{Admin: true}, []string{"image-resizer", "notifications", "payments-api", "web"}},
		{"no tags", fx.vis(), []string{}},
	}
	for _, c := range cases {
		rows, err := fx.s.ConfigSearchRows(ctx, store.TargetFilter{}, c.vis)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := configServiceNames(rows); !equalStrings(got, c.want) {
			t.Errorf("%s: services = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestConfigSearchRows_TargetFilter(t *testing.T) {
	fx := newReadFixture(t)
	ctx := context.Background()
	_, err := fx.s.UpsertServices(ctx, []model.Service{{
		Key:      model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: "222222222222", Region: "us-east-1", Cluster: "other"}, Service: "elsewhere"},
		PolledAt: fx.now,
	}})
	if err != nil {
		t.Fatal(err)
	}
	admin := store.Visibility{Admin: true}
	rows, err := fx.s.ConfigSearchRows(ctx, store.TargetFilter{AccountID: "222222222222"}, admin)
	if err != nil || !equalStrings(configServiceNames(rows), []string{"elsewhere"}) {
		t.Errorf("account filter: %v %v", configServiceNames(rows), err)
	}
	rows, err = fx.s.ConfigSearchRows(ctx, store.TargetFilter{AccountID: readAccount, Region: "us-east-1"}, admin)
	if err != nil || len(rows) != 0 {
		t.Errorf("account and region filter: %v %v", configServiceNames(rows), err)
	}
	rows, err = fx.s.ConfigSearchRows(ctx, store.TargetFilter{Region: "eu-west-1"}, admin)
	if err != nil || len(rows) != 4 {
		t.Errorf("region filter: %v %v", configServiceNames(rows), err)
	}
}

func TestConfigSearchRows_IncludesRunningActiveRevision(t *testing.T) {
	fx := newReadFixture(t)
	def := sampleDefinition()
	arn10 := putConfigTaskDefinition(t, fx, "notifications", 1, def) // numbered ":1" in the ARN helper; revision field below is what counts
	arn9 := putConfigTaskDefinition(t, fx, "notifications", 2, def)
	arn8 := putConfigTaskDefinition(t, fx, "notifications", 3, def)
	arn7 := putConfigTaskDefinition(t, fx, "notifications", 4, def)
	setConfigService(t, fx, "core-services", "notifications", "notifications", 10, arn10, []model.Deployment{
		{ID: "d1", Status: "PRIMARY", TaskDefinitionARN: arn10, Revision: 10, Running: 1},
		{ID: "d2", Status: "ACTIVE", TaskDefinitionARN: arn9, Revision: 9, Running: 1},
		{ID: "d3", Status: "ACTIVE", TaskDefinitionARN: arn8, Revision: 8},
		{ID: "d4", Status: "INACTIVE", TaskDefinitionARN: arn7, Revision: 7, Running: 2},
	})

	rows, err := fx.s.ConfigSearchRows(context.Background(), store.TargetFilter{}, store.Visibility{Admin: true})
	if err != nil {
		t.Fatal(err)
	}
	row := configRowFor(t, rows, "notifications")
	if len(row.Revisions) != 2 {
		t.Fatalf("revisions = %+v", row.Revisions)
	}
	if !row.Revisions[0].Primary || row.Revisions[0].Revision != 10 || row.Revisions[0].ARN != arn10 {
		t.Errorf("first revision = %+v", row.Revisions[0])
	}
	if row.Revisions[1].Primary || row.Revisions[1].Revision != 9 || row.Revisions[1].ARN != arn9 || !row.Revisions[1].Loaded {
		t.Errorf("second revision = %+v", row.Revisions[1])
	}
}

func TestConfigSearchRows_DefinitionNotCached(t *testing.T) {
	fx := newReadFixture(t)
	setConfigService(t, fx, "core-services", "payments-api", "payments", 3, configTDARN("payments", 3), nil)

	rows, err := fx.s.ConfigSearchRows(context.Background(), store.TargetFilter{}, store.Visibility{Admin: true})
	if err != nil {
		t.Fatal(err)
	}
	row := configRowFor(t, rows, "payments-api")
	if len(row.Revisions) != 1 || row.Revisions[0].Loaded || len(row.Revisions[0].Containers) != 0 {
		t.Errorf("revisions = %+v", row.Revisions)
	}
}

func TestConfigSearchRows_NullAndEmptyArrays(t *testing.T) {
	fx := newReadFixture(t)
	arn := putConfigTaskDefinition(t, fx, "bare", 1, ecstypes.TaskDefinition{
		ContainerDefinitions: []ecstypes.ContainerDefinition{{Name: aws.String("app"), Image: aws.String("busybox:1")}},
	})
	empty := putConfigTaskDefinition(t, fx, "empty", 1, ecstypes.TaskDefinition{}) // ContainerDefinitions marshals as null
	setConfigService(t, fx, "core-services", "payments-api", "bare", 1, arn, nil)
	setConfigService(t, fx, "core-services", "notifications", "empty", 1, empty, nil)

	rows, err := fx.s.ConfigSearchRows(context.Background(), store.TargetFilter{}, store.Visibility{Admin: true})
	if err != nil {
		t.Fatal(err)
	}
	bare := configRowFor(t, rows, "payments-api").Revisions[0].Containers
	want := []store.ConfigContainer{{Name: "app", Image: "busybox:1", Ports: []int{}, EnvNames: []string{}, SecretNames: []string{}}}
	if !reflect.DeepEqual(bare, want) {
		t.Errorf("containers = %+v, want %+v", bare, want)
	}
	if rev := configRowFor(t, rows, "notifications").Revisions[0]; !rev.Loaded || len(rev.Containers) != 0 {
		t.Errorf("empty definition = %+v", rev)
	}
}
