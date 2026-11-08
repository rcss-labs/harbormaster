package logs

import (
	"errors"
	"strings"
	"testing"
)

const serverTail = "| fields @timestamp, @message, @log, @logStream\n| sort @timestamp desc\n| limit 50"

func TestComposeQuery_ScopeStageFirst(t *testing.T) {
	scopes := []StreamScope{
		{AccountID: "111111111111", Group: "/ecs/prod", Prefixes: []string{"ecs/api.v2/", "ecs/web/"}},
		{AccountID: "222222222222", Group: "/ecs/stage", Prefixes: []string{"ecs/(x)+/"}},
	}
	got, err := ComposeQuery("filter @message like /err/", scopes, 50)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(got, "\n")
	wantFirst := `filter (@log = "111111111111:/ecs/prod" and @logStream like /^(ecs\/api\.v2\/|ecs\/web\/)/) or (@log = "222222222222:/ecs/stage" and @logStream like /^(ecs\/\(x\)\+\/)/)`
	if lines[0] != wantFirst {
		t.Fatalf("first line\n got: %s\nwant: %s", lines[0], wantFirst)
	}
	if lines[1] != "| filter @message like /err/" {
		t.Fatalf("user stage line = %q", lines[1])
	}
	if strings.Join(lines[2:], "\n") != serverTail {
		t.Fatalf("tail = %q", strings.Join(lines[2:], "\n"))
	}
}

func TestComposeQuery_NoScopesIsError(t *testing.T) {
	if _, err := ComposeQuery("", nil, 10); err == nil {
		t.Fatal("expected error for zero scopes")
	}
}

func TestComposeQuery_RejectsCommands(t *testing.T) {
	scopes := []StreamScope{{AccountID: "1", Group: "g", Prefixes: []string{"p/"}}}
	cases := []struct{ query, cmd, msg string }{
		{`SOURCE logGroups(namePrefix: ["/"])`, "SOURCE", "Remove the SOURCE command."},
		{`stats count(*)`, "stats", ""},
		{`fields x | display y`, "display", ""},
		{`unmask @message`, "unmask", ""},
		{`pattern @message`, "pattern", ""},
		{`filter a | diff`, "diff", ""},
		{`where a = 1`, "where", ""},
	}
	for _, tc := range cases {
		out, err := ComposeQuery(tc.query, scopes, 10)
		var qe *QueryError
		if !errors.As(err, &qe) {
			t.Fatalf("%q: want *QueryError, got %v", tc.query, err)
		}
		if out != "" {
			t.Errorf("%q: output must be empty, got %q", tc.query, out)
		}
		if !strings.Contains(qe.Message, tc.cmd) {
			t.Errorf("%q: message %q does not name %s", tc.query, qe.Message, tc.cmd)
		}
		if tc.msg != "" && !strings.Contains(qe.Message, tc.msg) {
			t.Errorf("%q: message %q missing %q", tc.query, qe.Message, tc.msg)
		}
		if ValidateQuery(tc.query) == nil {
			t.Errorf("%q: ValidateQuery accepted", tc.query)
		}
	}
}

func TestComposeQuery_AllowedCommandsCaseInsensitive(t *testing.T) {
	scopes := []StreamScope{{AccountID: "1", Group: "g", Prefixes: []string{"p/"}}}
	q := "FIELDS a | Filter a = 1 | parse @message \"x*\" as y | dedup a | sort a asc | limit 5"
	out, err := ComposeQuery(q, scopes, 10)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(strings.Split(out, "\n")); n != 1+6+3 {
		t.Fatalf("lines = %d, out=%q", n, out)
	}
}

func TestComposeQuery_PipesInsideLiteralsAreNotStages(t *testing.T) {
	scopes := []StreamScope{{AccountID: "1", Group: "g", Prefixes: []string{"p/"}}}
	for _, q := range []string{
		`filter @message like /a|b/`,
		`filter @message like "x | stats"`,
		`parse @message 'a|b' as c`,
		"filter @message like `a|b`",
		`filter @message not like /a\/|b/`,
		`filter @message =~ /a|b/`,
		`filter (@message like /a|b/)`,
		`filter @message like "a\"|b"`,
	} {
		out, err := ComposeQuery(q, scopes, 10)
		if err != nil {
			t.Errorf("%q: %v", q, err)
			continue
		}
		lines := strings.Split(out, "\n")
		if len(lines) != 5 || lines[1] != "| "+q {
			t.Errorf("%q: want one user stage, got %q", q, lines)
		}
	}
}

func TestComposeQuery_SlashIsDivisionOutsideRegexContext(t *testing.T) {
	scopes := []StreamScope{{AccountID: "1", Group: "g", Prefixes: []string{"p/"}}}
	// "/" after a field name is division, so the pipe after it is a real stage separator.
	_, err := ComposeQuery(`fields a / b | stats count(*) / 2`, scopes, 10)
	var qe *QueryError
	if !errors.As(err, &qe) || !strings.Contains(qe.Message, "stats") {
		t.Fatalf("want stats rejection, got %v", err)
	}
}

func TestComposeQuery_CommentsCannotHideServerStages(t *testing.T) {
	scopes := []StreamScope{{AccountID: "1", Group: "g", Prefixes: []string{"p/"}}}
	out, err := ComposeQuery("filter x # | limit 1", scopes, 7)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(out, "\n")
	if len(lines) != 5 || lines[1] != "| filter x" {
		t.Fatalf("got %q", lines)
	}
	if strings.Contains(out, "#") {
		t.Fatalf("comment survived: %q", out)
	}
	if strings.Join(lines[2:], "\n") != strings.Replace(serverTail, "| limit 50", "| limit 7", 1) {
		t.Fatalf("server stages damaged: %q", lines)
	}
	// # inside a literal is not a comment; a comment ending at newline resumes the query.
	out, err = ComposeQuery("filter @message like \"#x\" # c\n| sort @timestamp", scopes, 7)
	if err != nil {
		t.Fatal(err)
	}
	l := strings.Split(out, "\n")
	if len(l) != 6 || l[1] != `| filter @message like "#x"` || l[2] != "| sort @timestamp" {
		t.Fatalf("got %q", l)
	}
}

func TestComposeQuery_EmptyUserTextAndLength(t *testing.T) {
	scopes := []StreamScope{{AccountID: "1", Group: "g", Prefixes: []string{"p/"}}}
	for _, q := range []string{"", "  \n\t ", "# only a comment"} {
		out, err := ComposeQuery(q, scopes, 50)
		if err != nil {
			t.Fatalf("%q: %v", q, err)
		}
		if n := len(strings.Split(out, "\n")); n != 4 {
			t.Fatalf("%q: lines = %d", q, n)
		}
	}
	long := "filter @message like \"" + strings.Repeat("a", MaxQueryChars) + "\""
	err := ValidateQuery(long)
	var qe *QueryError
	if !errors.As(err, &qe) || qe.Message != "Queries can be up to 4,000 characters." {
		t.Fatalf("got %v", err)
	}
	if err := ValidateQuery(strings.Repeat("é", MaxQueryChars)); err == nil {
		// 4000 chars of a non-command word is rejected for the command, not length
		t.Fatal("expected command rejection")
	}
	ok := "filter @message like \"" + strings.Repeat("é", MaxQueryChars-23) + "\""
	if err := ValidateQuery(ok); err != nil {
		t.Fatalf("exactly 4000 characters must pass: %v", err)
	}
}

func TestComposeQuery_UnclosedLiteral(t *testing.T) {
	scopes := []StreamScope{{AccountID: "1", Group: "g", Prefixes: []string{"p/"}}}
	for _, q := range []string{`filter a like "abc`, `filter a like /abc`, `parse a 'x`, "filter a like `x", `filter a like "a\"`} {
		out, err := ComposeQuery(q, scopes, 10)
		var qe *QueryError
		if !errors.As(err, &qe) || qe.Message != "Could not read the query. Check quotes and regex delimiters." {
			t.Errorf("%q: got %v", q, err)
		}
		if out != "" {
			t.Errorf("%q: output %q", q, out)
		}
	}
}

func TestComposeQuery_TooLong(t *testing.T) {
	var scopes []StreamScope
	for i := 0; i < 300; i++ {
		scopes = append(scopes, StreamScope{AccountID: "111111111111", Group: strings.Repeat("g", 100), Prefixes: []string{"p/"}})
	}
	_, err := ComposeQuery("", scopes, 10)
	var qe *QueryError
	if !errors.As(err, &qe) || qe.Message != "Too many services for one query." {
		t.Fatalf("got %v", err)
	}
}
