// pkg/ralph/backend_internal_test.go
package ralph

import (
	"context"
	"errors"
	"testing"
	"time"
)

// mockHeadlessRunnerForBackend implements HeadlessRunner for testing InternalBackend.
type mockHeadlessRunnerForBackend struct {
	processCount  int
	lastInput     string
	shouldError   bool
	errorToReturn error
	state         string
	processDelay  time.Duration
}

func (m *mockHeadlessRunnerForBackend) ProcessInput(ctx context.Context, input string) error {
	m.processCount++
	m.lastInput = input

	if m.processDelay > 0 {
		select {
		case <-time.After(m.processDelay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	if m.shouldError {
		if m.errorToReturn != nil {
			return m.errorToReturn
		}
		return errors.New("mock error")
	}
	return nil
}

func (m *mockHeadlessRunnerForBackend) State() string {
	if m.state != "" {
		return m.state
	}
	return "idle"
}

func TestInternalBackend_Name(t *testing.T) {
	backend := NewInternalBackend("buckley", nil, InternalOptions{})

	if got := backend.Name(); got != "buckley" {
		t.Errorf("Name() = %q, want %q", got, "buckley")
	}
}

func TestInternalBackend_Name_CustomName(t *testing.T) {
	backend := NewInternalBackend("custom-backend", nil, InternalOptions{})

	if got := backend.Name(); got != "custom-backend" {
		t.Errorf("Name() = %q, want %q", got, "custom-backend")
	}
}

func TestInternalBackend_Available_Default(t *testing.T) {
	backend := NewInternalBackend("test", nil, InternalOptions{})

	// Should be available by default
	if !backend.Available() {
		t.Error("Available() = false, want true by default")
	}
}

func TestInternalBackend_SetAvailable(t *testing.T) {
	backend := NewInternalBackend("test", nil, InternalOptions{})

	// Set unavailable
	backend.SetAvailable(false)
	if backend.Available() {
		t.Error("Available() = true after SetAvailable(false)")
	}

	// Set available again
	backend.SetAvailable(true)
	if !backend.Available() {
		t.Error("Available() = false after SetAvailable(true)")
	}
}

func TestInternalBackend_Execute_Success(t *testing.T) {
	mock := &mockHeadlessRunnerForBackend{}
	backend := NewInternalBackend("buckley", mock, InternalOptions{})

	ctx := context.Background()
	req := BackendRequest{
		Prompt:      "Test prompt",
		SandboxPath: "/tmp/test",
		Iteration:   1,
		SessionID:   "test-session",
	}

	result, err := backend.Execute(ctx, req)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if result == nil {
		t.Fatal("Execute() returned nil result")
	}

	if result.Backend != "buckley" {
		t.Errorf("result.Backend = %q, want %q", result.Backend, "buckley")
	}

	if result.Duration <= 0 {
		t.Error("result.Duration should be positive")
	}

	if result.Error != nil {
		t.Errorf("result.Error = %v, want nil", result.Error)
	}

	if mock.processCount != 1 {
		t.Errorf("ProcessInput called %d times, want 1", mock.processCount)
	}

	if mock.lastInput != "Test prompt" {
		t.Errorf("ProcessInput received %q, want %q", mock.lastInput, "Test prompt")
	}
}

func TestInternalBackend_Execute_Error(t *testing.T) {
	expectedErr := errors.New("execution failed")
	mock := &mockHeadlessRunnerForBackend{
		shouldError:   true,
		errorToReturn: expectedErr,
	}
	backend := NewInternalBackend("buckley", mock, InternalOptions{})

	ctx := context.Background()
	req := BackendRequest{
		Prompt:    "Test prompt",
		Iteration: 1,
		SessionID: "test-session",
	}

	result, err := backend.Execute(ctx, req)

	// Execute should not return error directly; error is captured in result
	if err != nil {
		t.Fatalf("Execute() error = %v, want nil (error should be in result)", err)
	}

	if result == nil {
		t.Fatal("Execute() returned nil result")
	}

	if result.Error == nil {
		t.Error("result.Error = nil, want error")
	}

	if !errors.Is(result.Error, expectedErr) {
		t.Errorf("result.Error = %v, want %v", result.Error, expectedErr)
	}
}

func TestInternalBackend_Execute_ContextCanceled(t *testing.T) {
	mock := &mockHeadlessRunnerForBackend{
		processDelay: 100 * time.Millisecond,
	}
	backend := NewInternalBackend("buckley", mock, InternalOptions{})

	ctx, cancel := context.WithCancel(context.Background())

	// Cancel immediately
	cancel()

	req := BackendRequest{
		Prompt:    "Test prompt",
		Iteration: 1,
		SessionID: "test-session",
	}

	result, err := backend.Execute(ctx, req)

	if err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}

	if result == nil {
		t.Fatal("Execute() returned nil result")
	}

	// Should capture context cancellation error
	if result.Error == nil {
		t.Error("result.Error = nil, want context.Canceled")
	}
}

func TestInternalBackend_Execute_NilRunner(t *testing.T) {
	backend := NewInternalBackend("buckley", nil, InternalOptions{})

	ctx := context.Background()
	req := BackendRequest{
		Prompt:    "Test prompt",
		Iteration: 1,
		SessionID: "test-session",
	}

	result, err := backend.Execute(ctx, req)

	if err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}

	if result == nil {
		t.Fatal("Execute() returned nil result")
	}

	if result.Error == nil {
		t.Error("result.Error = nil, want error for nil runner")
	}
}

func TestInternalBackend_Execute_TracksDuration(t *testing.T) {
	delay := 50 * time.Millisecond
	mock := &mockHeadlessRunnerForBackend{
		processDelay: delay,
	}
	backend := NewInternalBackend("buckley", mock, InternalOptions{})

	ctx := context.Background()
	req := BackendRequest{
		Prompt:    "Test prompt",
		Iteration: 1,
		SessionID: "test-session",
	}

	result, err := backend.Execute(ctx, req)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if result.Duration < delay {
		t.Errorf("result.Duration = %v, want >= %v", result.Duration, delay)
	}
}

func TestInternalBackend_Execute_DefaultTokensAndCost(t *testing.T) {
	mock := &mockHeadlessRunnerForBackend{}
	backend := NewInternalBackend("buckley", mock, InternalOptions{})

	ctx := context.Background()
	req := BackendRequest{
		Prompt:    "Test prompt",
		Iteration: 1,
		SessionID: "test-session",
	}

	result, err := backend.Execute(ctx, req)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	// Prompt tokens should be counted; output/cost remain zero without output telemetry.
	if result.TokensIn == 0 {
		t.Errorf("result.TokensIn = %d, want > 0", result.TokensIn)
	}
	if result.TokensOut != 0 {
		t.Errorf("result.TokensOut = %d, want 0", result.TokensOut)
	}
	if result.Cost != 0 {
		t.Errorf("result.Cost = %f, want 0", result.Cost)
	}
}

func TestInternalBackend_ConcurrentAccess(t *testing.T) {
	mock := &mockHeadlessRunnerForBackend{}
	backend := NewInternalBackend("buckley", mock, InternalOptions{})

	// Test concurrent SetAvailable and Available calls
	done := make(chan bool)

	go func() {
		for i := 0; i < 100; i++ {
			backend.SetAvailable(i%2 == 0)
		}
		done <- true
	}()

	go func() {
		for i := 0; i < 100; i++ {
			_ = backend.Available()
		}
		done <- true
	}()

	<-done
	<-done

	// No race condition should occur
}

func TestInternalBackend_Options(t *testing.T) {
	opts := InternalOptions{
		ExecutionModel: "claude-3-opus",
		PlanningModel:  "claude-3-sonnet",
		ReasoningModel: "claude-3-opus",
		ApprovalMode:   "yolo",
	}

	mock := &mockHeadlessRunnerForBackend{}
	backend := NewInternalBackend("buckley", mock, opts)

	// Verify options are stored
	gotOpts := backend.Options()
	if gotOpts.ExecutionModel != opts.ExecutionModel {
		t.Errorf("Options().ExecutionModel = %q, want %q", gotOpts.ExecutionModel, opts.ExecutionModel)
	}
	if gotOpts.PlanningModel != opts.PlanningModel {
		t.Errorf("Options().PlanningModel = %q, want %q", gotOpts.PlanningModel, opts.PlanningModel)
	}
	if gotOpts.ReasoningModel != opts.ReasoningModel {
		t.Errorf("Options().ReasoningModel = %q, want %q", gotOpts.ReasoningModel, opts.ReasoningModel)
	}
	if gotOpts.ApprovalMode != opts.ApprovalMode {
		t.Errorf("Options().ApprovalMode = %q, want %q", gotOpts.ApprovalMode, opts.ApprovalMode)
	}
}

func TestNewInternalBackend(t *testing.T) {
	tests := []struct {
		name    string
		beName  string
		runner  HeadlessRunner
		options InternalOptions
	}{
		{
			name:   "with all fields",
			beName: "full-backend",
			runner: &mockHeadlessRunnerForBackend{},
			options: InternalOptions{
				ExecutionModel: "model-1",
				PlanningModel:  "model-2",
			},
		},
		{
			name:    "with nil runner",
			beName:  "nil-runner",
			runner:  nil,
			options: InternalOptions{},
		},
		{
			name:    "with empty name",
			beName:  "",
			runner:  &mockHeadlessRunnerForBackend{},
			options: InternalOptions{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := NewInternalBackend(tt.beName, tt.runner, tt.options)

			if backend == nil {
				t.Fatal("NewInternalBackend() returned nil")
			}

			if got := backend.Name(); got != tt.beName {
				t.Errorf("Name() = %q, want %q", got, tt.beName)
			}
		})
	}
}

func TestInternalBackend_ImplementsBackend(t *testing.T) {
	// Compile-time check that InternalBackend implements Backend
	var _ Backend = (*InternalBackend)(nil)
}
