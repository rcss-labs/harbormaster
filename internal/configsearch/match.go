package configsearch

import (
	"sort"
	"strconv"
	"strings"
)

const maxHits = 20

// Container is the name-only view of one container definition.
type Container struct {
	Name, Image, Tag, BaseImage, LogGroup string
	Ports                                 []int
	EnvNames, SecretNames                 []string
}

// Doc is everything searchable about one task definition revision.
type Doc struct {
	TaskRoleARN, ExecutionRoleARN string
	Containers                    []Container
}

// Hit names one value that satisfied a positive term. Container is empty for the two roles.
type Hit struct {
	Field            Field
	Container, Value string
}

// hitFieldOrder is the order hits are listed in within one container.
var hitFieldOrder = map[Field]int{
	FieldImage: 0, FieldTag: 1, FieldBase: 2, FieldEnv: 3, FieldSecret: 4, FieldPort: 5,
	FieldTaskRole: 6, FieldExecRole: 7, FieldLogGroup: 8,
}

type candidate struct {
	hit        Hit
	containerN int // index in Doc.Containers; len(Containers) for roles
	port       int // set for FieldPort candidates
}

// candidates lists every searchable value in the doc, in container order.
func candidates(d Doc) []candidate {
	var out []candidate
	for i, c := range d.Containers {
		add := func(f Field, v string) {
			if v != "" {
				out = append(out, candidate{hit: Hit{Field: f, Container: c.Name, Value: v}, containerN: i})
			}
		}
		add(FieldImage, c.Image)
		add(FieldTag, c.Tag)
		add(FieldBase, c.BaseImage)
		for _, n := range c.EnvNames {
			add(FieldEnv, n)
		}
		for _, n := range c.SecretNames {
			add(FieldSecret, n)
		}
		for _, p := range c.Ports {
			out = append(out, candidate{hit: Hit{Field: FieldPort, Container: c.Name, Value: strconv.Itoa(p)}, containerN: i, port: p})
		}
		add(FieldLogGroup, c.LogGroup)
	}
	roleN := len(d.Containers)
	if d.TaskRoleARN != "" {
		out = append(out, candidate{hit: Hit{Field: FieldTaskRole, Value: d.TaskRoleARN}, containerN: roleN})
	}
	if d.ExecutionRoleARN != "" {
		out = append(out, candidate{hit: Hit{Field: FieldExecRole, Value: d.ExecutionRoleARN}, containerN: roleN})
	}
	return out
}

// fieldApplies reports whether a term on termField looks at a candidate of candField.
func fieldApplies(termField, candField Field) bool {
	switch termField {
	case FieldAny:
		return candField != FieldTag // the tag is already part of the image string
	case FieldRole:
		return candField == FieldTaskRole || candField == FieldExecRole
	default:
		return termField == candField
	}
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

func termMatches(t Term, c candidate) bool {
	if !fieldApplies(t.Field, c.hit.Field) {
		return false
	}
	if c.hit.Field == FieldPort {
		// Ports are exact for both the port: field and an all-digit free-text term.
		if t.Field == FieldAny && !isDigits(t.Value) {
			return false
		}
		return c.hit.Value == t.Value
	}
	have, want := strings.ToLower(c.hit.Value), strings.ToLower(t.Value)
	if t.Exact {
		return have == want
	}
	return strings.Contains(have, want)
}

// Match reports whether every term holds for the doc and which values satisfied the positive terms. Terms are
// ANDed at service level, not per container. A negated term holds when nothing in the doc matches it. An empty
// query matches nothing.
func Match(q Query, d Doc) (hits []Hit, ok bool) {
	if q.Empty() {
		return nil, false
	}
	cands := candidates(d)
	matched := make(map[int]bool) // indexes into cands that satisfied a positive term
	for _, t := range q.Terms {
		any := false
		for i, c := range cands {
			if termMatches(t, c) {
				any = true
				if !t.Negate {
					matched[i] = true
				}
			}
		}
		if any == t.Negate {
			return nil, false
		}
	}
	idx := make([]int, 0, len(matched))
	for i := range matched {
		idx = append(idx, i)
	}
	sort.Slice(idx, func(a, b int) bool {
		ca, cb := cands[idx[a]], cands[idx[b]]
		if ca.containerN != cb.containerN {
			return ca.containerN < cb.containerN
		}
		if fa, fb := hitFieldOrder[ca.hit.Field], hitFieldOrder[cb.hit.Field]; fa != fb {
			return fa < fb
		}
		return idx[a] < idx[b]
	})
	seen := make(map[Hit]bool)
	for _, i := range idx {
		h := cands[i].hit
		if seen[h] {
			continue
		}
		seen[h] = true
		hits = append(hits, h)
		if len(hits) == maxHits {
			break
		}
	}
	return hits, true
}
