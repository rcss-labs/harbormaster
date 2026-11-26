package secretshape

import "maps"

// maxNames bounds a set; beyond it no more names are added.
const maxNames = 10_000

// Names holds cluster and service names that a text may show although they look random, as names given by CDK and
// CloudFormation do. Names get in only by decoding Harbormaster's own ids, never as strings. The zero value holds no
// names. A copy does not change when the original grows.
type Names struct{ set map[string]struct{} }

// NamesOf returns the names the ids decode to, under the conditions of Add.
func NamesOf(ids ...string) Names {
	var n Names
	n.Add(ids...)
	return n
}

// Add adds the cluster and service names the ids decode to. What is no well-formed id adds nothing, and neither does a
// name that the long-run rule would not hide or that must stay hidden. At most 10,000 names are kept.
func (n *Names) Add(ids ...string) {
	set, own := n.set, false
	for _, id := range ids {
		for _, name := range keptNames(id) {
			if _, known := set[name]; known {
				continue
			}
			if len(set) >= maxNames {
				break
			}
			if !own {
				if set, own = maps.Clone(set), true; set == nil {
					set = make(map[string]struct{})
				}
			}
			set[name] = struct{}{}
		}
	}
	n.set = set
}

// Empty reports whether no name is kept.
func (n Names) Empty() bool { return len(n.set) == 0 }

// NamedIn returns those of the ids, each once and in the order given, of which a kept name is a whole run of text. It
// reads the text as it stands, so it misses a name that only a decoded JSON string shows whole, as one right after an
// escape, and it also reports a name that another rule hides.
func (n Names) NamedIn(text string, ids ...string) []string {
	if n.Empty() || len(ids) == 0 {
		return nil
	}
	named := map[string]bool{}
	for _, run := range longRun.FindAllString(text, -1) {
		if n.holds(run) {
			named[run] = true
		}
	}
	if len(named) == 0 {
		return nil
	}
	var found []string
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		for _, name := range keptNames(id) {
			if named[name] {
				found = append(found, id)
				break
			}
		}
	}
	return found
}

// holds reports whether run, byte for byte, is a kept name.
func (n Names) holds(run string) bool {
	_, ok := n.set[run]
	return ok
}

// keptNames returns the names id decodes to that may be kept.
func keptNames(id string) []string {
	cluster, service, ok := IDNames(id)
	if !ok {
		return nil
	}
	var names []string
	for _, name := range []string{cluster, service} {
		if mayBeKept(name) {
			names = append(names, name)
		}
	}
	return names
}

// minRun is the shortest text longRun matches.
const minRun = 32

// mayBeKept reports whether the scrubber takes name for a random token and nothing else in it looks like a secret: a
// hex key or digest and a name with a value pattern stay hidden.
func mayBeKept(name string) bool {
	return len(name) >= minRun && looksLikeRandomRun(name) && !hexOnly.MatchString(name) && len(patternSpans(name)) == 0
}
