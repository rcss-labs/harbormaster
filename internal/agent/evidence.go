package agent

import (
	"bytes"
	"encoding/json"
	"slices"
	"strconv"
	"strings"

	"github.com/rcss-labs/harbormaster/internal/agent/provider"
	"github.com/rcss-labs/harbormaster/internal/agentresult"
	"github.com/rcss-labs/harbormaster/internal/hmtools"
)

// maxUnitNumber is the largest N of a unit reference e<N>. A result whose
// units would pass it is shown without markers.
const maxUnitNumber = 99999

// notCitable begins the line that holds the part of an answer no unit covers.
const notCitable = "[not citable] "

// citedUnit is what a reference stands for. All of it comes from code: the
// label from the tool call, the excerpt from the scrubbed answer.
type citedUnit struct{ tool, serviceID, action, excerpt string }

// numberedUnit is a unit as it is shown in a tool result.
type numberedUnit struct {
	ref string
	end int // where its marker ends in the shown text
	citedUnit
}

// findings is what an investigator run has read. It lives in memory for the
// run only and is never logged.
type findings struct {
	corpus agentresult.Corpus
	last   int // the last number given
	units  map[string]citedUnit
	unseen []*toolResult // results with units the model may not have been shown yet
}

func newFindings() *findings { return &findings{units: map[string]citedUnit{}} }

// readResult prepares a successful tool answer: its scrubbed text counts as
// read, and its units get their numbers. A unit whose marker does not survive
// the result cap is forgotten, and an answer cut by the read cap has none.
func (r *session) readResult(tool string, args map[string]any, text string) *toolResult {
	res, clean := r.cleanResult(tool, text)
	r.found.corpus.Add(clean)
	if res.readCap {
		res.keep(clean, r.resultLimit())
		return res
	}
	shown, units := numberUnits(tool, args, clean, r.found.last+1)
	res.keep(shown, r.resultLimit())
	r.found.last += len(units)
	for _, u := range units {
		if u.end <= len(res.data) {
			res.units = append(res.units, u)
		}
	}
	if len(res.units) > 0 {
		r.found.unseen = append(r.found.unseen, res)
	}
	return res
}

// noteShown makes citable the units whose markers the model was shown in the
// request msgs, which it has just answered. The final call may cut a result, or
// leave it out, before the model ever saw it whole.
func (f *findings) noteShown(msgs []provider.Message) {
	for _, res := range f.unseen {
		whole, header := res.render(len(res.data)), len(res.header())
		seen := 0
		for _, m := range msgs {
			if m.Role == provider.RoleTool {
				seen = max(seen, commonPrefix(m.Text, whole)-header)
			}
		}
		for _, u := range res.units {
			if u.end <= seen {
				f.units[u.ref] = u.citedUnit
			}
		}
	}
	f.unseen = nil
}

func commonPrefix(a, b string) int {
	n := min(len(a), len(b))
	for i := range n {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

// oneLine keeps a unit on one line, so that only the loop begins a line with a marker.
var oneLine = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ", "\v", " ", "\f", " ", "\u0085", " ", "\u2028", " ", "\u2029", " ")

// numberUnits splits a scrubbed answer into units numbered from next, and
// returns the text that shows them: one line each, then what no unit covers. An
// answer without units is returned as it is.
func numberUnits(tool string, args map[string]any, text string, next int) (string, []numberedUnit) {
	found := hmtools.Units(tool, args, text)
	if len(found) == 0 || next+len(found)-1 > maxUnitNumber {
		return text, nil
	}
	var shown strings.Builder
	units := make([]numberedUnit, len(found))
	for i, u := range found {
		if i > 0 {
			shown.WriteByte('\n')
		}
		ref := "e" + strconv.Itoa(next+i)
		shown.WriteString("[" + ref + "]")
		units[i] = numberedUnit{ref: ref, end: shown.Len(), citedUnit: citedUnit{
			tool: tool, serviceID: u.ServiceID, action: u.Action, excerpt: agentresult.CutExcerpt(u.Text),
		}}
		shown.WriteString(" " + oneLine.Replace(u.Text))
	}
	if rest := restOf(tool, text, found); rest != "" {
		shown.WriteString("\n" + notCitable + oneLine.Replace(rest))
	}
	return shown.String(), units
}

// restOf is the part of a JSON answer that no unit covers, as compact JSON, or
// "" when there is none. Units come in document order, so one walk finds it.
func restOf(tool, text string, units []hmtools.Unit) string {
	rule, _ := hmtools.EvidenceRuleFor(tool)
	if rule.Kind == hmtools.EvidencePerItemNested {
		return nestedRest(rule.Arrays, bytes.TrimSpace([]byte(text)), units)
	}
	covered := func(piece string) bool {
		if len(units) == 0 || units[0].Text != piece {
			return false
		}
		units = units[1:]
		return true
	}
	// uncovered returns the elements of a JSON array that are no unit.
	uncovered := func(prefix string, elements []json.RawMessage) (left []string) {
		for _, element := range elements {
			if compact := compactJSON(element); !covered(prefix + compact) {
				left = append(left, compact)
			}
		}
		return left
	}

	body := bytes.TrimSpace([]byte(text))
	if body[0] == '[' {
		var elements []json.RawMessage
		if json.Unmarshal(body, &elements) != nil {
			return ""
		}
		if left := uncovered("", elements); len(left) > 0 {
			return "[" + strings.Join(left, ",") + "]"
		}
		return ""
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return ""
	}
	var rest []string
	for decoder.More() {
		token, err := decoder.Token()
		name, isName := token.(string)
		var value json.RawMessage
		if err != nil || !isName || decoder.Decode(&value) != nil {
			break
		}
		prefix := name + ": "
		if rule.Kind == hmtools.EvidencePerItem {
			prefix = ""
		}
		compact := compactJSON(value)
		var elements []json.RawMessage
		switch {
		case rule.Kind == hmtools.EvidencePerItem && name != rule.Items:
		case compact[0] == '[' && json.Unmarshal(value, &elements) == nil:
			left := uncovered(prefix, elements)
			if len(left) == 0 && len(elements) > 0 {
				continue
			}
			compact = "[" + strings.Join(left, ",") + "]"
		default:
			if covered(prefix + compact) {
				continue
			}
		}
		key, _ := json.Marshal(name)
		rest = append(rest, string(key)+":"+compact)
	}
	if len(rest) == 0 {
		return ""
	}
	return "{" + strings.Join(rest, ",") + "}"
}

// nestedRest is the answer with the elements the units cover taken out of the named arrays, as compact JSON, or ""
// when nothing else is left. It follows the same walk as hmtools.Units, so the units are used up in the order the
// elements appear. An array or an object that the removal emptied is left out; one that was empty to begin with, an
// array that is not the named one and an object with a repeated member name (which gave no units) stay as they are.
func nestedRest(arrays []hmtools.ItemArray, body []byte, units []hmtools.Unit) string {
	covered := func(piece string) bool {
		if len(units) == 0 || units[0].Text != piece {
			return false
		}
		units = units[1:]
		return true
	}
	var strip func(raw json.RawMessage, path []string) (rest string, left bool)
	strip = func(raw json.RawMessage, path []string) (string, bool) {
		members, ok := distinctMembers(raw)
		if !ok {
			return compactJSON(raw), true
		}
		var kept []string
		removed := false
		for _, m := range members {
			here := append(path[:len(path):len(path)], m.name)
			value, keep := compactJSON(m.value), true
			for _, array := range arrays {
				if slices.Equal(array.Path, here) {
					var elements []json.RawMessage
					if !bytes.HasPrefix(bytes.TrimSpace(m.value), []byte("[")) || json.Unmarshal(m.value, &elements) != nil {
						continue
					}
					var uncovered []string
					for _, element := range elements {
						if compact := compactJSON(element); !covered(compact) {
							uncovered = append(uncovered, compact)
						}
					}
					if len(uncovered) == 0 && len(elements) > 0 {
						keep = false
					}
					value = "[" + strings.Join(uncovered, ",") + "]"
				} else if len(array.Path) > len(here) && slices.Equal(array.Path[:len(here)], here) && bytes.HasPrefix(bytes.TrimSpace(m.value), []byte("{")) {
					value, keep = strip(m.value, here)
				}
			}
			if !keep {
				removed = true
				continue
			}
			key, _ := json.Marshal(m.name)
			kept = append(kept, string(key)+":"+value)
		}
		if len(kept) == 0 && removed {
			return "", false
		}
		return "{" + strings.Join(kept, ",") + "}", true
	}
	if len(body) == 0 || body[0] != '{' {
		return ""
	}
	rest, _ := strip(body, nil)
	return rest
}

type namedValue struct {
	name  string
	value json.RawMessage
}

// distinctMembers reads a JSON object into its members in document order. It is false for anything that is not an
// object, and for an object that repeats a member name.
func distinctMembers(raw []byte) ([]namedValue, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return nil, false
	}
	seen := map[string]struct{}{}
	var members []namedValue
	for decoder.More() {
		token, err := decoder.Token()
		name, isName := token.(string)
		var value json.RawMessage
		if err != nil || !isName || decoder.Decode(&value) != nil {
			return nil, false
		}
		if _, repeated := seen[name]; repeated {
			return nil, false
		}
		seen[name] = struct{}{}
		members = append(members, namedValue{name, value})
	}
	return members, true
}

func compactJSON(raw []byte) string {
	var out bytes.Buffer
	if json.Compact(&out, raw) != nil {
		return string(raw)
	}
	return out.String()
}
