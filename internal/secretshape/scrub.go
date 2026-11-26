package secretshape

import (
	"encoding/json"
	"sort"
	"strings"
)

const (
	// maxJSONDepth bounds how deep JSON inside JSON strings is walked; below it the plain-text rules apply.
	maxJSONDepth = 8
	// maxEscapeLevels bounds how many levels of JSON escapes the plain-text rules look through: three for JSON
	// encoded up to three times, and one for the answer it sits in.
	maxEscapeLevels = 4
)

// edit replaces text[start:end].
type edit struct {
	start, end  int
	replacement string
}

// ScrubText replaces each secret-shaped span in text with Redacted and leaves the rest byte-identical. JSON, whole or
// one value per line, is walked structurally and stays valid; everything else is read as plain text.
func ScrubText(text string) string {
	return ScrubTextKeeping(text, Names{})
}

// ScrubTextKeeping is ScrubText that leaves a long run alone when the whole run is a name in keep. Every other rule
// hides such a name as ever. As with ScrubText, a later pass only hides more.
func ScrubTextKeeping(text string, keep Names) string {
	edits := scrub(text, keep, 0)
	if len(edits) == 0 {
		return text
	}
	var out strings.Builder
	last := 0
	for _, e := range edits {
		out.WriteString(text[last:e.start])
		out.WriteString(e.replacement)
		last = e.end
	}
	out.WriteString(text[last:])
	return out.String()
}

// scrub returns the edits for text, in order and not overlapping. depth counts the JSON strings text is nested in.
func scrub(text string, keep Names, depth int) []edit {
	if depth >= maxJSONDepth {
		return plainEdits(text, keep)
	}
	if isJSON(text, "{[\"-0123456789") {
		return jsonEdits(text, keep, depth)
	}
	if !strings.Contains(text, "\n") {
		return plainEdits(text, keep)
	}
	// Lines that are JSON are walked one by one; the stretches between them are plain text.
	var edits []edit
	plainStart := 0
	for lineStart := 0; lineStart < len(text); {
		lineEnd := len(text)
		if i := strings.IndexByte(text[lineStart:], '\n'); i >= 0 {
			lineEnd = lineStart + i + 1
		}
		if line := text[lineStart:lineEnd]; isJSON(line, "{[") {
			edits = appendShifted(edits, plainEdits(text[plainStart:lineStart], keep), plainStart)
			edits = appendShifted(edits, jsonEdits(line, keep, depth), lineStart)
			plainStart = lineEnd
		}
		lineStart = lineEnd
	}
	return appendShifted(edits, plainEdits(text[plainStart:], keep), plainStart)
}

func appendShifted(edits, more []edit, by int) []edit {
	for _, e := range more {
		edits = append(edits, edit{e.start + by, e.end + by, e.replacement})
	}
	return edits
}

// isJSON reports whether text is one valid JSON value that begins with one of first.
func isJSON(text, first string) bool {
	trimmed := strings.Trim(text, " \t\r\n")
	return trimmed != "" && strings.IndexByte(first, trimmed[0]) >= 0 && json.Valid([]byte(trimmed))
}

// plainEdits applies the plain-text rules to text as it is and as it reads with one to maxEscapeLevels levels of JSON
// escapes removed, so that a secret after \n or &, or a pair with escaped quotes, is found in JSON that was cut
// and cannot be parsed. Everything any reading finds is hidden.
func plainEdits(text string, keep Names) []edit {
	spans := append(pairSpans(text), valueSpans(text, keep)...)
	view := text
	var offsets []int // of each byte of view in text
	for level := 0; level < maxEscapeLevels && strings.IndexByte(view, '\\') >= 0; level++ {
		next, at := unescape(view)
		if offsets != nil {
			for i := range at {
				at[i] = offsets[at[i]]
			}
		}
		view, offsets = next, at
		for _, span := range append(pairSpans(view), valueSpans(view, keep)...) {
			spans = append(spans, [2]int{offsets[span[0]], offsets[span[1]]})
		}
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i][0] < spans[j][0] })
	var edits []edit
	for _, span := range spans {
		if n := len(edits); n > 0 && span[0] <= edits[n-1].end {
			edits[n-1].end = max(edits[n-1].end, span[1])
			continue
		}
		edits = append(edits, edit{span[0], span[1], Redacted})
	}
	return edits
}
