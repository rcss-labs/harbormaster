package deploy_test

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"
	"github.com/aws/smithy-go"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/deploy"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/poller"
)

var imageResizerKey = model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: homelabAccountID, Region: homelabRegion, Cluster: "background-jobs"}, Service: "image-resizer"}

// deployImage registers a new revision of the service's family whose main container uses
// image, makes it the PRIMARY deployment in the fake and polls so the snapshot follows.
func deployImage(t *testing.T, h *harness, key model.ServiceKey, image string) {
	t.Helper()
	h.fake.Estate.Mu.Lock()
	var newARN string
	for i := range h.fake.Estate.Services {
		svc := &h.fake.Estate.Services[i]
		if svc.ClusterName != key.Cluster || aws.ToString(svc.Service.ServiceName) != key.Service {
			continue
		}
		oldARN := aws.ToString(svc.Service.TaskDefinition)
		_, revision, err := deploy.ParseRevisionARN(oldARN)
		if err != nil {
			h.fake.Estate.Mu.Unlock()
			t.Fatal(err)
		}
		for _, td := range h.fake.Estate.TaskDefinitions {
			if aws.ToString(td.Definition.TaskDefinitionArn) != oldARN {
				continue
			}
			copied := td
			copied.Definition.Revision = int32(revision + 100)
			newARN = strings.TrimSuffix(oldARN, ":"+strconv.Itoa(revision)) + ":" + strconv.Itoa(revision+100)
			copied.Definition.TaskDefinitionArn = aws.String(newARN)
			copied.Definition.ContainerDefinitions = append(copied.Definition.ContainerDefinitions[:0:0], td.Definition.ContainerDefinitions...)
			copied.Definition.ContainerDefinitions[0].Image = aws.String(image)
			h.fake.Estate.TaskDefinitions = append(h.fake.Estate.TaskDefinitions, copied)
			break
		}
		svc.Service.TaskDefinition = aws.String(newARN)
		for j := range svc.Service.Deployments {
			if aws.ToString(svc.Service.Deployments[j].Status) == "PRIMARY" {
				svc.Service.Deployments[j].TaskDefinition = aws.String(newARN)
			}
		}
	}
	h.fake.Estate.Mu.Unlock()
	target := awsx.Target{AccountID: key.AccountID, Region: key.Region}
	if err := poller.PollOnce(t.Context(), h.store, h.fake, poller.Config{MaxInFlight: 4}, target); err != nil {
		t.Fatal(err)
	}
}

func TestImageTags_SameAccountECR(t *testing.T) {
	h := newHarness(t)
	got, err := h.svc.ImageTags(t.Context(), imageResizerKey)
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != "ecr" || got.Repository != "image-resizer" || got.Current != "0.4.2" {
		t.Fatalf("ImageTags = %+v", got)
	}
	var tags []string
	for _, info := range got.Tags {
		tags = append(tags, info.Tag)
	}
	if want := "0.4.4,0.4.3,0.4.2,0.4.1,0.4.0"; strings.Join(tags, ",") != want {
		t.Fatalf("tags = %v, want %s (newest first)", tags, want)
	}
	for i := 1; i < len(got.Tags); i++ {
		if !got.Tags[i-1].PushedAt.After(got.Tags[i].PushedAt) {
			t.Fatalf("tags not newest first: %+v", got.Tags)
		}
	}
}

func TestImageTags_OtherAccountIsFreeText(t *testing.T) {
	h := newHarness(t)
	deployImage(t, h, imageResizerKey, "111111111111.dkr.ecr.us-east-1.amazonaws.com/image-resizer:0.4.2")
	got, err := h.svc.ImageTags(t.Context(), imageResizerKey)
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != "none" || len(got.Tags) != 0 {
		t.Fatalf("ImageTags = %+v, want Source none", got)
	}
	if n := h.fake.Calls()["DescribeImages"]; n != 0 {
		t.Fatalf("DescribeImages called %d times for another account's image", n)
	}
}

func TestImageTags_PublicImageIsFreeText(t *testing.T) {
	h := newHarness(t)
	deployImage(t, h, imageResizerKey, "public.ecr.aws/nginx/nginx:1.27")
	got, err := h.svc.ImageTags(t.Context(), imageResizerKey)
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != "none" || got.Current != "1.27" {
		t.Fatalf("ImageTags = %+v, want Source none, Current 1.27", got)
	}
}

func TestImageTags_UnknownRepositoryIsFreeText(t *testing.T) {
	h := newHarness(t)
	h.fake.FailNext("DescribeImages", &ecrtypes.RepositoryNotFoundException{Message: aws.String("no such repository")})
	got, err := h.svc.ImageTags(t.Context(), imageResizerKey)
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != "none" {
		t.Fatalf("ImageTags = %+v, want Source none", got)
	}
}

func TestImageTags_ThrottleSurfaces(t *testing.T) {
	h := newHarness(t)
	h.fake.FailNext("DescribeImages", &smithy.GenericAPIError{Code: "ThrottlingException", Message: "Rate exceeded"})
	_, err := h.svc.ImageTags(t.Context(), imageResizerKey)
	if !awsx.IsThrottle(err) {
		t.Fatalf("err = %v, want a throttle error", err)
	}
}

func TestImageTags_CachedFor60s(t *testing.T) {
	h := newHarness(t)
	now := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
	h.svc.Now = func() time.Time { return now }
	for i := 0; i < 2; i++ {
		if _, err := h.svc.ImageTags(t.Context(), imageResizerKey); err != nil {
			t.Fatal(err)
		}
	}
	if n := h.fake.Calls()["DescribeImages"]; n != 1 {
		t.Fatalf("DescribeImages calls = %d within 60 s, want 1", n)
	}
	now = now.Add(61 * time.Second)
	if _, err := h.svc.ImageTags(t.Context(), imageResizerKey); err != nil {
		t.Fatal(err)
	}
	if n := h.fake.Calls()["DescribeImages"]; n != 2 {
		t.Fatalf("DescribeImages calls = %d after 61 s, want 2", n)
	}
}
