package terminal

import (
	"bytes"
	"strings"
	"testing"
)

func TestWriterPrint(t *testing.T) {
	var buf bytes.Buffer
	w := NewWithOutput(&buf)

	w.Print("Hello %s", "World")
	if got := buf.String(); got != "Hello World" {
		t.Errorf("Print = %q, want 'Hello World'", got)
	}
}

func TestWriterPrintln(t *testing.T) {
	var buf bytes.Buffer
	w := NewWithOutput(&buf)

	w.Println("Hello %s", "World")
	if got := buf.String(); got != "Hello World\n" {
		t.Errorf("Println = %q, want 'Hello World\\n'", got)
	}
}

func TestWriterError(t *testing.T) {
	var buf bytes.Buffer
	w := NewWithOutput(&buf)

	w.Error("something went wrong")
	got := buf.String()
	if !strings.Contains(got, "error:") {
		t.Errorf("Error output should contain 'error:', got %q", got)
	}
	if !strings.Contains(got, "something went wrong") {
		t.Errorf("Error output should contain message, got %q", got)
	}
}

func TestWriterWarn(t *testing.T) {
	var buf bytes.Buffer
	w := NewWithOutput(&buf)

	w.Warn("be careful")
	got := buf.String()
	if !strings.Contains(got, "warning:") {
		t.Errorf("Warn output should contain 'warning:', got %q", got)
	}
}

func TestWriterSuccess(t *testing.T) {
	var buf bytes.Buffer
	w := NewWithOutput(&buf)

	w.Success("it worked")
	got := buf.String()
	if !strings.Contains(got, "✓") {
		t.Errorf("Success output should contain '✓', got %q", got)
	}
}

func TestWriterInfo(t *testing.T) {
	var buf bytes.Buffer
	w := NewWithOutput(&buf)

	w.Info("FYI")
	got := buf.String()
	if !strings.Contains(got, "FYI") {
		t.Errorf("Info output should contain message, got %q", got)
	}
}

func TestWriterList(t *testing.T) {
	var buf bytes.Buffer
	w := NewWithOutput(&buf)

	w.List([]string{"one", "two", "three"})
	got := buf.String()
	if !strings.Contains(got, "• one") {
		t.Errorf("List should contain bullet points, got %q", got)
	}
	if !strings.Contains(got, "• two") {
		t.Errorf("List should contain all items, got %q", got)
	}
}

func TestWriterNumberedList(t *testing.T) {
	var buf bytes.Buffer
	w := NewWithOutput(&buf)

	w.NumberedList([]string{"first", "second"})
	got := buf.String()
	if !strings.Contains(got, "1. first") {
		t.Errorf("NumberedList should contain numbered items, got %q", got)
	}
	if !strings.Contains(got, "2. second") {
		t.Errorf("NumberedList should contain all items, got %q", got)
	}
}

func TestWriterStream(t *testing.T) {
	var buf bytes.Buffer
	w := NewWithOutput(&buf)

	w.Stream("Hello")
	w.Stream(" ")
	w.Stream("World")
	w.StreamEnd()

	if got := buf.String(); got != "Hello World\n" {
		t.Errorf("Stream = %q, want 'Hello World\\n'", got)
	}
}

func TestWriterMarkdown(t *testing.T) {
	var buf bytes.Buffer
	w := NewWithOutput(&buf)

	err := w.Markdown("# Hello\n\nThis is **bold** text.")
	if err != nil {
		t.Fatalf("Markdown error: %v", err)
	}

	got := buf.String()
	// Glamour transforms markdown - exact output depends on terminal
	// Just verify we got some output
	if got == "" {
		t.Error("Markdown produced no output")
	}
}

func TestWriterCodeBlock(t *testing.T) {
	var buf bytes.Buffer
	w := NewWithOutput(&buf)

	err := w.CodeBlock("func main() {}", "go")
	if err != nil {
		t.Fatalf("CodeBlock error: %v", err)
	}

	got := buf.String()
	if got == "" {
		t.Error("CodeBlock produced no output")
	}
}

func TestWriterProgress(t *testing.T) {
	var buf bytes.Buffer
	w := NewWithOutput(&buf)

	w.Progress(50, 100, "processing")
	got := buf.String()

	// Should contain progress bar characters
	if !strings.Contains(got, "█") && !strings.Contains(got, "░") {
		t.Errorf("Progress should contain bar chars, got %q", got)
	}
	if !strings.Contains(got, "50%") {
		t.Errorf("Progress should contain percentage, got %q", got)
	}
}

func TestRenderProgressBar(t *testing.T) {
	tests := []struct {
		current, total, width int
		wantFilled            int
	}{
		{0, 100, 10, 0},
		{50, 100, 10, 5},
		{100, 100, 10, 10},
		{25, 100, 20, 5},
		{0, 0, 10, 0}, // edge case: total is 0
	}

	for _, tt := range tests {
		bar := renderProgressBar(tt.current, tt.total, tt.width)
		filled := strings.Count(bar, "█")
		if filled != tt.wantFilled {
			t.Errorf("renderProgressBar(%d, %d, %d) filled=%d, want %d",
				tt.current, tt.total, tt.width, filled, tt.wantFilled)
		}
	}
}

func TestWriterDivider(t *testing.T) {
	var buf bytes.Buffer
	w := NewWithOutput(&buf)

	w.Divider()
	got := buf.String()
	if !strings.Contains(got, "─") {
		t.Errorf("Divider should contain line chars, got %q", got)
	}
}

func TestWriterNewline(t *testing.T) {
	var buf bytes.Buffer
	w := NewWithOutput(&buf)

	w.Newline()
	if got := buf.String(); got != "\n" {
		t.Errorf("Newline = %q, want '\\n'", got)
	}
}

func TestWriterCommitMessage(t *testing.T) {
	var buf bytes.Buffer
	w := NewWithOutput(&buf)

	msg := `feat(terminal): Add commit message formatting

- Parse subject/body for distinct styling
- Add terminal width detection`

	w.CommitMessage(msg)
	got := buf.String()

	// Should contain the COMMIT label
	if !strings.Contains(got, "COMMIT") {
		t.Errorf("CommitMessage should contain 'COMMIT' label, got %q", got)
	}

	// Should contain the subject line
	if !strings.Contains(got, "feat(terminal): Add commit message formatting") {
		t.Errorf("CommitMessage should contain subject, got %q", got)
	}

	// Should contain body content
	if !strings.Contains(got, "Parse subject/body") {
		t.Errorf("CommitMessage should contain body, got %q", got)
	}

	// Should contain the left border character
	if !strings.Contains(got, "│") {
		t.Errorf("CommitMessage should contain left border '│', got %q", got)
	}
}

func TestWriterCommitMessageSubjectOnly(t *testing.T) {
	var buf bytes.Buffer
	w := NewWithOutput(&buf)

	w.CommitMessage("fix: simple one-liner")
	got := buf.String()

	if !strings.Contains(got, "fix: simple one-liner") {
		t.Errorf("CommitMessage should handle subject-only messages, got %q", got)
	}
}

func TestGetTerminalWidth(t *testing.T) {
	// Should return a reasonable default when not in a TTY
	width := getTerminalWidth()
	if width < 40 || width > 500 {
		t.Errorf("getTerminalWidth() = %d, expected 40-500 range", width)
	}
}
