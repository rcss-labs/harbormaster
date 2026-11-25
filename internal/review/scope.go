package review

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/rcss-labs/harbormaster/internal/agentclient"
	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/notify"
	"github.com/rcss-labs/harbormaster/internal/store"
)

const (
	// maxEntryNameBytes is the longest name of a scope entry that the agent takes.
	maxEntryNameBytes = 255
	// maxLabelRunes is the longest scope label the store takes.
	maxLabelRunes = 300
	// maxSightLookups bounds the services looked up for one decision; any further one counts as not visible.
	maxSightLookups = 64
)

// sight is what the agent identity may read, as of now. It is asked anew for each decision and never kept.
type sight struct {
	ctx     context.Context
	st      *store.Store
	subject authz.Subject
	tags    authz.Tags
	exists  map[model.ServiceKey]bool
	err     error // why a service could not be looked up; every answer since is no
}

func readOnlySubject(ctx context.Context, st *store.Store, userID int64) (authz.Subject, error) {
	subject, err := st.LoadSubject(ctx, userID)
	subject.ReadOnly = true
	return subject, err
}

// sightOf loads the identity's permissions and the current tags.
func sightOf(ctx context.Context, st *store.Store, identityID int64) (*sight, error) {
	subject, err := readOnlySubject(ctx, st, identityID)
	if err != nil {
		return nil, err
	}
	tags, err := st.TagsSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	return &sight{ctx: ctx, st: st, subject: subject, tags: tags, exists: map[model.ServiceKey]bool{}}, nil
}

// can reports whether the service exists and the identity holds the action on its current tag.
func (s *sight) can(key model.ServiceKey, action authz.Action) bool {
	if s.err != nil || !authz.Can(s.subject, action, authz.EffectiveServiceTag(s.tags, key)) {
		return false
	}
	known, asked := s.exists[key]
	if asked {
		return known
	}
	if len(s.exists) >= maxSightLookups {
		return false
	}
	_, err := s.st.GetService(s.ctx, key)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.err = err
		return false
	}
	s.exists[key] = err == nil
	return err == nil
}

// Covered is what a review covers as of now.
type Covered struct {
	// Services counts the services in scope that the identity may View.
	Services int
	// Tags are all the tags the identity may View, not only those of the services in scope: the scope is a focus, and
	// the run may read all the rest. Who reads the result needs View on every one.
	Tags []int64
	// Label names the scope for display.
	Label string
	// Entries are the entries of the scope that hold a service the identity may View; none of an empty scope.
	Entries []agentclient.ScopeEntry
}

// Coverage works out what the review covers for subject, the agent identity. Nothing that the subject may not View is
// counted or named, except an entry whose target is gone, which the label calls missing.
func Coverage(ctx context.Context, st *store.Store, tags notify.TagMatcher, review store.Review, subject authz.Subject) (Covered, error) {
	current, err := st.TagsSnapshot(ctx)
	if err != nil {
		return Covered{}, err
	}
	allTags, err := st.ListTags(ctx)
	if err != nil {
		return Covered{}, err
	}
	targets, err := st.NotificationTargets(ctx)
	if err != nil {
		return Covered{}, err
	}
	out := Covered{Tags: []int64{}, Entries: []agentclient.ScopeEntry{}}
	for _, tag := range allTags {
		if authz.Can(subject, authz.View, tag.ID) {
			out.Tags = append(out.Tags, int64(tag.ID))
		}
	}
	var visible []model.ServiceKey
	for _, target := range targets.Services {
		if authz.Can(subject, authz.View, authz.EffectiveServiceTag(current, target.Key)) {
			visible = append(visible, target.Key)
		}
	}
	if len(review.Scope) == 0 {
		out.Services, out.Label = len(visible), "Everything the agent can see"
		return out, nil
	}

	counted := map[model.ServiceKey]bool{}
	var labels []string
	unseen := 0
	for _, info := range notify.DescribeScope(review.Scope, targets, allTags) {
		scope, err := notify.ResolveScope(ctx, tags, []store.NotificationSelector{info.Selector})
		if err != nil {
			return Covered{}, err
		}
		holds := false
		for _, key := range visible {
			if inScope(scope, key) {
				counted[key], holds = true, true
			}
		}
		entry, label := describeEntry(info)
		switch {
		case holds:
			out.Entries = append(out.Entries, entry)
			labels = append(labels, label)
		case info.Missing:
			labels = append(labels, label)
		default:
			unseen++
		}
	}
	if unseen == 1 {
		labels = append(labels, "1 entry the agent cannot see")
	} else if unseen > 1 {
		labels = append(labels, fmt.Sprintf("%d entries the agent cannot see", unseen))
	}
	out.Services, out.Label = len(counted), cutLabel(strings.Join(labels, ", "))
	return out, nil
}

// inScope is how a route's preview decides it, on one entry. A scope of everything comes from no entry at all.
func inScope(scope notify.Scope, key model.ServiceKey) bool {
	return scope.Services[key] || slices.ContainsFunc(scope.Groups, func(g notify.GroupSelector) bool { return g.Contains(key) })
}

// describeEntry is an entry as the agent is told it, with the ids of its own kind only, and as the label names it.
func describeEntry(info notify.SelectorInfo) (agentclient.ScopeEntry, string) {
	sel := info.Selector
	var entry agentclient.ScopeEntry
	var label string
	switch sel.Kind {
	case store.NotificationSelectorHarbormaster:
		entry = agentclient.ScopeEntry{Kind: "tag", TagID: strconv.FormatInt(int64(sel.TagID), 10), Name: oneLine(info.TagName)}
		label = "Tag: " + orElse(entry.Name, entry.TagID)
	case store.NotificationSelectorCluster:
		key := model.ClusterKey{AccountID: sel.AccountID, Region: sel.Region, Cluster: sel.Cluster}
		entry = agentclient.ScopeEntry{Kind: "cluster", ClusterID: key.ID(), Name: oneLine(sel.Cluster)}
		label = "Cluster: " + entry.Name
	default:
		entry = agentclient.ScopeEntry{Kind: "account", AccountID: sel.AccountID, Region: sel.Region, Name: oneLine(info.AccountName)}
		label = "Account: " + orElse(entry.Name, oneLine(sel.AccountID))
		if sel.Region != "" {
			label += " (" + oneLine(sel.Region) + ")"
		}
	}
	if info.Missing {
		label += " (missing)"
	}
	entry.Name = cutBytes(entry.Name, maxEntryNameBytes)
	return entry, label
}

func orElse(text, other string) string {
	if text == "" {
		return other
	}
	return text
}

// oneLine is text that a person chose, as one line without hidden or control characters.
func oneLine(text string) string {
	text = strings.Map(func(r rune) rune {
		switch {
		case unicode.IsSpace(r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r):
			return ' '
		case !unicode.IsPrint(r):
			return -1
		}
		return r
	}, strings.ToValidUTF8(text, ""))
	return strings.Join(strings.Fields(text), " ")
}

// cutBytes cuts text to at most limit bytes between two characters.
func cutBytes(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	for limit > 0 && !utf8.RuneStart(text[limit]) {
		limit--
	}
	return text[:limit]
}

func cutLabel(text string) string {
	if utf8.RuneCountInString(text) <= maxLabelRunes {
		return text
	}
	return string([]rune(text)[:maxLabelRunes-1]) + "…"
}
