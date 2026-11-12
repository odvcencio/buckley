package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFormatAndParsePRText_RoundTrip(t *testing.T) {
	for _, text := range []prText{
		{Title: "add(api): retry budget", Body: "## Summary\n\nAdds the budget.\n\n## Changes\n\n- One"},
		{Title: "fix: handle \"Title: x\" lines", Body: "Title: not the title\n\n| a | b |\n|---|---|"},
		{Title: "document: readme", Body: "One line."},
		{Title: "add: template body", Body: "## Summary\n\nChange\n\n- [ ] A  \n  continued\n- [x] B"},
	} {
		if got := parsePRText(formatPRText(text)); got != text {
			t.Errorf("round trip of %+v = %+v", text, got)
		}
	}

	// A body that starts with blank lines (a template may start that way): after
	// the round trip its blank lines at the start are gone and nothing else
	// changed, so its top-level headings and checkbox lines are the same.
	lead := prText{Title: "add: x", Body: "\n\n## Summary\n\nChange\n\n- [ ] A  \n- [X] B"}
	got := parsePRText(formatPRText(lead))
	if want := strings.TrimLeft(lead.Body, "\n"); got.Body != want || got.Title != lead.Title {
		t.Errorf("round trip of a body with leading blank lines = %+v, want body %q", got, want)
	}
	if !strings.HasPrefix(got.Body, "## Summary") || !strings.Contains(got.Body, "- [ ] A  \n- [X] B") {
		t.Errorf("headings or checkbox lines changed: %q", got.Body)
	}

	if got := parsePRText("Title:  spaced title  \r\n\r\nBody with CRLF.\r\n"); got != (prText{Title: "spaced title", Body: "Body with CRLF."}) {
		t.Errorf("CRLF text = %+v", got)
	}
	if got := parsePRText("## Summary\n\nNo title line.\n"); got != (prText{Body: "## Summary\n\nNo title line."}) {
		t.Errorf("text without a title line = %+v", got)
	}
}

func TestReadPRText(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "full.md", "Title: add: thing\n\nBody.\n")
	writeFile(t, dir, "body-only.md", "Body.\n")
	writeFile(t, dir, "title-only.md", "Title: add: thing\n\n")

	if got, err := readPRText(filepath.Join(dir, "full.md"), ""); err != nil || got != (prText{Title: "add: thing", Body: "Body."}) {
		t.Errorf("full file = %+v, %v", got, err)
	}
	if got, err := readPRText(filepath.Join(dir, "full.md"), "fix: other"); err != nil || got.Title != "fix: other" {
		t.Errorf("--title must replace the file's title: %+v, %v", got, err)
	}
	if got, err := readPRText(filepath.Join(dir, "body-only.md"), "fix: other"); err != nil || got != (prText{Title: "fix: other", Body: "Body."}) {
		t.Errorf("body-only file with --title = %+v, %v", got, err)
	}
	if _, err := readPRText(filepath.Join(dir, "body-only.md"), ""); err == nil || !strings.Contains(err.Error(), "has no title") {
		t.Errorf("body-only file without --title: error = %v", err)
	}
	if _, err := readPRText(filepath.Join(dir, "title-only.md"), ""); err == nil || !strings.Contains(err.Error(), "has no body") {
		t.Errorf("title-only file: error = %v", err)
	}
	if _, err := readPRText(filepath.Join(dir, "missing.md"), ""); err == nil {
		t.Error("missing file: want an error")
	}
}

func TestParsePRCommandOptions_TextFlags(t *testing.T) {
	opts, err := parsePRCommandOptions([]string{"--title", "  fix: x  ", "--output", "o.md", "--body-file", "b.md"})
	if err != nil {
		t.Fatalf("parsePRCommandOptions: %v", err)
	}
	if opts.title != "fix: x" || opts.output != "o.md" || opts.bodyFile != "b.md" {
		t.Fatalf("opts = %+v", opts)
	}
	if _, err := parsePRCommandOptions([]string{"--title", "two\nlines"}); err == nil {
		t.Error("a title of two lines must be refused")
	}
	if _, err := parsePRCommandOptions([]string{"--body-file", "b.md", "--context-file", "n.md"}); err == nil || !strings.Contains(err.Error(), "--context-file has no effect") {
		t.Errorf("--body-file with --context-file: error = %v", err)
	}
}

// The model is called again on the real run, so the text a dry run showed is
// not the text that gets posted. --output saves what the dry run showed and
// --body-file posts that file without calling a model.
func TestRunPRCommand_BodyFilePostsWhatTheDryRunWrote(t *testing.T) {
	r := newPRRepo(t)
	r.commit(t, "feature", "f.txt")
	r.park(t)
	stubPRDependencies(t, func() error { return nil })
	stubPRModel(t)
	file := filepath.Join(t.TempDir(), "pr.md")

	var err error
	var out string
	stderr := captureStderr(t, func() {
		out = captureStdout(t, func() {
			err = runPRCommand([]string{"--dry-run", "--cost=false", "--head", "feature", "--output", file})
		})
	})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	written, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("the dry run did not write --output: %v", err)
	}
	reviewed := parsePRText(string(written))
	if reviewed.Title != "add: feature file" || !strings.HasPrefix(reviewed.Body, "## Summary\n\nAdds the feature file.") {
		t.Fatalf("--output holds %+v", reviewed)
	}
	if !strings.Contains(out, reviewed.Body) {
		t.Errorf("the dry run printed a text other than the one it wrote:\n%s", out)
	}
	if !strings.Contains(stderr, "--body-file "+file) {
		t.Errorf("stderr does not say how to post the file:\n%s", stderr)
	}

	// The author edits the file, then posts it. No model may run.
	edited := string(written) + "\n## Known gaps\n\nWindows is not covered.\n"
	if err := os.WriteFile(file, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	previous := newPRCommandRuntimeFn
	t.Cleanup(func() { newPRCommandRuntimeFn = previous })
	newPRCommandRuntimeFn = func(prCommandOptions) (*prCommandRuntime, func(), error) {
		t.Error("--body-file built a model runtime")
		return nil, func() {}, errors.New("no model in this test")
	}
	stubGh(t)
	g := installFakeGh(t, "[]", "https://github.com/o/r/pull/12\n")

	captureStdout(t, func() { err = runPRCommand([]string{"--yes", "--head", "feature", "--body-file", file}) })
	if err != nil {
		t.Fatalf("posting --body-file: %v", err)
	}
	want := parsePRText(edited)
	if body := ghBodyArg(t, g, "pr create"); body != want.Body {
		t.Errorf("gh received the body\n%s\n\nwant the file's\n%s", body, want.Body)
	}
	for _, call := range g.calls(t) {
		if strings.Join(call[:2], " ") != "pr create" {
			continue
		}
		for i, arg := range call {
			if arg == "--title" && call[i+1] != want.Title {
				t.Errorf("gh received the title %q, want the file's %q", call[i+1], want.Title)
			}
		}
	}
}

func TestRunPRCommand_TitleFlagReplacesTheGeneratedTitle(t *testing.T) {
	r := newPRRepo(t)
	r.commit(t, "feature", "f.txt")
	r.park(t)
	stubPRDependencies(t, func() error { return nil })
	stubPRModel(t)

	var err error
	out := captureStdout(t, func() {
		err = runPRCommand([]string{"--dry-run", "--cost=false", "--head", "feature", "--title", "fix(feature): the title the author chose"})
	})
	if err != nil {
		t.Fatalf("runPRCommand: %v", err)
	}
	if !strings.Contains(out, "Title: fix(feature): the title the author chose\n") || strings.Contains(out, "add: feature file") {
		t.Errorf("the output does not use the --title value:\n%s", out)
	}
}

// Under -C, --output and --body-file name files where the caller launched,
// like --context-file.
func TestRunPRCommand_TextFilesResolveFromTheLaunchDirectory(t *testing.T) {
	isolateWorkDirGlobals(t)
	r := newPRRepo(t)
	r.commit(t, "feature", "f.txt")
	r.park(t)
	launch := t.TempDir()
	writeFile(t, launch, "pr.md", "Title: add: thing\n\nBody from the launch directory.\n")
	chdirTemp(t, launch)

	var err error
	out := captureStdout(t, func() {
		err = runPRCommand([]string{"-C", r.work, "--head", "feature", "--dry-run", "--body-file", "pr.md", "--output", "copy.md"})
	})
	if err != nil {
		t.Fatalf("runPRCommand: %v", err)
	}
	if !strings.Contains(out, "Body from the launch directory.") {
		t.Errorf("the body was not read from the launch directory:\n%s", out)
	}
	copied, err := os.ReadFile(filepath.Join(launch, "copy.md"))
	if err != nil {
		t.Fatalf("--output was not written in the launch directory: %v", err)
	}
	if string(copied) != "Title: add: thing\n\nBody from the launch directory.\n" {
		t.Errorf("--output = %q", copied)
	}
	if _, err := os.Stat(filepath.Join(r.work, "copy.md")); err == nil {
		t.Error("--output was written into the -C target")
	}
}
