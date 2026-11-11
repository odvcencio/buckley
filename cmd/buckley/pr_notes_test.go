package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"m31labs.dev/buckley/pkg/oneshot"
	"m31labs.dev/buckley/pkg/oneshot/commands"
	"m31labs.dev/buckley/pkg/transparency"
)

// stubPRModelWith replaces the model with one that answers raw and returns
// where it keeps the prompt.
func stubPRModelWith(t *testing.T, raw string) *string {
	t.Helper()
	t.Setenv("BUCKLEY_PROMPT_PR", "")
	t.Setenv("BUCKLEY_PROMPT_PR_FILE", "")
	prompt := new(string)
	previous := newPRCommandRuntimeFn
	t.Cleanup(func() { newPRCommandRuntimeFn = previous })
	newPRCommandRuntimeFn = func(prCommandOptions) (*prCommandRuntime, func(), error) {
		return &prCommandRuntime{
			backend:   oneshotBackendAPI,
			modelID:   "test-model",
			ledger:    transparency.NewCostLedger(),
			framework: oneshot.NewFramework(fakeModelInvoker{raw: json.RawMessage(raw), prompt: prompt}, nil),
		}, func() {}, nil
	}
	return prompt
}

// ghBodyArg returns the --body value of the one gh call named verb.
func ghBodyArg(t *testing.T, g *fakeGh, verb string) string {
	t.Helper()
	var bodies []string
	for _, call := range g.calls(t) {
		if strings.Join(call[:2], " ") != verb {
			continue
		}
		for i, arg := range call {
			if arg == "--body" && i+1 < len(call) {
				bodies = append(bodies, call[i+1])
			}
		}
	}
	if len(bodies) != 1 {
		t.Fatalf("gh %s calls with --body = %d, want 1", verb, len(bodies))
	}
	return bodies[0]
}

// The 2026-11-09 session supplied counters, evidence, a not-verified list and
// known gaps in a context file, and the generated body had none of them. The
// model here drops them too, invents testing commands and sends an empty
// issue; the body that reaches gh must still carry the author's sections
// exactly and must not carry the inventions.
func TestRunPRCommand_AuthorSectionsReachTheBodyGhReceives(t *testing.T) {
	r := newPRRepo(t)
	r.commit(t, "feature", "f.txt")
	r.park(t)
	stubPRDependencies(t, func() error { return nil })
	stubGh(t)
	g := installFakeGh(t, "[]", "https://github.com/o/r/pull/12\n")
	prompt := stubPRModelWith(t, `{"action":"refactor","scope":"ccore","title":"port the lex automaton",
		"summary":"Ports the automaton into the core.","changes":["Move the DFA walk into the package"],
		"testing":["go test ./internal/benchfixtures -run Ledger","bash cgo_h/docker/run_single_grammar_parity.sh <grammar>"],
		"issues":["", "none"]}`)

	counters := "| counter | before | after |\n|---|---|---|\n| lex steps | 1,204 | 1,204 |"
	testing := "- `bash cgo_harness/docker/run_single_grammar_parity.sh json`: 412 of 412 match"
	notes := filepath.Join(t.TempDir(), "notes.md")
	writeFile(t, filepath.Dir(notes), "notes.md", strings.Join([]string{
		"The kernel seam is gone; see the commits.",
		"",
		"## Testing", "", testing, "",
		"## Counters", "", counters, "",
		"## Not verified", "", "- Parity on Windows.", "- TestScanOrigins under -race.", "",
		"## Known gaps", "", "Alternatives A and B were not taken here; the owner decides.",
	}, "\n"))

	var err error
	captureStdout(t, func() {
		err = runPRCommand([]string{"--yes", "--cost=false", "--head", "feature", "--context-file", notes})
	})
	if err != nil {
		t.Fatalf("runPRCommand: %v", err)
	}

	body := ghBodyArg(t, g, "pr create")
	for _, want := range []string{
		"## Testing\n\n" + testing + "\n",
		"## Counters\n\n" + counters + "\n",
		"## Not verified\n\n- Parity on Windows.\n- TestScanOrigins under -race.\n",
		"## Known gaps\n\nAlternatives A and B were not taken here; the owner decides.",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the body gh received lacks the author's text %q:\n%s", want, body)
		}
	}
	for _, unwanted := range []string{"benchfixtures", "cgo_h/docker", "Related Issues", "Buckley wrote this text"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("the body gh received has %q:\n%s", unwanted, body)
		}
	}
	for _, line := range strings.Split(body, "\n") {
		if strings.TrimSpace(line) == "-" {
			t.Fatalf("the body has an empty bullet:\n%s", body)
		}
	}
	if !strings.Contains(*prompt, "word for word: Testing; Counters; Not verified; Known gaps.") {
		t.Errorf("the prompt does not tell the model which sections are copied:\n%s", *prompt)
	}
}

func TestRunPRCommand_SaysNothingWasVerifiedWhenTheAuthorSaysNothing(t *testing.T) {
	r := newPRRepo(t)
	r.commit(t, "feature", "f.txt")
	r.park(t)
	stubPRDependencies(t, func() error { return nil })
	stubPRModel(t)

	var err error
	out := captureStdout(t, func() { err = runPRCommand([]string{"--dry-run", "--cost=false", "--head", "feature"}) })
	if err != nil {
		t.Fatalf("runPRCommand: %v", err)
	}
	if !strings.Contains(out, "## Not verified\n\nThe author supplied no notes.") {
		t.Errorf("the default body has no not-verified section:\n%s", out)
	}
}

func TestRunPRCommand_WarnsAboutCutNotesAndKeepsBothSections(t *testing.T) {
	r := newPRRepo(t)
	r.commit(t, "feature", "f.txt")
	r.park(t)
	stubPRDependencies(t, func() error { return nil })
	stubPRModel(t)

	dir := t.TempDir()
	writeFile(t, dir, "old.md", "## Not verified\n\nTestScanOrigins fails.\n\n"+strings.Repeat("background line\n", prContextNotesMaxBytes/16))
	writeFile(t, dir, "new.md", "## Not verified\n\nNothing: the test was narrowed and passes.\n")

	var err error
	var out string
	stderr := captureStderr(t, func() {
		out = captureStdout(t, func() {
			err = runPRCommand([]string{"--dry-run", "--cost=false", "--head", "feature",
				"--context-file", filepath.Join(dir, "old.md"), "--context-file", filepath.Join(dir, "new.md")})
		})
	})
	if err != nil {
		t.Fatalf("runPRCommand: %v", err)
	}
	if !strings.Contains(stderr, "too long for the prompt") {
		t.Errorf("stderr does not say that the notes were cut:\n%s", stderr)
	}
	if !strings.Contains(out, "Nothing: the test was narrowed and passes.") || !strings.Contains(out, "TestScanOrigins fails") {
		t.Errorf("the body must carry both Not verified sections:\n%s", out)
	}
	if strings.Index(out, "TestScanOrigins fails") > strings.Index(out, "Nothing: the test was narrowed") {
		t.Errorf("the sections must stay in file order:\n%s", out)
	}
}

func TestPRNotesPrompt_LabelsTheOrderAndReportsTheCut(t *testing.T) {
	one, cut := prNotesPrompt([]commands.PRNote{{Path: "a.md", Text: "alpha"}})
	if one != "### a.md\n\nalpha" || cut != 0 {
		t.Fatalf("one note = %q (cut %d)", one, cut)
	}

	two, cut := prNotesPrompt([]commands.PRNote{{Path: "a.md", Text: "alpha"}, {Path: "b.md", Text: "beta"}})
	want := "### Note 1 of 2: a.md\n\nalpha\n\n### Note 2 of 2, the latest: b.md\n\nbeta"
	if two != want || cut != 0 {
		t.Fatalf("two notes = %q (cut %d), want %q", two, cut, want)
	}

	// A cut never splits a multi-byte character and is reported in bytes.
	long := strings.Repeat("é", prContextNotesMaxBytes)
	text, cut := prNotesPrompt([]commands.PRNote{{Path: "long.md", Text: long}})
	if cut == 0 || !strings.HasSuffix(text, "\n... (truncated)") {
		t.Fatalf("cut = %d, suffix of text = %q", cut, text[len(text)-20:])
	}
	if !utf8.ValidString(text) {
		t.Fatal("the cut split a UTF-8 sequence")
	}
	if kept := len(text) - len("\n... (truncated)"); kept > prContextNotesMaxBytes || kept+cut != len("### long.md\n\n")+len(long) {
		t.Fatalf("kept %d + cut %d bytes does not add up to the input", kept, cut)
	}

	// With line breaks in reach, the cut falls on one.
	lines := strings.Repeat("0123456789abcde\n", prContextNotesMaxBytes/8)
	text, _ = prNotesPrompt([]commands.PRNote{{Path: "lines.md", Text: lines}})
	if !strings.HasSuffix(text, "0123456789abcde\n... (truncated)") {
		t.Fatalf("the cut is not at a line end: %q", text[len(text)-40:])
	}
}

func TestLoadPRNotes_KeepsOrderAndSkipsEmptyFiles(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{"a.md": "first", "empty.md": "  \n", "b.md": "second"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	notes, err := loadPRNotes([]string{filepath.Join(dir, "b.md"), filepath.Join(dir, "empty.md"), filepath.Join(dir, "a.md")})
	if err != nil {
		t.Fatalf("loadPRNotes: %v", err)
	}
	if len(notes) != 2 || notes[0].Text != "second" || notes[1].Text != "first" {
		t.Fatalf("notes = %+v, want b.md then a.md", notes)
	}
}

// Commented-out text reaches neither the prompt, nor the carried sections, nor
// the sources, wherever the comment is (reviews of #468, three rounds).
func TestLoadPRNotes_CommentedTextReachesNothing(t *testing.T) {
	const token = "secret-token-4711"
	notes := map[string]string{
		"plain":         "## Testing\n\n- go vet passes\n\n<!-- " + token + " `go test ./hidden` 98765 -->\n\n## Not verified\n\n- Windows\n",
		"in code fence": "## Testing\n\n````markdown\n```\n````\n<!-- " + token + " -->\n\n```\n<!-- " + token + " -->\n```\n\n## Not verified\n\n- Windows\n",
		"escaped ticks": "## Evidence\n\nLiteral \\` <!-- " + token + " --> \\`.\n\n## Not verified\n\n- Windows\n",
		"inline code":   "## Testing\n\n- `<!-- " + token + " -->` is a comment\n\n## Not verified\n\n- Windows\n",
	}
	for name, content := range notes {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, dir, "n.md", content)
			loaded, err := loadPRNotes([]string{filepath.Join(dir, "n.md")})
			if err != nil {
				t.Fatal(err)
			}
			prompt, _ := prNotesPrompt(loaded)
			sources := commands.NewPRSources(loaded, nil)
			var carried string
			for _, s := range commands.AuthorSections(loaded) {
				carried += s.Body
			}
			for what, text := range map[string]string{"prompt": prompt, "sections": carried, "record": sources.Record, "all": sources.All} {
				if strings.Contains(text, token) || strings.Contains(text, "98765") || strings.Contains(text, "go test ./hidden") {
					t.Errorf("the %s holds commented-out text: %q", what, text)
				}
			}
			if !strings.Contains(carried, "Windows") {
				t.Errorf("sections lost real text: %q", carried)
			}
		})
	}
}

// An unclosed opener is refused, naming the file and the line.
func TestLoadPRNotes_UnclosedCommentIsRefused(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "n.md", "## Testing\n\n- Run `printf '<!--'`\n")
	_, err := loadPRNotes([]string{filepath.Join(dir, "n.md")})
	if err == nil || !strings.Contains(err.Error(), "n.md") || !strings.Contains(err.Error(), "line 3") || !strings.Contains(err.Error(), "&lt;!--") {
		t.Fatalf("error = %v, want one naming the file, line 3 and the alternative", err)
	}
}

// The three inputs of the fourth reviews: a removed comment joins characters
// into a new opener. The file is refused with its name and line, or the later
// section survives in the prompt, the sources and the carried sections; never
// a silent loss.
func TestLoadPRNotes_AJoinedOpenerIsRefusedNotTruncated(t *testing.T) {
	inputs := map[string]struct {
		text string
		line int
	}{
		"468 fence":  {"## Evidence\n\n```html\n<!<!-- remove me -->-- keep this example\n```\n\n## Not verified\n\n- Windows\n", 4},
		"469 inline": {"## Evidence\n\n`<<!-- remove me -->!--`\n\n## Not verified\n\n- Windows was not tested.\n", 3},
		"470 inline": {"## Evidence\n\n`<<!-- hidden -->!--` keep this text\n\n## Not verified\n\n- Windows was not tested.\n", 3},
	}
	for name, tc := range inputs {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, dir, "n.md", tc.text)
			loaded, err := loadPRNotes([]string{filepath.Join(dir, "n.md")})
			if err != nil {
				want := fmt.Sprintf("line %d", tc.line)
				if !strings.Contains(err.Error(), "n.md") || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "&lt;!--") {
					t.Fatalf("error = %v, want one naming the file, %s and the alternative", err, want)
				}
				return
			}
			prompt, _ := prNotesPrompt(loaded)
			sources := commands.NewPRSources(loaded, nil)
			var carried string
			for _, s := range commands.AuthorSections(loaded) {
				carried += s.Body + "\n"
			}
			for what, text := range map[string]string{"prompt": prompt, "record": sources.Record, "sections": carried} {
				if !strings.Contains(text, "Windows") {
					t.Errorf("the %s lost the Not verified text without an error: %q", what, text)
				}
			}
		})
	}
}
