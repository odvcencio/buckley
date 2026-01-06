package terminal

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// Spinner provides a simple terminal spinner for async operations.
type Spinner struct {
	out       io.Writer
	message   string
	frames    []string
	current   int
	done      chan struct{}
	mu        sync.Mutex
	style     lipgloss.Style
	startTime time.Time
	showTime  bool
}

// SpinnerFrames are the default spinner animation frames.
var SpinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// DotsFrames are simpler dots animation.
var DotsFrames = []string{"⣾", "⣽", "⣻", "⢿", "⡿", "⣟", "⣯", "⣷"}

// NewSpinner creates a new spinner with the given message.
func NewSpinner(message string) *Spinner {
	return NewSpinnerWithOutput(os.Stdout, message)
}

// NewSpinnerWithOutput creates a spinner with custom output.
func NewSpinnerWithOutput(out io.Writer, message string) *Spinner {
	return &Spinner{
		out:      out,
		message:  message,
		frames:   SpinnerFrames,
		done:     make(chan struct{}),
		showTime: true, // Show elapsed time by default
		style: lipgloss.NewStyle().
			Foreground(lipgloss.AdaptiveColor{Light: "#0066CC", Dark: "#5599FF"}),
	}
}

// WithoutTime disables elapsed time display.
func (s *Spinner) WithoutTime() *Spinner {
	s.showTime = false
	return s
}

// SetFrames sets custom animation frames.
func (s *Spinner) SetFrames(frames []string) *Spinner {
	s.frames = frames
	return s
}

// SetMessage updates the spinner message.
func (s *Spinner) SetMessage(message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.message = message
}

// Start begins the spinner animation.
func (s *Spinner) Start() {
	s.startTime = time.Now()
	go s.run()
}

func (s *Spinner) run() {
	ticker := time.NewTicker(80 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
			s.mu.Lock()
			frame := s.frames[s.current%len(s.frames)]
			msg := s.message
			showTime := s.showTime
			startTime := s.startTime
			s.current++
			s.mu.Unlock()

			if showTime && !startTime.IsZero() {
				elapsed := time.Since(startTime).Round(time.Second)
				fmt.Fprintf(s.out, "\r%s %s (%s)", s.style.Render(frame), msg, elapsed)
			} else {
				fmt.Fprintf(s.out, "\r%s %s", s.style.Render(frame), msg)
			}
		}
	}
}

// Elapsed returns the time since the spinner started.
func (s *Spinner) Elapsed() time.Duration {
	if s.startTime.IsZero() {
		return 0
	}
	return time.Since(s.startTime)
}

// Stop stops the spinner and clears the line.
func (s *Spinner) Stop() {
	close(s.done)
	// Clear the line
	fmt.Fprintf(s.out, "\r\033[K")
}

// StopWithMessage stops and prints a final message.
func (s *Spinner) StopWithMessage(message string) {
	close(s.done)
	fmt.Fprintf(s.out, "\r\033[K%s\n", message)
}

// StopWithSuccess stops and prints a success message.
func (s *Spinner) StopWithSuccess(message string) {
	elapsed := s.Elapsed().Round(time.Millisecond)
	successStyle := lipgloss.NewStyle().
		Foreground(lipgloss.AdaptiveColor{Light: "#008000", Dark: "#55FF55"})
	close(s.done)
	if s.showTime && elapsed > 0 {
		fmt.Fprintf(s.out, "\r\033[K%s %s (%s)\n", successStyle.Render("✓"), message, elapsed)
	} else {
		fmt.Fprintf(s.out, "\r\033[K%s %s\n", successStyle.Render("✓"), message)
	}
}

// StopWithError stops and prints an error message.
func (s *Spinner) StopWithError(message string) {
	elapsed := s.Elapsed().Round(time.Millisecond)
	errorStyle := lipgloss.NewStyle().
		Foreground(lipgloss.AdaptiveColor{Light: "#D00000", Dark: "#FF5555"}).
		Bold(true)
	close(s.done)
	if s.showTime && elapsed > 0 {
		fmt.Fprintf(s.out, "\r\033[K%s %s (%s)\n", errorStyle.Render("✗"), message, elapsed)
	} else {
		fmt.Fprintf(s.out, "\r\033[K%s %s\n", errorStyle.Render("✗"), message)
	}
}

// WithSpinner runs a function with a spinner active.
// Returns the function result and stops the spinner appropriately.
func WithSpinner[T any](message string, fn func() (T, error)) (T, error) {
	spinner := NewSpinner(message)
	spinner.Start()

	result, err := fn()

	if err != nil {
		spinner.StopWithError(err.Error())
	} else {
		spinner.StopWithSuccess(message + " done")
	}

	return result, err
}
