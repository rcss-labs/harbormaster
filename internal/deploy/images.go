package deploy

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"

	"github.com/rcss-labs/harbormaster/internal/model"
)

const (
	imageTagsTTL         = 60 * time.Second
	defaultImageTagLimit = 10
	maxImageTagLimit     = 200
	// DescribeImages is not ordered by push time, so every page is needed to find the newest tags.
	imagePageSize = 1000
	maxImagePages = 10
)

// ImageTags are the ECR tags a service container's image can be switched to.
// Source is "ecr" when the tags come from the service's own account, otherwise
// "none" and the UI shows a free-text tag field. Image is the image reference without
// its tag. Total counts every tag of the repository, Matched those that fit the search,
// and Tags holds at most the requested limit of them, newest first; Truncated is true
// when Matched exceeds what Tags holds.
type ImageTags struct {
	Source            string
	Container         string
	Image             string
	Repository        string
	Current, Deployed string
	Total, Matched    int
	Truncated         bool
	Tags              []ImageTagInfo
}

// ImageTagQuery selects the container, narrows the tags by a case-insensitive substring
// and caps how many are returned. The zero value means the main container and the
// default limit.
type ImageTagQuery struct {
	Container string
	Search    string
	Limit     int
}

// ImageTagInfo is one tag of an ECR image and when its image was pushed.
type ImageTagInfo struct {
	Tag      string
	PushedAt time.Time
}

var ecrImagePattern = regexp.MustCompile(`^(\d{12})\.dkr\.ecr\.([a-z0-9-]+)\.amazonaws\.com/([^:@]+)`)

// ImageTags lists the ECR tags of the repository behind one container of the service
// (the main container by default), newest first. The full tag list is cached for 60 s
// per account, region and repository; search and limit apply after the cache. Images that
// are not in the service's own account, not in ECR, or whose repository does not exist
// give Source "none". An unknown container or an out-of-range limit is a *ValidationError.
func (s *Service) ImageTags(ctx context.Context, key model.ServiceKey, query ImageTagQuery) (ImageTags, error) {
	limit := query.Limit
	if limit == 0 {
		limit = defaultImageTagLimit
	}
	if limit < 0 || limit > maxImageTagLimit {
		return ImageTags{}, &ValidationError{Message: fmt.Sprintf("Limit must be between 1 and %d", maxImageTagLimit)}
	}
	_, ref, err := s.primaryDefinition(ctx, key)
	if err != nil {
		return ImageTags{}, err
	}
	container := ref.MainContainer(key.Service)
	if query.Container != "" {
		container = model.ContainerDef{}
		for _, candidate := range ref.Containers {
			if candidate.Name == query.Container {
				container = candidate
			}
		}
		if container.Name == "" {
			return ImageTags{}, &ValidationError{Message: fmt.Sprintf("Container %q is not in the task definition", query.Container)}
		}
	}
	current := imageTagOf(container.Image)
	result := ImageTags{Source: "none", Container: container.Name, Image: imageWithoutTag(container.Image), Current: current, Deployed: current, Tags: []ImageTagInfo{}}

	match := ecrImagePattern.FindStringSubmatch(container.Image)
	if match == nil || match[1] != key.AccountID {
		return result, nil
	}
	repoRegion, repository := match[2], match[3]
	cacheKey := fmt.Sprintf("%s/%s/%s", match[1], repoRegion, repository)

	all, cached := s.imageTagCache().get(cacheKey, s.now())
	if !cached {
		all, err = s.describeImageTags(ctx, key, repoRegion, repository)
		if err != nil {
			var missing *ecrtypes.RepositoryNotFoundException
			if errors.As(err, &missing) {
				return result, nil
			}
			return ImageTags{}, err
		}
		s.imageTagCache().put(cacheKey, append([]ImageTagInfo(nil), all...), s.now())
	}
	needle := strings.ToLower(query.Search)
	matched := make([]ImageTagInfo, 0, min(len(all), limit))
	matchedCount := 0
	for _, info := range all {
		if needle != "" && !strings.Contains(strings.ToLower(info.Tag), needle) {
			continue
		}
		matchedCount++
		if len(matched) < limit {
			matched = append(matched, info)
		}
	}
	result.Source = "ecr"
	result.Repository = repository
	result.Total = len(all)
	result.Matched = matchedCount
	result.Truncated = matchedCount > len(matched)
	result.Tags = matched
	return result, nil
}

// imageTagOf returns the tag of an image reference, "" when there is none. A ':' in the
// registry host port (before the last '/') does not start a tag.
func imageTagOf(image string) string {
	ref := image
	if at := strings.Index(ref, "@"); at >= 0 {
		ref = ref[:at]
	}
	colon := strings.LastIndex(ref, ":")
	if colon < 0 || colon < strings.LastIndex(ref, "/") {
		return ""
	}
	return ref[colon+1:]
}

// imageWithoutTag drops the tag and digest from an image reference.
func imageWithoutTag(image string) string {
	if at := strings.Index(image, "@"); at >= 0 {
		image = image[:at]
	}
	if colon := strings.LastIndex(image, ":"); colon >= 0 && colon > strings.LastIndex(image, "/") {
		image = image[:colon]
	}
	return image
}

// describeImageTags pages through DescribeImages and returns every tag it finds (up to maxImagePages pages), newest first.
func (s *Service) describeImageTags(ctx context.Context, key model.ServiceKey, region, repository string) ([]ImageTagInfo, error) {
	ecrAPI, err := s.Clients.ECR(ctx, targetOf(model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: key.AccountID, Region: region}}))
	if err != nil {
		return nil, err
	}
	var tags []ImageTagInfo
	var next *string
	for page := 0; page < maxImagePages; page++ {
		out, err := ecrAPI.DescribeImages(ctx, &ecr.DescribeImagesInput{
			RepositoryName: aws.String(repository),
			Filter:         &ecrtypes.DescribeImagesFilter{TagStatus: ecrtypes.TagStatusTagged},
			MaxResults:     aws.Int32(imagePageSize),
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
		if next == nil {
			break
		}
	}
	sort.SliceStable(tags, func(i, j int) bool { return tags[i].PushedAt.After(tags[j].PushedAt) })
	return tags, nil
}
