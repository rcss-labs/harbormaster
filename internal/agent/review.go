package agent

import (
	"encoding/json"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/rcss-labs/harbormaster/internal/agent/provider"
	"github.com/rcss-labs/harbormaster/internal/agentresult"
	"github.com/rcss-labs/harbormaster/internal/hmtools"
	"github.com/rcss-labs/harbormaster/internal/secretshape"
)

// maxReviewToolCalls is how many tools a reviewer run may call. submit_result
// is not one of them.
const maxReviewToolCalls = 60

// reviewType is one kind of review. Its type decides what the first message
// asks for, and whether the frame explains an admin's request, and nothing else.
type reviewType struct{ id, label, instructions string }

var reviewTypes = []reviewType{
	{"cost", "Cost", costInstructions},
	{"security", "Security findings", securityInstructions},
	{"reliability", "Reliability", reliabilityInstructions},
	{"capacity", "Capacity", capacityInstructions},
	{typeCustom, "Custom", customLeadIn},
}

// typeCustom is the review whose subject and layout an admin describes.
const typeCustom = "custom"

const (
	costInstructions = `Read get_cost_analytics first. Pass it the range and the scope's filters, once for each entry of the scope.
Write the summary in three parts, in this order: spend, largest costs, savings.
- Spend: the total for the range and what makes it up.
- Largest costs: the clusters, accounts, tags or services that cost most.
- Savings: the largest savings the answer lists and which to take first. Say which are estimates.
Open a single service only to explain one of the largest costs or savings.
Cite the rows of single services that your findings rest on.`

	securityInstructions = `Read list_security first. Pass it the scope's filters, once for each entry of the scope. It takes no range: it shows the latest scans. Pass the range to the other tools that take it.
Write the summary in three parts, in this order: exposure, worst images, what to fix first.
- Exposure: how many images have critical or high findings, and how many were not scanned. If scanning is off, say that and nothing else.
- Worst images: the images with the most serious findings and the services that run them.
- What to fix first: the image or package whose fix removes the most, with the fixed version when one exists.
Its answer cannot be cited. Read get_service_security for the services that run the worst images.
Cite the rows of single services that your findings rest on.`

	reliabilityInstructions = `Read get_reliability_analytics first. Pass it the range and the scope's filters, once for each entry of the scope.
Write the summary in three parts, in this order: availability, services at risk, what to check.
- Availability: how available the services in scope were over the range.
- Services at risk: the least available services, those getting worse, and how their tasks stopped.
- What to check: for the worst few, the next thing a person should look at. You have no logs, so reason from stop reasons, exit codes and the timeline.
Open a single service only when it is among the worst few.
Cite the rows of single services that your findings rest on.`

	capacityInstructions = `Read get_capacity_analytics first. Pass it the range and the scope's filters, once for each entry of the scope. It shows the latest poll and recent history whatever the range is.
Write the summary in three parts, in this order: headroom, at their limit, over-reserved.
- Headroom: how much is reserved, and which subnets or clusters are close to full.
- At their limit: the services that run at their scaling maximum.
- Over-reserved: the services that reserve far more than they use, and what to lower first.
Open a single service only when it is among the worst few.
Cite the rows of single services that your findings rest on.`

	// customLeadIn stands before the admin's request.
	customLeadIn = `Read the totals that fit the request first: get_overview, get_cost_analytics, list_security, get_reliability_analytics, get_capacity_analytics, get_delivery_analytics or get_operations_analytics. Pass the range to the tools that take it, and the scope's filters, once for each entry of the scope.
Open single services only for the worst few.
Cite the rows of single services that your findings rest on.
Keep the summary within 1200 characters, whatever layout the request asks for. Write it and the notes in your own words: do not repeat the request.
The request stands between the admin_request tags.`
)

func reviewTypeOf(id string) (reviewType, bool) {
	for _, rt := range reviewTypes {
		if rt.id == id {
			return rt, true
		}
	}
	return reviewType{}, false
}

// reviewTools are the tools a reviewer is offered: the shared table without the
// tools that read logs. A tool that is not named here is not offered.
var reviewTools = map[string]bool{
	"whoami": true, "get_overview": true, "list_clusters": true, "get_cluster": true, "list_services": true,
	"get_service": true, "list_service_tasks": true, "list_tasks": true, "search": true,
	"get_task_definition": true, "list_revisions": true, "get_revision": true, "get_scaling": true, "get_target_health": true,
	"get_service_timeline": true, "list_service_operations": true, "get_service_metrics": true, "get_service_offhours": true,
	"get_desired_count": true, "get_arm64_readiness": true,
	"list_alerts": true, "get_timeline_task": true,
	"list_security": true, "get_service_security": true, "get_image_security": true,
	"get_cost_analytics": true, "get_capacity_analytics": true, "get_reliability_analytics": true, "get_delivery_analytics": true,
	"get_operations_analytics": true, "list_networks": true, "list_region_vpcs": true, "get_vpc_tree": true, "list_storage": true,
	"search_config": true,
}

// reviewing reports whether the run is a reviewer's.
func (r *session) reviewing() bool { return r.in.Role == roleReviewer }

// lookupTool finds a tool that this run's role has. What it does not find is
// neither offered nor run.
func (r *session) lookupTool(name string) (hmtools.Tool, bool) {
	tool, ok := lookupTool(name)
	if !ok || (r.reviewing() && !reviewTools[name]) {
		return hmtools.Tool{}, false
	}
	return tool, true
}

// countCall counts a tool call, the i-th of its turn from 0, and returns the
// sentence that answers it when it is over a limit, or "". The run's cap comes
// first: past it, asking for fewer calls at a time would not help.
func (r *session) countCall(i int) string {
	r.toolCalls++
	switch {
	case r.reviewing() && r.toolCalls > maxReviewToolCalls:
		return "No tool calls are left in this review. Call submit_result now."
	case i >= maxToolCallsPerTurn:
		return "Too many tool calls in one turn. Ask for fewer at a time."
	}
	return ""
}

// replacementSummary stands in for a summary of this run that failed a check.
func (r *session) replacementSummary() string {
	if r.reviewing() {
		return agentresult.ReplacementReviewSummary
	}
	return agentresult.ReplacementSummary
}

// taskFits reports whether task is one that role works on.
func taskFits(role string, task *Task) bool {
	switch role {
	case roleInvestigator:
		return task != nil && task.Review == nil && task.Scope == nil
	case roleReviewer:
		if task == nil || task.Review == nil || task.Scope == nil {
			return false
		}
		_, known := reviewTypeOf(task.Review.Type)
		return known && instructionsRefusal(*task.Review) == ""
	}
	return task == nil
}

// customReview reports whether the run is a review that follows an admin's request.
func (r *session) customReview() bool { return r.reviewing() && r.in.Task.Review.Type == typeCustom }

// instructionsRefusal says in a fixed sentence why a review cannot run with its
// instructions, or returns "". A secret shape is no reason: the text is scrubbed
// before it is sent.
func instructionsRefusal(review TaskReview) string {
	text := review.Instructions
	if review.Type != typeCustom {
		if text != "" {
			return "Only a custom review takes instructions."
		}
		return ""
	}
	switch agentresult.CheckInstructions(text) {
	case agentresult.InstructionsOK, agentresult.InstructionsSecret:
		if agentresult.CleanInstructions(text) != text {
			return "instructions must have only line feeds as line breaks and no white space at either end."
		}
		return ""
	case agentresult.InstructionsNotText:
		return instructionsNotText
	case agentresult.InstructionsRequired:
		return "A custom review needs instructions."
	case agentresult.InstructionsTooLong:
		return "instructions must hold at most 2000 characters and 8000 bytes."
	case agentresult.InstructionsHidden:
		return "instructions must hold no hidden characters, and only line feeds as line breaks."
	}
	return "instructions are not valid."
}

const instructionsNotText = "instructions must be valid UTF-8 without a NUL character."

// reviewMessage renders a review as its first message: a fixed template with
// the type's instructions, which are the loop's own words, then an admin's
// request, if the review has one, in an envelope of its own, then the scope, whose
// names people chose, as data.
func (r *session) reviewMessage(task Task) provider.Message {
	rt, _ := reviewTypeOf(task.Review.Type)
	text := "Write this review and submit it.\n\nReview: " + rt.label + "\nRange: " + task.Review.Range + "\nPeriod: " + task.Review.Period +
		"\n\nInstructions:\n" + rt.instructions
	if r.request != "" {
		tag := "admin_request_" + r.tagCode
		text += "\n\n<" + tag + ">\n" + r.request + "\n</" + tag + ">"
	}
	text += "\n\nServices in scope: " + strconv.Itoa(task.Scope.ServicesTotal) + "\n"
	if len(task.Scope.Entries) == 0 {
		return provider.Message{Role: provider.RoleUser, Text: text + "The scope is everything you can see."}
	}
	lines := make([]string, len(task.Scope.Entries))
	for i, entry := range task.Scope.Entries {
		lines[i] = scopeLine(entry)
	}
	scope := r.newResult("scope", strings.Join(lines, "\n"))
	return provider.Message{Role: provider.RoleUser, Text: text + "The scope, one entry on each line:\n" + scope.render(len(scope.data))}
}

// scopeLine is one entry of a scope as JSON on one line, with each text scrubbed
// on its own first, so that no name can reach into what stands beside it.
func scopeLine(entry ScopeEntry) string {
	line, _ := json.Marshal(struct { // escapes every angle bracket
		Kind      string `json:"kind"`
		TagID     string `json:"tag_id,omitempty"`
		ClusterID string `json:"cluster_id,omitempty"`
		AccountID string `json:"account_id,omitempty"`
		Region    string `json:"region,omitempty"`
		Name      string `json:"name,omitempty"`
	}{
		alertLine(entry.Kind), alertLine(entry.TagID), alertLine(entry.ClusterID),
		alertLine(entry.AccountID), alertLine(entry.Region), alertLine(entry.Name),
	})
	return string(line)
}

// viewOnly reports whether every unit is one that View alone shows.
func viewOnly(units []numberedUnit) bool {
	return !slices.ContainsFunc(units, func(u numberedUnit) bool { return u.action != hmtools.EvidenceView })
}

// idNames returns the names of the clusters and services whose ids are string
// values of the JSON in text. Text that is cut short counts up to the cut.
func idNames(text string) (names []string) {
	text = strings.TrimSpace(text)
	if text == "" || (text[0] != '{' && text[0] != '[') {
		return nil
	}
	decoder := json.NewDecoder(strings.NewReader(text))
	var open []bool // the open containers: true for an object
	key := false    // the next string is a key
	for {
		token, err := decoder.Token()
		if err != nil {
			return names
		}
		inObject := func() bool { return len(open) > 0 && open[len(open)-1] }
		switch value := token.(type) {
		case json.Delim:
			if value == '{' || value == '[' {
				open = append(open, value == '{')
				key = value == '{'
				continue
			}
			open = open[:len(open)-1]
		case string:
			if key {
				key = false
				continue
			}
			cluster, service, _ := secretshape.IDNames(value)
			for _, name := range []string{cluster, service} {
				if name != "" {
					names = append(names, name)
				}
			}
		}
		key = inObject()
	}
}

const (
	maxScopeEntries   = 10
	maxScopeServices  = 100_000
	maxScopeTextBytes = 200
	maxScopeNameBytes = 255 // an ECS cluster name may be this long
	// maxClusterIDBytes: an id grows with its cluster's name, beyond the limit for texts.
	maxClusterIDBytes = 512
)

var reviewRanges = []string{"24h", "7d", "30d"}

// reviewPeriod is what a period may hold. It stands outside the data envelope,
// so it has no line break, bracket, slash or quote.
var reviewPeriod = regexp.MustCompile(`^[A-Za-z0-9 ,.:()-]{1,80}$`)

// validateReview checks what a reviewer run needs in place of messages.
func validateReview(req *runRequest) string {
	if len(req.Messages) != 0 || req.Page.Path != "" || req.Page.ServiceID != "" || req.Page.ClusterID != "" {
		return "This kind of run takes a task, and no messages or page."
	}
	if req.Task == nil || req.Task.Kind != "review" {
		return "task must be of kind review."
	}
	subject := req.Task.Subject
	if subject.Kind != "" || len(subject.ServiceIDs) != 0 || subject.ClusterID != "" || subject.AccountID != "" || subject.Region != "" ||
		req.Task.Alert != (TaskAlert{}) {
		return "A review takes no subject or alert."
	}
	review, scope := req.Task.Review, req.Task.Scope
	if review == nil || scope == nil {
		return "A review needs review and scope."
	}
	if _, known := reviewTypeOf(review.Type); !known {
		return "This type of review is not supported."
	}
	if review.Type == typeCustom && !req.validUTF8 {
		return instructionsNotText // decoding has replaced the broken bytes
	}
	if message := instructionsRefusal(*review); message != "" {
		return message
	}
	if !slices.Contains(reviewRanges, review.Range) {
		return "range must be 24h, 7d or 30d."
	}
	if !reviewPeriod.MatchString(review.Period) {
		return "period must hold between 1 and 80 letters, digits, spaces and simple punctuation."
	}
	if scope.ServicesTotal < 0 || scope.ServicesTotal > maxScopeServices {
		return "services_total must be between 0 and 100000."
	}
	if len(scope.Entries) > maxScopeEntries {
		return "The scope must hold at most 10 entries."
	}
	for _, entry := range scope.Entries {
		if message := validateScopeEntry(entry); message != "" {
			return message
		}
	}
	return ""
}

func validateScopeEntry(entry ScopeEntry) string {
	var id string
	others := entry // without the ids of its own kind
	switch entry.Kind {
	case "tag":
		id, others.TagID = entry.TagID, ""
	case "cluster":
		id, others.ClusterID = entry.ClusterID, ""
	case "account":
		id, others.AccountID, others.Region = entry.AccountID, "", ""
	default:
		return "Each scope entry must be of kind tag, cluster or account."
	}
	if strings.TrimSpace(id) == "" {
		return "Each scope entry needs the id of its kind."
	}
	if others.TagID != "" || others.ClusterID != "" || others.AccountID != "" || others.Region != "" {
		return "A scope entry takes only the ids of its kind."
	}
	if len(entry.ClusterID) > maxClusterIDBytes {
		return "Each cluster id of the scope must be at most 512 bytes."
	}
	for _, text := range []string{entry.TagID, entry.AccountID, entry.Region} {
		if len(text) > maxScopeTextBytes {
			return "Each text of the scope must be at most 200 bytes."
		}
	}
	if len(entry.Name) > maxScopeNameBytes {
		return "Each name of the scope must be at most 255 bytes."
	}
	return ""
}
