package agent

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/rcss-labs/harbormaster/internal/secretshape"
)

// answerTooLargeNote ends a tool answer that reached the upstream read cap.
const answerTooLargeNote = "\n[The answer was larger than could be read; the rest was left out.]"

// resultPlaceholder stands for a tool result the final call has no room for.
const resultPlaceholder = "[This tool result was left out: there was no room for it.]"

// toolResult is a tool answer as the loop keeps it, so that the final call can
// show less of it than was fetched.
type toolResult struct {
	tag     string
	tool    string
	data    string // scrubbed, the run's codes removed, cut to the result cap
	omitted int    // bytes of the scrubbed answer that are not in data
	units   []numberedUnit
	readCap bool // the answer reached the upstream read cap: more existed than was read
}

// newResult prepares a tool answer: scrub, then remove the run's codes,
// and only then cut, so a cut cannot split a secret and the cap holds for what is
// sent.
func (r *session) newResult(tool, text string) *toolResult {
	res, clean := r.cleanResult(tool, text)
	res.keep(clean, r.resultLimit())
	return res
}

func (r *session) resultLimit() int {
	if limit := r.l.Limits.MaxResultBytes; limit > 0 {
		return limit
	}
	return defaultResultBytes
}

// cleanResult returns the answer's text scrubbed and without the run's codes,
// and a result that does not hold it yet.
func (r *session) cleanResult(tool, text string) (*toolResult, string) {
	res := &toolResult{tag: "tool_result_" + r.tagCode, tool: tool}
	if strings.HasSuffix(text, answerTooLargeNote) {
		text = strings.TrimSuffix(text, answerTooLargeNote)
		res.readCap = true
	}
	// An invalid byte travels as a three-byte replacement, so it is replaced
	// before anything is counted.
	text = secretshape.ScrubText(strings.ToValidUTF8(text, "\uFFFD"))
	return res, r.codes.ReplaceAllString(text, "[removed]")
}

// keep stores clean text, cut to limit bytes.
func (res *toolResult) keep(text string, limit int) {
	cut := cutAt(text, limit)
	res.data, res.omitted = text[:cut], len(text)-cut
}

// render is the tool message text: the first n bytes of the data (less when
// that would split a character) inside the run's tag, followed by a note in
// the loop's own words when anything was left out.
func (res *toolResult) render(n int) string {
	shown := cutAt(res.data, n)
	return res.header() + res.data[:shown] + res.note(shown) + "\n</" + res.tag + ">"
}

func (res *toolResult) header() string { return "<" + res.tag + " tool=\"" + res.tool + "\">\n" }

// renderSize is len(render(n)) without building it.
func (res *toolResult) renderSize(n int) int {
	shown := cutAt(res.data, n)
	return len("<"+" tool=\""+"\">\n"+"\n</"+">") + 2*len(res.tag) + len(res.tool) + shown + len(res.note(shown))
}

func (res *toolResult) note(shown int) string {
	total := res.omitted + len(res.data)
	left := total - shown
	switch {
	case left > 0 && res.readCap:
		return "\n[truncated: at least " + strconv.Itoa(left) + " of at least " + strconv.Itoa(total) +
			" bytes were left out; the answer was larger than could be read]"
	case left > 0:
		return "\n[truncated: " + strconv.Itoa(left) + " of " + strconv.Itoa(total) + " bytes were left out]"
	case res.readCap:
		return answerTooLargeNote
	}
	return ""
}

// cutAt returns where to cut text to at most n bytes without splitting a character.
func cutAt(text string, n int) int {
	if len(text) <= n {
		return len(text)
	}
	for n > 0 && !utf8.RuneStart(text[n]) {
		n--
	}
	return n
}
