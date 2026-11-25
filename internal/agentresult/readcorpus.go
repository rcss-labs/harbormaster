package agentresult

import (
	"encoding/json"
	"io"
	"strings"
)

// LooseRun is how many normalised characters a copied run of a loose value needs to be refused. VerbatimRun, the
// shorter one, holds for everything else.
const LooseRun = 80

// ReadCorpus indexes what a run read in two classes. Strict text (log lines, environment values, commands, and
// whatever no rule of looseRules names) may not be copied for VerbatimRun characters, loose text not for LooseRun. Both
// share one CorpusCap.
type ReadCorpus struct {
	strict Corpus // also counts the raw bytes of both classes
	loose  Corpus
}

// Add keeps the string values of a successful answer of tool. A value is loose only when the tool's rule names its
// member; in text that is not one JSON value of the expected shape nothing is.
func (c *ReadCorpus) Add(tool, text string) {
	c.strict.raw += len(text)
	if c.strict.raw > CorpusCap {
		c.strict.full = true // which fails every check
		c.strict.text, c.loose.text = nil, nil
		return
	}
	c.loose.run = LooseRun
	trimmed := strings.TrimSpace(text)
	if trimmed != "" && (trimmed[0] == '{' || trimmed[0] == '[') {
		if values, ok := classedStrings(trimmed, looseText[tool]); ok {
			for _, v := range values {
				if v.loose {
					c.loose.store(v.text)
				} else {
					c.strict.store(v.text)
				}
			}
			return
		}
	}
	c.strict.store(text)
}

// AllowNames is Corpus.AllowNames for both classes.
func (c *ReadCorpus) AllowNames(names ...string) {
	c.strict.AllowNames(names...)
	c.loose.AllowNames(names...)
}

// CheckReadSummary is CheckSummary against both classes of what the run read.
func CheckReadSummary(summary string, read *ReadCorpus) (clean string, replaced bool) {
	return CheckReadSummaryAs(summary, read, ReplacementSummary)
}

// CheckReadSummaryAs is CheckSummaryAs against both classes of what the run read.
func CheckReadSummaryAs(summary string, read *ReadCorpus, replacement string) (clean string, replaced bool) {
	if read == nil {
		return CheckSummaryAs(summary, nil, replacement)
	}
	if clean, replaced = CheckSummaryAs(summary, &read.strict, replacement); replaced {
		return clean, true
	}
	return CheckSummaryAs(summary, &read.loose, replacement)
}

// CheckReadNote is CheckNote against both classes of what the run read.
func CheckReadNote(note string, read *ReadCorpus) string {
	if read == nil {
		return CheckNote(note, nil)
	}
	return CheckNote(CheckNote(note, &read.strict), &read.loose)
}

type classedString struct {
	text  string
	loose bool
}

// classedStrings is jsonStringValues with the class of each value, by the rule of the JSON value in text.
func classedStrings(text string, rule *looseRule) (values []classedString, ok bool) {
	type container struct {
		rule           *looseRule
		object, expect bool // expect: the next token of an object is a key
	}
	dec := json.NewDecoder(strings.NewReader(text))
	var open []container
	next := rule // of the value that comes next
	valueDone := func() {
		next = nil // a second value at the top has no rule
		if n := len(open); n > 0 {
			open[n-1].expect = open[n-1].object
			next = open[n-1].rule.item() // in an object the key decides
		}
	}
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return values, len(open) == 0
		}
		if err != nil {
			return nil, false
		}
		switch v := tok.(type) {
		case json.Delim:
			switch v {
			case '{':
				open = append(open, container{rule: next, object: true, expect: true})
			case '[':
				open = append(open, container{rule: next})
				next = next.item()
			default:
				open = open[:len(open)-1]
				valueDone()
			}
		case string:
			if n := len(open); n > 0 && open[n-1].expect {
				open[n-1].expect = false
				next = open[n-1].rule.member(v)
				continue
			}
			values = append(values, classedString{text: v, loose: next.text()})
			valueDone()
		default:
			valueDone()
		}
	}
}
