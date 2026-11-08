package fake

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"

	"github.com/rcss-labs/harbormaster/internal/awsx"
)

func ecrForHomelab(t *testing.T, f *Fake) awsx.ECRAPI {
	t.Helper()
	api, err := f.ECR(context.Background(), awsx.Target{AccountID: homelabID, Region: "us-east-1"})
	if err != nil {
		t.Fatal(err)
	}
	return api
}

func TestFakeDescribeImages_FiltersByTagAndDigest(t *testing.T) {
	f := newFake()
	api := ecrForHomelab(t, f)
	ctx := context.Background()
	wantDigest := f.Estate.ImageDigestFor("image-resizer:0.4.2")

	byTag, err := api.DescribeImages(ctx, &ecr.DescribeImagesInput{
		RepositoryName: aws.String("image-resizer"),
		ImageIds:       []ecrtypes.ImageIdentifier{{ImageTag: aws.String("0.4.2")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(byTag.ImageDetails) != 1 {
		t.Fatalf("lookup by tag returned %d details, want 1", len(byTag.ImageDetails))
	}
	detail := byTag.ImageDetails[0]
	if got := aws.ToString(detail.ImageDigest); got != wantDigest {
		t.Fatalf("digest = %s, want %s", got, wantDigest)
	}
	if aws.ToString(detail.ImageManifestMediaType) == "" || aws.ToInt64(detail.ImageSizeInBytes) <= 0 {
		t.Fatalf("media type %q and size %d must both be filled", aws.ToString(detail.ImageManifestMediaType), aws.ToInt64(detail.ImageSizeInBytes))
	}

	byDigest, err := api.DescribeImages(ctx, &ecr.DescribeImagesInput{
		RepositoryName: aws.String("image-resizer"),
		ImageIds:       []ecrtypes.ImageIdentifier{{ImageDigest: aws.String(wantDigest)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(byDigest.ImageDetails) != 1 || aws.ToString(byDigest.ImageDetails[0].ImageDigest) != wantDigest ||
		len(byDigest.ImageDetails[0].ImageTags) != 1 || byDigest.ImageDetails[0].ImageTags[0] != "0.4.2" {
		t.Fatalf("lookup by digest returned %+v, want the 0.4.2 image", byDigest.ImageDetails)
	}

	_, err = api.DescribeImages(ctx, &ecr.DescribeImagesInput{
		RepositoryName: aws.String("image-resizer"),
		ImageIds:       []ecrtypes.ImageIdentifier{{ImageTag: aws.String("9.9.9")}},
	})
	var notFound *ecrtypes.ImageNotFoundException
	if !errors.As(err, &notFound) {
		t.Fatalf("unknown tag err = %v, want ImageNotFoundException", err)
	}
}

func TestFakeMoveTag_ChangesTheResolvedDigest(t *testing.T) {
	f := newFake()
	api := ecrForHomelab(t, f)
	ctx := context.Background()
	resolve := func(tag string) string {
		t.Helper()
		out, err := api.DescribeImages(ctx, &ecr.DescribeImagesInput{
			RepositoryName: aws.String("image-resizer"),
			ImageIds:       []ecrtypes.ImageIdentifier{{ImageTag: aws.String(tag)}},
		})
		if err != nil || len(out.ImageDetails) != 1 {
			t.Fatalf("resolve %s: %v, %d details", tag, err, len(out.ImageDetails))
		}
		return aws.ToString(out.ImageDetails[0].ImageDigest)
	}
	before, neighbour := resolve("0.4.2"), resolve("0.4.1")

	moved := f.Estate.MoveTag("image-resizer", "0.4.2")

	if moved == "" || moved == before {
		t.Fatalf("MoveTag returned %q, want a digest different from %s", moved, before)
	}
	if got := resolve("0.4.2"); got != moved {
		t.Fatalf("tag resolves to %s after the move, want %s", got, moved)
	}
	if got := f.Estate.ImageDigestFor("image-resizer:0.4.2"); got != moved {
		t.Fatalf("ImageDigestFor = %s after the move, want %s", got, moved)
	}
	if got := resolve("0.4.1"); got != neighbour {
		t.Fatalf("another tag changed digest to %s, want %s", got, neighbour)
	}
}
