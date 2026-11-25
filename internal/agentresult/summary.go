package agentresult

import (
	"strings"
	"unicode/utf8"

	"github.com/rcss-labs/harbormaster/internal/secretshape"
)

// hasImageMarkup reports Markdown or HTML image syntax, which would make a reader's client fetch a URL.
func hasImageMarkup(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "![") || strings.Contains(lower, "<img")
}

// flatten turns line breaks into spaces: a note is one line.
func flatten(text string) string {
	return strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ", " ", " ", " ", " ").Replace(text)
}

// passesBasics holds the tests a repeat can make without the logs: not empty, within the cap, unchanged by the scrubber.
func passesBasics(text string, limit int) bool {
	if strings.TrimSpace(text) == "" || utf8.RuneCountInString(text) > limit {
		return false
	}
	return secretshape.ScrubText(text) == text
}

// CheckSummary is the agent service's full check. A summary that fails any test is replaced whole, never trimmed:
// a partly cleaned text could still carry the rest of a secret. A nil or full corpus cannot vouch for it, so it fails.
func CheckSummary(summary string, corpus *Corpus) (clean string, replaced bool) {
	if !passesBasics(summary, SummaryCap) || hasImageMarkup(summary) || corpus == nil || corpus.Full() || corpus.HasRunIn(summary) {
		return ReplacementSummary, true
	}
	return summary, false
}

// RecheckSummary is Harbormaster's repeat: the cap and the scrubber only, it holds no corpus.
func RecheckSummary(summary string) (clean string, replaced bool) {
	if !passesBasics(summary, SummaryCap) {
		return ReplacementSummary, true
	}
	return summary, false
}

// CheckNote is CheckSummary for a one-line note with the 300 cap.
func CheckNote(note string, corpus *Corpus) string {
	note = flatten(note)
	if !passesBasics(note, NoteCap) || hasImageMarkup(note) || corpus == nil || corpus.Full() || corpus.HasRunIn(note) {
		return ReplacementNote
	}
	return note
}

// RecheckNote is RecheckSummary for a note.
func RecheckNote(note string) string {
	note = flatten(note)
	if !passesBasics(note, NoteCap) {
		return ReplacementNote
	}
	return note
}

// CutExcerpt keeps at most ExcerptCap bytes, cut on a rune boundary, with CutMarker at the end when it was shortened.
func CutExcerpt(text string) string {
	text = strings.ToValidUTF8(text, "�")
	if len(text) <= ExcerptCap {
		return text
	}
	n := ExcerptCap - len(CutMarker)
	for n > 0 && !utf8.RuneStart(text[n]) {
		n--
	}
	return text[:n] + CutMarker
}
