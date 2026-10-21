package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"

	"m31labs.dev/buckley/pkg/commitmsg"
	"m31labs.dev/buckley/pkg/terminal"
)

type delayedCommitRunner struct {
	fakeCommitRunner
}

func (r *delayedCommitRunner) Run(ctx context.Context) (*commitRunResult, error) {
	time.Sleep(240 * time.Millisecond)
	return r.fakeCommitRunner.Run(ctx)
}

func captureCommitProgress(t *testing.T, tty bool, fn func()) string {
	t.Helper()
	var out, reader *os.File
	var err error
	if tty {
		reader, out, err = pty.Open()
		if errors.Is(err, pty.ErrUnsupported) {
			t.Skip("PTY unavailable on this platform")
		}
	} else {
		reader, out, err = os.Pipe()
	}
	if err != nil {
		t.Fatal(err)
	}
	oldStdout := os.Stdout
	os.Stdout = out
	t.Cleanup(func() {
		os.Stdout = oldStdout
		_ = out.Close()
		_ = reader.Close()
	})
	fn()
	_ = out.Close()
	os.Stdout = oldStdout
	data, _ := io.ReadAll(reader) // Linux PTYs return EIO after the slave closes.
	return string(data)
}

func TestRunCommitGeneration_ProgressOutput(t *testing.T) {
	oldQuiet := quietMode
	t.Cleanup(func() { quietMode = oldQuiet })
	cause := errors.New("backend unavailable")
	for _, tc := range []struct {
		name            string
		tty, min, quiet bool
		runErr, msgErr  error
		progress        bool
	}{
		{name: "pipe"},
		{name: "minimal_pipe", min: true},
		{name: "minimal_tty", tty: true, min: true},
		{name: "quiet_tty", tty: true, quiet: true},
		{name: "interactive", tty: true, progress: true},
		{name: "pipe_runner_error", runErr: cause},
		{name: "pipe_message_error", msgErr: cause},
		{name: "interactive_runner_error", tty: true, runErr: cause, progress: true},
		{name: "interactive_message_error", tty: true, msgErr: cause, progress: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			quietMode = tc.quiet
			opts := commitCommandOptions{compactOutput: tc.min}
			want := fakeMergeResult("add", "", []string{"useful result"})
			want.Error = tc.msgErr
			runner := &delayedCommitRunner{fakeCommitRunner{result: want, err: tc.runErr}}
			var got *commitRunResult
			var runErr error
			output := captureCommitProgress(t, tc.tty, func() {
				got, runErr = runCommitGeneration(context.Background(), runner, opts)
			})
			if !errors.Is(runErr, tc.runErr) || runner.calls != 1 {
				t.Fatalf("error=%v, calls=%d", runErr, runner.calls)
			}
			if tc.runErr == nil && got != want {
				t.Fatal("generation result changed")
			}
			if tc.progress {
				if !strings.Contains(output, "Generating commit message...") || !strings.Contains(output, "\r") {
					t.Fatalf("interactive progress missing: %q", output)
				}
				if (tc.runErr != nil || tc.msgErr != nil) && !strings.Contains(output, cause.Error()) {
					t.Fatalf("interactive error missing: %q", output)
				}
			} else if output != "" {
				t.Fatalf("unexpected progress output: %q", output)
			}
			t.Logf("tty=%t minimal=%t quiet=%t progress_bytes=%d calls=%d error_preserved=true result_preserved=true", tc.tty, tc.min, tc.quiet, len(output), runner.calls)
		})
	}
}

func TestHandleCommitPrompt_MinimalRegeneration(t *testing.T) {
	oldStdin, oldTermOut, oldQuiet := os.Stdin, termOut, quietMode
	t.Cleanup(func() { os.Stdin, termOut, quietMode = oldStdin, oldTermOut, oldQuiet })
	quietMode = false
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "success"},
		{name: "failure", err: errors.New("backend unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input, err := os.CreateTemp(t.TempDir(), "stdin")
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			_, _ = fmt.Fprintln(input, "r")
			_, _ = input.Seek(0, io.SeekStart)
			os.Stdin = input
			runner := &delayedCommitRunner{fakeCommitRunner{result: fakeMergeResult("add", "", nil), err: tc.err}}
			var action, message string
			output := captureCommitProgress(t, true, func() {
				termOut = terminal.NewWithOutput(os.Stdout)
				action, message = handleCommitPrompt("original", runner, context.Background(), commitCommandOptions{compactOutput: true}, nil, commitmsg.ChangeMetadata{})
			})
			if action != "regenerate" || runner.calls != 1 || strings.Contains(output, "Generating commit") || strings.Contains(output, "\x1b[K") {
				t.Fatalf("action=%q calls=%d output=%q", action, runner.calls, output)
			}
			if tc.err != nil {
				if message != "original" || !strings.Contains(output, tc.err.Error()) {
					t.Fatalf("failure lost original message or error: %q, %q", message, output)
				}
			} else if message == "original" || message == "" {
				t.Fatalf("regenerated message missing: %q", message)
			}
			t.Logf("minimal_regeneration=%s spinner_frames=0 error_visible=%t message_preserved=true", tc.name, tc.err == nil || strings.Contains(output, tc.err.Error()))
		})
	}
}
