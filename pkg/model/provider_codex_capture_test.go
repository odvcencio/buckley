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

func TestCodexCLIProvider_WorkspacePreparationFailureCleansUp(t *testing.T) {
	for _, empty := range []bool{true, false} {
		t.Run(fmt.Sprintf("empty_%v", empty), func(t *testing.T) {
			provider := NewCodexCLIProvider(config.CodexConfig{}, config.SandboxConfig{}, config.ApprovalConfig{})
			root := t.TempDir()
			snapshot, err := NewReviewSnapshot(ReviewSnapshotHead, root, root, strings.Repeat("1", 40), nil)
			if err != nil {
				t.Fatal(err)
			}
			cleaned := false
			provider.reviewWorkspace = func(context.Context, *ReviewSnapshot) (string, func(), error) {
				dir := root
				if empty {
					dir = ""
				}
				return dir, func() { cleaned = true }, nil
			}
			provider.runner = func(context.Context, CodexCLICommand) (CodexCLICommandResult, error) {
				t.Fatal("started Codex after workspace preparation failure")
				return CodexCLICommandResult{}, nil
			}
			if _, err := provider.ChatCompletion(context.Background(), ChatRequest{ReviewSnapshot: snapshot}); err == nil || !cleaned {
				t.Fatalf("workspace preparation leaked cleanup: cleaned=%v err=%v", cleaned, err)
			}
		})
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
