package scan

import (
	"sort"
	"time"
)

// TagInfo is one tag of an ECR repository.
type TagInfo struct {
	Tag, Digest, ManifestMediaType string
	PushedAt                       time.Time
}

// SelectCandidates picks up to n tags pushed after the running image, newest
// first, one per digest, skipping the running image's index and platform digests.
func SelectCandidates(tags []TagInfo, runningDigest, runningPlatformDigest string, runningPushedAt time.Time, n int) []TagInfo {
	newer := make([]TagInfo, 0, len(tags))
	for _, tag := range tags {
		if tag.Digest == "" || !tag.PushedAt.After(runningPushedAt) ||
			tag.Digest == runningDigest || tag.Digest == runningPlatformDigest {
			continue
		}
		newer = append(newer, tag)
	}
	sort.SliceStable(newer, func(i, j int) bool { return newer[i].PushedAt.After(newer[j].PushedAt) })

	seen := make(map[string]struct{}, len(newer))
	picked := make([]TagInfo, 0, n)
	for _, tag := range newer {
		if len(picked) >= n {
			break
		}
		if _, dup := seen[tag.Digest]; dup {
			continue
		}
		seen[tag.Digest] = struct{}{}
		picked = append(picked, tag)
	}
	return picked
}
