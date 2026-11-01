package api

import (
	"reflect"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/store"
)

func TestNarrowVisibility(t *testing.T) {
	cases := []struct {
		name string
		vis  store.Visibility
		tags []authz.TagID
		want store.Visibility
	}{
		{"admin narrows to the tag", store.Visibility{Admin: true, TagIDs: []int64{1, 2, 3}}, []authz.TagID{3}, store.Visibility{Admin: false, TagIDs: []int64{3}}},
		{"user intersects", store.Visibility{TagIDs: []int64{1, 2}}, []authz.TagID{2, 3}, store.Visibility{TagIDs: []int64{2}}},
		{"user never widens", store.Visibility{TagIDs: []int64{1, 2}}, []authz.TagID{3}, store.Visibility{TagIDs: []int64{}}},
		{"no tags leaves it unchanged", store.Visibility{Admin: true, TagIDs: []int64{1}}, nil, store.Visibility{Admin: true, TagIDs: []int64{1}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := narrowVisibility(tc.vis, tc.tags)
			if got.Admin != tc.want.Admin || !reflect.DeepEqual(got.TagIDs, tc.want.TagIDs) {
				t.Errorf("narrowVisibility = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestParseTagFilter(t *testing.T) {
	got, err := parseTagFilter([]string{"2", "3", "2"})
	if err != nil || !reflect.DeepEqual(got, []authz.TagID{2, 3}) {
		t.Errorf("parseTagFilter dedupe = %v, %v; want [2 3]", got, err)
	}
	if _, err := parseTagFilter([]string{"x"}); err == nil || err.Error() != "tag must be a number." {
		t.Errorf("parseTagFilter(x) error = %v, want \"tag must be a number.\"", err)
	}
	if got, err := parseTagFilter(nil); err != nil || got != nil {
		t.Errorf("parseTagFilter(nil) = %v, %v; want nil", got, err)
	}
}
