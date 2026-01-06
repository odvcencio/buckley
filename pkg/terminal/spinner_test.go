package terminal

import (
	"bytes"
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
