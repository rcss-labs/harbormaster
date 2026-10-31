package fake

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/rcss-labs/harbormaster/internal/awsx"
)

func TestFakeRegisterTaskDefinition_IncrementsRevisionAndKeepsTags(t *testing.T) {
	api := ecsFor(t, newFake(), homelabID, "us-east-1")
	ctx := context.Background()
	tags := []ecstypes.Tag{{Key: aws.String("owner"), Value: aws.String("pavel")}}
	out, err := api.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:               aws.String("payments-api"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{Name: aws.String("payments-api"), Image: aws.String("payments-api:1.18.3")}},
		Tags:                 tags,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.TaskDefinition.Revision != 43 {
		t.Fatalf("revision = %d, want 43 (seeded payments-api is at 42)", out.TaskDefinition.Revision)
	}
	listed, err := api.ListTagsForResource(ctx, &ecs.ListTagsForResourceInput{ResourceArn: out.TaskDefinition.TaskDefinitionArn})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Tags) != 1 || aws.ToString(listed.Tags[0].Key) != "owner" || aws.ToString(listed.Tags[0].Value) != "pavel" {
		t.Fatalf("tags on new revision = %+v, want the input tags", listed.Tags)
	}
}

func TestFakeListTaskDefinitions_PrefixMatchesLikeAWS(t *testing.T) {
	api := ecsFor(t, newFake(), homelabID, "us-east-1")
	ctx := context.Background()
	for _, family := range []string{"api", "api", "api-worker"} {
		if _, err := api.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
			Family:               aws.String(family),
			ContainerDefinitions: []ecstypes.ContainerDefinition{{Name: aws.String(family), Image: aws.String(family + ":1")}},
		}); err != nil {
			t.Fatal(err)
		}
	}

	all, err := api.ListTaskDefinitions(ctx, &ecs.ListTaskDefinitionsInput{FamilyPrefix: aws.String("api")})
	if err != nil {
		t.Fatal(err)
	}
	if len(all.TaskDefinitionArns) != 3 {
		t.Fatalf("prefix api returned %v, want api:1, api:2 and api-worker:1", all.TaskDefinitionArns)
	}

	desc, err := api.ListTaskDefinitions(ctx, &ecs.ListTaskDefinitionsInput{FamilyPrefix: aws.String("api"), Sort: ecstypes.SortOrderDesc})
	if err != nil {
		t.Fatal(err)
	}
	var apiRevisions []string
	for _, arn := range desc.TaskDefinitionArns {
		if strings.HasSuffix(arn, "task-definition/api:1") || strings.HasSuffix(arn, "task-definition/api:2") {
			apiRevisions = append(apiRevisions, arn[strings.LastIndex(arn, ":")+1:])
		}
	}
	if len(apiRevisions) != 2 || apiRevisions[0] != "2" || apiRevisions[1] != "1" {
		t.Fatalf("DESC revisions of family api = %v, want [2 1]", apiRevisions)
	}

	page, err := api.ListTaskDefinitions(ctx, &ecs.ListTaskDefinitionsInput{FamilyPrefix: aws.String("api"), MaxResults: aws.Int32(1)})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.TaskDefinitionArns) != 1 || page.NextToken == nil {
		t.Fatalf("maxResults 1 page = %v next=%v, want one item and a next token", page.TaskDefinitionArns, page.NextToken)
	}
}

func TestFakeStopTask_SetsReasonAndEvent(t *testing.T) {
	api := ecsFor(t, newFake(), homelabID, "us-east-1")
	ctx := context.Background()
	running, err := api.ListTasks(ctx, &ecs.ListTasksInput{Cluster: aws.String("core-services"), ServiceName: aws.String("notifications")})
	if err != nil || len(running.TaskArns) == 0 {
		t.Fatalf("ListTasks: %v %v", err, running.TaskArns)
	}
	taskARN := running.TaskArns[0]
	const reason = "Stopped by pavel via Harbormaster"
	if _, err := api.StopTask(ctx, &ecs.StopTaskInput{Cluster: aws.String("core-services"), Task: aws.String(taskARN), Reason: aws.String(reason)}); err != nil {
		t.Fatal(err)
	}

	desc, err := api.DescribeTasks(ctx, &ecs.DescribeTasksInput{Cluster: aws.String("core-services"), Tasks: []string{taskARN}})
	if err != nil || len(desc.Tasks) != 1 {
		t.Fatalf("DescribeTasks: %v %+v", err, desc)
	}
	if got := aws.ToString(desc.Tasks[0].LastStatus); got != "STOPPED" {
		t.Fatalf("last status = %s, want STOPPED", got)
	}
	if got := aws.ToString(desc.Tasks[0].StoppedReason); got != reason {
		t.Fatalf("stopped reason = %q, want %q", got, reason)
	}

	svc, err := api.DescribeServices(ctx, &ecs.DescribeServicesInput{Cluster: aws.String("core-services"), Services: []string{"notifications"}})
	if err != nil || len(svc.Services) != 1 {
		t.Fatalf("DescribeServices: %v", err)
	}
	found := false
	for _, ev := range svc.Services[0].Events {
		if strings.Contains(aws.ToString(ev.Message), "has stopped 1 running tasks") {
			found = true
		}
	}
	if !found {
		t.Fatal("service events do not record the stop")
	}
}

func TestFakeDescribeImages_SeededTagsNewestFirst(t *testing.T) {
	f := newFake()
	ctx := context.Background()
	repo, err := f.ECR(ctx, awsx.Target{AccountID: homelabID, Region: "us-east-1"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := repo.DescribeImages(ctx, &ecr.DescribeImagesInput{RepositoryName: aws.String("image-resizer")})
	if err != nil {
		t.Fatal(err)
	}
	var tags []string
	for _, img := range out.ImageDetails {
		tags = append(tags, img.ImageTags...)
	}
	want := []string{"0.4.4", "0.4.3", "0.4.2", "0.4.1", "0.4.0"}
	if strings.Join(tags, ",") != strings.Join(want, ",") {
		t.Fatalf("image tags = %v, want %v", tags, want)
	}

	_, err = repo.DescribeImages(ctx, &ecr.DescribeImagesInput{RepositoryName: aws.String("nosuchrepo")})
	var notFound *ecrtypes.RepositoryNotFoundException
	if !errors.As(err, &notFound) {
		t.Fatalf("err = %v, want RepositoryNotFoundException", err)
	}
}
