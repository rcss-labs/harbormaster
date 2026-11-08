package scanner

import (
	"strings"
	"testing"
)

func TestResolvePlatformDigest_IndexPicksArchitecture(t *testing.T) {
	index := testdata(t, "manifest_index.json")
	for platform, want := range map[string]string{
		"linux/amd64": digestAMD64,
		"linux/arm64": digestARM64,
	} {
		digest, isIndex, err := ResolvePlatformDigest(index, mediaIndex, platform)
		if err != nil || !isIndex || digest != want {
			t.Errorf("%s: digest=%s isIndex=%v err=%v, want %s from an index", platform, digest, isIndex, err, want)
		}
	}
	if _, _, err := ResolvePlatformDigest(index, mediaIndex, "linux/s390x"); err == nil {
		t.Error("a platform missing from the index must be an error")
	}
	// Docker manifest lists use the same layout under another media type.
	list := strings.Replace(string(index), mediaIndex, "application/vnd.docker.distribution.manifest.list.v2+json", 1)
	digest, isIndex, err := ResolvePlatformDigest([]byte(list), "application/vnd.docker.distribution.manifest.list.v2+json", "linux/arm64")
	if err != nil || !isIndex || digest != digestARM64 {
		t.Errorf("manifest list: digest=%s isIndex=%v err=%v", digest, isIndex, err)
	}
}

func TestResolvePlatformDigest_SingleManifestIsItself(t *testing.T) {
	digest, isIndex, err := ResolvePlatformDigest(testdata(t, "manifest_image.json"), mediaDocker, "linux/amd64")
	if err != nil || isIndex || digest != "" {
		t.Errorf("digest=%q isIndex=%v err=%v, want no index and an empty digest (the caller keeps its own)", digest, isIndex, err)
	}
}
