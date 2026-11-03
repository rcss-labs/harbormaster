package fake

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/rcss-labs/harbormaster/internal/awsx"
)

func TestFakeUpdateService_AppliesExecAndDeploymentConfiguration(t *testing.T) {
	f := newFake()
	api := ecsFor(t, f, homelabID, "us-east-1")
	ctx := context.Background()
	_, err := api.UpdateService(ctx, &ecs.UpdateServiceInput{
		Cluster: aws.String("core-services"), Service: aws.String("payments-api"),
		EnableExecuteCommand:          aws.Bool(false),
		HealthCheckGracePeriodSeconds: aws.Int32(120),
		DeploymentConfiguration: &ecstypes.DeploymentConfiguration{
			MinimumHealthyPercent:    aws.Int32(50),
			MaximumPercent:           aws.Int32(150),
			DeploymentCircuitBreaker: &ecstypes.DeploymentCircuitBreaker{Enable: true, Rollback: false},
		}})
	if err != nil {
		t.Fatal(err)
	}
	svc := describe(t, f, "core-services", "payments-api")
	cfg := svc.DeploymentConfiguration
	if svc.EnableExecuteCommand || aws.ToInt32(svc.HealthCheckGracePeriodSeconds) != 120 ||
		aws.ToInt32(cfg.MinimumHealthyPercent) != 50 || aws.ToInt32(cfg.MaximumPercent) != 150 ||
		!cfg.DeploymentCircuitBreaker.Enable || cfg.DeploymentCircuitBreaker.Rollback {
		t.Fatalf("service = exec %v grace %v config %+v", svc.EnableExecuteCommand, svc.HealthCheckGracePeriodSeconds, cfg)
	}
	if len(svc.Deployments) != 1 {
		t.Fatalf("settings-only update started a deployment: %d deployments", len(svc.Deployments))
	}

	var invalid *ecstypes.InvalidParameterException
	_, err = api.UpdateService(ctx, &ecs.UpdateServiceInput{
		Cluster: aws.String("core-services"), Service: aws.String("payments-api"),
		DeploymentConfiguration: &ecstypes.DeploymentConfiguration{
			DeploymentCircuitBreaker: &ecstypes.DeploymentCircuitBreaker{Enable: false, Rollback: true}}})
	if !errors.As(err, &invalid) {
		t.Fatalf("rollback without enable: err = %v, want InvalidParameterException", err)
	}
	edge := ecsFor(t, f, homelabID, "eu-central-1")
	_, err = edge.UpdateService(ctx, &ecs.UpdateServiceInput{
		Cluster: aws.String("edge-proxy"), Service: aws.String("cert-renewer"),
		HealthCheckGracePeriodSeconds: aws.Int32(30)})
	if !errors.As(err, &invalid) {
		t.Fatalf("grace period without load balancer: err = %v, want InvalidParameterException", err)
	}
}

func TestFakeRollout_NewTasksInheritExec(t *testing.T) {
	f := newFake()
	api := ecsFor(t, f, homelabID, "eu-central-1")
	if _, err := api.UpdateService(context.Background(), &ecs.UpdateServiceInput{
		Cluster: aws.String("edge-proxy"), Service: aws.String("cert-renewer"),
		EnableExecuteCommand: aws.Bool(true), ForceNewDeployment: true}); err != nil {
		t.Fatal(err)
	}
	advance(f, 10)
	running := tasksOf(f, "cert-renewer", "RUNNING")
	if len(running) != 1 || !running[0].EnableExecuteCommand {
		t.Fatalf("running tasks after exec rollout = %+v, want one with exec enabled", running)
	}
	agents := running[0].Containers[0].ManagedAgents
	if len(agents) != 1 || agents[0].Name != ecstypes.ManagedAgentNameExecuteCommandAgent {
		t.Fatalf("managed agents = %+v, want the ExecuteCommandAgent", agents)
	}
}

func tagsOfRepository(t *testing.T, f *Fake, account, region, repository string) []string {
	t.Helper()
	repo, err := f.ECR(context.Background(), awsx.Target{AccountID: account, Region: region})
	if err != nil {
		t.Fatal(err)
	}
	out, err := repo.DescribeImages(context.Background(), &ecr.DescribeImagesInput{RepositoryName: aws.String(repository)})
	if err != nil {
		t.Fatalf("DescribeImages %s: %v", repository, err)
	}
	var tags []string
	for _, img := range out.ImageDetails {
		tags = append(tags, img.ImageTags...)
	}
	return tags
}

func TestFakeECR_DescribeImagesPerRepository(t *testing.T) {
	f := newFake()
	payments := tagsOfRepository(t, f, homelabID, "us-east-1", "payments-api")
	if len(payments) != 24 || payments[0] != "1.18.4" || payments[2] != "1.18.2" || payments[23] != "1.16.1" {
		t.Fatalf("payments-api tags = %v, want 24 newest first", payments)
	}
	seen := map[string]bool{}
	for _, tag := range payments {
		if seen[tag] {
			t.Fatalf("duplicate tag %s in %v", tag, payments)
		}
		seen[tag] = true
	}

	migrator := tagsOfRepository(t, f, stagingID, "us-east-1", "migrator")
	if len(migrator) != 6 || migrator[0] != "1.4.4" {
		t.Fatalf("migrator tags = %v, want 6 newest first", migrator)
	}
	// The sidecar repository exists in the staging account only.
	repo, _ := f.ECR(context.Background(), awsx.Target{AccountID: homelabID, Region: "us-east-1"})
	_, err := repo.DescribeImages(context.Background(), &ecr.DescribeImagesInput{RepositoryName: aws.String("migrator")})
	var notFound *ecrtypes.RepositoryNotFoundException
	if !errors.As(err, &notFound) {
		t.Fatalf("migrator in homelab: err = %v, want RepositoryNotFoundException", err)
	}
	if got := tagsOfRepository(t, f, stagingID, "us-east-1", "api"); len(got) == 0 {
		t.Fatal("staging api repository has no tags")
	}
}
