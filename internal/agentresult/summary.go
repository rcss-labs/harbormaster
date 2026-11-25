package agentresult

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/rcss-labs/harbormaster/internal/secretshape"
)

// imageMarkers are the starts of Markdown and HTML that make a reader's client fetch or render something.
var imageMarkers = []string{"![", "<img", "<svg", "<picture", "<video", "<iframe", "<object", "<embed"}

// hasImageMarkup reports image-like markup, in any letter case.
func hasImageMarkup(text string) bool {
	lower := strings.ToLower(text)
	for _, marker := range imageMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// lineBreaks turns every kind of line break into a space: a note is one line.
var lineBreaks = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ", "\v", " ", "\f", " ", "\u0085", " ", "\u2028", " ", "\u2029", " ")

func flatten(text string) string { return lineBreaks.Replace(text) }

// isHiddenRune reports a rune a reader cannot see or that reorders what they see: control characters other than
// line feed and tab, every format character (zero-width, bidi, tags), variation selectors, and the rest of Unicode's
// default-ignorable set (fillers, reserved format code points). Text with a zero-width joiner is refused too: failing
// closed there costs a few emoji and some scripts, and is documented.
func isHiddenRune(r rune) bool {
	if r == '\n' || r == '\t' {
		return false
	}
	return unicode.Is(unicode.Cc, r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Variation_Selector, r) ||
		unicode.Is(unicode.Other_Default_Ignorable_Code_Point, r)
}

// passesBasics holds the tests a repeat can make without the logs: valid UTF-8, nothing hidden, not empty, within the
// cap, unchanged by the scrubber.
func passesBasics(text string, limit int) bool {
	if strings.TrimSpace(text) == "" || !utf8.ValidString(text) || utf8.RuneCountInString(text) > limit {
		return false
	}
	for _, r := range text {
		if isHiddenRune(r) {
			return false
		}
	}
	return secretshape.ScrubText(text) == text
}

// knownReplacement returns replacement when it is one of the two summary sentences and the investigation sentence
// otherwise.
func knownReplacement(replacement string) string {
	if replacement == ReplacementReviewSummary {
		return replacement
	}
	return ReplacementSummary
}

// CheckSummary is the agent service's full check. A summary that fails any test is replaced whole, never trimmed:
// a partly cleaned text could still carry the rest of a secret. A nil or full corpus cannot vouch for it, so it fails.
func CheckSummary(summary string, corpus *Corpus) (clean string, replaced bool) {
	return CheckSummaryAs(summary, corpus, ReplacementSummary)
}

// CheckSummaryAs is CheckSummary with the sentence a failed summary is replaced by. Only the two replacement constants
// are accepted; anything else gets the investigation sentence, so a caller cannot make the check hand back its own text.
func CheckSummaryAs(summary string, corpus *Corpus, replacement string) (clean string, replaced bool) {
	replacement = knownReplacement(replacement)
	if !passesBasics(summary, SummaryCap) || hasImageMarkup(summary) || corpus == nil || corpus.Full() || corpus.HasRunIn(summary) {
		return replacement, true
	}
	return summary, false
}

// RecheckSummary is Harbormaster's repeat: the cap and the scrubber only, it holds no corpus.
func RecheckSummary(summary string) (clean string, replaced bool) {
	return RecheckSummaryAs(summary, ReplacementSummary)
}

// RecheckSummaryAs is RecheckSummary with the replacement sentence, under the rule of CheckSummaryAs.
func RecheckSummaryAs(summary, replacement string) (clean string, replaced bool) {
	replacement = knownReplacement(replacement)
	if !passesBasics(summary, SummaryCap) {
		return replacement, true
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

// dropsFromExcerpt reports what an excerpt never shows (it is replaced by a space): control characters other than line feed and tab, bidi
// controls and tag characters.
func dropsFromExcerpt(r rune) bool {
	switch {
	case r == '\n' || r == '\t':
		return false
	case unicode.Is(unicode.Cc, r):
		return true
	case r == 0x061C, r == 0x200E, r == 0x200F, r >= 0x202A && r <= 0x202E, r >= 0x2066 && r <= 0x2069:
		return true
	}
	return r >= 0xE0000 && r <= 0xE007F
}

// CutExcerpt keeps at most ExcerptCap bytes, cut on a rune boundary, with CutMarker at the end when it was shortened.
// Invalid bytes become U+FFFD and hidden control characters become spaces first, so nothing joins and the cap counts
// what is kept.
func CutExcerpt(text string) string {
	text = strings.ToValidUTF8(text, "\uFFFD")
	text = strings.Map(func(r rune) rune {
		if dropsFromExcerpt(r) {
			return ' '
		}
		return r
	}, text)
	if len(text) <= ExcerptCap {
		return text
	}
	n := ExcerptCap - len(CutMarker)
	for n > 0 && !utf8.RuneStart(text[n]) {
		n--
	}
	return text[:n] + CutMarker
}
