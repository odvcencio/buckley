package model

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/config"
)

func TestCodexLineWriter_FragmentedRecords(t *testing.T) {
	var lines [][]byte
	writer := codexLineWriter{
		all:    codexCaptureBuffer{limit: maxCodexStdoutBytes},
		onLine: func(line []byte) { lines = append(lines, line) },
	}
	for _, part := range []string{"first", "\nsec", "ond\n\nlast"} {
		if n, err := writer.Write([]byte(part)); err != nil || n != len(part) {
			t.Fatalf("Write = %d, %v", n, err)
		}
	}
	writer.Flush()
	if len(lines) != 4 {
		t.Fatalf("received %d records, want 4", len(lines))
	}
	for i, want := range []string{"first", "second", "", "last"} {
		if string(lines[i]) != want {
			t.Fatalf("record %d = %q, want %q", i, lines[i], want)
		}
	}
	if writer.all.buf.String() != "first\nsecond\n\nlast" || writer.pending != nil {
		t.Fatal("capture or flush is incomplete")
	}
}

func TestCodexLineWriter_OversizedRecordRecovers(t *testing.T) {
	var lines []string
	writer := codexLineWriter{
		all:    codexCaptureBuffer{limit: maxCodexStdoutBytes},
		onLine: func(line []byte) { lines = append(lines, string(line)) },
	}
	chunk := bytes.Repeat([]byte("x"), 64*1024)
	for i := 0; i <= maxCodexStdoutBytes/len(chunk); i++ {
		_, _ = writer.Write(chunk)
		if len(writer.pending) > maxCodexStdoutBytes || writer.all.buf.Len() > maxCodexStdoutBytes {
			t.Fatal("capture grew beyond its limit")
		}
	}
	_, _ = writer.Write([]byte("\nnext\nlast"))
	writer.Flush()
	if !writer.all.truncated || !writer.lineTruncated || writer.pending != nil {
		t.Fatal("oversized capture was not marked incomplete or released")
	}
	if len(lines) != 2 || lines[0] != "next" || lines[1] != "last" {
		t.Fatalf("callback received truncated records or lost later records: %q", lines)
	}
}

func TestRunCodexCLICommand_CaptureLimits(t *testing.T) {
	for _, tc := range []struct {
		name, stream string
		exitCode     int
		wantError    bool
	}{
		{"stdout", "stdout", 0, true},
		{"stdout_failed", "stdout", 7, true},
		{"stderr", "stderr", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			var lastRecord string
			result, err := runCodexCLICommand(context.Background(), CodexCLICommand{
				Name:         executable,
				Args:         []string{"-test.run=^TestCodexCaptureHelperProcess$"},
				Env:          append(os.Environ(), "BUCKLEY_TEST_CAPTURE_STREAM="+tc.stream, "BUCKLEY_TEST_CAPTURE_EXIT="+strconv.Itoa(tc.exitCode)),
				OnStdoutLine: func(line []byte) { lastRecord = string(line) },
			})
			if (err != nil) != tc.wantError {
				t.Fatalf("run error = %v", err)
			}
			if len(result.Stdout) > maxCodexStdoutBytes || len(result.Stderr) > maxCodexStderrBytes+len("\n[stderr truncated]") {
				t.Fatalf("capture exceeds ceiling: stdout=%d stderr=%d", len(result.Stdout), len(result.Stderr))
			}
			if tc.stream == "stdout" && !strings.Contains(err.Error(), "execution evidence are incomplete") {
				t.Fatalf("missing explicit capture failure: %v", err)
			}
			if tc.exitCode != 0 {
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) || exitErr.ExitCode() != tc.exitCode {
					t.Fatalf("lost child exit error: %v", err)
				}
			}
			if lastRecord != `{"type":"turn.completed","usage":{"input_tokens":3,"output_tokens":2}}` {
				t.Fatalf("child was not drained to completion: %q", lastRecord)
			}
			if tc.stream == "stderr" {
				usage, exact := parseCodexUsage(result.Stdout)
				if !exact || usage.TotalTokens != 5 || !bytes.HasSuffix(result.Stderr, []byte("[stderr truncated]")) {
					t.Fatal("stderr ceiling lost exact usage or truncation marker")
				}
			}
		})
	}
}

func TestCodexCaptureHelperProcess(t *testing.T) {
	stream := os.Getenv("BUCKLEY_TEST_CAPTURE_STREAM")
	if stream == "" {
		return
	}
	output, limit := os.Stdout, maxCodexStdoutBytes
	if stream == "stderr" {
		output, limit = os.Stderr, maxCodexStderrBytes
	}
	chunk := bytes.Repeat([]byte("x"), 64*1024)
	for written := 0; written <= limit; written += len(chunk) {
		_, _ = output.Write(chunk)
	}
	_, _ = output.Write([]byte("\n"))
	fmt.Fprintln(os.Stdout, `{"type":"turn.completed","usage":{"input_tokens":3,"output_tokens":2}}`)
	exitCode, _ := strconv.Atoi(os.Getenv("BUCKLEY_TEST_CAPTURE_EXIT"))
	os.Exit(exitCode)
}

func TestFormatCodexCLIError_BoundedDiagnostics(t *testing.T) {
	result := CodexCLICommandResult{
		Stdout: bytes.Repeat([]byte("a"), maxCodexStdoutBytes),
		Stderr: bytes.Repeat([]byte("b"), maxCodexStderrBytes),
	}
	err := formatCodexCLIError(errors.New("failed"), result)
	if len(err.Error()) > 2*maxCodexDiagnosticBytes+100 || !strings.Contains(err.Error(), "diagnostic truncated") {
		t.Fatalf("unbounded error diagnostic: %d bytes", len(err.Error()))
	}
}

func TestReadCodexLastMessage_OversizedResponse(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "last-message-*")
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxCodexStdoutBytes + 1); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	content, err := readCodexLastMessage(file.Name(), []byte("fallback"))
	if err == nil || content != "" || !strings.Contains(err.Error(), "response is incomplete") {
		t.Fatalf("oversized response fell back silently: %q, %v", content, err)
	}
}

// Exactly one cleanup call, for a real preparation error and for an empty
// workspace (review of #472, finding 3).
func TestCodexCLIProvider_WorkspacePreparationFailureCleansUp(t *testing.T) {
	for _, mode := range []string{"preparation_error", "empty_workspace"} {
		t.Run(mode, func(t *testing.T) {
			provider := NewCodexCLIProvider(config.CodexConfig{}, config.SandboxConfig{}, config.ApprovalConfig{})
			root := t.TempDir()
			snapshot, err := NewReviewSnapshot(ReviewSnapshotHead, root, root, strings.Repeat("1", 40), nil)
			if err != nil {
				t.Fatal(err)
			}
			cleanups := 0
			provider.reviewWorkspace = func(context.Context, *ReviewSnapshot) (string, func(), error) {
				if mode == "preparation_error" {
					return root, func() { cleanups++ }, errors.New("materialize failed")
				}
				return "", func() { cleanups++ }, nil
			}
			provider.runner = func(context.Context, CodexCLICommand) (CodexCLICommandResult, error) {
				t.Fatal("started Codex after workspace preparation failure")
				return CodexCLICommandResult{}, nil
			}
			if _, err := provider.ChatCompletion(context.Background(), ChatRequest{ReviewSnapshot: snapshot}); err == nil {
				t.Fatal("preparation failure returned no error")
			}
			if cleanups != 1 {
				t.Fatalf("cleanup called %d times, want exactly 1", cleanups)
			}
		})
	}
}

// A successful turn that exceeds the capture limit while its command output says
// "session not found" must not be run again in a new thread: native shell
// actions would run twice (review of #472, finding 1).
func TestCodexCLIProvider_CaptureLimitDoesNotTriggerResumeRecovery(t *testing.T) {
	provider := NewCodexCLIProvider(config.CodexConfig{}, config.SandboxConfig{}, config.ApprovalConfig{})
	provider.setSessionThread("session-1", "thread-1")
	calls := 0
	provider.runner = func(context.Context, CodexCLICommand) (CodexCLICommandResult, error) {
		calls++
		return CodexCLICommandResult{Stdout: []byte(`{"type":"item.completed","item":{"aggregated_output":"grep: session not found"}}` + "\n")},
			fmt.Errorf("%w (%d bytes); response and execution evidence are incomplete", errCodexCaptureLimit, maxCodexStdoutBytes)
	}
	_, err := provider.ChatCompletion(context.Background(), ChatRequest{SessionID: "session-1", Messages: []Message{{Role: "user", Content: "run it"}}})
	if err == nil || !errors.Is(err, errCodexCaptureLimit) {
		t.Fatalf("err = %v, want the capture-limit error", err)
	}
	if calls != 1 {
		t.Fatalf("Codex ran %d times, want 1: the turn was replayed", calls)
	}
	if provider.threadForSession("session-1") != "thread-1" {
		t.Fatal("the valid thread mapping was deleted")
	}

	// A real resume failure still recovers.
	calls = 0
	provider.runner = func(_ context.Context, cmd CodexCLICommand) (CodexCLICommandResult, error) {
		calls++
		if calls == 1 {
			return CodexCLICommandResult{Stderr: []byte("thread not found")}, errors.New("exit status 1")
		}
		return CodexCLICommandResult{Stdout: []byte(`{"type":"thread.started","thread_id":"thread-2"}` + "\n")}, nil
	}
	_, _ = provider.ChatCompletion(context.Background(), ChatRequest{SessionID: "session-1", Messages: []Message{{Role: "user", Content: "again"}}})
	if calls != 2 {
		t.Fatalf("a real resume failure ran Codex %d times, want 2", calls)
	}
}

// Command output reached the error through the stdout fallback (review of
// #472, finding 2).
func TestCodexCLIProvider_CaptureLimitErrorHoldsNoCommandOutput(t *testing.T) {
	const secret = "SENTINEL-SECRET-TOKEN-4711"
	provider := NewCodexCLIProvider(config.CodexConfig{}, config.SandboxConfig{}, config.ApprovalConfig{})
	provider.runner = func(context.Context, CodexCLICommand) (CodexCLICommandResult, error) {
		return CodexCLICommandResult{
				Stdout: []byte(`{"type":"item.completed","item":{"command":"cat .env","aggregated_output":"` + secret + `"}}` + "\n" + secret + " /home/someone/.config\n"),
				Stderr: []byte("stderr " + secret),
			},
			fmt.Errorf("%w (%d bytes); response and execution evidence are incomplete", errCodexCaptureLimit, maxCodexStdoutBytes)
	}
	_, err := provider.ChatCompletion(context.Background(), ChatRequest{Messages: []Message{{Role: "user", Content: "go"}}})
	if err == nil {
		t.Fatal("no error")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "/home/someone") {
		t.Fatalf("the error repeats command output: %v", err)
	}
	if !strings.Contains(err.Error(), "capture limit") {
		t.Fatalf("the error does not name the limit: %v", err)
	}
	if got := formatCodexCLIError(err, CodexCLICommandResult{Stdout: []byte(secret)}); strings.Contains(got.Error(), secret) {
		t.Fatalf("formatCodexCLIError repeats output: %v", got)
	}
}

func BenchmarkCodexLineWriter_VerboseOutput(b *testing.B) {
	for _, callback := range []bool{false, true} {
		b.Run(fmt.Sprintf("callback_%v", callback), func(b *testing.B) {
			chunk := bytes.Repeat([]byte("x"), 64*1024)
			chunk[len(chunk)-1] = '\n'
			b.ReportAllocs()
			b.SetBytes(64 * 1024 * 1024)
			for i := 0; i < b.N; i++ {
				writer := codexLineWriter{all: codexCaptureBuffer{limit: maxCodexStdoutBytes}}
				if callback {
					writer.onLine = func([]byte) {}
				}
				for written := 0; written < 64*1024*1024; written += len(chunk) {
					_, _ = writer.Write(chunk)
				}
				writer.Flush()
			}
		})
	}
}
