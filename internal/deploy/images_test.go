package deploy_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
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
	deployMutated(t, h, key, func(containers []ecstypes.ContainerDefinition) []ecstypes.ContainerDefinition {
		containers[0].Image = aws.String(image)
		return containers
	})
}

// deployMutated is deployImage for any change to the container list of the new revision.
func deployMutated(t *testing.T, h *harness, key model.ServiceKey, mutate func([]ecstypes.ContainerDefinition) []ecstypes.ContainerDefinition) {
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
			copied.Definition.ContainerDefinitions = mutate(copied.Definition.ContainerDefinitions)
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
	got, err := h.svc.ImageTags(t.Context(), imageResizerKey, deploy.ImageTagQuery{})
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
	got, err := h.svc.ImageTags(t.Context(), imageResizerKey, deploy.ImageTagQuery{})
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
	got, err := h.svc.ImageTags(t.Context(), imageResizerKey, deploy.ImageTagQuery{})
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
	got, err := h.svc.ImageTags(t.Context(), imageResizerKey, deploy.ImageTagQuery{})
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
	_, err := h.svc.ImageTags(t.Context(), imageResizerKey, deploy.ImageTagQuery{})
	if !awsx.IsThrottle(err) {
		t.Fatalf("err = %v, want a throttle error", err)
	}
}

func TestImageTags_CachedFor60s(t *testing.T) {
	h := newHarness(t)
	now := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
	h.svc.Now = func() time.Time { return now }
	for i := 0; i < 2; i++ {
		if _, err := h.svc.ImageTags(t.Context(), imageResizerKey, deploy.ImageTagQuery{}); err != nil {
			t.Fatal(err)
		}
	}
	if n := h.fake.Calls()["DescribeImages"]; n != 1 {
		t.Fatalf("DescribeImages calls = %d within 60 s, want 1", n)
	}
	now = now.Add(61 * time.Second)
	if _, err := h.svc.ImageTags(t.Context(), imageResizerKey, deploy.ImageTagQuery{}); err != nil {
		t.Fatal(err)
	}
	if n := h.fake.Calls()["DescribeImages"]; n != 2 {
		t.Fatalf("DescribeImages calls = %d after 61 s, want 2", n)
	}
}

// oldestFirstECR serves 60 tagged images over pages of 25, oldest push first,
// the opposite of the order the picker needs.
type oldestFirstECR struct{ awsx.Clients }

func (o oldestFirstECR) ECR(context.Context, awsx.Target) (awsx.ECRAPI, error) {
	return pagedImages{}, nil
}

type pagedImages struct{}

func (pagedImages) DescribeImages(_ context.Context, in *ecr.DescribeImagesInput, _ ...func(*ecr.Options)) (*ecr.DescribeImagesOutput, error) {
	start := 0
	if in.NextToken != nil {
		start, _ = strconv.Atoi(*in.NextToken)
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	out := &ecr.DescribeImagesOutput{}
	end := start + 25
	if end < 60 {
		out.NextToken = aws.String(strconv.Itoa(end))
	} else {
		end = 60
	}
	for i := start; i < end; i++ {
		out.ImageDetails = append(out.ImageDetails, ecrtypes.ImageDetail{
			ImageTags: []string{"t" + strconv.Itoa(i)}, ImagePushedAt: aws.Time(base.Add(time.Duration(i) * time.Hour)),
		})
	}
	return out, nil
}

func TestImageTags_CallerCannotCorruptCache(t *testing.T) {
	h := newHarness(t)
	first, err := h.svc.ImageTags(t.Context(), imageResizerKey, deploy.ImageTagQuery{})
	if err != nil {
		t.Fatal(err)
	}
	first.Tags[0].Tag = "mutated"
	second, err := h.svc.ImageTags(t.Context(), imageResizerKey, deploy.ImageTagQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if second.Tags[0].Tag == "mutated" {
		t.Fatal("cached tags share memory with the returned slice")
	}
}

func TestImageTags_AllPagesKeptNewestFirst(t *testing.T) {
	h := newHarness(t)
	h.svc.Clients = oldestFirstECR{Clients: h.fake}
	got, err := h.svc.ImageTags(t.Context(), imageResizerKey, deploy.ImageTagQuery{Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Tags) != 60 || got.Total != 60 || got.Tags[0].Tag != "t59" || got.Tags[59].Tag != "t0" {
		t.Fatalf("got %d tags (total %d), first %q; want all 60 newest first", len(got.Tags), got.Total, got.Tags[0].Tag)
	}
}

// repoImages is an ECR fake serving tags per repository and counting DescribeImages calls.
type repoImages struct {
	tags  map[string][]string
	calls int
}

type repoECR struct {
	awsx.Clients
	api *repoImages
}

func (r repoECR) ECR(context.Context, awsx.Target) (awsx.ECRAPI, error) { return r.api, nil }

func (r *repoImages) DescribeImages(_ context.Context, in *ecr.DescribeImagesInput, _ ...func(*ecr.Options)) (*ecr.DescribeImagesOutput, error) {
	r.calls++
	tags, ok := r.tags[aws.ToString(in.RepositoryName)]
	if !ok {
		return nil, &ecrtypes.RepositoryNotFoundException{Message: aws.String("no such repository")}
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	out := &ecr.DescribeImagesOutput{}
	for i, tag := range tags { // tags are listed oldest first
		out.ImageDetails = append(out.ImageDetails, ecrtypes.ImageDetail{ImageTags: []string{tag}, ImagePushedAt: aws.Time(base.Add(time.Duration(i) * time.Hour))})
	}
	return out, nil
}

// addSidecar deploys a new revision with a second container.
func addSidecar(t *testing.T, h *harness, key model.ServiceKey, name, image string) {
	t.Helper()
	deployMutated(t, h, key, func(containers []ecstypes.ContainerDefinition) []ecstypes.ContainerDefinition {
		return append(containers, ecstypes.ContainerDefinition{Name: aws.String(name), Image: aws.String(image), Essential: aws.Bool(false)})
	})
}

func tagNames(infos []deploy.ImageTagInfo) string {
	var names []string
	for _, info := range infos {
		names = append(names, info.Tag)
	}
	return strings.Join(names, ",")
}

func registryHost() string {
	return homelabAccountID + ".dkr.ecr." + homelabRegion + ".amazonaws.com"
}

func TestImageTags_DefaultsToMainContainer(t *testing.T) {
	h := newHarness(t)
	addSidecar(t, h, imageResizerKey, "migrator", registryHost()+"/migrator:2.0.0")
	got, err := h.svc.ImageTags(t.Context(), imageResizerKey, deploy.ImageTagQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Container != "image-resizer" || got.Repository != "image-resizer" || got.Image != registryHost()+"/image-resizer" || got.Current != "0.4.2" {
		t.Fatalf("ImageTags = %+v", got)
	}
}

func TestImageTags_SidecarFromECR(t *testing.T) {
	h := newHarness(t)
	addSidecar(t, h, imageResizerKey, "migrator", registryHost()+"/migrator:2.0.0")
	h.svc.Clients = repoECR{Clients: h.fake, api: &repoImages{tags: map[string][]string{"migrator": {"1.0.0", "2.0.0", "2.1.0"}}}}
	got, err := h.svc.ImageTags(t.Context(), imageResizerKey, deploy.ImageTagQuery{Container: "migrator"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != "ecr" || got.Container != "migrator" || got.Repository != "migrator" || got.Current != "2.0.0" || got.Image != registryHost()+"/migrator" {
		t.Fatalf("ImageTags = %+v", got)
	}
	if want := "2.1.0,2.0.0,1.0.0"; tagNames(got.Tags) != want {
		t.Fatalf("tags = %s, want %s", tagNames(got.Tags), want)
	}
}

func TestImageTags_PublicImageIsSourceNone(t *testing.T) {
	h := newHarness(t)
	addSidecar(t, h, imageResizerKey, "otel", "public.ecr.aws/aws-observability/aws-otel-collector:v0.40.0")
	got, err := h.svc.ImageTags(t.Context(), imageResizerKey, deploy.ImageTagQuery{Container: "otel"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != "none" || got.Container != "otel" || got.Current != "v0.40.0" || got.Image != "public.ecr.aws/aws-observability/aws-otel-collector" || len(got.Tags) != 0 {
		t.Fatalf("ImageTags = %+v", got)
	}
}

func TestImageTags_SearchIsCaseInsensitiveSubstringNewestFirst(t *testing.T) {
	h := newHarness(t)
	api := &repoImages{tags: map[string][]string{"image-resizer": {"v1.0.0", "v1.1.0-RC1", "v2.0.0", "v2.1.0-rc2", "latest"}}}
	h.svc.Clients = repoECR{Clients: h.fake, api: api}
	got, err := h.svc.ImageTags(t.Context(), imageResizerKey, deploy.ImageTagQuery{Search: "Rc"})
	if err != nil {
		t.Fatal(err)
	}
	if want := "v2.1.0-rc2,v1.1.0-RC1"; tagNames(got.Tags) != want || got.Matched != 2 || got.Total != 5 || got.Truncated {
		t.Fatalf("tags = %s matched %d total %d truncated %v", tagNames(got.Tags), got.Matched, got.Total, got.Truncated)
	}
}

func TestImageTags_LimitAndTotal(t *testing.T) {
	h := newHarness(t)
	h.svc.Clients = oldestFirstECR{Clients: h.fake} // 60 tags
	cases := []struct {
		name           string
		query          deploy.ImageTagQuery
		returned       int
		matched, total int
		truncated      bool
	}{
		{"default limit is 10", deploy.ImageTagQuery{}, 10, 60, 60, true},
		{"explicit limit", deploy.ImageTagQuery{Limit: 25}, 25, 60, 60, true},
		{"limit above matches", deploy.ImageTagQuery{Limit: 200}, 60, 60, 60, false},
		{"search narrows matched not total", deploy.ImageTagQuery{Search: "t5", Limit: 3}, 3, 11, 60, true},
	}
	for _, tc := range cases {
		got, err := h.svc.ImageTags(t.Context(), imageResizerKey, tc.query)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if len(got.Tags) != tc.returned || got.Matched != tc.matched || got.Total != tc.total || got.Truncated != tc.truncated {
			t.Errorf("%s: returned %d matched %d total %d truncated %v", tc.name, len(got.Tags), got.Matched, got.Total, got.Truncated)
		}
	}
	for _, limit := range []int{-1, 201} {
		_, err := h.svc.ImageTags(t.Context(), imageResizerKey, deploy.ImageTagQuery{Limit: limit})
		var validation *deploy.ValidationError
		if !errors.As(err, &validation) {
			t.Errorf("limit %d: err = %v, want ValidationError", limit, err)
		}
	}
}

func TestImageTags_SearchServedFromCacheNoSecondDescribe(t *testing.T) {
	h := newHarness(t)
	api := &repoImages{tags: map[string][]string{"image-resizer": {"a1", "b2", "a3"}}}
	h.svc.Clients = repoECR{Clients: h.fake, api: api}
	for _, search := range []string{"", "a", "b", "zzz"} {
		if _, err := h.svc.ImageTags(t.Context(), imageResizerKey, deploy.ImageTagQuery{Search: search}); err != nil {
			t.Fatal(err)
		}
	}
	if api.calls != 1 {
		t.Fatalf("DescribeImages calls = %d, want 1", api.calls)
	}
}

func TestImageTags_UnknownContainer422(t *testing.T) {
	h := newHarness(t)
	_, err := h.svc.ImageTags(t.Context(), imageResizerKey, deploy.ImageTagQuery{Container: "nope"})
	var validation *deploy.ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("err = %v, want ValidationError", err)
	}
}
