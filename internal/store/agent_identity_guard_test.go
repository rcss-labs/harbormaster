package store_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// A query on users either states which kind of user it means (kind = 'human' or kind = 'agent', or the kind column of an
// insert) or is listed here, with the reason any kind is right. A new unfiltered query fails this test, so a path that
// enumerates, authenticates or changes users cannot reach the agent identity unnoticed. The key is the file and the
// function (or package-level constant) that holds the query.
var anyKindUsersQueries = map[string]string{
	"users.go GetUser":                          "the one general lookup: the token path, audit names and the off-hours username need the identity too",
	"agent_identity.go UsernameTaken":           "a name is taken by a user of any kind; the unique index says the same",
	"api_tokens.go CreateRunToken":              "a run token belongs to a human (chat) or to the identity (background runs)",
	"permissions.go UsersWithView":              "plan call 3: a delete-tag impact list names the identity, because it does lose access",
	"permissions.go LoadSubject":                "reads kind and decides in Go: the identity is masked, an unknown kind is ErrNotFound",
	"oplog.go appendOperation":                  "display name of the actor; any kind",
	"oplog_batch.go AppendOperations":           "display name of the actor; any kind",
	"timeline.go TimelineDeployments":           "display name of who started a deployment; any kind",
	"revisions.go RevisionRegistrars":           "display name of who registered a revision; any kind",
	"scan_requests.go scanRequestFrom":          "display name of the requester; any kind",
	"scan_acceptances.go scanAcceptanceFrom":    "display name of who accepted a risk; any kind",
	"agent_reconcile.go auditFinishedAgentRuns": "audit actor name of a finished run; the identity owns background runs",
	"log_search.go SaveLogSearch":               "locks the user row to serialise one user's saved searches; any kind, and no kind can race itself",
}

var (
	usersTableRef = regexp.MustCompile(`(?i)\b(from|join|update|into)\s+users\b`)
	kindRule      = regexp.MustCompile(`(?i)\bkind\s*(=|\bin\b)\s*\(?\s*'(human|agent)'`)
	kindInsert    = regexp.MustCompile(`(?is)\binsert\s+into\s+users\b[^)]*\bkind\b.*'(human|agent)'`)
)

// sqlOf returns the text of a string expression, with a placeholder for every operand that is not a literal: a constant
// such as the column list is not resolved, so the kind inside userColumns cannot satisfy a rule on the query around it.
func sqlOf(e ast.Expr) (string, bool) {
	switch v := e.(type) {
	case *ast.BasicLit:
		if v.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(v.Value)
		return s, err == nil
	case *ast.BinaryExpr:
		if v.Op != token.ADD {
			return "", false
		}
		l, lok := sqlOf(v.X)
		r, rok := sqlOf(v.Y)
		if !lok && !rok {
			return "", false
		}
		if !lok {
			l = " <expr> "
		}
		if !rok {
			r = " <expr> "
		}
		return l + r, true
	case *ast.ParenExpr:
		return sqlOf(v.X)
	}
	return "", false
}

func TestEveryUsersQueryStatesItsKindRule(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	used := map[string]bool{}
	checked := 0
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			owner := ""
			switch d := decl.(type) {
			case *ast.FuncDecl:
				owner = d.Name.Name
			case *ast.GenDecl:
				if len(d.Specs) == 1 {
					if vs, ok := d.Specs[0].(*ast.ValueSpec); ok && len(vs.Names) == 1 {
						owner = vs.Names[0].Name
					}
				}
			}
			ast.Inspect(decl, func(n ast.Node) bool {
				expr, ok := n.(ast.Expr)
				if !ok {
					return true
				}
				sql, ok := sqlOf(expr)
				if !ok {
					return true
				}
				// A concatenation is one statement: do not look at its parts again.
				if !usersTableRef.MatchString(sql) {
					return false
				}
				checked++
				refs := len(usersTableRef.FindAllString(sql, -1))
				rules := len(kindRule.FindAllString(sql, -1))
				if kindInsert.MatchString(sql) {
					rules++
				}
				key := name + " " + owner
				if _, listed := anyKindUsersQueries[key]; listed {
					used[key] = true
				} else if rules < refs {
					t.Errorf("%s: a query on users names %d tables of users but states its kind %d times: %q", key, refs, rules, strings.Join(strings.Fields(sql), " "))
				}
				return false
			})
		}
	}
	if checked < 20 {
		t.Fatalf("found only %d queries on users; the scan is not reading the sources", checked)
	}
	var stale []string
	for key := range anyKindUsersQueries {
		if !used[key] {
			stale = append(stale, key)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("allowlist entries that no longer match a query: %v", stale)
	}
}
