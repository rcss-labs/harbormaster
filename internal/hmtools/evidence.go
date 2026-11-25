package hmtools

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
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
	// EvidencePerItemNested: the answer lists many services in arrays nested in objects; each element of those
	// arrays names its own.
	EvidencePerItemNested
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
	// Arrays is, for EvidencePerItemNested, the arrays whose elements become units. Items and IDField are not used.
	Arrays []ItemArray
}

// ItemArray locates one array of a nested per-item rule.
type ItemArray struct {
	// Path is the object members to follow from the top of the answer to the array.
	Path []string
	// IDField is the member of each element that holds the service id.
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

	// Many services in rows nested in the answer: each row of the named arrays is labelled by its own id member.
	// The totals by cluster, account and tag, the series and the KPIs of these answers are not citable.
	"get_cost_analytics": {Kind: EvidencePerItemNested, Action: EvidenceView, Arrays: []ItemArray{
		{Path: []string{"rightsizing", "services"}, IDField: "service_id"},
		{Path: []string{"spot", "services"}, IDField: "service_id"},
		{Path: []string{"arm64", "services"}, IDField: "service_id"},
	}},
	"get_reliability_analytics": {Kind: EvidencePerItemNested, Action: EvidenceView, Arrays: []ItemArray{
		{Path: []string{"least_available", "rows"}, IDField: "service_id"},
		{Path: []string{"getting_worse", "data", "rows"}, IDField: "service_id"},
	}},
	"get_capacity_analytics": {Kind: EvidencePerItemNested, Action: EvidenceView, Arrays: []ItemArray{
		{Path: []string{"at_scaling_max"}, IDField: "service_id"},
		{Path: []string{"over_reserved", "data", "rows"}, IDField: "service_id"},
	}},

	// No single service and action can label these answers; the model may read them but cannot cite them.
	"whoami":                   {Kind: EvidenceNone},
	"get_overview":             {Kind: EvidenceNone},
	"list_clusters":            {Kind: EvidenceNone},
	"get_cluster":              {Kind: EvidenceNone},
	"list_tasks":               {Kind: EvidenceNone},
	"search":                   {Kind: EvidenceNone},
	"list_security":            {Kind: EvidenceNone},
	"get_image_security":       {Kind: EvidenceNone},
	"list_log_search_services": {Kind: EvidenceNone},
	"estimate_log_search":      {Kind: EvidenceNone},
	"get_delivery_analytics":   {Kind: EvidenceNone},
	"get_operations_analytics": {Kind: EvidenceNone},
	"list_networks":            {Kind: EvidenceNone},
	"list_region_vpcs":         {Kind: EvidenceNone},
	"get_vpc_tree":             {Kind: EvidenceNone},
	"list_storage":             {Kind: EvidenceNone},
}

// EvidenceRuleFor returns the tool's rule; false for a tool with no entry.
func EvidenceRuleFor(tool string) (EvidenceRule, bool) {
	rule, ok := evidenceRules[tool]
	return rule, ok
}

// EvidenceAction is the action ("view" or "logs") Harbormaster uses to derive a label again from the tool name.
// It is false for EvidenceNone tools, unknown tools, an unknown kind and an action other than view or logs.
func EvidenceAction(tool string) (string, bool) {
	rule, ok := evidenceRules[tool]
	if !ok || !citable(rule) {
		return "", false
	}
	return rule.Action, true
}

// citable is true for a rule of a known, citable kind with a known action; every other rule gives no evidence.
func citable(rule EvidenceRule) bool {
	if rule.Kind != EvidenceServiceArg && rule.Kind != EvidencePerItem && rule.Kind != EvidencePerItemNested {
		return false
	}
	return rule.Action == EvidenceView || rule.Action == EvidenceLogs
}

// Unit is one citable piece of a tool answer, labelled with the one service and action that govern it.
// ServiceID is the caller's string, not a parsed id: for EvidenceServiceArg it is the call's own argument, for
// the per-item kinds the element's id member exactly as it appeared in the (possibly scrubbed) body. It is only known
// to be non-empty, at most 512 bytes, free of control characters, edge spaces and "[" (the scrubber's marker); the
// caller must still parse it (model.ParseServiceID) and check what the identity may read.
type Unit struct {
	ServiceID string
	Action    string
	Text      string
}

// Units splits a tool answer into citable units. It is pure and never panics. Anything it cannot attribute to
// exactly one service is dropped: an unknown tool, a rule of kind none, an invalid or oversized body, a bad
// service_id argument, a duplicate key where it would make the attribution ambiguous, or an element without a
// usable id. The service id of a EvidenceServiceArg unit is the call's own argument, never a value found in the
// body.
func Units(tool string, args map[string]any, body string) []Unit {
	rule, ok := evidenceRules[tool]
	if !ok || !citable(rule) || len(body) > maxEvidenceBody || !json.Valid([]byte(body)) {
		return nil
	}
	switch rule.Kind {
	case EvidenceServiceArg:
		serviceID, isString := args["service_id"].(string)
		if !isString || !usableServiceID(serviceID) {
			return nil
		}
		return serviceArgUnits(serviceID, rule.Action, []byte(body))
	case EvidencePerItem:
		return perItemUnits(rule, []byte(body))
	case EvidencePerItemNested:
		return nestedUnits(rule.Action, rule.Arrays, []byte(body))
	}
	return nil
}

// usableServiceID accepts a non-empty id of at most 512 bytes without control characters, leading or trailing
// space, or "[" (which the secret scrubber uses for its markers, so such an id was not the original).
func usableServiceID(id string) bool {
	if id == "" || len(id) > maxEvidenceIDBytes || strings.ContainsRune(id, '[') || strings.TrimSpace(id) != id {
		return false
	}
	for i := 0; i < len(id); i++ {
		if id[i] < 0x20 || id[i] == 0x7f {
			return false
		}
	}
	return true
}

// member is one member of a JSON object, in document order.
type member struct {
	name  string
	value json.RawMessage
}

// objectMembers reads a valid JSON object into its members in document order. It reports a repeated name, which
// different readers resolve differently, so the caller decides what to do with it.
func objectMembers(raw []byte) (members []member, duplicate, ok bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return nil, false, false
	}
	seen := map[string]struct{}{}
	for decoder.More() {
		token, err := decoder.Token()
		name, isName := token.(string)
		if err != nil || !isName {
			return nil, false, false
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return nil, false, false
		}
		if _, repeated := seen[name]; repeated {
			duplicate = true
		}
		seen[name] = struct{}{}
		members = append(members, member{name: name, value: value})
	}
	return members, duplicate, true
}

// arrayElements reads a valid JSON array into its elements.
func arrayElements(raw []byte) ([]json.RawMessage, bool) {
	var elements []json.RawMessage
	if json.Unmarshal(raw, &elements) != nil {
		return nil, false
	}
	return elements, true
}

// serviceArgUnits makes one unit per top-level member of an object (one per element, each prefixed with the member
// name, when the member is an array), one per element of a top-level array, or one for a scalar body. A repeated
// top-level member name gives no units.
func serviceArgUnits(serviceID, action string, body []byte) []Unit {
	var units []Unit
	add := func(prefix string, raw []byte) bool {
		units = append(units, Unit{ServiceID: serviceID, Action: action, Text: prefix + compactJSON(raw)})
		return len(units) < maxEvidenceUnits
	}
	switch firstByte(body) {
	case '{':
		members, duplicate, ok := objectMembers(body)
		if !ok || duplicate {
			return nil
		}
		for _, m := range members {
			if firstByte(m.value) != '[' {
				if !add(m.name+": ", m.value) {
					return units
				}
				continue
			}
			elements, ok := arrayElements(m.value)
			if !ok {
				return nil
			}
			for _, element := range elements {
				if !add(m.name+": ", element) {
					return units
				}
			}
		}
	case '[':
		elements, ok := arrayElements(body)
		if !ok {
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
// An element without a usable string id, or with a repeated member name, is dropped, and so is everything outside
// the array. A repeated top-level member name gives no units.
func perItemUnits(rule EvidenceRule, body []byte) []Unit {
	top, duplicate, ok := objectMembers(body)
	if !ok || duplicate {
		return nil
	}
	var elements []json.RawMessage
	for _, m := range top {
		if m.name != rule.Items {
			continue
		}
		if firstByte(m.value) != '[' {
			return nil
		}
		if elements, ok = arrayElements(m.value); !ok {
			return nil
		}
	}
	var units []Unit
	for _, element := range elements {
		if unit, ok := elementUnit(element, rule.IDField, rule.Action); ok {
			units = append(units, unit)
		}
		if len(units) >= maxEvidenceUnits {
			break
		}
	}
	return units
}

// elementUnit makes the unit of one array element: an object with exactly one usable string id member. Anything
// else, a repeated member name included, gives no unit.
func elementUnit(element json.RawMessage, idField, action string) (Unit, bool) {
	if firstByte(element) != '{' {
		return Unit{}, false
	}
	members, duplicate, ok := objectMembers(element)
	if !ok || duplicate {
		return Unit{}, false
	}
	for _, m := range members {
		if m.name != idField {
			continue
		}
		var serviceID string
		if json.Unmarshal(m.value, &serviceID) != nil || !usableServiceID(serviceID) {
			return Unit{}, false
		}
		return Unit{ServiceID: serviceID, Action: action, Text: compactJSON(element)}, true
	}
	return Unit{}, false
}

// nestedUnits turns the elements of the named arrays into units, each attributed to the id in its own member, in
// document order and at most maxEvidenceUnits across all arrays. A step of a path that is missing, null or not an
// object, or an end that is not an array, gives nothing for that array and is no error. An object with a repeated
// member name on the way to an array gives nothing below it, and neither does a body that is not an object.
func nestedUnits(action string, arrays []ItemArray, body []byte) []Unit {
	var units []Unit
	var visit func(object []byte, path []string) bool
	visit = func(object []byte, path []string) (more bool) {
		members, duplicate, ok := objectMembers(object)
		if !ok || duplicate {
			return true
		}
		for _, m := range members {
			here := append(path[:len(path):len(path)], m.name)
			var onTheWay bool
			for _, array := range arrays {
				switch {
				case slices.Equal(array.Path, here):
					elements, ok := arrayElements(m.value)
					if !ok {
						continue
					}
					for _, element := range elements {
						if unit, ok := elementUnit(element, array.IDField, action); ok {
							units = append(units, unit)
						}
						if len(units) >= maxEvidenceUnits {
							return false
						}
					}
				case len(array.Path) > len(here) && slices.Equal(array.Path[:len(here)], here):
					onTheWay = true
				}
			}
			if onTheWay && firstByte(m.value) == '{' && !visit(m.value, here) {
				return false
			}
		}
		return true
	}
	if firstByte(body) == '{' {
		visit(body, nil)
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
