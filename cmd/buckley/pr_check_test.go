package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestRunPRCommand_LeavesOutTestingStepsWithoutASource(t *testing.T) {
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
	if strings.Contains(out, "benchfixtures") {
		t.Errorf("the invented step is in the text:\n%s", out)
	}
	if !strings.Contains(stderr, "left out the testing step") || !strings.Contains(stderr, "benchfixtures") {
		t.Errorf("stderr does not name the step that was left out:\n%s", stderr)
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

// Review of #468, second round, finding 4: a testing step with an unsupported
// argument is left out, and --strict refuses it before gh runs.
func TestRunPRCommand_StrictRefusesAnUnsupportedTestingStep(t *testing.T) {
	r := newPRRepo(t)
	r.commit(t, "feature", "f.txt")
	r.park(t)
	stubPRDependencies(t, func() error { return nil })
	stubGh(t)
	g := installFakeGh(t, "[]", "https://github.com/o/r/pull/12\n")
	stubPRModelWith(t, "{\"action\":\"add\",\"title\":\"feature file\",\"summary\":\"Adds a file.\",\"changes\":[\"Add f.txt\"],\"testing\":[\"go test ./pkg -run `TestMissing`\"]}")
	dir := t.TempDir()
	writeFile(t, dir, "notes.md", "Ran go test ./pkg -run TestFoo and it passed.\n")

	var err error
	stderr := captureStderr(t, func() {
		captureStdout(t, func() {
			err = runPRCommand([]string{"--yes", "--cost=false", "--strict", "--head", "feature", "--context-file", filepath.Join(dir, "notes.md")})
		})
	})
	if !strings.Contains(stderr, "left out the testing step") {
		t.Errorf("the step was not left out:\n%s", stderr)
	}
	if err == nil || !strings.Contains(err.Error(), "--strict") {
		t.Fatalf("error = %v, want the --strict refusal", err)
	}
	for _, call := range g.calls(t) {
		if strings.Join(call[:2], " ") == "pr create" {
			t.Fatal("gh pr create ran although --strict refused")
		}
	}
}

// Review of #468, third round: --strict fails on an unsupported flag after the
// last code span too.
func TestRunPRCommand_StrictRefusesAnUnsupportedFlagAfterTheLastSpan(t *testing.T) {
	r := newPRRepo(t)
	r.commit(t, "feature", "f.txt")
	r.park(t)
	stubPRDependencies(t, func() error { return nil })
	stubGh(t)
	g := installFakeGh(t, "[]", "https://github.com/o/r/pull/12\n")
	stubPRModelWith(t, "{\"action\":\"add\",\"title\":\"feature file\",\"summary\":\"Adds a file.\",\"changes\":[\"Add f.txt\"],\"testing\":[\"go test ./pkg -run `TestFoo` -race\"]}")
	dir := t.TempDir()
	writeFile(t, dir, "notes.md", "Ran go test ./pkg -run TestFoo and it passed.\n")

	var err error
	stderr := captureStderr(t, func() {
		captureStdout(t, func() {
			err = runPRCommand([]string{"--yes", "--cost=false", "--strict", "--head", "feature", "--context-file", filepath.Join(dir, "notes.md")})
		})
	})
	if !strings.Contains(stderr, "left out the testing step") || err == nil || !strings.Contains(err.Error(), "--strict") {
		t.Fatalf("stderr = %q, error = %v, want the step left out and --strict refusing", stderr, err)
	}
	for _, call := range g.calls(t) {
		if strings.Join(call[:2], " ") == "pr create" {
			t.Fatal("gh pr create ran although --strict refused")
		}
	}
}
