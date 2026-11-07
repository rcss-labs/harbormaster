package scan

import (
	"reflect"
	"testing"
	"time"
)

func TestSelectCandidates_NewestThreeDistinctExcludingRunning(t *testing.T) {
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	at := func(days int) time.Time { return base.Add(time.Duration(days) * 24 * time.Hour) }
	tags := []TagInfo{
		{Tag: "old", Digest: "sha256:d0", PushedAt: at(-5)},
		{Tag: "v2", Digest: "sha256:d2", PushedAt: at(2)},
		{Tag: "v2-alias", Digest: "sha256:d2", PushedAt: at(1)},
		{Tag: "v3", Digest: "sha256:d3", PushedAt: at(3)},
		{Tag: "plat", Digest: "sha256:plat", PushedAt: at(4)},
		{Tag: "v4", Digest: "sha256:d4", PushedAt: at(5)},
		{Tag: "v5", Digest: "sha256:d5", PushedAt: at(6)},
	}
	got := SelectCandidates(tags, "sha256:running", "sha256:plat", base, 3)
	want := []TagInfo{tags[6], tags[5], tags[3]}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	got = SelectCandidates(tags, "sha256:d5", "sha256:plat", base, 10)
	if len(got) != 3 || got[0].Tag != "v4" || got[2].Tag != "v2" {
		t.Errorf("running digest excluded and distinct expected, got %+v", got)
	}
}
