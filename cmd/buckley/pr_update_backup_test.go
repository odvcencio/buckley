package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// previousPRTexts returns the saved PR texts under the repository's git
// directory.
func previousPRTexts(t *testing.T, r prRepo) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(r.work, ".git", "buckley", "pr-*.md"))
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// A run against a head that already has an open PR writes a new text over the
// PR. A body that was edited by hand after the first run was lost.
func TestCreatePR_UpdateKeepsThePreviousText(t *testing.T) {
	r := newPRRepo(t)
	gh := newPreflightGh(prListJSON(prListEntry(5, "feature", "main", "o")))
	gh.responses["pr view"] = []byte(`{"title":"add: thing (edited)","body":"## Summary\r\n\r\nEdited by hand on GitHub.\r\n"}`)

	var err error
	stderr := captureStderr(t, func() {
		captureStdout(t, func() { _, err = createPR(gh.run, testPRResult, testPRPlan(""), prCommandOptions{}) })
	})
	if err != nil {
		t.Fatalf("createPR: %v", err)
	}

	saved := previousPRTexts(t, r)
	if len(saved) != 1 || !strings.Contains(filepath.Base(saved[0]), "pr-5-") {
		t.Fatalf("saved texts = %v, want one file for PR 5", saved)
	}
	data, err := os.ReadFile(saved[0])
	if err != nil {
		t.Fatal(err)
	}
	if want := "Title: add: thing (edited)\n\n## Summary\n\nEdited by hand on GitHub.\n"; string(data) != want {
		t.Errorf("saved text = %q, want %q", data, want)
	}
	if got, err := readPRText(saved[0], ""); err != nil || got.Title != "add: thing (edited)" {
		t.Errorf("the saved file does not read back as a --body-file: %+v, %v", got, err)
	}
	if !strings.Contains(stderr, saved[0]) || !strings.Contains(stderr, "--body-file") {
		t.Errorf("stderr does not say where the previous text is: %s", stderr)
	}

	edited := false
	for _, call := range gh.calls {
		if strings.Join(call[:2], " ") == "pr edit" {
			edited = true
		}
	}
	if !edited {
		t.Error("the PR was not updated")
	}
}

func TestCreatePR_UpdateWithoutAChangeOrACopy(t *testing.T) {
	t.Run("the same text needs no copy", func(t *testing.T) {
		r := newPRRepo(t)
		gh := newPreflightGh(prListJSON(prListEntry(5, "feature", "main", "o")))
		gh.responses["pr view"] = []byte(`{"title":"` + testPRResult.Title + `","body":"## Summary\n\nAdds a thing.\n\n## Changes\n\n- Add it\n"}`)
		var err error
		captureStdout(t, func() { _, err = createPR(gh.run, testPRResult, testPRPlan(""), prCommandOptions{}) })
		if err != nil {
			t.Fatalf("createPR: %v", err)
		}
		if saved := previousPRTexts(t, r); len(saved) != 0 {
			t.Errorf("saved %v although nothing changes", saved)
		}
	})

	t.Run("a failed read is reported and the update goes on", func(t *testing.T) {
		r := newPRRepo(t)
		gh := newPreflightGh(prListJSON(prListEntry(5, "feature", "main", "o")))
		gh.errs["pr view"] = errors.New("gh pr view: HTTP 502")
		var err error
		stderr := captureStderr(t, func() {
			captureStdout(t, func() { _, err = createPR(gh.run, testPRResult, testPRPlan(""), prCommandOptions{}) })
		})
		if err != nil {
			t.Fatalf("createPR: %v", err)
		}
		if !strings.Contains(stderr, "replaced without a copy") {
			t.Errorf("stderr does not report the missing copy: %s", stderr)
		}
		if saved := previousPRTexts(t, r); len(saved) != 0 {
			t.Errorf("saved %v", saved)
		}
	})
}

// Review of #468, finding 7: two updates within one second chose the same file
// name, and the second overwrote the first copy of a hand-edited body.
func TestCreatePR_TwoUpdatesInOneSecondKeepBothCopies(t *testing.T) {
	r := newPRRepo(t)
	// Both saves use the same instant, so the test never depends on the clock.
	previousNow := prBackupNow
	prBackupNow = func() time.Time { return time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { prBackupNow = previousNow })
	for i, body := range []string{"First hand edit.", "Second hand edit."} {
		gh := newPreflightGh(prListJSON(prListEntry(5, "feature", "main", "o")))
		gh.responses["pr view"] = []byte(`{"title":"add: thing","body":"` + body + `"}`)
		captureStderr(t, func() {
			captureStdout(t, func() { createPR(gh.run, testPRResult, testPRPlan(""), prCommandOptions{}) })
		})
		if got := len(previousPRTexts(t, r)); got != i+1 {
			t.Fatalf("after update %d there are %d saved copies, want %d", i+1, got, i+1)
		}
	}
	var all string
	for _, path := range previousPRTexts(t, r) {
		data, _ := os.ReadFile(path)
		all += string(data)
	}
	if !strings.Contains(all, "First hand edit.") || !strings.Contains(all, "Second hand edit.") {
		t.Errorf("a copy was lost:\n%s", all)
	}
}
