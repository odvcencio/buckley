package main

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/commitmsg"
	"m31labs.dev/buckley/pkg/oneshot/commands"
)

// deadlineRunner records whether its context was still usable.
type deadlineRunner struct {
	sawErr    error
	remaining time.Duration
}

func (r *deadlineRunner) Run(ctx context.Context) (*commitRunResult, error) {
	r.sawErr = ctx.Err()
	if deadline, ok := ctx.Deadline(); ok {
		r.remaining = time.Until(deadline)
	}
	if r.sawErr != nil {
		return nil, r.sawErr
	}
	return &commitRunResult{Commit: &commands.CommitResult{Action: "fix", Subject: "guard the nil map", Body: []string{"Return early"}}}, nil
}

// answerPrompt feeds one line to the confirmation prompt.
func answerPrompt(t *testing.T, line string) {
	t.Helper()
	stdin, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString(line + "\n"); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	previous := os.Stdin
	os.Stdin = stdin
	t.Cleanup(func() { os.Stdin = previous })
}

// The deadline of the first generation was still the deadline when the
// author answered [r] at the prompt. After reading for longer than --timeout,
// regeneration failed at once.
func TestHandleCommitPrompt_RegenerateGetsItsOwnDeadline(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stageRepo(t, map[string]string{"main.go": "package main\n"})
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Minute))
	defer cancel()
	runner := &deadlineRunner{}
	answerPrompt(t, "r")

	var action, message string
	captureStdout(t, func() {
		action, message = handleCommitPrompt("old message", runner, expired,
			commitCommandOptions{compactOutput: true, timeout: 90 * time.Second, breaking: breakingAuto}, nil, commitmsg.ChangeMetadata{})
	})
	if runner.sawErr != nil {
		t.Fatalf("the regeneration ran under the expired deadline: %v", runner.sawErr)
	}
	if action != "regenerate" || !strings.HasPrefix(message, "fix: guard the nil map") {
		t.Fatalf("action = %q, message = %q; want the regenerated message", action, message)
	}
	if runner.remaining <= 0 || runner.remaining > 90*time.Second {
		t.Errorf("the regeneration had %s left, want the --timeout of 90s", runner.remaining)
	}
}
