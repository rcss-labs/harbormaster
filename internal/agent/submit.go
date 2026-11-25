package agent

import (
	"strings"

	"github.com/rcss-labs/harbormaster/internal/agent/provider"
	"github.com/rcss-labs/harbormaster/internal/agentresult"
	"github.com/rcss-labs/harbormaster/internal/secretshape"
)

// submitTool is how an investigator or a reviewer finishes. The loop handles the call itself:
// it is in no tool table and never reaches Harbormaster.
const submitTool = "submit_result"

// submitToolDef describes submit_result to a run of the given kind.
func submitToolDef(kind string) provider.ToolDef {
	return provider.ToolDef{
		Name: submitTool,
		Description: "Finish the " + kind + " with your conclusion and the evidence for it. Call it once, alone: nothing runs after it. " +
			"The text of each piece of evidence is taken from what you read; you only name it.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"summary": map[string]any{
					"type":        "string",
					"description": "The conclusion in your own words, within 1200 characters. No quoted log lines, values or links.",
				},
				"evidence": map[string]any{
					"type":        "array",
					"maxItems":    agentresult.EvidenceCap,
					"description": "What the conclusion rests on, most important first.",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"ref":  map[string]any{"type": "string", "description": "The marker of one piece of a tool result without its brackets, for example e12."},
							"note": map[string]any{"type": "string", "description": "One line in your own words, within 300 characters: what it shows."},
						},
						"required":             []string{"ref", "note"},
						"additionalProperties": false,
					},
				},
			},
			"required":             []string{"summary"},
			"additionalProperties": false,
		},
	}
}

// submission is a well-formed submit_result call. Its words are the model's
// and are not checked yet.
type submission struct {
	summary  string
	evidence []citation
}

type citation struct {
	ref  string // "" when the model gave something that is not a string
	note string
}

// findSubmit returns the first submit_result call of an investigator's or a reviewer's turn.
func (r *session) findSubmit(calls []provider.ToolCall) (provider.ToolCall, bool) {
	if r.found != nil {
		for _, call := range calls {
			if call.Name == submitTool {
				return call, true
			}
		}
	}
	return provider.ToolCall{}, false
}

// parseSubmission reads a submit_result call, or says in a sentence for the
// model what is wrong with it. The sentence never repeats the arguments.
func parseSubmission(call provider.ToolCall) (sub submission, problem string) {
	if call.ArgError != "" {
		return submission{}, "The arguments of submit_result were not valid: they were not a JSON object."
	}
	for name := range call.Arguments {
		if name != "summary" && name != "evidence" {
			return submission{}, "submit_result takes only summary and evidence."
		}
	}
	var ok bool
	if sub.summary, ok = call.Arguments["summary"].(string); !ok || strings.TrimSpace(sub.summary) == "" {
		return submission{}, "submit_result needs a summary: a string that is not empty."
	}
	given := call.Arguments["evidence"]
	if given == nil { // absent or null
		return sub, ""
	}
	list, ok := given.([]any)
	if !ok {
		return submission{}, "The evidence of submit_result must be an array."
	}
	for _, item := range list {
		entry, ok := item.(map[string]any)
		if !ok {
			return submission{}, "Each entry in the evidence of submit_result must be an object with ref and note."
		}
		var c citation
		for name, value := range entry {
			switch name {
			case "ref":
				c.ref, _ = value.(string)
			case "note":
				if c.note, ok = value.(string); !ok && value != nil {
					return submission{}, "The note of each entry in the evidence of submit_result must be a string."
				}
			default:
				return submission{}, "Each entry in the evidence of submit_result takes only ref and note."
			}
		}
		sub.evidence = append(sub.evidence, c)
	}
	return sub, ""
}

// maxNameIDs is the most ids of learned names a result reports.
const maxNameIDs = 32

// buildResult makes the result of a run. The model's words reach it only as the
// checked summary and notes; the rest comes from units. A reference that is not
// a unit the model was shown, or is repeated, is dropped. replacement stands in for
// a summary that failed its check. The words may use the names of taskIDs and of
// those of the learned ids that the result reports, so that Harbormaster can repeat
// the check with the same names.
func buildResult(sub submission, units map[string]citedUnit, corpus *agentresult.ReadCorpus, replacement string, taskIDs, learned []string) agentresult.Result {
	result := agentresult.Result{Evidence: []agentresult.Evidence{}}
	cited := map[string]bool{}
	for _, c := range sub.evidence {
		unit, known := units[c.ref]
		if !known || cited[c.ref] || len(result.Evidence) == agentresult.EvidenceCap {
			result.EvidenceDropped++
			continue
		}
		cited[c.ref] = true
		result.Evidence = append(result.Evidence, agentresult.Evidence{
			Tool: unit.tool, ServiceID: unit.serviceID, Action: unit.action, Note: c.note, Excerpt: unit.excerpt,
		})
	}

	keep := secretshape.NamesOf(taskIDs...)
	if len(learned) > 0 {
		keep.Add(learned...)
		text := []string{sub.summary}
		for _, e := range result.Evidence {
			text = append(text, agentresult.CheckReadNoteNaming(e.Note, corpus, keep), e.Excerpt)
		}
		result.NameIDs = reportedIDs(strings.Join(text, "\n"), keep, taskIDs, learned)
		keep = secretshape.NamesOf(taskIDs...)
		keep.Add(result.NameIDs...)
	}
	result.Summary, result.SummaryReplaced = agentresult.CheckReadSummaryNaming(sub.summary, corpus, replacement, keep)
	for i := range result.Evidence {
		result.Evidence[i].Note = agentresult.CheckReadNoteNaming(result.Evidence[i].Note, corpus, keep)
	}
	return result
}

// reportedIDs returns the first of the learned ids whose names stand in text, at most maxNameIDs, in the order they were
// learned and not that of their use. One that stands there
// by its cluster alone is left out when the task or an earlier one gives that name: every service of a cluster would
// count for the cluster's name otherwise, and leave no room for those whose own names are used.
func reportedIDs(text string, keep secretshape.Names, taskIDs, learned []string) (ids []string) {
	clusters := map[string]bool{}
	for _, id := range taskIDs {
		cluster, _, _ := secretshape.IDNames(id)
		clusters[cluster] = true
	}
	for _, id := range keep.NamedIn(text, learned...) {
		if len(ids) == maxNameIDs {
			break
		}
		cluster, service, _ := secretshape.IDNames(id)
		own := len(keep.NamedIn(service, id)) > 0 && strings.Contains(text, service)
		if clusters[cluster] && !own {
			continue
		}
		clusters[cluster] = true
		ids = append(ids, id)
	}
	return ids
}

// dropEchoes replaces the summary and the notes of a result that repeat request.
func dropEchoes(result *agentresult.Result, request, replacement string) {
	if agentresult.Echoes(result.Summary, request) {
		result.Summary, result.SummaryReplaced = replacement, true
	}
	for i := range result.Evidence {
		if agentresult.Echoes(result.Evidence[i].Note, request) {
			result.Evidence[i].Note = agentresult.ReplacementNote
		}
	}
}

// submit ends the run with the result of a submit_result call, or returns what
// is wrong with the call.
func (r *session) submit(call provider.ToolCall) (problem string) {
	sub, problem := parseSubmission(call)
	if problem != "" {
		return problem
	}
	result := buildResult(sub, r.found.units, &r.found.corpus, r.replacementSummary(), r.found.taskIDs, r.found.learned)
	dropEchoes(&result, r.request, r.replacementSummary())
	r.result = &result
	r.emit(Event{Type: "result", Result: &result})
	r.emit(Event{Type: "done"})
	return ""
}

// refuse answers a call of a turn whose submit_result was not accepted.
func (r *session) refuse(call provider.ToolCall, problem string) (provider.Message, *toolResult) {
	if call.Name == submitTool {
		return r.resultMessage(call, submitTool, problem, true)
	}
	name := "unknown_tool"
	if tool, known := r.lookupTool(call.Name); known {
		name = tool.Name
	}
	return r.resultMessage(call, name, "This tool was not run: submit_result ends the "+r.kind()+", so call it alone.", true)
}

// remind prepares the one reminder turn after a reply without a tool call, or
// ends the run when it has had it. The reminder travels with the next gathering
// call only and is not kept: it must not become the question that trimming and
// the final call hold on to in place of the task.
func (r *session) remind(resp provider.Response) (again bool) {
	if r.reminded {
		if resp.StopReason == "max_tokens" {
			r.stop("The reply was cut short by the output limit.")
		} else {
			r.fail("no_result", "The "+r.kind()+" ended without a result.")
		}
		return false
	}
	r.reminded = true
	if strings.TrimSpace(resp.Text) != "" && resp.StopReason != "max_tokens" {
		r.reminder = append(r.reminder, provider.Message{Role: provider.RoleAssistant, Text: resp.Text, Opaque: resp.Opaque})
	}
	r.reminder = append(r.reminder, r.loopMessage(reminderLine))
	return true
}

// logFindings writes the one line an investigator or reviewer run leaves in the
// agent log: the type of a review and counts, never content.
func (r *session) logFindings() {
	if r.l.Log == nil {
		return
	}
	message := "investigation finished"
	attrs := []any{"run_id", r.in.RunID, "units", len(r.found.units), "result", r.result != nil}
	if r.reviewing() {
		message = "review finished"
		attrs = append(attrs, "review_type", r.in.Task.Review.Type, "scope_entries", len(r.in.Task.Scope.Entries),
			"services_total", r.in.Task.Scope.ServicesTotal, "tool_calls", r.toolCalls)
	}
	if r.customReview() {
		attrs = append(attrs, "request_bytes", len(r.in.Task.Review.Instructions))
	}
	if r.result != nil {
		attrs = append(attrs, "evidence", len(r.result.Evidence), "evidence_dropped", r.result.EvidenceDropped, "summary_replaced", r.result.SummaryReplaced)
	}
	r.l.Log.Info(message, attrs...)
}
