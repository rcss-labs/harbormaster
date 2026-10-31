package deploy

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"

	"github.com/rcss-labs/harbormaster/internal/model"
)

const (
	imageTagsTTL = 60 * time.Second
	maxImageTags = 50
)

// ImageTags are the ECR tags a service's main container image can be switched to.
// Source is "ecr" when the tags come from the service's own account, otherwise
// "none" and the UI shows a free-text tag field.
type ImageTags struct {
	Source            string
	Repository        string
	Current, Deployed string
	Tags              []ImageTagInfo
}

// ImageTagInfo is one tag of an ECR image and when its image was pushed.
type ImageTagInfo struct {
	Tag      string
	PushedAt time.Time
}

var ecrImagePattern = regexp.MustCompile(`^(\d{12})\.dkr\.ecr\.([a-z0-9-]+)\.amazonaws\.com/([^:@]+)`)

// ImageTags lists the ECR tags of the repository behind the service's main container,
// newest first, cached for 60 s per account, region and repository. Images that are not
// in the service's own account, not in ECR, or whose repository does not exist
// give Source "none".
func (s *Service) ImageTags(ctx context.Context, key model.ServiceKey) (ImageTags, error) {
	_, ref, err := s.primaryDefinition(ctx, key)
	if err != nil {
		return ImageTags{}, err
	}
	current := ref.ImageTag(key.Service)
	result := ImageTags{Source: "none", Current: current, Deployed: current}

	match := ecrImagePattern.FindStringSubmatch(ref.MainContainer(key.Service).Image)
	if match == nil || match[1] != key.AccountID {
		return result, nil
	}
	repoRegion, repository := match[2], match[3]
	cacheKey := fmt.Sprintf("%s/%s/%s", match[1], repoRegion, repository)

	tags, cached := s.imageTagCache().get(cacheKey, s.now())
	if !cached {
		tags, err = s.describeImageTags(ctx, key, repoRegion, repository)
		if err != nil {
			var missing *ecrtypes.RepositoryNotFoundException
			if errors.As(err, &missing) {
				return result, nil
			}
			return ImageTags{}, err
		}
		s.imageTagCache().put(cacheKey, tags, s.now())
	}
	result.Source = "ecr"
	result.Repository = repository
	result.Tags = tags
	return result, nil
}

// describeImageTags pages through DescribeImages and returns up to maxImageTags tags, newest first.
func (s *Service) describeImageTags(ctx context.Context, key model.ServiceKey, region, repository string) ([]ImageTagInfo, error) {
	ecrAPI, err := s.Clients.ECR(ctx, targetOf(model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: key.AccountID, Region: region}}))
	if err != nil {
		return nil, err
	}
	var tags []ImageTagInfo
	var next *string
	for {
		out, err := ecrAPI.DescribeImages(ctx, &ecr.DescribeImagesInput{
			RepositoryName: aws.String(repository),
			Filter:         &ecrtypes.DescribeImagesFilter{TagStatus: ecrtypes.TagStatusTagged},
			NextToken:      next,
		})
		if err != nil {
			return nil, err
		}
		for _, image := range out.ImageDetails {
			for _, tag := range image.ImageTags {
				tags = append(tags, ImageTagInfo{Tag: tag, PushedAt: aws.ToTime(image.ImagePushedAt).UTC()})
			}
		}
		next = out.NextToken
		if next == nil || len(tags) >= maxImageTags {
			break
		}
	}
	sort.SliceStable(tags, func(i, j int) bool { return tags[i].PushedAt.After(tags[j].PushedAt) })
	if len(tags) > maxImageTags {
		tags = tags[:maxImageTags]
	}
	return tags, nil
}
