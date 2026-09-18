// pkg/ralph/backend_test.go
package ralph

import (
	"context"
	"testing"
	"time"
)

// mockBackend implements Backend for testing.
type mockBackend struct {
	name      string
	available bool
	execErr   error
	execDelay time.Duration
}

func (m *mockBackend) Name() string {
	return m.name
}

func (m *mockBackend) Execute(ctx context.Context, req BackendRequest) (*BackendResult, error) {
	if m.execDelay > 0 {
		select {
		case <-time.After(m.execDelay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if m.execErr != nil {
		return nil, m.execErr
	}
	return &BackendResult{
		Backend:  m.name,
		Duration: 100 * time.Millisecond,
		Output:   "mock output",
	}, nil
}

func (m *mockBackend) Available() bool {
	return m.available
}

func TestBackendRequest_Fields(t *testing.T) {
	req := BackendRequest{
		Prompt:      "test prompt",
		SandboxPath: "/tmp/sandbox",
		Iteration:   1,
		SessionID:   "session-123",
		Context: map[string]any{
			"key": "value",
		},
	}

	if req.Prompt != "test prompt" {
		t.Errorf("expected prompt 'test prompt', got %q", req.Prompt)
	}
	if req.SandboxPath != "/tmp/sandbox" {
		t.Errorf("expected sandbox path '/tmp/sandbox', got %q", req.SandboxPath)
	}
	if req.Iteration != 1 {
		t.Errorf("expected iteration 1, got %d", req.Iteration)
	}
	if req.SessionID != "session-123" {
		t.Errorf("expected session ID 'session-123', got %q", req.SessionID)
	}
	if req.Context["key"] != "value" {
		t.Errorf("expected context key 'value', got %v", req.Context["key"])
	}
}

func TestBackendResult_Fields(t *testing.T) {
	result := BackendResult{
		Backend:      "test-backend",
		Duration:     500 * time.Millisecond,
		TokensIn:     100,
		TokensOut:    200,
		Cost:         0.005,
		CostEstimate: 0.006,
		FilesChanged: []string{"file1.go", "file2.go"},
		TestsPassed:  5,
		TestsFailed:  1,
		Output:       "test output",
		Error:        nil,
	}

	if result.Backend != "test-backend" {
		t.Errorf("expected backend 'test-backend', got %q", result.Backend)
	}
	if result.Duration != 500*time.Millisecond {
		t.Errorf("expected duration 500ms, got %v", result.Duration)
	}
	if result.TokensIn != 100 {
		t.Errorf("expected tokens in 100, got %d", result.TokensIn)
	}
	if result.TokensOut != 200 {
		t.Errorf("expected tokens out 200, got %d", result.TokensOut)
	}
	if result.Cost != 0.005 {
		t.Errorf("expected cost 0.005, got %f", result.Cost)
	}
	if result.CostEstimate != 0.006 {
		t.Errorf("expected cost estimate 0.006, got %f", result.CostEstimate)
	}
	if len(result.FilesChanged) != 2 {
		t.Errorf("expected 2 files changed, got %d", len(result.FilesChanged))
	}
	if result.TestsPassed != 5 {
		t.Errorf("expected 5 tests passed, got %d", result.TestsPassed)
	}
	if result.TestsFailed != 1 {
		t.Errorf("expected 1 test failed, got %d", result.TestsFailed)
	}
	if result.Output != "test output" {
		t.Errorf("expected output 'test output', got %q", result.Output)
	}
}

// These should not panic

// Pre-populate some backends

// Writer goroutine

// Reader goroutine - Get

// Reader goroutine - List

// Reader goroutine - Available

// Wait for all goroutines

func TestMockBackend_Execute(t *testing.T) {
	backend := &mockBackend{name: "test", available: true}

	req := BackendRequest{
		Prompt:      "test prompt",
		SandboxPath: "/tmp",
		Iteration:   1,
		SessionID:   "sess-1",
	}

	result, err := backend.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if result.Backend != "test" {
		t.Errorf("expected backend 'test', got %q", result.Backend)
	}
	if result.Output != "mock output" {
		t.Errorf("expected output 'mock output', got %q", result.Output)
	}
}

func TestMockBackend_ExecuteWithContext(t *testing.T) {
	backend := &mockBackend{
		name:      "slow",
		available: true,
		execDelay: 1 * time.Second,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	req := BackendRequest{Prompt: "test"}

	_, err := backend.Execute(ctx, req)
	if err != context.DeadlineExceeded {
		t.Errorf("expected context.DeadlineExceeded, got %v", err)
	}
}
