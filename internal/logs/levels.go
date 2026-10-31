package logs

import (
	"regexp"
	"strings"
)

var (
	levelKeyValue = regexp.MustCompile(`(?i)level"?[=:]\s*"?(warn|warning|error|fatal|panic|critical)`)
	levelToken    = regexp.MustCompile(`\b(WARN|WARNING|ERROR|FATAL|PANIC|CRITICAL)\b`)
	levelBracket  = regexp.MustCompile(`(?i)\[(warn|error|fatal)\]`)
)

// Level classifies a log line as "error", "warn" or "" (anything else). It is
// the server-side twin of the web client's heuristic. An error-class match
// wins over a warn-class match in the same line.
func Level(msg string) string {
	result := ""
	for _, re := range []*regexp.Regexp{levelKeyValue, levelToken, levelBracket} {
		for _, m := range re.FindAllStringSubmatch(msg, -1) {
			switch strings.ToLower(m[1]) {
			case "warn", "warning":
				result = "warn"
			default:
				return "error"
			}
		}
	}
	return result
}
