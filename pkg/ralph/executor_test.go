// pkg/ralph/executor_test.go
package ralph

import (
	"context"
	"os"
	"reflect"
	"runtime"
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

func TestExecutor_HandleScheduleAction_BackendActions(t *testing.T) {
	baseConfig := func() *ControlConfig {
		return &ControlConfig{
			Mode: ModeSequential,
			Rotation: RotationConfig{
				Order: []string{"claude", "codex", "buckley"},
			},
			Backends: map[string]BackendConfig{
				"claude":  {Command: "claude", Enabled: true},
				"codex":   {Command: "codex", Enabled: true},
				"buckley": {Command: "buckley", Enabled: true},
			},
		}
	}

	tests := []struct {
		name          string
		action        *ScheduleAction
		lastBackend   string
		expectedOrder []string
	}{
		{
			name:          "set_backend",
			action:        &ScheduleAction{Action: "set_backend", Backend: "codex"},
			expectedOrder: []string{"codex", "claude", "buckley"},
		},
		{
			name:          "rotate_backend",
			action:        &ScheduleAction{Action: "rotate_backend"},
			expectedOrder: []string{"codex", "buckley", "claude"},
		},
		{
			name:          "next_backend",
			action:        &ScheduleAction{Action: "next_backend"},
			lastBackend:   "codex",
			expectedOrder: []string{"buckley", "claude", "codex"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := baseConfig()
			orch := NewOrchestrator(NewBackendRegistry(), cfg)
			sess := NewSession(SessionConfig{
				SessionID: "test-schedule",
				Prompt:    "test",
				Sandbox:   t.TempDir(),
			})
			exec := NewExecutor(sess, &mockHeadlessRunner{}, nil, WithOrchestrator(orch))
			exec.lastBackend = tt.lastBackend

			exec.handleScheduleAction(tt.action)

			got := orch.Config().Rotation.Order
			if !reflect.DeepEqual(got, tt.expectedOrder) {
				t.Fatalf("expected order %v, got %v", tt.expectedOrder, got)
			}
			if orch.currentBackend != 0 {
				t.Errorf("expected currentBackend reset to 0, got %d", orch.currentBackend)
			}
		})
	}
}

func TestExecutor_OverridePaused(t *testing.T) {
	cfg := &ControlConfig{
		Mode: ModeSequential,
		Backends: map[string]BackendConfig{
			"test": {Type: BackendTypeInternal, Enabled: true},
		},
		Override: OverrideConfig{Paused: true},
	}

	orch := NewOrchestrator(NewBackendRegistry(), cfg)
	sess := NewSession(SessionConfig{
		SessionID:     "test-pause-override",
		Prompt:        "test",
		Sandbox:       t.TempDir(),
		MaxIterations: 2,
	})

	mock := &mockHeadlessRunner{}
	exec := NewExecutor(sess, mock, nil, WithOrchestrator(orch))

	// Start executor in a goroutine; it should block on pause
	done := make(chan struct{})
	go func() {
		exec.Run(context.Background())
		close(done)
	}()

	// Give it time to enter the pause loop
	time.Sleep(100 * time.Millisecond)

	// Verify no iterations ran while paused
	if mock.processCount != 0 {
		t.Errorf("expected 0 iterations while paused, got %d", mock.processCount)
	}

	// Unpause
	orch.UpdateConfig(&ControlConfig{
		Mode: ModeSequential,
		Backends: map[string]BackendConfig{
			"test": {Type: BackendTypeInternal, Enabled: true},
		},
		Override: OverrideConfig{Paused: false},
	})

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("executor did not complete after unpause")
	}
}

func TestExecutor_Verification(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh -c")
	}

	sess := NewSession(SessionConfig{
		SessionID:     "test-verify",
		Prompt:        "Build something",
		Sandbox:       t.TempDir(),
		MaxIterations: 1,
		VerifyCommand: `echo "ok  pkg/foo  0.5s" && echo "ok  pkg/bar  0.2s" && echo "FAIL  pkg/baz  0.3s"`,
	})

	mock := &mockHeadlessRunner{}
	exec := NewExecutor(sess, mock, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := exec.Run(ctx)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	// Since we're running without an orchestrator, results slice is empty.
	// The verify command still runs and sets lastError on failure.
	// With 2 ok + 1 FAIL, the verify command itself succeeds (exit 0),
	// but the fail count should be tracked.
}

func TestParseTestResults_GoFormat(t *testing.T) {
	output := `ok  	github.com/foo/bar/pkg/a	0.5s
ok  	github.com/foo/bar/pkg/b	0.2s
FAIL	github.com/foo/bar/pkg/c	0.3s
ok  	github.com/foo/bar/pkg/d	(cached)
`
	passed, failed := parseTestResults(output)
	if passed != 3 {
		t.Errorf("expected 3 passed, got %d", passed)
	}
	if failed != 1 {
		t.Errorf("expected 1 failed, got %d", failed)
	}
}

func TestParseTestResults_GenericFormat(t *testing.T) {
	output := `Running tests...
42 tests passed
3 tests failed
Done.`
	passed, failed := parseTestResults(output)
	if passed != 42 {
		t.Errorf("expected 42 passed, got %d", passed)
	}
	if failed != 3 {
		t.Errorf("expected 3 failed, got %d", failed)
	}
}

func TestParseTestResults_Empty(t *testing.T) {
	passed, failed := parseTestResults("")
	if passed != 0 || failed != 0 {
		t.Errorf("expected 0/0, got %d/%d", passed, failed)
	}
}
