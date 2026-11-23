package fake

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"

	"github.com/rcss-labs/harbormaster/internal/registry"
)

const ociIndexType = "application/vnd.oci.image.index.v1+json"

func batchGet(t *testing.T, f *Fake, repository, tag string) (*ecr.BatchGetImageOutput, error) {
	t.Helper()
	return ecrForSandbox(t, f).BatchGetImage(context.Background(), &ecr.BatchGetImageInput{
		RepositoryName: aws.String(repository),
		ImageIds:       []ecrtypes.ImageIdentifier{{ImageTag: aws.String(tag)}},
	})
}

func describeOne(t *testing.T, f *Fake, repository, tag string) ecrtypes.ImageDetail {
	t.Helper()
	out, err := ecrForSandbox(t, f).DescribeImages(context.Background(), &ecr.DescribeImagesInput{
		RepositoryName: aws.String(repository), ImageIds: []ecrtypes.ImageIdentifier{{ImageTag: aws.String(tag)}}})
	if err != nil || len(out.ImageDetails) != 1 {
		t.Fatalf("DescribeImages %s:%s = %+v, %v", repository, tag, out, err)
	}
	return out.ImageDetails[0]
}

func TestBatchGetImage_IndexListsSeededPlatforms(t *testing.T) {
	f := newFake()
	f.Estate.SetImageIndex("image-resizer", true)
	out, err := batchGet(t, f, "image-resizer", "0.4.2")
	if err != nil || len(out.Images) != 1 || len(out.Failures) != 0 {
		t.Fatalf("BatchGetImage = %+v, %v", out, err)
	}
	image := out.Images[0]
	if aws.ToString(image.ImageManifestMediaType) != ociIndexType {
		t.Fatalf("media type = %q, want the OCI index type", aws.ToString(image.ImageManifestMediaType))
	}
	platforms, isIndex, err := registry.Platforms([]byte(aws.ToString(image.ImageManifest)), ociIndexType)
	if err != nil || !isIndex || strings.Join(platforms, ",") != "linux/amd64,linux/arm64" {
		t.Fatalf("platforms = %v (index %v, %v), want amd64 and arm64 with the attestation skipped", platforms, isIndex, err)
	}
	if !strings.Contains(aws.ToString(image.ImageManifest), `"unknown"`) {
		t.Error("the index has no unknown/unknown attestation entry to skip")
	}
	// The digest ECR reports is the digest of the manifest body, as it is for a real index.
	want := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(aws.ToString(image.ImageManifest))))
	if got := aws.ToString(describeOne(t, f, "image-resizer", "0.4.2").ImageDigest); got != want {
		t.Fatalf("DescribeImages digest = %s, want sha256 of the manifest %s", got, want)
	}
	if got := aws.ToString(image.ImageId.ImageDigest); got != want {
		t.Fatalf("BatchGetImage digest = %s, want %s", got, want)
	}
}

func TestBatchGetImage_IndexWithoutArm64(t *testing.T) {
	f := newFake()
	f.Estate.SetImageIndex("image-resizer", false)
	out, err := batchGet(t, f, "image-resizer", "0.4.2")
	if err != nil {
		t.Fatal(err)
	}
	platforms, _, _ := registry.Platforms([]byte(aws.ToString(out.Images[0].ImageManifest)), ociIndexType)
	if strings.Join(platforms, ",") != "linux/amd64" {
		t.Fatalf("platforms = %v, want only amd64", platforms)
	}
}

func TestBatchGetImage_SingleManifestForDefaultRepositories(t *testing.T) {
	f := newFake()
	out, err := batchGet(t, f, "image-resizer", "0.4.2")
	if err != nil || len(out.Images) != 1 {
		t.Fatalf("BatchGetImage = %+v, %v", out, err)
	}
	mediaType := aws.ToString(out.Images[0].ImageManifestMediaType)
	if mediaType != fakeImageMediaType {
		t.Fatalf("media type = %q, want the single v2 manifest type", mediaType)
	}
	if _, isIndex, _ := registry.Platforms([]byte(aws.ToString(out.Images[0].ImageManifest)), mediaType); isIndex {
		t.Fatal("a default repository answered an index")
	}
}

func TestDescribeImages_ReportsIndexMediaType(t *testing.T) {
	f := newFake()
	if got := aws.ToString(describeOne(t, f, "image-resizer", "0.4.2").ImageManifestMediaType); got != fakeImageMediaType {
		t.Fatalf("default media type = %q", got)
	}
	f.Estate.SetImageIndex("image-resizer", true)
	if got := aws.ToString(describeOne(t, f, "image-resizer", "0.4.2").ImageManifestMediaType); got != ociIndexType {
		t.Fatalf("index media type = %q, want the OCI index type", got)
	}
}

func TestBatchGetImage_MovedTagGivesANewDigest(t *testing.T) {
	f := newFake()
	f.Estate.SetImageIndex("image-resizer", true)
	before := aws.ToString(describeOne(t, f, "image-resizer", "0.4.2").ImageDigest)
	f.Estate.MoveTag("image-resizer", "0.4.2")
	after := aws.ToString(describeOne(t, f, "image-resizer", "0.4.2").ImageDigest)
	out, _ := batchGet(t, f, "image-resizer", "0.4.2")
	body := aws.ToString(out.Images[0].ImageManifest)
	if before == after || after != fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(body))) {
		t.Fatalf("digest %s -> %s, want a new one that still hashes the manifest", before, after)
	}
}

func TestBatchGetImage_UnknownRepositoryAndTag(t *testing.T) {
	f := newFake()
	if _, err := batchGet(t, f, "no-such-repo", "1"); err == nil {
		t.Error("an unknown repository was served")
	}
	out, err := batchGet(t, f, "image-resizer", "9.9.9")
	if err != nil || len(out.Images) != 0 || len(out.Failures) != 1 {
		t.Fatalf("unknown tag = %+v, %v; want one failure, as ECR reports it", out, err)
	}
}

func TestBatchGetImage_FailInjectionAndCalls(t *testing.T) {
	f := newFake()
	boom := errors.New("boom")
	f.FailNext("BatchGetImage", boom)
	if _, err := batchGet(t, f, "image-resizer", "0.4.2"); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the injected error", err)
	}
	if _, err := batchGet(t, f, "image-resizer", "0.4.2"); err != nil {
		t.Fatal(err)
	}
	if n := f.Calls()["BatchGetImage"]; n != 2 {
		t.Fatalf("BatchGetImage calls = %d, want 2", n)
	}
}

func TestRegistry_AnswersSeededPublicImages(t *testing.T) {
	r := NewRegistry()
	ref, err := registry.ParseRef("public.ecr.aws/aws-observability/aws-otel-collector:v0.40.0")
	if err != nil {
		t.Fatal(err)
	}
	digest, err := r.Digest(context.Background(), ref)
	if err != nil || digest == "" {
		t.Fatalf("Digest = %q, %v", digest, err)
	}
	body, mediaType, err := r.Manifest(context.Background(), ref)
	if err != nil || fmt.Sprintf("sha256:%x", sha256.Sum256(body)) != digest {
		t.Fatalf("Manifest = %d bytes, %q, %v; want a body that hashes to %s", len(body), mediaType, err, digest)
	}
	platforms, isIndex, _ := registry.Platforms(body, mediaType)
	if !isIndex || strings.Join(platforms, ",") != "linux/amd64,linux/arm64" {
		t.Fatalf("platforms = %v (index %v)", platforms, isIndex)
	}
	other, _ := registry.ParseRef("quay.io/x/y:1")
	if _, err := r.Digest(context.Background(), other); !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("an unseeded image: err = %v, want ErrNotFound", err)
	}
	if got := strings.Join(r.Hosts(), ","); got != "public.ecr.aws,public.ecr.aws,quay.io" {
		t.Fatalf("hosts asked = %q, want every request recorded", got)
	}
}
