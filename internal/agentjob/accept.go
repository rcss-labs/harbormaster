package agentjob

import (
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/rcss-labs/harbormaster/internal/agentclient"
	"github.com/rcss-labs/harbormaster/internal/agentresult"
	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/hmtools"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/secretshape"
	"github.com/rcss-labs/harbormaster/internal/store"
)

const (
	// maxNameRunes is the longest part of a service key the store takes.
	maxNameRunes = 255
	// maxDroppedCount bounds the count the agent states, which is stored as an integer.
	maxDroppedCount = 1_000_000
	// maxReportedIDs is the most ids of an answer that are looked at. With the evidence they stay under maxSightLookups.
	maxReportedIDs = 32
)

// AcceptOptions are the differences between the kinds of result.
type AcceptOptions struct {
	// AllowLogs keeps evidence that Logs shows; without it only View evidence is kept.
	AllowLogs bool
	// Replacement is the sentence that stands in for a summary that failed a check.
	Replacement string
	// Echo, when not empty, is the text the run was given to follow: a summary or a note that repeats it is replaced.
	Echo string
	// TaskIDs are the ids of the clusters and services in the task Harbormaster sent: the result may use their names.
	TaskIDs []string
}

// Accept turns what the agent service answered into what is stored. Nothing it says about permissions is taken as
// given: an item is kept only when its action, derived again from its tool, is the one it states, its service id
// names a service, and the identity may take that action on that service now (canRead). Summary, notes and excerpts
// pass the checks that need no logs once more, and a summary or note that repeats Echo is replaced. Items that fail, or
// that the options do not allow, are counted. The checks let random-looking names stand when they are decoded here from
// an id of the task or from one the answer reports that verifies now; a log excerpt keeps none. The ids whose names the
// stored summary uses are stored with it.
func Accept(res agentresult.Result, canRead func(key model.ServiceKey, action authz.Action) bool, opts AcceptOptions) store.InvestigationResult {
	ids := append(namingIDs(opts.TaskIDs), verifiedIDs(res.NameIDs, canRead)...)
	keep := secretshape.NamesOf(ids...)
	summary, replaced := agentresult.RecheckSummaryNaming(res.Summary, opts.Replacement, keep)
	var named []string
	if !replaced {
		named, replaced = idsUsed(summary, keep, ids)
	}
	if !replaced && repeats(summary, opts) {
		replaced = true
	}
	if replaced {
		summary, _ = agentresult.RecheckSummaryAs("", opts.Replacement) // an empty summary is always replaced
		named = nil
	}
	out := store.InvestigationResult{Summary: summary, SummaryReplaced: res.SummaryReplaced || replaced, NameIDs: named}
	dropped := min(max(res.EvidenceDropped, 0), maxDroppedCount)
	for _, item := range res.Evidence {
		if len(out.Evidence) == agentresult.EvidenceCap {
			dropped++
			continue
		}
		kept, ok := acceptItem(item, canRead, opts, keep)
		if !ok {
			dropped++
			continue
		}
		kept.Ordinal = len(out.Evidence) + 1
		out.Evidence = append(out.Evidence, kept)
	}
	out.EvidenceDropped = min(dropped, maxDroppedCount)
	return out
}

// namingIDs returns those of the ids, each once and in order, that give a name the scrubber would hide. An id that could
// not be stored gives none: the ids a review learns have the same bound (hmtools.Units), but the agent service does not
// bound the task's own, of which no cluster's id and, by scope, no service's is as long.
func namingIDs(ids []string) (naming []string) {
	for _, id := range ids {
		if len(id) <= store.MaxSummaryNameIDBytes && !slices.Contains(naming, id) && !secretshape.NamesOf(id).Empty() {
			naming = append(naming, id)
		}
	}
	return naming
}

// verifiedIDs returns those of the reported ids, the first maxReportedIDs of them, that give a name, are spelled as
// Harbormaster spells the id of a service, and name one that the identity may View now.
func verifiedIDs(reported []string, canRead func(key model.ServiceKey, action authz.Action) bool) (verified []string) {
	seen := map[string]bool{}
	for _, id := range reported {
		if seen[id] {
			continue
		}
		if len(seen) == maxReportedIDs {
			break
		}
		seen[id] = true
		key, err := model.ParseServiceID(id)
		if err != nil || key.ID() != id || !storable(key) || len(namingIDs([]string{id})) == 0 {
			continue
		}
		if canRead != nil && canRead(key, authz.View) {
			verified = append(verified, id)
		}
	}
	return verified
}

// idsUsed returns those of the ids whose names a summary that passed its check with keep uses, so that the check passes
// with them alone when the summary is sent. replaced is true when there is no such list that fits.
func idsUsed(summary string, keep secretshape.Names, ids []string) (used []string, replaced bool) {
	enough := func(ids []string) bool {
		_, replaced := agentresult.RecheckSummaryNaming(summary, "", secretshape.NamesOf(ids...))
		return !replaced && len(ids) <= store.MaxSummaryNameIDs
	}
	if keep.Empty() {
		return nil, false
	}
	if used = keep.NamedIn(summary, ids...); enough(used) {
		return used, false
	}
	// A name after an escape in JSON stands in the string and not in the text that holds it, so it was not found.
	if ids = namingIDs(ids); enough(ids) {
		return ids, false
	}
	return nil, true
}

func acceptItem(item agentresult.Evidence, canRead func(key model.ServiceKey, action authz.Action) bool, opts AcceptOptions, keep secretshape.Names) (store.InvestigationEvidence, bool) {
	var none store.InvestigationEvidence
	derived, ok := hmtools.EvidenceAction(item.Tool)
	if !ok || derived != item.Action {
		return none, false
	}
	if derived != hmtools.EvidenceView && !opts.AllowLogs {
		return none, false
	}
	action, ok := actionOf(derived)
	if !ok {
		return none, false
	}
	key, err := model.ParseServiceID(item.ServiceID)
	if err != nil || !storable(key) || canRead == nil || !canRead(key, action) {
		return none, false
	}
	inExcerpt := keep
	if derived != hmtools.EvidenceView {
		inExcerpt = secretshape.Names{} // log text, which the agent service scrubs without names too
	}
	// Scrubbed before it is cut: a secret that the cut halves would no longer look like one.
	excerpt := agentresult.CutExcerpt(secretshape.ScrubTextKeeping(item.Excerpt, inExcerpt))
	if strings.TrimSpace(excerpt) == "" {
		return none, false
	}
	note := agentresult.RecheckNoteNaming(item.Note, keep)
	if repeats(note, opts) {
		note = agentresult.ReplacementNote
	}
	return store.InvestigationEvidence{
		Tool: item.Tool, Action: derived,
		AccountID: key.AccountID, Region: key.Region, ClusterName: key.Cluster, ServiceName: key.Service,
		Note: note, Excerpt: excerpt,
	}, true
}

// repeats reports whether text repeats what the run was given to follow.
func repeats(text string, opts AcceptOptions) bool {
	return opts.Echo != "" && agentresult.Echoes(text, opts.Echo)
}

// actionOf is the permission an evidence action asks for; an unknown action has none.
func actionOf(action string) (authz.Action, bool) {
	switch action {
	case hmtools.EvidenceView:
		return authz.View, true
	case hmtools.EvidenceLogs:
		return authz.Logs, true
	default:
		return 0, false
	}
}

// storable reports whether every part of a decoded key is text the store takes.
func storable(key model.ServiceKey) bool {
	for _, part := range []string{key.AccountID, key.Region, key.Cluster, key.Service} {
		if !utf8.ValidString(part) || strings.ContainsRune(part, 0) || utf8.RuneCountInString(part) > maxNameRunes {
			return false
		}
	}
	return true
}

// TaskIDs returns the ids of the clusters and services in a task, those whose names the agent service lets its run use:
// of the subject of an investigation, or of the clusters in a review's scope. A task's id is neither.
func TaskIDs(task agentclient.Task) (ids []string) {
	given := append([]string{task.Subject.ClusterID}, task.Subject.ServiceIDs...)
	if task.Kind == "review" {
		given = nil
		if task.Scope != nil {
			for _, entry := range task.Scope.Entries {
				given = append(given, entry.ClusterID)
			}
		}
	}
	for _, id := range given {
		cluster, err := model.ParseClusterID(id)
		isCluster := err == nil && !strings.HasPrefix(cluster.Cluster, "arn:")
		if _, err := model.ParseServiceID(id); isCluster || err == nil {
			ids = append(ids, id)
		}
	}
	return ids
}
