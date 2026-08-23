// Package terminal provides Claude Code-style terminal output with
// rich markdown rendering, syntax highlighting, and styled output.
// No TUI framework - just print/stream/scroll.
package terminal

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"golang.org/x/term"
)

// Writer provides styled terminal output with markdown rendering.
type Writer struct {
	out      io.Writer
	renderer *glamour.TermRenderer
	mu       sync.Mutex

	// Styles
	errorStyle   lipgloss.Style
	warnStyle    lipgloss.Style
	successStyle lipgloss.Style
	infoStyle    lipgloss.Style
	dimStyle     lipgloss.Style
	boldStyle    lipgloss.Style
	codeStyle    lipgloss.Style
	headerStyle  lipgloss.Style
}

// New creates a new terminal Writer with the default output (stdout).
func New() *Writer {
	return NewWithOutput(os.Stdout)
}

// NewWithOutput creates a terminal Writer with a custom output destination.
func NewWithOutput(out io.Writer) *Writer {
	// Initialize glamour renderer with dark theme
	renderer, _ := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(100),
	)

	// Detect color profile for adaptive colors
	// lipgloss uses this internally for AdaptiveColor
	_ = termenv.ColorProfile()

	return &Writer{
		out:      out,
		renderer: renderer,

		// Red for errors
		errorStyle: lipgloss.NewStyle().
			Foreground(lipgloss.AdaptiveColor{Light: "#D00000", Dark: "#FF5555"}).
			Bold(true),

		// Yellow for warnings
		warnStyle: lipgloss.NewStyle().
			Foreground(lipgloss.AdaptiveColor{Light: "#B8860B", Dark: "#FFAA00"}),

		// Green for success
		successStyle: lipgloss.NewStyle().
			Foreground(lipgloss.AdaptiveColor{Light: "#008000", Dark: "#55FF55"}),

		// Blue for info
		infoStyle: lipgloss.NewStyle().
			Foreground(lipgloss.AdaptiveColor{Light: "#0066CC", Dark: "#5599FF"}),

		// Dim for secondary content
		dimStyle: lipgloss.NewStyle().
			Foreground(lipgloss.AdaptiveColor{Light: "#666666", Dark: "#888888"}),

		// Bold
		boldStyle: lipgloss.NewStyle().Bold(true),

		// Inline code style
		codeStyle: lipgloss.NewStyle().
			Background(lipgloss.AdaptiveColor{Light: "#E8E8E8", Dark: "#333333"}).
			Foreground(lipgloss.AdaptiveColor{Light: "#333333", Dark: "#E0E0E0"}).
			Padding(0, 1),

		// Headers
		headerStyle: lipgloss.NewStyle().
			Foreground(lipgloss.AdaptiveColor{Light: "#333333", Dark: "#FFFFFF"}).
			Bold(true).
			BorderStyle(lipgloss.NormalBorder()).
			BorderBottom(true).
			BorderForeground(lipgloss.AdaptiveColor{Light: "#CCCCCC", Dark: "#444444"}),
	}
}

// Print writes text to the terminal.
func (w *Writer) Print(format string, args ...interface{}) {
	w.mu.Lock()
	defer w.mu.Unlock()
	fmt.Fprintf(w.out, format, args...)
}

// Println writes text with a newline.
func (w *Writer) Println(format string, args ...interface{}) {
	w.mu.Lock()
	defer w.mu.Unlock()
	fmt.Fprintf(w.out, format+"\n", args...)
}

// Markdown renders markdown to the terminal with syntax highlighting.
func (w *Writer) Markdown(md string) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.renderer == nil {
		// Fallback to plain text
		fmt.Fprintln(w.out, md)
		return nil
	}

	rendered, err := w.renderer.Render(md)
	if err != nil {
		fmt.Fprintln(w.out, md)
		return err
	}

	fmt.Fprint(w.out, rendered)
	return nil
}

// Error prints an error message in red.
func (w *Writer) Error(format string, args ...interface{}) {
	w.mu.Lock()
	defer w.mu.Unlock()
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintln(w.out, w.errorStyle.Render("error: "+msg))
}

// Warn prints a warning message in yellow.
func (w *Writer) Warn(format string, args ...interface{}) {
	w.mu.Lock()
	defer w.mu.Unlock()
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintln(w.out, w.warnStyle.Render("warning: "+msg))
}

// Success prints a success message in green.
func (w *Writer) Success(format string, args ...interface{}) {
	w.mu.Lock()
	defer w.mu.Unlock()
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintln(w.out, w.successStyle.Render("✓ "+msg))
}

// Info prints an info message in blue.
func (w *Writer) Info(format string, args ...interface{}) {
	w.mu.Lock()
	defer w.mu.Unlock()
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintln(w.out, w.infoStyle.Render(msg))
}

// Dim prints dimmed/secondary text.
func (w *Writer) Dim(format string, args ...interface{}) {
	w.mu.Lock()
	defer w.mu.Unlock()
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintln(w.out, w.dimStyle.Render(msg))
}

// Bold prints bold text.
func (w *Writer) Bold(format string, args ...interface{}) {
	w.mu.Lock()
	defer w.mu.Unlock()
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintln(w.out, w.boldStyle.Render(msg))
}

// Header prints a section header.
func (w *Writer) Header(title string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	fmt.Fprintln(w.out, w.headerStyle.Render(title))
}

// Code prints inline code.
func (w *Writer) Code(code string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	fmt.Fprint(w.out, w.codeStyle.Render(code))
}

// CodeBlock prints a code block with optional language syntax highlighting.
func (w *Writer) CodeBlock(code, language string) error {
	md := fmt.Sprintf("```%s\n%s\n```", language, code)
	return w.Markdown(md)
}

// Newline prints a blank line.
func (w *Writer) Newline() {
	w.mu.Lock()
	defer w.mu.Unlock()
	fmt.Fprintln(w.out)
}

// Divider prints a horizontal divider.
func (w *Writer) Divider() {
	w.mu.Lock()
	defer w.mu.Unlock()
	fmt.Fprintln(w.out, w.dimStyle.Render(strings.Repeat("─", 60)))
}

// Box renders content in a styled box.
func (w *Writer) Box(title, content string) {
	w.mu.Lock()
	defer w.mu.Unlock()

	width := getTerminalWidth()
	boxWidth := min(width-4, 80) // Max 80, leave margin

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.AdaptiveColor{Light: "#CCCCCC", Dark: "#444444"}).
		Padding(1, 2).
		Width(boxWidth)

	titleStyle := lipgloss.NewStyle().Bold(true)

	var output string
	if title != "" {
		output = titleStyle.Render(title) + "\n\n" + content
	} else {
		output = content
	}

	fmt.Fprintln(w.out, boxStyle.Render(output))
}

// CommitMessage renders a git commit message with proper formatting.
func (w *Writer) CommitMessage(message string) {
	w.mu.Lock()
	defer w.mu.Unlock()

	width := getTerminalWidth()
	contentWidth := min(width-8, 76) // Conservative for commit messages

	// Parse into subject and body
	parts := strings.SplitN(strings.TrimSpace(message), "\n", 2)
	subject := parts[0]
	body := ""
	if len(parts) > 1 {
		body = strings.TrimSpace(parts[1])
	}

	// Subject line style - bold, prominent
	subjectStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.AdaptiveColor{Light: "#1a1a2e", Dark: "#ffffff"}).
		Width(contentWidth)

	// Body style - normal, slightly dimmed
	bodyStyle := lipgloss.NewStyle().
		Foreground(lipgloss.AdaptiveColor{Light: "#333333", Dark: "#b0b0b0"}).
		Width(contentWidth)

	// Label style
	labelStyle := lipgloss.NewStyle().
		Foreground(lipgloss.AdaptiveColor{Light: "#666666", Dark: "#666666"}).
		Bold(true)

	// Container style - subtle border on left
	containerStyle := lipgloss.NewStyle().
		BorderStyle(lipgloss.Border{Left: "│"}).
		BorderForeground(lipgloss.AdaptiveColor{Light: "#4CAF50", Dark: "#4CAF50"}).
		PaddingLeft(2).
		MarginLeft(1)

	// Build output
	var sb strings.Builder
	sb.WriteString(labelStyle.Render("COMMIT"))
	sb.WriteString("\n\n")
	sb.WriteString(subjectStyle.Render(subject))

	if body != "" {
		sb.WriteString("\n\n")
		sb.WriteString(bodyStyle.Render(body))
	}

	fmt.Fprintln(w.out, containerStyle.Render(sb.String()))
}

// getTerminalWidth returns the terminal width, defaulting to 80.
func getTerminalWidth() int {
	width, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || width == 0 {
		return 80
	}
	return width
}

// List prints a bulleted list.
func (w *Writer) List(items []string) {
	w.mu.Lock()
	defer w.mu.Unlock()

	for _, item := range items {
		fmt.Fprintln(w.out, "  • "+item)
	}
}

// NumberedList prints a numbered list.
func (w *Writer) NumberedList(items []string) {
	w.mu.Lock()
	defer w.mu.Unlock()

	for i, item := range items {
		fmt.Fprintf(w.out, "  %d. %s\n", i+1, item)
	}
}

// Stream writes a single character or string chunk for streaming output.
// Use for real-time model responses.
func (w *Writer) Stream(chunk string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	fmt.Fprint(w.out, chunk)
}

// StreamEnd finalizes streaming output with a newline.
func (w *Writer) StreamEnd() {
	w.mu.Lock()
	defer w.mu.Unlock()
	fmt.Fprintln(w.out)
}

// Progress prints a progress indicator.
func (w *Writer) Progress(current, total int, message string) {
	w.mu.Lock()
	defer w.mu.Unlock()

	pct := float64(current) / float64(total) * 100
	bar := renderProgressBar(current, total, 30)

	// Use carriage return to overwrite previous line
	fmt.Fprintf(w.out, "\r%s %3.0f%% %s", bar, pct, message)
}

// ProgressDone finalizes progress output.
func (w *Writer) ProgressDone() {
	w.mu.Lock()
	defer w.mu.Unlock()
	fmt.Fprintln(w.out)
}

func renderProgressBar(current, total, width int) string {
	if total == 0 {
		return strings.Repeat("░", width)
	}

	filled := int(float64(current) / float64(total) * float64(width))
	if filled > width {
		filled = width
	}

	return strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
}

// MenuItem represents a menu option.
type MenuItem struct {
	Key         string // Keyboard shortcut (e.g., "1", "a", "q")
	Label       string // Display label
	Description string // Optional description
	Disabled    bool   // Greyed out if true
}

// Menu displays an interactive menu and returns the selected key.
// Returns empty string if user cancels (Ctrl+C) or enters invalid input.
func (w *Writer) Menu(title string, items []MenuItem) string {
	w.mu.Lock()
	defer w.mu.Unlock()

	// Print title
	fmt.Fprintln(w.out)
	fmt.Fprintln(w.out, w.boldStyle.Render(title))
	fmt.Fprintln(w.out)

	// Print menu items
	keyStyle := lipgloss.NewStyle().
		Foreground(lipgloss.AdaptiveColor{Light: "#0066CC", Dark: "#5599FF"}).
		Bold(true)
	disabledStyle := lipgloss.NewStyle().
		Foreground(lipgloss.AdaptiveColor{Light: "#999999", Dark: "#666666"})

	for _, item := range items {
		if item.Disabled {
			line := fmt.Sprintf("  [%s] %s", item.Key, item.Label)
			if item.Description != "" {
				line += " - " + item.Description
			}
			fmt.Fprintln(w.out, disabledStyle.Render(line))
		} else {
			key := keyStyle.Render(fmt.Sprintf("[%s]", item.Key))
			line := fmt.Sprintf("  %s %s", key, item.Label)
			if item.Description != "" {
				line += w.dimStyle.Render(" - "+item.Description)
			}
			fmt.Fprintln(w.out, line)
		}
	}

	fmt.Fprintln(w.out)
	fmt.Fprint(w.out, w.dimStyle.Render("Enter choice: "))

	// Read input
	var input string
	fmt.Scanln(&input)
	input = strings.TrimSpace(strings.ToLower(input))

	// Validate input
	for _, item := range items {
		if !item.Disabled && strings.ToLower(item.Key) == input {
			return item.Key
		}
	}

	return ""
}

// Confirm prompts for yes/no confirmation.
// Returns true if user confirms, false otherwise.
func (w *Writer) Confirm(prompt string, defaultYes bool) bool {
	w.mu.Lock()
	defer w.mu.Unlock()

	hint := "y/N"
	if defaultYes {
		hint = "Y/n"
	}

	fmt.Fprintf(w.out, "%s [%s]: ", prompt, hint)

	var input string
	fmt.Scanln(&input)
	input = strings.TrimSpace(strings.ToLower(input))

	if input == "" {
		return defaultYes
	}
	return input == "y" || input == "yes"
}

// Prompt asks for text input.
func (w *Writer) Prompt(prompt, defaultValue string) string {
	w.mu.Lock()
	defer w.mu.Unlock()

	if defaultValue != "" {
		fmt.Fprintf(w.out, "%s [%s]: ", prompt, defaultValue)
	} else {
		fmt.Fprintf(w.out, "%s: ", prompt)
	}

	var input string
	fmt.Scanln(&input)
	input = strings.TrimSpace(input)

	if input == "" {
		return defaultValue
	}
	return input
}
