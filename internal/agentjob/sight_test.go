package agentjob

import (
	"context"
	"slices"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/authz"
)

// The tags to View are those of the identity's permissions, each once and in order; a tag without View is not one.
func TestSight_ViewTags(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	admin, err := f.st.CreateUser(ctx, "dana", "h", true, false)
	if err != nil {
		t.Fatal(err)
	}
	perms := map[authz.TagID]authz.ActionSet{}
	var want []int64
	for _, name := range []string{"finance", "payments", "platform", "other"} {
		tag, err := f.st.CreateTag(ctx, name, "red", false)
		if err != nil {
			t.Fatal(err)
		}
		switch name {
		case "other":
		case "platform":
			perms[tag.ID] = authz.ActionSet(authz.View | authz.Logs)
			want = append(want, int64(tag.ID))
		default:
			perms[tag.ID] = authz.ActionSet(authz.View)
			want = append(want, int64(tag.ID))
		}
	}
	if err := f.st.SetAgentIdentityPermissions(ctx, perms, admin.ID); err != nil {
		t.Fatal(err)
	}
	see, err := SightOf(ctx, f.st, f.identity.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := see.ViewTags(); !slices.Equal(got, want) {
		t.Errorf("tags = %v, want %v", got, want)
	}
	see.subject.Perms[authz.TagID(want[0])] = authz.ActionSet(authz.Logs)
	if got := see.ViewTags(); !slices.Equal(got, want[1:]) {
		t.Errorf("tags = %v, want %v without the one that lacks View", got, want[1:])
	}
}
