package terminal

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"
)

// Spinner provides a simple terminal spinner for async operations.
type Spinner struct {
	out       io.Writer
	message   string
	frames    []string
	current   int
	done      chan struct{}
	wg        sync.WaitGroup
	mu        sync.Mutex
	stopOnce  sync.Once
	style     lipgloss.Style
	startTime time.Time
	showTime  bool
	// plain is set when out is a file that is not a terminal (a redirect or a
	// pipe). Animation frames and cursor escapes are noise there: a 60-second
	// run wrote about 750 frames into the file. A plain spinner writes only
	// its final line.
	plain bool
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
	file, isFile := out.(*os.File)
	return &Spinner{
		out:      out,
		plain:    isFile && !term.IsTerminal(int(file.Fd())),
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

// Start begins the spinner animation. A plain spinner only starts its clock.
func (s *Spinner) Start() {
	s.startTime = time.Now()
	if s.plain {
		return
	}
	s.wg.Add(1)
	go s.run()
}

func (s *Spinner) run() {
	defer s.wg.Done()
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

// stop signals the run goroutine to exit (idempotent).
func (s *Spinner) stop() {
	s.stopOnce.Do(func() { close(s.done) })
	s.wg.Wait()
}

// clearLine is what a final line starts with: the escape that erases the
// animation frame, or nothing when no frame was written.
func (s *Spinner) clearLine() string {
	if s.plain {
		return ""
	}
	return "\r\033[K"
}

// Stop stops the spinner and clears the line.
func (s *Spinner) Stop() {
	s.stop()
	// Clear the line
	fmt.Fprint(s.out, s.clearLine())
}

// StopWithMessage stops and prints a final message.
func (s *Spinner) StopWithMessage(message string) {
	s.stop()
	fmt.Fprintf(s.out, "%s%s\n", s.clearLine(), message)
}

// StopWithSuccess stops and prints a success message.
func (s *Spinner) StopWithSuccess(message string) {
	elapsed := s.Elapsed().Round(time.Millisecond)
	successStyle := lipgloss.NewStyle().
		Foreground(lipgloss.AdaptiveColor{Light: "#008000", Dark: "#55FF55"})
	s.stop()
	mark := successStyle.Render("✓")
	if s.plain {
		mark = "✓"
	}
	if s.showTime && elapsed > 0 {
		fmt.Fprintf(s.out, "%s%s %s (%s)\n", s.clearLine(), mark, message, elapsed)
	} else {
		fmt.Fprintf(s.out, "%s%s %s\n", s.clearLine(), mark, message)
	}
}

// StopWithError stops and prints an error message.
func (s *Spinner) StopWithError(message string) {
	elapsed := s.Elapsed().Round(time.Millisecond)
	errorStyle := lipgloss.NewStyle().
		Foreground(lipgloss.AdaptiveColor{Light: "#D00000", Dark: "#FF5555"}).
		Bold(true)
	s.stop()
	mark := errorStyle.Render("✗")
	if s.plain {
		mark = "✗"
	}
	if s.showTime && elapsed > 0 {
		fmt.Fprintf(s.out, "%s%s %s (%s)\n", s.clearLine(), mark, message, elapsed)
	} else {
		fmt.Fprintf(s.out, "%s%s %s\n", s.clearLine(), mark, message)
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
