package policy

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"maps"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// This package keeps a model from running buckley commands that change buckley's
// own trust, approval, or credential state once the operator lets it launch
// agents. It holds its own lists of the trust verbs, of the other commands, and
// of the startup flags that take a value. These tests read the cmd/buckley code
// those lists describe, so a command, verb, or flag that changes there and not
// here fails here.

func TestTrustWriteVerbs_MatchRunTrustCommand(t *testing.T) {
	clauses := buckleySwitchClauses(t, "project_trust.go", "runTrustCommand", "subCmd")
	got := caseStrings(t, clauses, func(*ast.CaseClause) bool { return true })
	got = slices.DeleteFunc(got, func(verb string) bool { return verb == "status" })

	if want := slices.Sorted(maps.Keys(buckleyTrustWriteVerbs)); !slices.Equal(got, want) {
		t.Fatalf("runTrustCommand handles the verbs %q besides status, but pkg/policy denies %q:"+
			" update buckleyTrustWriteVerbs in pkg/policy/shell_classification.go", got, want)
	}
}

func TestStartupValueFlags_MatchConsumeStartupFlag(t *testing.T) {
	clauses := buckleySwitchClauses(t, "main.go", "consumeStartupFlag", "arg")
	got := caseStrings(t, clauses, clauseSetsPending)

	if want := slices.Sorted(maps.Keys(buckleyStartupValueFlags)); !slices.Equal(got, want) {
		t.Fatalf("consumeStartupFlag reads a value after %q, but pkg/policy skips the value of %q:"+
			" update buckleyStartupValueFlags in pkg/policy/shell_classification.go", got, want)
	}
}

// buckleyDispatchers names the function in cmd/buckley that reads the word after
// each prefix of a command, and the expression it switches on. Each one picks its
// subcommand from the first word after its own name.
var buckleyDispatchers = map[string]struct{ file, fn, tag string }{
	"":              {"main.go", "dispatchSubcommand", "args[0]"},
	"goal":          {"goal.go", "runGoalCommand", "args[0]"},
	"remote":        {"remote.go", "runRemoteCommand", "args[0]"},
	"remote tokens": {"remote.go", "runRemoteTokens", "sub"},
}

// buckleyOtherSubcommands lists, under each word that has a state command below
// it, the subcommands that are not state commands and not the way to one. It is
// the other half of buckleyStateCommands: every subcommand under these words is
// on one list or the other.
var buckleyOtherSubcommands = map[string][]string{
	"goal":          {"preflight", "start", "status", "list", "report", "run", "audit", "replay", "worker"},
	"remote":        {"attach", "sessions", "console"},
	"remote tokens": {"list"},
}

// TestStateCommands_MatchDispatch checks that each word of buckleyStateCommands
// is still a case of the function in cmd/buckley that reads it. If a dispatcher
// drops or renames a word, the guard watches a command that no longer exists and
// leaves the new one open.
func TestStateCommands_MatchDispatch(t *testing.T) {
	for _, command := range buckleyStateCommands {
		name := strings.Join(command, " ")
		for depth, word := range command {
			prefix := strings.Join(command[:depth], " ")
			dispatcher, ok := buckleyDispatchers[prefix]
			if !ok {
				t.Fatalf("buckley %s: this test does not know which function reads the word after %q: add it to buckleyDispatchers", name, prefix)
			}
			clauses := buckleySwitchClauses(t, dispatcher.file, dispatcher.fn, dispatcher.tag)
			if cases := caseStrings(t, clauses, func(*ast.CaseClause) bool { return true }); !slices.Contains(cases, word) {
				t.Errorf("%s in cmd/buckley/%s has no case %q, but pkg/policy denies `buckley %s`:"+
					" update buckleyStateCommands in pkg/policy/shell_classification.go",
					dispatcher.fn, dispatcher.file, word, name)
			}
		}
	}
}

// TestStateCommands_SiblingSubcommandsAreSorted fails when a dispatcher with a
// state command under it gains a subcommand that is on neither list. A sibling
// such as `buckley goal deny` could resolve an approval too, and the guard would
// let it run. The new name stays a failure until someone decides which list it
// belongs on.
func TestStateCommands_SiblingSubcommandsAreSorted(t *testing.T) {
	for prefix, other := range buckleyOtherSubcommands {
		sorted := slices.Clone(other)
		for _, command := range buckleyStateCommands {
			if depth := len(strings.Fields(prefix)); depth < len(command) && strings.Join(command[:depth], " ") == prefix {
				sorted = append(sorted, command[depth])
			}
		}
		dispatcher, ok := buckleyDispatchers[prefix]
		if !ok {
			t.Fatalf("this test does not know which function reads the word after %q: add it to buckleyDispatchers", prefix)
		}
		clauses := buckleySwitchClauses(t, dispatcher.file, dispatcher.fn, dispatcher.tag)
		cases := caseStrings(t, clauses, func(*ast.CaseClause) bool { return true })
		for _, name := range cases {
			if !slices.Contains(sorted, name) {
				t.Errorf("%s in cmd/buckley/%s handles the subcommand %q, which is on neither list:"+
					" if `buckley %s %s` changes trust, approval, or credential state, add it to buckleyStateCommands in"+
					" pkg/policy/shell_classification.go, otherwise add it to buckleyOtherSubcommands in this test",
					dispatcher.fn, dispatcher.file, name, prefix, name)
			}
		}
		for _, name := range other {
			if !slices.Contains(cases, name) {
				t.Errorf("buckleyOtherSubcommands lists %q under %q, but %s in cmd/buckley/%s has no such case: remove it",
					name, prefix, dispatcher.fn, dispatcher.file)
			}
		}
	}
}

// clauseSetsPending reports whether a case clause makes the next word the
// flag's value, which consumeStartupFlag does by assigning s.pending.
func clauseSetsPending(clause *ast.CaseClause) bool {
	found := false
	ast.Inspect(clause, func(n ast.Node) bool {
		if assign, ok := n.(*ast.AssignStmt); ok {
			for _, lhs := range assign.Lhs {
				if selector, ok := lhs.(*ast.SelectorExpr); ok && selector.Sel.Name == "pending" {
					found = true
				}
			}
		}
		return true
	})
	return found
}

// buckleySwitchClauses returns the case clauses of the first switch on tag
// inside the function fn of the named cmd/buckley file. It writes tag as Go
// source, for example "arg" or "args[0]". It finds the file from this test's own
// location, so the working directory does not matter.
func buckleySwitchClauses(t *testing.T, name, fn, tag string) []*ast.CaseClause {
	t.Helper()
	_, here, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locating the test source")
	}
	path := filepath.Join(filepath.Dir(here), "..", "..", "cmd", "buckley", name)
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	var clauses []*ast.CaseClause
	for _, decl := range file.Decls {
		function, ok := decl.(*ast.FuncDecl)
		if !ok || function.Name.Name != fn {
			continue
		}
		ast.Inspect(function.Body, func(n ast.Node) bool {
			stmt, ok := n.(*ast.SwitchStmt)
			if !ok || clauses != nil {
				return true
			}
			if stmt.Tag != nil && types.ExprString(stmt.Tag) == tag {
				for _, entry := range stmt.Body.List {
					clauses = append(clauses, entry.(*ast.CaseClause))
				}
			}
			return true
		})
	}
	if len(clauses) == 0 {
		t.Fatalf("%s has no switch on %s in %s: update this test and the guard in"+
			" pkg/policy/shell_classification.go with the code they read", name, tag, fn)
	}
	return clauses
}

// caseStrings returns, sorted, the string literals in the case lists of the
// clauses that keep accepts.
func caseStrings(t *testing.T, clauses []*ast.CaseClause, keep func(*ast.CaseClause) bool) []string {
	t.Helper()
	var values []string
	for _, clause := range clauses {
		if !keep(clause) {
			continue
		}
		for _, expr := range clause.List {
			literal, ok := expr.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				continue
			}
			value, err := strconv.Unquote(literal.Value)
			if err != nil {
				t.Fatalf("unquote %s: %v", literal.Value, err)
			}
			values = append(values, value)
		}
	}
	slices.Sort(values)
	return values
}
