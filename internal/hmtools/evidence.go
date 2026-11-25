package hmtools

import (
	"bytes"
	"encoding/json"
	"sort"
)

// EvidenceKind says how a tool's answer is attributed to services.
type EvidenceKind int

// The zero value is EvidenceNone, so a tool or rule nobody filled in is not citable.
const (
	// EvidenceNone: the answer is not citable (no single service and action can label it).
	EvidenceNone EvidenceKind = iota
	// EvidenceServiceArg: the whole answer belongs to the service named by the call's service_id argument.
	EvidenceServiceArg
	// EvidencePerItem: the answer lists many services; each element of one array names its own.
	EvidencePerItem
)

// Evidence actions: the permission a reader needs on the service's tag.
const (
	EvidenceView = "view"
	EvidenceLogs = "logs"
)

const (
	// maxEvidenceBody is the largest answer that is read for evidence; a larger one gives no units.
	maxEvidenceBody = 2 << 20
	// maxEvidenceUnits is the most units one call yields; the rest of the answer is not citable.
	maxEvidenceUnits = 2000
	// maxEvidenceIDBytes is the longest service id accepted from an argument or an element.
	maxEvidenceIDBytes = 512
)

// EvidenceRule is the fixed label rule of one tool.
type EvidenceRule struct {
	Kind EvidenceKind
	// Action is "view" or "logs"; empty for EvidenceNone.
	Action string
	// Items is, for EvidencePerItem, the top-level array whose elements become units.
	Items string
	// IDField is, for EvidencePerItem, the member of each element that holds the service id.
	IDField string
}

// evidenceRules has an explicit entry for every tool, EvidenceNone included. There is no default: a tool that is
// missing here is unknown and gives no evidence (and TestEveryToolHasAnExplicitEvidenceRule fails for it).
var evidenceRules = map[string]EvidenceRule{
	// Whole answer is about the service in the call; reading it needs View.
	"get_service":             {Kind: EvidenceServiceArg, Action: EvidenceView},
	"list_service_tasks":      {Kind: EvidenceServiceArg, Action: EvidenceView},
	"get_task_definition":     {Kind: EvidenceServiceArg, Action: EvidenceView},
	"list_revisions":          {Kind: EvidenceServiceArg, Action: EvidenceView},
	"get_revision":            {Kind: EvidenceServiceArg, Action: EvidenceView},
	"get_scaling":             {Kind: EvidenceServiceArg, Action: EvidenceView},
	"get_target_health":       {Kind: EvidenceServiceArg, Action: EvidenceView},
	"get_service_timeline":    {Kind: EvidenceServiceArg, Action: EvidenceView},
	"list_service_operations": {Kind: EvidenceServiceArg, Action: EvidenceView},
	"get_service_metrics":     {Kind: EvidenceServiceArg, Action: EvidenceView},
	"get_service_offhours":    {Kind: EvidenceServiceArg, Action: EvidenceView},
	"get_desired_count":       {Kind: EvidenceServiceArg, Action: EvidenceView},
	"get_arm64_readiness":     {Kind: EvidenceServiceArg, Action: EvidenceView},
	"get_timeline_task":       {Kind: EvidenceServiceArg, Action: EvidenceView},
	"get_service_security":    {Kind: EvidenceServiceArg, Action: EvidenceView},
	// Log text: reading it needs Logs.
	"get_service_logs": {Kind: EvidenceServiceArg, Action: EvidenceLogs},

	// Many services in one answer: each element is labelled by its own id member.
	"list_services": {Kind: EvidencePerItem, Action: EvidenceView, Items: "items", IDField: "id"},
	"list_alerts":   {Kind: EvidencePerItem, Action: EvidenceView, Items: "items", IDField: "service_id"},
	"search_config": {Kind: EvidencePerItem, Action: EvidenceView, Items: "items", IDField: "id"},
	"search_logs":   {Kind: EvidencePerItem, Action: EvidenceLogs, Items: "rows", IDField: "service_id"},

	// No single service and action can label these answers; the model may read them but cannot cite them.
	"whoami":                    {Kind: EvidenceNone},
	"get_overview":              {Kind: EvidenceNone},
	"list_clusters":             {Kind: EvidenceNone},
	"get_cluster":               {Kind: EvidenceNone},
	"list_tasks":                {Kind: EvidenceNone},
	"search":                    {Kind: EvidenceNone},
	"list_security":             {Kind: EvidenceNone},
	"get_image_security":        {Kind: EvidenceNone},
	"list_log_search_services":  {Kind: EvidenceNone},
	"estimate_log_search":       {Kind: EvidenceNone},
	"get_cost_analytics":        {Kind: EvidenceNone},
	"get_capacity_analytics":    {Kind: EvidenceNone},
	"get_reliability_analytics": {Kind: EvidenceNone},
	"get_delivery_analytics":    {Kind: EvidenceNone},
	"get_operations_analytics":  {Kind: EvidenceNone},
	"list_networks":             {Kind: EvidenceNone},
	"list_region_vpcs":          {Kind: EvidenceNone},
	"get_vpc_tree":              {Kind: EvidenceNone},
	"list_storage":              {Kind: EvidenceNone},
}

// EvidenceRuleFor returns the tool's rule; false for a tool with no entry.
func EvidenceRuleFor(tool string) (EvidenceRule, bool) {
	rule, ok := evidenceRules[tool]
	return rule, ok
}

// EvidenceAction is the action ("view" or "logs") Harbormaster uses to derive a label again from the tool name.
// It is false for EvidenceNone tools and for unknown tools.
func EvidenceAction(tool string) (string, bool) {
	rule, ok := evidenceRules[tool]
	if !ok || rule.Kind == EvidenceNone || (rule.Action != EvidenceView && rule.Action != EvidenceLogs) {
		return "", false
	}
	return rule.Action, true
}

// Unit is one citable piece of a tool answer, labelled with the one service and action that govern it.
type Unit struct {
	ServiceID string
	Action    string
	Text      string
}

// Units splits a tool answer into citable units. It is pure and never panics. Anything it cannot attribute to
// exactly one service is dropped: an unknown tool, a rule of kind none, an invalid or oversized body, a bad
// service_id argument, or an element without a usable id. The service id of a EvidenceServiceArg unit is the
// call's own argument, never a value found in the body.
func Units(tool string, args map[string]any, body string) []Unit {
	rule, ok := evidenceRules[tool]
	if !ok || len(body) > maxEvidenceBody || !json.Valid([]byte(body)) {
		return nil
	}
	if rule.Action != EvidenceView && rule.Action != EvidenceLogs {
		return nil
	}
	switch rule.Kind {
	case EvidenceServiceArg:
		serviceID, isString := args["service_id"].(string)
		if !isString || serviceID == "" || len(serviceID) > maxEvidenceIDBytes {
			return nil
		}
		return serviceArgUnits(serviceID, rule.Action, []byte(body))
	case EvidencePerItem:
		return perItemUnits(rule, []byte(body))
	}
	return nil
}

// serviceArgUnits makes one unit per top-level member of an object (one per element when the member is an array),
// one per element of a top-level array, or one for a scalar body.
func serviceArgUnits(serviceID, action string, body []byte) []Unit {
	var units []Unit
	add := func(prefix string, raw json.RawMessage) bool {
		units = append(units, Unit{ServiceID: serviceID, Action: action, Text: prefix + compactJSON(raw)})
		return len(units) < maxEvidenceUnits
	}
	switch firstByte(body) {
	case '{':
		var members map[string]json.RawMessage
		if json.Unmarshal(body, &members) != nil {
			return nil
		}
		names := make([]string, 0, len(members))
		for name := range members {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if firstByte(members[name]) == '[' {
				var elements []json.RawMessage
				if json.Unmarshal(members[name], &elements) != nil {
					continue
				}
				for _, element := range elements {
					if !add("", element) {
						return units
					}
				}
				continue
			}
			if !add(name+": ", members[name]) {
				return units
			}
		}
	case '[':
		var elements []json.RawMessage
		if json.Unmarshal(body, &elements) != nil {
			return nil
		}
		for _, element := range elements {
			if !add("", element) {
				return units
			}
		}
	default:
		add("", body)
	}
	return units
}

// perItemUnits turns the elements of the rule's array into units, each attributed to the id in its own member.
// An element without a non-empty string id of at most 512 bytes is dropped, and so is everything outside the array.
func perItemUnits(rule EvidenceRule, body []byte) []Unit {
	if firstByte(body) != '{' {
		return nil
	}
	var top map[string]json.RawMessage
	if json.Unmarshal(body, &top) != nil {
		return nil
	}
	rawItems, present := top[rule.Items]
	if !present || firstByte(rawItems) != '[' {
		return nil
	}
	var elements []json.RawMessage
	if json.Unmarshal(rawItems, &elements) != nil {
		return nil
	}
	var units []Unit
	for _, element := range elements {
		if firstByte(element) != '{' {
			continue
		}
		var members map[string]json.RawMessage
		if json.Unmarshal(element, &members) != nil {
			continue
		}
		var serviceID string
		if json.Unmarshal(members[rule.IDField], &serviceID) != nil || serviceID == "" || len(serviceID) > maxEvidenceIDBytes {
			continue
		}
		units = append(units, Unit{ServiceID: serviceID, Action: rule.Action, Text: compactJSON(element)})
		if len(units) >= maxEvidenceUnits {
			break
		}
	}
	return units
}

// firstByte is the first byte of raw that is not JSON whitespace, or 0.
func firstByte(raw []byte) byte {
	for _, b := range raw {
		switch b {
		case ' ', '\t', '\r', '\n':
			continue
		}
		return b
	}
	return 0
}

// compactJSON removes insignificant whitespace from a valid JSON value.
func compactJSON(raw []byte) string {
	var out bytes.Buffer
	if json.Compact(&out, raw) != nil {
		return string(raw)
	}
	return out.String()
}
