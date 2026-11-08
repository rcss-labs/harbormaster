package logs

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxQueryChars is the longest user query text accepted.
const MaxQueryChars = 4000

// maxComposedChars guards the size of the full composed query sent to AWS.
const maxComposedChars = 10000

// StreamScope is one log group and the stream-name prefixes searched in it.
// Every prefix ends in "/".
type StreamScope struct {
	AccountID string
	Group     string
	Prefixes  []string
}

// QueryError is a user-facing problem with the query text (surfaced as a 422).
type QueryError struct{ Message string }

func (e *QueryError) Error() string { return e.Message }

const errUnreadable = "Could not read the query. Check quotes and regex delimiters."

// allowedCommands is an allow-list, not a deny-list: SOURCE would let a query
// choose its own log groups, and stats/display/unmask/pattern/diff etc. change
// the result shape or reveal data the fan-out searcher does not expect.
var allowedCommands = map[string]bool{
	"fields": true, "filter": true, "parse": true,
	"dedup": true, "sort": true, "limit": true,
}

// ValidateQuery tokenizes the user text and checks every stage against the
// allow-list. It makes no AWS call and is also used for saved queries.
func ValidateQuery(user string) error {
	_, err := splitStages(user)
	return err
}

// ComposeQuery returns a Logs Insights query that first restricts events to
// the given scopes, then runs the user's stages, then fixes the output shape.
// Each stage is on its own "| " line so a comment in user text cannot swallow a
// server-owned stage.
func ComposeQuery(user string, scopes []StreamScope, limit int) (string, error) {
	stages, err := splitStages(user)
	if err != nil {
		return "", err
	}
	if len(scopes) == 0 {
		return "", fmt.Errorf("compose query: no scopes")
	}
	if limit <= 0 {
		return "", fmt.Errorf("compose query: limit must be positive, got %d", limit)
	}
	clauses := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		if len(scope.Prefixes) == 0 {
			// Fail closed: no prefixes would otherwise mean "every stream".
			return "", fmt.Errorf("compose query: scope %q has no stream prefixes", scope.Group)
		}
		escaped := make([]string, len(scope.Prefixes))
		for i, p := range scope.Prefixes {
			escaped[i] = escapeRegexLiteral(p)
		}
		clauses = append(clauses, fmt.Sprintf(`(@log = "%s" and @logStream like /^(%s)/)`,
			escapeQuoted(scope.AccountID+":"+scope.Group), strings.Join(escaped, "|")))
	}
	// Insights separates commands with "|". The scope filter is first with no
	// leading pipe; every later stage starts its own line with "| ".
	lines := []string{"filter " + strings.Join(clauses, " or ")}
	for _, stage := range stages {
		lines = append(lines, "| "+stage)
	}
	lines = append(lines,
		"| fields @timestamp, @message, @log, @logStream",
		"| sort @timestamp desc",
		fmt.Sprintf("| limit %d", limit),
	)
	composed := strings.Join(lines, "\n")
	if utf8.RuneCountInString(composed) > maxComposedChars {
		return "", &QueryError{Message: "Too many services for one query."}
	}
	return composed, nil
}

func escapeRegexLiteral(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(`\.+*?()|[]{}^$/`, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

func escapeQuoted(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s)
}

// splitStages splits user text on pipes outside literals, strips comments,
// and validates each stage's command. Empty or comment-only text gives no stages.
func splitStages(user string) ([]string, error) {
	if utf8.RuneCountInString(user) > MaxQueryChars {
		return nil, &QueryError{Message: "Queries can be up to 4,000 characters."}
	}
	var (
		stages  []string
		cur     strings.Builder
		code    strings.Builder // current stage with literals blanked, for the alias check
		sawPipe bool
	)
	runes := []rune(user)
	flush := func() error {
		stage := strings.TrimSpace(cur.String())
		cur.Reset()
		codeText := code.String()
		code.Reset()
		if stage == "" {
			if sawPipe {
				return &QueryError{Message: "A pipe must be followed by a command."}
			}
			return nil
		}
		if err := checkCommand(stage); err != nil {
			return err
		}
		if aliasesToAtName(codeText) {
			return &QueryError{Message: "Log search cannot rename fields that start with @."}
		}
		stages = append(stages, stage)
		return nil
	}
	for i := 0; i < len(runes); {
		r := runes[i]
		switch {
		case r == '|':
			sawPipe = true
			if err := flush(); err != nil {
				return nil, err
			}
			i++
		case r == '#':
			for i < len(runes) && runes[i] != '\n' {
				i++
			}
		case r == '\n' || r == '\r':
			cur.WriteByte(' ')
			code.WriteByte(' ')
			i++
		case r == '"' || r == '\'' || r == '`' || (r == '/' && opensRegex(cur.String())):
			end, ok := scanLiteral(runes, i)
			if !ok {
				return nil, &QueryError{Message: errUnreadable}
			}
			cur.WriteString(string(runes[i:end]))
			code.WriteString(" _ ")
			i = end
		default:
			if r == '/' && quoteFollows(runes, i+1) {
				// Division followed by a quote: guessing could desync from AWS's reading.
				return nil, &QueryError{Message: errUnreadable}
			}
			cur.WriteRune(r)
			code.WriteRune(r)
			i++
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return stages, nil
}

// scanLiteral returns the index just past the literal opened at runes[start].
// Backslash escapes the next rune in strings and regexes but not in backticks.
// A raw newline inside any literal is unreadable: it would break the
// one-stage-per-line layout.
func scanLiteral(runes []rune, start int) (int, bool) {
	delim := runes[start]
	for i := start + 1; i < len(runes); i++ {
		switch runes[i] {
		case '\n', '\r':
			return 0, false
		case '\\':
			if delim != '`' {
				i++
				if i < len(runes) && (runes[i] == '\n' || runes[i] == '\r') {
					return 0, false
				}
			}
		case delim:
			return i + 1, true
		}
	}
	return 0, false
}

// opensRegex reports whether a "/" starts a regex literal: in a parse stage,
// or after like, not like, =~ or "(". Anywhere else it is division.
func opensRegex(stageSoFar string) bool {
	return strings.EqualFold(leadingWord(stageSoFar), "parse") || regexAllowedAfter(stageSoFar)
}

func quoteFollows(runes []rune, from int) bool {
	for i := from; i < len(runes); i++ {
		if unicode.IsSpace(runes[i]) {
			continue
		}
		return runes[i] == '"' || runes[i] == '\'' || runes[i] == '`'
	}
	return false
}

// aliasesToAtName reports whether the literal-blanked stage text contains
// "as @name". Attribution relies on @log and @logStream staying intact.
func aliasesToAtName(code string) bool {
	words := strings.Fields(code)
	for i := 0; i+1 < len(words); i++ {
		if strings.EqualFold(words[i], "as") && strings.HasPrefix(words[i+1], "@") {
			return true
		}
	}
	return false
}

// leadingWord returns the first identifier-like word of s after leading space.
func leadingWord(s string) string {
	s = strings.TrimLeftFunc(s, unicode.IsSpace)
	end := 0
	for end < len(s) {
		r, size := utf8.DecodeRuneInString(s[end:])
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_') {
			break
		}
		end += size
	}
	return s[:end]
}

// regexAllowedAfter reports whether a "/" opens a regex literal: only after
// like, not like, =~ or "(" with nothing but whitespace in between. Anywhere
// else "/" is division.
func regexAllowedAfter(before string) bool {
	t := strings.TrimRightFunc(before, unicode.IsSpace)
	if strings.HasSuffix(t, "=~") || strings.HasSuffix(t, "(") {
		return true
	}
	if len(t) >= 4 && strings.EqualFold(t[len(t)-4:], "like") {
		if len(t) == 4 {
			return true
		}
		prev, _ := utf8.DecodeLastRuneInString(t[:len(t)-4])
		return !(unicode.IsLetter(prev) || unicode.IsDigit(prev) || prev == '_' || prev == '@' || prev == '.')
	}
	return false
}

func checkCommand(stage string) error {
	command := leadingWord(stage)
	if command == "" {
		r, _ := utf8.DecodeRuneInString(stage)
		command = string(r)
	}
	lower := strings.ToLower(command)
	if allowedCommands[lower] {
		return nil
	}
	if lower == "source" {
		return &QueryError{Message: "Log search chooses log groups from the selected services. Remove the SOURCE command."}
	}
	return &QueryError{Message: fmt.Sprintf("Log search does not support the %s command. Use fields, filter, parse, dedup, sort and limit.", command)}
}
