package agentjob

import (
	"strings"
	"unicode/utf8"

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
)

// AcceptOptions are the differences between the kinds of result.
type AcceptOptions struct {
	// AllowLogs keeps evidence that Logs shows; without it only View evidence is kept.
	AllowLogs bool
	// Replacement is the sentence that stands in for a summary that failed a check.
	Replacement string
	// Echo, when not empty, is the text the run was given to follow: a summary or a note that repeats it is replaced.
	Echo string
}

// Accept turns what the agent service answered into what is stored. Nothing it says about permissions is taken as
// given: an item is kept only when its action, derived again from its tool, is the one it states, its service id
// names a service, and the identity may take that action on that service now (canRead). Summary, notes and excerpts
// pass the checks that need no logs once more, and a summary or note that repeats Echo is replaced. Items that fail, or
// that the options do not allow, are counted.
func Accept(res agentresult.Result, canRead func(key model.ServiceKey, action authz.Action) bool, opts AcceptOptions) store.InvestigationResult {
	summary, replaced := agentresult.RecheckSummaryAs(res.Summary, opts.Replacement)
	if !replaced && repeats(summary, opts) {
		summary, replaced = agentresult.RecheckSummaryAs("", opts.Replacement) // an empty summary is always replaced
	}
	out := store.InvestigationResult{Summary: summary, SummaryReplaced: res.SummaryReplaced || replaced}
	dropped := min(max(res.EvidenceDropped, 0), maxDroppedCount)
	for _, item := range res.Evidence {
		if len(out.Evidence) == agentresult.EvidenceCap {
			dropped++
			continue
		}
		kept, ok := acceptItem(item, canRead, opts)
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

func acceptItem(item agentresult.Evidence, canRead func(key model.ServiceKey, action authz.Action) bool, opts AcceptOptions) (store.InvestigationEvidence, bool) {
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
	// Scrubbed before it is cut: a secret that the cut halves would no longer look like one.
	excerpt := agentresult.CutExcerpt(secretshape.ScrubText(item.Excerpt))
	if strings.TrimSpace(excerpt) == "" {
		return none, false
	}
	note := agentresult.RecheckNote(item.Note)
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
