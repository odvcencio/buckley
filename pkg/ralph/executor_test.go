// pkg/ralph/executor_test.go
package ralph

import (
	"context"
	"os"
	"testing"
	"time"
)

type mockHeadlessRunner struct {
	processCount int
	shouldError  bool
}

func (m *mockHeadlessRunner) ProcessInput(ctx context.Context, input string) error {
	m.processCount++
	if m.shouldError {
		return context.Canceled
	}
	return nil
}

func (m *mockHeadlessRunner) State() string {
	return "idle"
}

func TestExecutor_RunsIterations(t *testing.T) {
	sess := NewSession(SessionConfig{
		SessionID:     "test-exec",
		Prompt:        "Build something",
		Sandbox:       t.TempDir(),
		MaxIterations: 3,
	})

	mock := &mockHeadlessRunner{}
	exec := NewExecutor(sess, mock, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Run should complete after max iterations
	err := exec.Run(ctx)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	if mock.processCount != 3 {
		t.Errorf("expected 3 iterations, got %d", mock.processCount)
	}
}

func TestExecutor_RespectsTimeout(t *testing.T) {
	sess := NewSession(SessionConfig{
		SessionID:     "test-timeout",
		Prompt:        "Build something",
		Sandbox:       t.TempDir(),
		Timeout:       100 * time.Millisecond,
		MaxIterations: 1000, // High limit to ensure timeout hits first
	})

	mock := &mockHeadlessRunner{}
	exec := NewExecutor(sess, mock, nil)

	ctx := context.Background()
	start := time.Now()

	err := exec.Run(ctx)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	// Should have stopped due to timeout, not max iterations
	if elapsed > 500*time.Millisecond {
		t.Errorf("executor ran too long: %v", elapsed)
	}
}

func TestExecutor_PauseResume(t *testing.T) {
	sess := NewSession(SessionConfig{
		SessionID: "test-pause",
		Prompt:    "Build something",
		Sandbox:   t.TempDir(),
	})

	mock := &mockHeadlessRunner{}
	exec := NewExecutor(sess, mock, nil)

	// Start the session to transition to running state
	sess.Start()
	if err := sess.TransitionTo(StateRunning); err != nil {
		t.Fatalf("transition to running failed: %v", err)
	}

	// Test pause
	if err := exec.Pause(); err != nil {
		t.Fatalf("Pause failed: %v", err)
	}
	if sess.State() != StatePaused {
		t.Errorf("expected StatePaused, got %s", sess.State())
	}

	// Test resume
	if err := exec.Resume(); err != nil {
		t.Fatalf("Resume failed: %v", err)
	}
	if sess.State() != StateRunning {
		t.Errorf("expected StateRunning, got %s", sess.State())
	}
}

func TestExecutor_NilGuards(t *testing.T) {
	// Test nil executor
	var nilExec *Executor
	if err := nilExec.Run(context.Background()); err == nil {
		t.Error("expected error for nil executor")
	}
	if err := nilExec.Pause(); err == nil {
		t.Error("expected error for nil executor Pause")
	}
	if err := nilExec.Resume(); err == nil {
		t.Error("expected error for nil executor Resume")
	}

	// Test executor with nil session
	exec := &Executor{}
	if err := exec.Run(context.Background()); err == nil {
		t.Error("expected error for nil session")
	}
}

func TestExecutor_PromptReload(t *testing.T) {
	dir := t.TempDir()
	promptFile := dir + "/prompt.txt"

	// Write initial prompt
	if err := os.WriteFile(promptFile, []byte("Initial prompt"), 0644); err != nil {
		t.Fatalf("write prompt file: %v", err)
	}

	sess := NewSession(SessionConfig{
		SessionID:     "test-reload",
		Prompt:        "Initial prompt",
		PromptFile:    promptFile,
		Sandbox:       dir,
		MaxIterations: 2,
	})

	mock := &mockHeadlessRunner{}
	exec := NewExecutor(sess, mock, nil)

	// Run first iteration
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Start a goroutine to update the prompt file after a short delay
	go func() {
		time.Sleep(50 * time.Millisecond)
		os.WriteFile(promptFile, []byte("Updated prompt"), 0644)
	}()

	err := exec.Run(ctx)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	// Note: Due to timing, we can't guarantee the reload happened,
	// but we're testing the code path doesn't panic or error
	if mock.processCount != 2 {
		t.Errorf("expected 2 iterations, got %d", mock.processCount)
	}
}
