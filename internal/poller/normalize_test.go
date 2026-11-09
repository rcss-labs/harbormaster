package poller

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

var normTarget = awsx.Target{AccountID: "482109375512", Region: "us-east-1"}

func loadTaskFixture(t *testing.T) ecstypes.Task {
	t.Helper()
	data, err := os.ReadFile("testdata/task.json")
	if err != nil {
		t.Fatal(err)
	}
	var task ecstypes.Task
	if err := json.Unmarshal(data, &task); err != nil {
		t.Fatalf("fixture does not decode into ecstypes.Task: %v", err)
	}
	return task
}

func TestNormalizeTask_Table(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	fixture := loadTaskFixture(t)

	standalone := fixture
	standalone.Group = aws.String("family:release-build")
	standalone.StartedBy = aws.String("events-rule/nightly-release")

	cases := []struct {
		name string
		task ecstypes.Task
		want func(t *testing.T, got model.Task)
	}{
		{"service task", fixture, func(t *testing.T, got model.Task) {
			checks := map[string][2]string{
				"ServiceName":      {got.ServiceName, "payments-api"},
				"SubnetID":         {got.SubnetID, "subnet-0a1b2c3d4e5f60718"},
				"PrivateIP":        {got.PrivateIP, "10.0.12.34"},
				"PublicIP":         {got.PublicIP, "3.88.120.45"},
				"ENIID":            {got.ENIID, "eni-0123456789abcdef0"},
				"CapacityProvider": {got.CapacityProvider, "FARGATE_SPOT"},
				"LaunchType":       {got.LaunchType, "FARGATE"},
				"Family":           {got.Family, "payments-api"},
				"LastStatus":       {got.LastStatus, "RUNNING"},
				"HealthStatus":     {got.HealthStatus, "HEALTHY"},
				"AvailabilityZone": {got.AvailabilityZone, "us-east-1b"},
				"CPU":              {got.CPU, "512"},
				"Memory":           {got.Memory, "1024"},
				"Group":            {got.Group, "service:payments-api"},
			}
			for field, pair := range checks {
				if pair[0] != pair[1] {
					t.Errorf("%s = %q, want %q", field, pair[0], pair[1])
				}
			}
			if got.Revision != 42 {
				t.Errorf("Revision = %d, want 42", got.Revision)
			}
			if len(got.Containers) != 2 {
				t.Fatalf("containers = %d, want 2", len(got.Containers))
			}
			main := got.Containers[0]
			if main.Name != "payments-api" || main.ExecAgentStatus != "RUNNING" || main.RuntimeID == "" || main.ImageDigest == "" || main.ExitCode != nil {
				t.Errorf("main container = %+v", main)
			}
			side := got.Containers[1]
			if side.ExitCode == nil || *side.ExitCode != 0 || side.Reason == "" || side.ExecAgentStatus != "" {
				t.Errorf("sidecar = %+v", side)
			}
			if got.StartedAt == nil || got.StartedAt.Location() != time.UTC {
				t.Errorf("StartedAt = %v, want UTC", got.StartedAt)
			}
			if got.CreatedAt.Location() != time.UTC || !got.CreatedAt.Equal(time.Date(2026, 10, 31, 11, 58, 20, 0, time.UTC)) {
				t.Errorf("CreatedAt = %v", got.CreatedAt)
			}
			if got.StoppedAt != nil {
				t.Errorf("StoppedAt = %v, want nil", got.StoppedAt)
			}
		}},
		{"standalone task", standalone, func(t *testing.T, got model.Task) {
			if got.ServiceName != "" || got.Group != "family:release-build" || got.StartedBy != "events-rule/nightly-release" {
				t.Errorf("standalone = service %q group %q startedBy %q", got.ServiceName, got.Group, got.StartedBy)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeTask(normTarget, "core-services", tc.task, now)
			if got.Cluster.Cluster != "core-services" || got.Cluster.AccountID != normTarget.AccountID || got.Cluster.Region != normTarget.Region {
				t.Errorf("cluster key = %+v", got.Cluster)
			}
			if !got.PolledAt.Equal(now) {
				t.Errorf("PolledAt = %v, want %v", got.PolledAt, now)
			}
			var back ecstypes.Task
			if err := json.Unmarshal(got.Raw, &back); err != nil || aws.ToString(back.TaskArn) != aws.ToString(tc.task.TaskArn) {
				t.Errorf("Raw does not round-trip: err=%v arn=%q", err, aws.ToString(back.TaskArn))
			}
			tc.want(t, got)
		})
	}
}

func TestNormalizeTask_HashIndependentOfLocalTimezone(t *testing.T) {
	now := time.Now()
	task := loadTaskFixture(t)
	first := store.ContentHash(normalizeTask(normTarget, "core-services", task, now))

	original := time.Local
	defer func() { time.Local = original }()
	time.Local = time.FixedZone("far-east", 9*3600)
	shifted := loadTaskFixture(t) // times re-parsed in the new local zone
	if second := store.ContentHash(normalizeTask(normTarget, "core-services", shifted, now)); second != first {
		t.Fatalf("content hash changed with process timezone: %s vs %s", first, second)
	}
}

func TestNormalizeService_Deployments(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	created := time.Date(2026, 10, 31, 9, 0, 0, 0, time.FixedZone("x", 3*3600))
	oldTD := "arn:aws:ecs:us-east-1:482109375512:task-definition/notifications:9"
	newTD := "arn:aws:ecs:us-east-1:482109375512:task-definition/notifications:10"
	svc := ecstypes.Service{
		ServiceArn:           aws.String("arn:aws:ecs:us-east-1:482109375512:service/core-services/notifications"),
		ServiceName:          aws.String("notifications"),
		Status:               aws.String("ACTIVE"),
		DesiredCount:         2,
		RunningCount:         2,
		PendingCount:         1,
		TaskDefinition:       aws.String(newTD),
		LaunchType:           ecstypes.LaunchTypeFargate,
		SchedulingStrategy:   ecstypes.SchedulingStrategyReplica,
		EnableExecuteCommand: true,
		CreatedAt:            &created,
		DeploymentController: &ecstypes.DeploymentController{Type: ecstypes.DeploymentControllerTypeEcs},
		DeploymentConfiguration: &ecstypes.DeploymentConfiguration{
			MaximumPercent: aws.Int32(200), MinimumHealthyPercent: aws.Int32(100),
			DeploymentCircuitBreaker: &ecstypes.DeploymentCircuitBreaker{Enable: true, Rollback: true},
		},
		NetworkConfiguration: &ecstypes.NetworkConfiguration{AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{
			Subnets: []string{"subnet-a", "subnet-b"}, SecurityGroups: []string{"sg-1"}, AssignPublicIp: ecstypes.AssignPublicIpEnabled}},
		LoadBalancers: []ecstypes.LoadBalancer{{TargetGroupArn: aws.String("arn:tg"), ContainerName: aws.String("notifications"), ContainerPort: aws.Int32(8081)}},
		Deployments: []ecstypes.Deployment{
			{Id: aws.String("ecs-svc/2"), Status: aws.String("PRIMARY"), TaskDefinition: aws.String(newTD), DesiredCount: 2, PendingCount: 1, FailedTasks: 3,
				RolloutState: ecstypes.DeploymentRolloutStateInProgress, RolloutStateReason: aws.String("in progress"), CreatedAt: &created, UpdatedAt: &created},
			{Id: aws.String("ecs-svc/1"), Status: aws.String("ACTIVE"), TaskDefinition: aws.String(oldTD), DesiredCount: 2, RunningCount: 2,
				RolloutState: ecstypes.DeploymentRolloutStateCompleted},
		},
		Events: []ecstypes.ServiceEvent{
			{Id: aws.String("evt-2"), CreatedAt: &created, Message: aws.String("started 1 tasks")},
			{Id: aws.String("evt-1"), CreatedAt: &created, Message: aws.String("steady state")},
		},
	}
	key := "core-services"
	got, events := normalizeService(normTarget, key, svc, now)

	if got.Key.Service != "notifications" || got.Key.Cluster != key || got.Family != "notifications" || got.Revision != 10 {
		t.Errorf("key/family/revision = %+v %q %d", got.Key, got.Family, got.Revision)
	}
	if got.TaskDefinitionARN != newTD || got.Desired != 2 || got.Running != 2 || got.Pending != 1 {
		t.Errorf("td/counts = %q %d %d %d", got.TaskDefinitionARN, got.Desired, got.Running, got.Pending)
	}
	if !got.EnableExecuteCommand || got.LaunchType != "FARGATE" || got.SchedulingStrategy != "REPLICA" || got.DeploymentController != "ECS" {
		t.Errorf("flags = %+v", got)
	}
	if got.MinHealthyPercent != 100 || got.MaxPercent != 200 || !got.CircuitBreakerEnable || !got.CircuitBreakerRollback {
		t.Errorf("deployment configuration not carried: %+v", got)
	}
	if len(got.Subnets) != 2 || got.SecurityGroups[0] != "sg-1" || !got.AssignPublicIP {
		t.Errorf("network = %v %v %v", got.Subnets, got.SecurityGroups, got.AssignPublicIP)
	}
	if len(got.LoadBalancers) != 1 || got.LoadBalancers[0].ContainerPort != 8081 {
		t.Errorf("load balancers = %+v", got.LoadBalancers)
	}
	if len(got.Deployments) != 2 {
		t.Fatalf("deployments = %d, want 2", len(got.Deployments))
	}
	primary, active := got.Deployments[0], got.Deployments[1]
	if primary.Status != "PRIMARY" || primary.Revision != 10 || primary.RolloutState != "IN_PROGRESS" || primary.Failed != 3 || primary.Pending != 1 {
		t.Errorf("primary = %+v", primary)
	}
	if active.Status != "ACTIVE" || active.Revision != 9 || active.RolloutState != "COMPLETED" || active.Running != 2 {
		t.Errorf("active = %+v", active)
	}
	if primary.CreatedAt.Location() != time.UTC || !got.CreatedAt.Equal(created) || got.CreatedAt.Location() != time.UTC {
		t.Errorf("times not UTC: %v %v", primary.CreatedAt, got.CreatedAt)
	}
	if len(events) != 2 || events[0].ID != "evt-2" || events[0].Message != "started 1 tasks" || events[0].Key != got.Key {
		t.Errorf("events = %+v", events)
	}
	var back ecstypes.Service
	if err := json.Unmarshal(got.Raw, &back); err != nil || aws.ToString(back.ServiceName) != "notifications" {
		t.Errorf("Raw does not round-trip: %v", err)
	}
}

func TestNormalizeCluster_And_TaskDefinition(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	cluster := normalizeCluster(normTarget, ecstypes.Cluster{
		ClusterArn: aws.String("arn:aws:ecs:us-east-1:482109375512:cluster/core-services"), ClusterName: aws.String("core-services"),
		Status: aws.String("ACTIVE"), RunningTasksCount: 7, PendingTasksCount: 1, ActiveServicesCount: 4,
		CapacityProviders: []string{"FARGATE"},
		Settings:          []ecstypes.ClusterSetting{{Name: ecstypes.ClusterSettingNameContainerInsights, Value: aws.String("enhanced")}},
	}, now)
	if cluster.Key.Cluster != "core-services" || cluster.RunningTasks != 7 || cluster.PendingTasks != 1 || cluster.ActiveServices != 4 || !cluster.ContainerInsights {
		t.Errorf("cluster = %+v", cluster)
	}

	registered := time.Date(2026, 10, 30, 8, 0, 0, 0, time.FixedZone("x", -5*3600))
	td := normalizeTaskDefinition(normTarget, ecstypes.TaskDefinition{
		TaskDefinitionArn: aws.String("arn:aws:ecs:us-east-1:482109375512:task-definition/report-worker:31"),
		Family:            aws.String("report-worker"), Revision: 31, Cpu: aws.String("1024"), Memory: aws.String("2048"),
		NetworkMode: ecstypes.NetworkModeAwsvpc, TaskRoleArn: aws.String("arn:role/task"), ExecutionRoleArn: aws.String("arn:role/exec"),
		RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate}, RegisteredAt: &registered,
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			Name: aws.String("report-worker"), Image: aws.String("repo/report-worker:5.0.1"), Essential: aws.Bool(true),
			LogConfiguration: &ecstypes.LogConfiguration{LogDriver: ecstypes.LogDriverAwslogs, Options: map[string]string{
				"awslogs-group": "/ecs/report-worker", "awslogs-region": "us-east-1", "awslogs-stream-prefix": "ecs"}},
			MountPoints: []ecstypes.MountPoint{{SourceVolume: aws.String("reports"), ContainerPath: aws.String("/data"), ReadOnly: aws.Bool(true)}},
		}},
		Volumes: []ecstypes.Volume{{Name: aws.String("reports"), EfsVolumeConfiguration: &ecstypes.EFSVolumeConfiguration{
			FileSystemId: aws.String("fs-123"), AuthorizationConfig: &ecstypes.EFSAuthorizationConfig{AccessPointId: aws.String("fsap-9")}}}},
	}, now)
	if td.Family != "report-worker" || td.Revision != 31 || td.NetworkMode != "awsvpc" || td.AccountID != normTarget.AccountID || td.Region != "us-east-1" {
		t.Errorf("td = %+v", td)
	}
	if len(td.Containers) != 1 || td.Containers[0].LogGroup != "/ecs/report-worker" || td.Containers[0].LogRegion != "us-east-1" ||
		td.Containers[0].LogStreamPrefix != "ecs" || td.Containers[0].LogDriver != "awslogs" || !td.Containers[0].Essential ||
		len(td.Containers[0].MountPoints) != 1 || !td.Containers[0].MountPoints[0].ReadOnly {
		t.Errorf("containers = %+v", td.Containers)
	}
	if len(td.EFSVolumes) != 1 || td.EFSVolumes[0] != (model.EFSVolume{Name: "reports", FileSystemID: "fs-123", AccessPointID: "fsap-9"}) {
		t.Errorf("efs = %+v", td.EFSVolumes)
	}
	if td.RegisteredAt.Location() != time.UTC {
		t.Errorf("RegisteredAt not UTC: %v", td.RegisteredAt)
	}
}

func TestChunk(t *testing.T) {
	got := chunk([]int{1, 2, 3, 4, 5}, 2)
	if len(got) != 3 || len(got[2]) != 1 || len(chunk([]int{}, 3)) != 0 {
		t.Fatalf("chunk = %v", got)
	}
}

func TestNormalizeService_TagsDriveContentHash(t *testing.T) {
	now := time.Now()
	withTags := func(tags ...ecstypes.Tag) ecstypes.Service {
		return ecstypes.Service{ServiceName: aws.String("api"), Status: aws.String("ACTIVE"), Tags: tags}
	}
	env := ecstypes.Tag{Key: aws.String("env"), Value: aws.String("staging")}
	team := ecstypes.Tag{Key: aws.String("team"), Value: aws.String("core")}
	hash := func(s ecstypes.Service) string {
		svc, _ := normalizeService(normTarget, "core-services", s, now)
		return store.ContentHash(svc)
	}

	if hash(withTags(env, team)) != hash(withTags(team, env)) {
		t.Error("same tags in a different order changed the content hash")
	}
	base := hash(withTags(env, team))
	for name, changed := range map[string]ecstypes.Service{
		"removed": withTags(team),
		"edited":  withTags(ecstypes.Tag{Key: aws.String("env"), Value: aws.String("prod")}, team),
		"added":   withTags(env, team, ecstypes.Tag{Key: aws.String("owner"), Value: aws.String("x")}),
	} {
		if hash(changed) == base {
			t.Errorf("tag %s did not change the content hash", name)
		}
	}
	if hash(withTags()) != hash(ecstypes.Service{ServiceName: aws.String("api"), Status: aws.String("ACTIVE")}) {
		t.Error("empty and absent tags hash differently")
	}
}
