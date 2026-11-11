package main

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestRunPRCommand_WarnsAboutTestingStepsWithoutASource(t *testing.T) {
	r := newPRRepo(t)
	r.commit(t, "feature", "f.txt")
	r.park(t)
	stubPRDependencies(t, func() error { return nil })
	stubPRModelWith(t, `{"action":"add","title":"feature file","summary":"Adds the feature file.","changes":["Add f.txt"],
		"testing":["`+"`go test ./pkg/feature -count=1`"+`","go test ./internal/benchfixtures -run Ledger"]}`)
	dir := t.TempDir()
	writeFile(t, dir, "notes.md", "Ran `go test ./pkg/feature -count=1` on Linux.\n")

	var err error
	var out string
	stderr := captureStderr(t, func() {
		out = captureStdout(t, func() {
			err = runPRCommand([]string{"--dry-run", "--cost=false", "--head", "feature", "--context-file", filepath.Join(dir, "notes.md")})
		})
	})
	if err != nil {
		t.Fatalf("runPRCommand: %v", err)
	}
	if !strings.Contains(out, "## Testing\n\n- `go test ./pkg/feature -count=1`\n") {
		t.Errorf("the step whose command is in the notes is missing:\n%s", out)
	}
	// The command check is advisory: the step stays, and stderr names it.
	if !strings.Contains(out, "benchfixtures") {
		t.Errorf("the check dropped the step; it must only warn:\n%s", out)
	}
	if !strings.Contains(stderr, "testing step") || !strings.Contains(stderr, "benchfixtures") || !strings.Contains(stderr, "stays in the text") {
		t.Errorf("stderr does not name the step with the unsourced command:\n%s", stderr)
	}
}

// "not taken here; the owner decides" came back as "were rejected". Code
// cannot rewrite the sentence, so it warns, and --strict stops before gh.
func TestRunPRCommand_ADecisionWithoutASourceWarnsOrStops(t *testing.T) {
	for _, strict := range []bool{false, true} {
		name := "default warns and creates the PR"
		if strict {
			name = "--strict stops before gh"
		}
		t.Run(name, func(t *testing.T) {
			r := newPRRepo(t)
			r.commit(t, "feature", "f.txt")
			r.park(t)
			stubPRDependencies(t, func() error { return nil })
			stubGh(t)
			g := installFakeGh(t, "[]", "https://github.com/o/r/pull/12\n")
			stubPRModelWith(t, `{"action":"add","title":"feature file","summary":"Alternatives A and B were rejected.","changes":["Add f.txt"]}`)
			dir := t.TempDir()
			writeFile(t, dir, "notes.md", "Alternatives A and B were not taken here; the owner decides.\n")

			args := []string{"--yes", "--cost=false", "--head", "feature", "--context-file", filepath.Join(dir, "notes.md")}
			if strict {
				args = append(args, "--strict")
			}
			var err error
			stderr := captureStderr(t, func() { captureStdout(t, func() { err = runPRCommand(args) }) })

			if !strings.Contains(stderr, `the text says "rejected"`) {
				t.Errorf("stderr does not flag the decision:\n%s", stderr)
			}
			created := 0
			for _, call := range g.calls(t) {
				if strings.Join(call[:2], " ") == "pr create" {
					created++
				}
			}
			if strict {
				if err == nil || !strings.Contains(err.Error(), "--strict") {
					t.Fatalf("runPRCommand() error = %v, want the --strict refusal", err)
				}
				if created != 0 {
					t.Fatalf("gh pr create ran %d time(s) although --strict refused", created)
				}
				return
			}
			if err != nil {
				t.Fatalf("runPRCommand: %v", err)
			}
			if created != 1 {
				t.Fatalf("gh pr create ran %d time(s), want 1", created)
			}
		})
	}
}

// The command check is a warning (decision of the fifth review round): a
// command warning never fails --strict, and the step stays in the body.
func TestRunPRCommand_StrictIgnoresACommandWarning(t *testing.T) {
	for _, tc := range []struct {
		name, notes, step string
		warns             bool
	}{
		{"unsourced argument", "Ran go test ./pkg -run TestFoo and it passed.\n", "go test ./pkg -run `TestMissing`", true},
		{"unsourced flag after the span", "Ran go test ./pkg -run TestFoo and it passed.\n", "go test ./pkg -run `TestFoo` -race", true},
		{"unsourced flag after a separate value", "Ran go test ./pkg -run TestFoo -count 1 and it passed.\n", "go test ./pkg -run `TestFoo` -count 1 -race", true},
		{"flag in a code span after prose (fifth review)", "Ran go test ./pkg -run TestFoo and it passed.\n", "go test ./pkg -run `TestFoo` passed locally with `-race`", true},
		{"hyphen is prose", "Ran go test ./pkg -run TestFoo and it passed.\n", "go test ./pkg -run `TestFoo` - passed locally", false},
		{"sentence period after a flag (fifth review)", "Ran go test ./pkg -run TestFoo -race and it passed.\n", "go test ./pkg -run `TestFoo` -race passed.", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newPRRepo(t)
			r.commit(t, "feature", "f.txt")
			r.park(t)
			stubPRDependencies(t, func() error { return nil })
			stubGh(t)
			g := installFakeGh(t, "[]", "https://github.com/o/r/pull/12\n")
			stubPRModelWith(t, "{\"action\":\"add\",\"title\":\"feature file\",\"summary\":\"Adds a file.\",\"changes\":[\"Add f.txt\"],\"testing\":["+strconv.Quote(tc.step)+"]}")
			dir := t.TempDir()
			writeFile(t, dir, "notes.md", tc.notes)
			var err error
			stderr := captureStderr(t, func() {
				captureStdout(t, func() {
					err = runPRCommand([]string{"--yes", "--cost=false", "--strict", "--head", "feature", "--context-file", filepath.Join(dir, "notes.md")})
				})
			})
			if err != nil {
				t.Fatalf("--strict failed on a command warning: %v\n%s", err, stderr)
			}
			if got := strings.Contains(stderr, "stays in the text"); got != tc.warns {
				t.Errorf("warned = %v, want %v\n%s", got, tc.warns, stderr)
			}
			body := ghBodyArg(t, g, "pr create")
			if !strings.Contains(body, strings.ReplaceAll(tc.step, "`", "`")) {
				t.Errorf("the step is not in the body as written:\n%s", body)
			}
		})
	}
}
