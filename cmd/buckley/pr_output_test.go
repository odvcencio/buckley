package main

import (
	"os"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/terminal"
)

// noTerminalNoise fails when out has what a terminal display adds: cursor
// escapes, spinner frames or box-drawing.
func noTerminalNoise(t *testing.T, out string) {
	t.Helper()
	if i := strings.IndexAny(out, "\x1b\r⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏╭╰│─"); i >= 0 {
		t.Errorf("stdout has terminal noise at byte %d:\n%q", i, out)
	}
}

// With output redirected to a file, a dry run wrote hundreds of spinner
// frames, a boxed first copy of the text and then the text. A script had to
// cut the text out. Stdout that is not a terminal now holds the text once, in
// the form --body-file reads.
func TestRunPRCommand_DryRunStdoutIsTheTextAndNothingElse(t *testing.T) {
	r := newPRRepo(t)
	r.commit(t, "feature", "f.txt")
	r.park(t)
	stubPRDependencies(t, func() error { return nil })
	stubPRModel(t)

	var err error
	var out string
	stderr := captureStderr(t, func() {
		out = captureStdout(t, func() { err = runPRCommand([]string{"--dry-run", "--head", "feature"}) })
	})
	if err != nil {
		t.Fatalf("runPRCommand: %v", err)
	}

	want := "Title: add: feature file\n\n" +
		"## Summary\n\nAdds the feature file.\n\n" +
		"## Changes\n\n- Add f.txt\n\n" +
		"## Not verified\n\nThe author supplied no notes. Buckley wrote this text from the commits and the diff, and ran nothing.\n"
	if out != want {
		t.Fatalf("stdout =\n%q\nwant\n%q", out, want)
	}
	noTerminalNoise(t, out)
	if text := parsePRText(out); formatPRText(text) != out {
		t.Errorf("stdout does not read back as a --body-file: %+v", text)
	}
	for _, status := range []string{"Using model: test-model", "Generated PR"} {
		if !strings.Contains(stderr, status) {
			t.Errorf("stderr lacks the status %q:\n%s", status, stderr)
		}
	}
}

func TestRunPRCommand_RealRunStdoutIsTheTextThenTheURL(t *testing.T) {
	r := newPRRepo(t)
	r.commit(t, "feature", "f.txt")
	r.park(t)
	stubPRDependencies(t, func() error { return nil })
	stubPRModel(t)
	stubGh(t)
	installFakeGh(t, "[]", "https://github.com/o/r/pull/12\n")

	var err error
	var out string
	stderr := captureStderr(t, func() {
		out = captureStdout(t, func() { err = runPRCommand([]string{"--yes", "--cost=false", "--head", "feature"}) })
	})
	if err != nil {
		t.Fatalf("runPRCommand: %v", err)
	}

	if !strings.HasPrefix(out, "Title: add: feature file\n\n## Summary\n") || !strings.HasSuffix(out, "ran nothing.\nhttps://github.com/o/r/pull/12\n") {
		t.Fatalf("stdout must be the text and then the PR's URL on its own line:\n%q", out)
	}
	if strings.Count(out, "## Summary") != 1 {
		t.Errorf("stdout has the text %d times, want once", strings.Count(out, "## Summary"))
	}
	noTerminalNoise(t, out)
	for _, status := range []string{"Pushed: ", "PR created: https://github.com/o/r/pull/12"} {
		if !strings.Contains(stderr, status) {
			t.Errorf("stderr lacks the status %q:\n%s", status, stderr)
		}
	}
}

// On a terminal the display is unchanged: header, preview box, then the text.
func TestRunPRCommand_TerminalKeepsTheHeaderAndPreview(t *testing.T) {
	r := newPRRepo(t)
	r.commit(t, "feature", "f.txt")
	r.park(t)
	stubPRDependencies(t, func() error { return nil })
	stubPRModel(t)
	previous := stdoutIsTerminalFn
	t.Cleanup(func() { stdoutIsTerminalFn = previous })
	stdoutIsTerminalFn = func() bool { return true }

	var err error
	out := captureStdout(t, func() {
		// termOut holds the stdout it was created with, so point it at the
		// captured one for this run.
		previousOut := termOut
		termOut = terminal.NewWithOutput(os.Stdout)
		defer func() { termOut = previousOut }()
		err = runPRCommand([]string{"--dry-run", "--cost=false", "--head", "feature"})
	})
	if err != nil {
		t.Fatalf("runPRCommand: %v", err)
	}
	for _, want := range []string{"Using model: test-model", "PULL REQUEST: feature → main", "╭", "Title: add: feature file\n\n## Summary"} {
		if !strings.Contains(out, want) {
			t.Errorf("terminal output lacks %q:\n%s", want, out)
		}
	}
}
