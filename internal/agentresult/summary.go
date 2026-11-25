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
// line feed and tab, every format character (zero-width, bidi, tags), and the default-ignorable fillers and selectors.
func isHiddenRune(r rune) bool {
	switch {
	case r == '\n' || r == '\t':
		return false
	case unicode.Is(unicode.Cc, r), unicode.Is(unicode.Cf, r), unicode.Is(unicode.Variation_Selector, r):
		return true
	case r == 0x034F, r == 0x115F, r == 0x1160, r == 0x17B4, r == 0x17B5, r == 0x3164, r == 0xFFA0:
		return true
	case r >= 0xE0000 && r <= 0xE0FFF: // tags and the variation selector supplement
		return true
	}
	return false
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

// dropsFromExcerpt reports what an excerpt never shows: control characters other than line feed and tab, bidi
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
// Invalid bytes become U+FFFD and hidden control characters are dropped first, so the cap counts what is kept.
func CutExcerpt(text string) string {
	text = strings.ToValidUTF8(text, "\uFFFD")
	text = strings.Map(func(r rune) rune {
		if dropsFromExcerpt(r) {
			return -1
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
