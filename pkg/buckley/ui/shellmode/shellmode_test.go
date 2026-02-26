package shellmode

import (
	"strings"
	"testing"
	"time"
)

func TestDetectMode(t *testing.T) {
	tests := []struct {
		input       string
		wantMode    Mode
		wantCommand string
	}{
		{"hello", ModeNormal, "hello"},
		{"!ls -la", ModeShell, "ls -la"},
		{"$HOME", ModeEnv, "HOME"},
		{"!git status", ModeShell, "git status"},
		{"$PATH", ModeEnv, "PATH"},
		{"  !echo test  ", ModeShell, "echo test"},
		{"", ModeNormal, ""},
		{"!", ModeShell, ""},
		{"$", ModeEnv, ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			mode, cmd := DetectMode(tt.input)
			if mode != tt.wantMode {
				t.Errorf("DetectMode(%q) mode = %v, want %v", tt.input, mode, tt.wantMode)
			}
			if cmd != tt.wantCommand {
				t.Errorf("DetectMode(%q) cmd = %q, want %q", tt.input, cmd, tt.wantCommand)
			}
		})
	}
}

func TestHandler_Execute(t *testing.T) {
	h := NewHandler("")

	t.Run("simple command", func(t *testing.T) {
		result := h.Execute("echo hello")
		if result.Error != nil {
			t.Fatalf("unexpected error: %v", result.Error)
		}
		if result.ExitCode != 0 {
			t.Errorf("exit code = %d, want 0", result.ExitCode)
		}
		if !strings.Contains(result.Output, "hello") {
			t.Errorf("output = %q, want to contain 'hello'", result.Output)
		}
	})

	t.Run("failing command", func(t *testing.T) {
		result := h.Execute("false")
		if result.ExitCode == 0 {
			t.Error("expected non-zero exit code for 'false'")
		}
	})

	t.Run("command with stderr", func(t *testing.T) {
		result := h.Execute("echo error >&2")
		if !strings.Contains(result.Output, "error") {
			t.Errorf("should capture stderr, got %q", result.Output)
		}
	})
}

func TestHandler_GetEnv(t *testing.T) {
	h := NewHandler("")

	// HOME should be set on most systems
	home := h.GetEnv("HOME")
	if home == "" {
		t.Skip("HOME not set")
	}

	if !strings.HasPrefix(home, "/") {
		t.Errorf("HOME = %q, expected absolute path", home)
	}
}

func TestHandler_ExpandEnv(t *testing.T) {
	h := NewHandler("")

	result := h.ExpandEnv("$HOME/test")
	if strings.Contains(result, "$HOME") {
		t.Errorf("should expand HOME, got %q", result)
	}
}

func TestHandler_History(t *testing.T) {
	h := NewHandler("")

	t.Run("empty history", func(t *testing.T) {
		hist := h.History()
		if len(hist) != 0 {
			t.Error("new handler should have empty history")
		}
	})

	t.Run("add to history", func(t *testing.T) {
		h.Execute("echo one")
		h.Execute("echo two")

		hist := h.History()
		if len(hist) != 2 {
			t.Errorf("history len = %d, want 2", len(hist))
		}
	})

	t.Run("no duplicate consecutive", func(t *testing.T) {
		h.ClearHistory()
		h.Execute("echo same")
		h.Execute("echo same")
		h.Execute("echo same")

		hist := h.History()
		if len(hist) != 1 {
			t.Errorf("should not add consecutive duplicates, got %d", len(hist))
		}
	})

	t.Run("history navigation", func(t *testing.T) {
		h.ClearHistory()
		h.Execute("first")
		h.Execute("second")
		h.Execute("third")

		h.ResetHistoryPosition()

		// Go up
		cmd := h.HistoryUp()
		if cmd != "third" {
			t.Errorf("HistoryUp() = %q, want 'third'", cmd)
		}

		cmd = h.HistoryUp()
		if cmd != "second" {
			t.Errorf("HistoryUp() = %q, want 'second'", cmd)
		}

		// Go down
		cmd = h.HistoryDown()
		if cmd != "third" {
			t.Errorf("HistoryDown() = %q, want 'third'", cmd)
		}

		// Past end
		cmd = h.HistoryDown()
		if cmd != "" {
			t.Errorf("past end should return empty, got %q", cmd)
		}
	})
}

func TestHandler_Timeout(t *testing.T) {
	h := NewHandler("")
	h.SetTimeout(100 * time.Millisecond)

	result := h.Execute("sleep 5")
	if result.Error == nil && result.ExitCode == 0 {
		t.Error("expected timeout error")
	}
}

func TestHandler_Cancel(t *testing.T) {
	h := NewHandler("")
	h.SetTimeout(10 * time.Second)

	done := make(chan Result)
	go func() {
		done <- h.Execute("sleep 10")
	}()

	// Give it a moment to start
	time.Sleep(50 * time.Millisecond)

	if !h.IsRunning() {
		t.Error("command should be running")
	}

	h.Cancel()

	result := <-done
	if result.ExitCode == 0 {
		t.Error("cancelled command should have non-zero exit")
	}
}

func TestHandler_OutputCallback(t *testing.T) {
	h := NewHandler("")

	var gotOutput string
	var gotIsError bool

	h.SetOutputCallback(func(output string, isError bool) {
		gotOutput = output
		gotIsError = isError
	})

	h.Execute("echo callback")

	if !strings.Contains(gotOutput, "callback") {
		t.Errorf("callback output = %q, want 'callback'", gotOutput)
	}
	if gotIsError {
		t.Error("should not be error")
	}
}

func TestExpandQuickCommand(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"gs", "git status"},
		{"gd", "git diff"},
		{"unknown", "unknown"},
		{"", ""},
	}

	for _, tt := range tests {
		got := ExpandQuickCommand(tt.input)
		if got != tt.want {
			t.Errorf("ExpandQuickCommand(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestHandler_AlreadyRunning(t *testing.T) {
	h := NewHandler("")
	h.SetTimeout(10 * time.Second)

	// Start a long command
	done := make(chan struct{})
	go func() {
		h.Execute("sleep 5")
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)

	// Try to run another command
	result := h.Execute("echo test")
	if result.Error != ErrAlreadyRunning {
		t.Errorf("expected ErrAlreadyRunning, got %v", result.Error)
	}

	h.Cancel()
	<-done
}

func BenchmarkExecute(b *testing.B) {
	h := NewHandler("")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h.Execute("echo benchmark")
	}
}
