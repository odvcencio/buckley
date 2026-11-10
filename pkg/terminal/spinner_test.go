package terminal

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"
)

func TestNewSpinnerWithOutput(t *testing.T) {
	var buf bytes.Buffer
	spinner := NewSpinnerWithOutput(&buf, "Loading")

	if spinner.message != "Loading" {
		t.Errorf("message = %q, want 'Loading'", spinner.message)
	}
	if spinner.out != &buf {
		t.Error("output writer not set correctly")
	}
	if len(spinner.frames) == 0 {
		t.Error("frames should be set")
	}
	if !spinner.showTime {
		t.Error("showTime should be true by default")
	}
}

func TestSpinner_WithoutTime(t *testing.T) {
	var buf bytes.Buffer
	spinner := NewSpinnerWithOutput(&buf, "Loading").WithoutTime()

	if spinner.showTime {
		t.Error("showTime should be false after WithoutTime")
	}
}

func TestSpinner_SetFrames(t *testing.T) {
	var buf bytes.Buffer
	customFrames := []string{"-", "\\", "|", "/"}
	spinner := NewSpinnerWithOutput(&buf, "Loading").SetFrames(customFrames)

	if len(spinner.frames) != len(customFrames) {
		t.Errorf("frames length = %d, want %d", len(spinner.frames), len(customFrames))
	}
}

func TestSpinner_SetMessage(t *testing.T) {
	var buf bytes.Buffer
	spinner := NewSpinnerWithOutput(&buf, "Loading")

	spinner.SetMessage("Processing")
	if spinner.message != "Processing" {
		t.Errorf("message = %q, want 'Processing'", spinner.message)
	}
}

func TestSpinner_Elapsed(t *testing.T) {
	var buf bytes.Buffer
	spinner := NewSpinnerWithOutput(&buf, "Loading")

	// Before start, elapsed should be 0
	if spinner.Elapsed() != 0 {
		t.Error("Elapsed should be 0 before start")
	}

	spinner.Start()
	time.Sleep(50 * time.Millisecond)
	elapsed := spinner.Elapsed()
	spinner.Stop()

	if elapsed < 40*time.Millisecond {
		t.Errorf("Elapsed = %v, expected at least 40ms", elapsed)
	}
}

func TestSpinner_Stop(t *testing.T) {
	var buf bytes.Buffer
	spinner := NewSpinnerWithOutput(&buf, "Loading")

	spinner.Start()
	time.Sleep(100 * time.Millisecond) // Let it render at least once
	spinner.Stop()

	// Should contain clear sequence
	output := buf.String()
	if !strings.Contains(output, "\r") {
		t.Error("Stop should write carriage return")
	}
}

func TestSpinner_StopWithMessage(t *testing.T) {
	var buf bytes.Buffer
	spinner := NewSpinnerWithOutput(&buf, "Loading")

	spinner.Start()
	time.Sleep(50 * time.Millisecond)
	spinner.StopWithMessage("Complete!")

	output := buf.String()
	if !strings.Contains(output, "Complete!") {
		t.Errorf("StopWithMessage output should contain message, got %q", output)
	}
}

func TestSpinner_StopWithSuccess(t *testing.T) {
	var buf bytes.Buffer
	spinner := NewSpinnerWithOutput(&buf, "Loading")

	spinner.Start()
	time.Sleep(50 * time.Millisecond)
	spinner.StopWithSuccess("Done")

	output := buf.String()
	if !strings.Contains(output, "✓") {
		t.Errorf("StopWithSuccess output should contain checkmark, got %q", output)
	}
	if !strings.Contains(output, "Done") {
		t.Errorf("StopWithSuccess output should contain message, got %q", output)
	}
}

func TestSpinner_StopWithSuccess_WithoutTime(t *testing.T) {
	var buf bytes.Buffer
	spinner := NewSpinnerWithOutput(&buf, "Loading").WithoutTime()

	spinner.Start()
	time.Sleep(50 * time.Millisecond)
	spinner.StopWithSuccess("Done")

	output := buf.String()
	if !strings.Contains(output, "✓") {
		t.Errorf("StopWithSuccess output should contain checkmark, got %q", output)
	}
}

func TestSpinner_StopWithError(t *testing.T) {
	var buf bytes.Buffer
	spinner := NewSpinnerWithOutput(&buf, "Loading")

	spinner.Start()
	time.Sleep(50 * time.Millisecond)
	spinner.StopWithError("Failed")

	output := buf.String()
	if !strings.Contains(output, "✗") {
		t.Errorf("StopWithError output should contain X mark, got %q", output)
	}
	if !strings.Contains(output, "Failed") {
		t.Errorf("StopWithError output should contain message, got %q", output)
	}
}

func TestSpinner_StopWithError_WithoutTime(t *testing.T) {
	var buf bytes.Buffer
	spinner := NewSpinnerWithOutput(&buf, "Loading").WithoutTime()

	spinner.Start()
	time.Sleep(50 * time.Millisecond)
	spinner.StopWithError("Failed")

	output := buf.String()
	if !strings.Contains(output, "✗") {
		t.Errorf("StopWithError output should contain X mark, got %q", output)
	}
}

func TestWithSpinner_Success(t *testing.T) {
	result, err := WithSpinner("Testing", func() (string, error) {
		return "success", nil
	})

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if result != "success" {
		t.Errorf("result = %q, want 'success'", result)
	}
}

func TestWithSpinner_Error(t *testing.T) {
	_, err := WithSpinner("Testing", func() (string, error) {
		return "", &testError{msg: "something failed"}
	})

	if err == nil {
		t.Error("expected error, got nil")
	}
	if err.Error() != "something failed" {
		t.Errorf("error = %q, want 'something failed'", err.Error())
	}
}

type testError struct {
	msg string
}

func (e *testError) Error() string {
	return e.msg
}

func TestSpinnerFrames(t *testing.T) {
	if len(SpinnerFrames) == 0 {
		t.Error("SpinnerFrames should not be empty")
	}
	if len(DotsFrames) == 0 {
		t.Error("DotsFrames should not be empty")
	}
}

// A spinner on a file that is not a terminal writes no animation frames and
// no cursor escapes: only its final line.
func TestSpinner_PlainOnAFileThatIsNotATerminal(t *testing.T) {
	read := func(t *testing.T, use func(*Spinner)) string {
		t.Helper()
		file, err := os.CreateTemp(t.TempDir(), "spinner")
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		spinner := NewSpinnerWithOutput(file, "Generating PR...")
		spinner.Start()
		time.Sleep(200 * time.Millisecond) // two frames on a terminal
		use(spinner)
		data, err := os.ReadFile(file.Name())
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}

	success := read(t, func(s *Spinner) { s.WithoutTime().StopWithSuccess("Generated PR") })
	if success != "✓ Generated PR\n" {
		t.Errorf("success output = %q, want one plain line", success)
	}
	failure := read(t, func(s *Spinner) { s.WithoutTime().StopWithError("model request failed") })
	if failure != "✗ model request failed\n" {
		t.Errorf("error output = %q, want one plain line", failure)
	}
	timed := read(t, func(s *Spinner) { s.StopWithSuccess("Generated PR") })
	if !strings.HasPrefix(timed, "✓ Generated PR (") || strings.ContainsAny(timed, "\r\x1b⠋⠙") {
		t.Errorf("timed output = %q, want one plain line with the elapsed time", timed)
	}
	if stopped := read(t, func(s *Spinner) { s.Stop() }); stopped != "" {
		t.Errorf("Stop wrote %q, want nothing", stopped)
	}
	if message := read(t, func(s *Spinner) { s.StopWithMessage("done") }); message != "done\n" {
		t.Errorf("StopWithMessage wrote %q", message)
	}
}
