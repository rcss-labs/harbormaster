package agent

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/rcss-labs/harbormaster/internal/agent/provider"
	"github.com/rcss-labs/harbormaster/internal/agentresult"
	"github.com/rcss-labs/harbormaster/internal/hmtools"
	"github.com/rcss-labs/harbormaster/internal/secretshape"
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

// findings is what an investigator or reviewer run has read. It lives in memory for the
// run only and is never logged.
type findings struct {
	corpus agentresult.ReadCorpus
	last   int // the last number given
	units  map[string]citedUnit
	unseen []*toolResult // results with units the model may not have been shown yet

	taskIDs []string          // ids of the clusters and services the run is about
	learned []string          // ids of services a review read about, in the order it read them
	known   map[string]bool   // the learned ids
	keep    secretshape.Names // the names the task's ids and the learned ones decode to
}

func newFindings() *findings {
	return &findings{units: map[string]citedUnit{}, known: map[string]bool{}}
}

// allowSubject lets the summary name what the run is about: the services and
// clusters its subject's ids decode to, and no others.
func (f *findings) allowSubject(subject TaskSubject) {
	for _, id := range append([]string{subject.ClusterID}, subject.ServiceIDs...) {
		cluster, service, _ := secretshape.IDNames(id)
		f.corpus.AllowNames(cluster, service)
	}
}

// nameTask lets the run see and write the names its task's own ids decode to: those of an investigation's subject, or of
// the clusters in a review's scope.
func (f *findings) nameTask(role string, task Task) {
	ids := append([]string{task.Subject.ClusterID}, task.Subject.ServiceIDs...)
	if role == roleReviewer {
		ids = nil
		for _, entry := range task.Scope.Entries {
			ids = append(ids, entry.ClusterID)
		}
	}
	for _, id := range ids {
		if isResourceID(id) {
			f.taskIDs = append(f.taskIDs, id)
		}
	}
	f.keep = secretshape.NamesOf(f.taskIDs...)
}

// isResourceID reports whether id is a cluster's or a service's. A task's id is neither: the cluster in its ARN may be
// of any length.
func isResourceID(id string) bool {
	_, _, ok := secretshape.IDNames(id)
	raw, err := base64.RawURLEncoding.DecodeString(id)
	return ok && err == nil && !bytes.Contains(raw, []byte("arn:"))
}

// learn lets the run see and write the names of the services that code attributes the units of a raw answer to. An id
// anywhere else in the answer teaches nothing, and neither does an answer that may be log text or is cut.
func (f *findings) learn(tool string, args map[string]any, raw string) {
	if hmtools.ReadsLogs(tool) || strings.HasSuffix(raw, answerTooLargeNote) {
		return
	}
	for _, unit := range hmtools.Units(tool, args, raw) {
		id := unit.ServiceID
		if f.known[id] {
			continue
		}
		if _, service, _ := secretshape.IDNames(id); service == "" {
			continue
		}
		f.known[id] = true
		f.learned = append(f.learned, id)
		f.keep.Add(id)
	}
}

// readResult prepares a successful tool answer: its scrubbed text counts as
// read, and its units get their numbers. A unit whose marker does not survive
// the result cap is forgotten, and an answer cut by the read cap has none.
func (r *session) readResult(tool string, args map[string]any, text string) *toolResult {
	if r.reviewing() {
		r.found.learn(tool, args, text)
	}
	var keep secretshape.Names
	if !hmtools.ReadsLogs(tool) {
		keep = r.found.keep
	}
	res, clean := r.cleanResult(tool, text, keep)
	r.found.corpus.Add(tool, clean)
	if r.reviewing() {
		r.found.corpus.AllowNames(idNames(clean)...)
	}
	if res.readCap {
		res.keep(clean, r.resultLimit())
		return res
	}
	shown, units := numberUnits(tool, args, clean, r.found.last+1)
	if r.reviewing() && !viewOnly(units) {
		shown, units = clean, nil // a review reads no logs, so it cites none
	}
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
// returns the text that shows them: one line each, then what no unit covers
// (before the units for a nested answer). An answer without units is returned as
// it is.
func numberUnits(tool string, args map[string]any, text string, next int) (string, []numberedUnit) {
	found := hmtools.Units(tool, args, text)
	if len(found) == 0 || next+len(found)-1 > maxUnitNumber {
		return text, nil
	}
	var shown strings.Builder
	rest := restOf(tool, text, found)
	// The totals of an analytics answer come before its rows: a result over the byte cap is cut from the end,
	// and the rows are what can be spared.
	restFirst := rest != "" && restComesFirst(tool)
	if restFirst {
		shown.WriteString(notCitable + oneLine.Replace(rest) + "\n")
	}
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
	if rest != "" && !restFirst {
		shown.WriteString("\n" + notCitable + oneLine.Replace(rest))
	}
	return shown.String(), units
}

// restComesFirst is true for the tools whose answers hold totals beside their rows.
func restComesFirst(tool string) bool {
	rule, _ := hmtools.EvidenceRuleFor(tool)
	return rule.Kind == hmtools.EvidencePerItemNested
}

// restOf is the part of a JSON answer that no unit covers, as compact JSON, or
// "" when there is none. Units come in document order, so one walk finds it; a
// nested answer is walked by hmtools, which makes its units.
func restOf(tool, text string, units []hmtools.Unit) string {
	rule, _ := hmtools.EvidenceRuleFor(tool)
	if rule.Kind == hmtools.EvidencePerItemNested {
		return hmtools.NestedRest(tool, text)
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

func compactJSON(raw []byte) string {
	var out bytes.Buffer
	if json.Compact(&out, raw) != nil {
		return string(raw)
	}
	return out.String()
}
