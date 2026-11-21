// Package configsearch parses config search queries and matches them against the names projected out of a
// service's task definition. It never sees environment values or secret references.
package configsearch

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// Limits on a query. They are constants rather than settings so every caller reports the same errors.
const (
	MaxQueryChars    = 500
	MaxTerms         = 10
	MinFreeTextChars = 2
)

// Field names the part of a task definition a term looks at. The empty field means any field.
type Field string

const (
	FieldAny      Field = ""
	FieldImage    Field = "image"
	FieldTag      Field = "tag"
	FieldBase     Field = "base"
	FieldEnv      Field = "env"
	FieldSecret   Field = "secret"
	FieldPort     Field = "port"
	FieldRole     Field = "role"
	FieldTaskRole Field = "taskrole"
	FieldExecRole Field = "execrole"
	FieldLogGroup Field = "loggroup"
)

// fieldAliases maps every accepted prefix (lower case) to its field.
var fieldAliases = map[string]Field{
	"image": FieldImage, "tag": FieldTag, "base": FieldBase, "env": FieldEnv, "secret": FieldSecret,
	"port": FieldPort, "role": FieldRole, "taskrole": FieldTaskRole, "execrole": FieldExecRole,
	"loggroup": FieldLogGroup,
	"log":      FieldLogGroup, "log-group": FieldLogGroup,
	"task-role": FieldTaskRole,
	"exec-role": FieldExecRole, "executionrole": FieldExecRole,
	"secrets": FieldSecret,
	"ports":   FieldPort,
}

// Term is one search condition. Exact means the value was quoted and must match a whole value.
type Term struct {
	Field         Field
	Value         string
	Exact, Negate bool
}

// Query is a list of terms that must all hold for the same service.
type Query struct{ Terms []Term }

// ParseError carries a user-facing message; the API returns it as a 422.
type ParseError struct{ Message string }

func (e *ParseError) Error() string { return e.Message }

func parseErr(msg string) error { return &ParseError{Message: msg} }

// Parse splits text into terms. A term is "field:value" only when the text before its first colon is a known
// field or alias; anything else is free text, because image references such as node:18 contain colons.
func Parse(text string) (Query, error) {
	if utf8.RuneCountInString(text) > MaxQueryChars {
		return Query{}, parseErr("Searches can be up to 500 characters.")
	}
	tokens := strings.Fields(text)
	if len(tokens) > MaxTerms {
		return Query{}, parseErr("Use at most 10 search terms.")
	}
	var q Query
	for _, token := range tokens {
		term, err := parseTerm(token)
		if err != nil {
			return Query{}, err
		}
		q.Terms = append(q.Terms, term)
	}
	return q, nil
}

func parseTerm(token string) (Term, error) {
	var term Term
	rest := token
	if strings.HasPrefix(rest, "-") {
		term.Negate = true
		rest = rest[1:]
	}
	value := rest
	if i := strings.Index(rest, ":"); i >= 0 {
		if field, ok := fieldAliases[strings.ToLower(rest[:i])]; ok {
			term.Field = field
			value = rest[i+1:]
		}
	}
	if strings.HasPrefix(value, `"`) {
		if len(value) < 2 || !strings.HasSuffix(value, `"`) {
			return Term{}, parseErr("Close the quote in " + token + ".")
		}
		term.Exact = true
		value = value[1 : len(value)-1]
	}
	if value == "" {
		if term.Field != FieldAny {
			return Term{}, parseErr("Add a value after " + string(term.Field) + ":")
		}
		return Term{}, parseErr("Search terms need at least 2 characters.")
	}
	if term.Field == FieldPort {
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > 65535 {
			return Term{}, parseErr("port: needs a number from 1 to 65535.")
		}
		value = strconv.Itoa(n) // port:080 and port:+80 mean port 80
	}
	if term.Field == FieldAny && utf8.RuneCountInString(value) < MinFreeTextChars {
		return Term{}, parseErr("Search terms need at least 2 characters.")
	}
	term.Value = value
	return term, nil
}

// String is the normalized query text; parsing it again yields an equal Query.
func (q Query) String() string {
	parts := make([]string, 0, len(q.Terms))
	for _, t := range q.Terms {
		var b strings.Builder
		if t.Negate {
			b.WriteByte('-')
		}
		if t.Field != FieldAny {
			b.WriteString(string(t.Field))
			b.WriteByte(':')
		}
		if t.Exact {
			b.WriteString(`"` + t.Value + `"`)
		} else {
			b.WriteString(t.Value)
		}
		parts = append(parts, b.String())
	}
	return strings.Join(parts, " ")
}

// Empty reports whether the query has no terms.
func (q Query) Empty() bool { return len(q.Terms) == 0 }
