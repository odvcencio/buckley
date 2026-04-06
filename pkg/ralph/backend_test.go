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

func TestBackendRegistry_Register(t *testing.T) {
	r := NewBackendRegistry()

	backend := &mockBackend{name: "test-backend", available: true}
	r.Register(backend)

	got, ok := r.Get("test-backend")
	if !ok {
		t.Fatal("expected to find registered backend")
	}
	if got.Name() != "test-backend" {
		t.Errorf("expected name 'test-backend', got %q", got.Name())
	}
}

func TestBackendRegistry_RegisterOverwrites(t *testing.T) {
	r := NewBackendRegistry()

	backend1 := &mockBackend{name: "test-backend", available: true}
	backend2 := &mockBackend{name: "test-backend", available: false}

	r.Register(backend1)
	r.Register(backend2)

	got, ok := r.Get("test-backend")
	if !ok {
		t.Fatal("expected to find registered backend")
	}
	if got.Available() {
		t.Error("expected backend to be unavailable after overwrite")
	}
}

func TestBackendRegistry_Get_NotFound(t *testing.T) {
	r := NewBackendRegistry()

	_, ok := r.Get("nonexistent")
	if ok {
		t.Error("expected Get to return false for nonexistent backend")
	}
}

func TestBackendRegistry_List(t *testing.T) {
	r := NewBackendRegistry()

	r.Register(&mockBackend{name: "backend-a", available: true})
	r.Register(&mockBackend{name: "backend-b", available: false})
	r.Register(&mockBackend{name: "backend-c", available: true})

	list := r.List()
	if len(list) != 3 {
		t.Fatalf("expected 3 backends, got %d", len(list))
	}

	names := make(map[string]bool)
	for _, b := range list {
		names[b.Name()] = true
	}

	for _, name := range []string{"backend-a", "backend-b", "backend-c"} {
		if !names[name] {
			t.Errorf("expected to find backend %q in list", name)
		}
	}
}

func TestBackendRegistry_Available(t *testing.T) {
	r := NewBackendRegistry()

	r.Register(&mockBackend{name: "available-1", available: true})
	r.Register(&mockBackend{name: "unavailable", available: false})
	r.Register(&mockBackend{name: "available-2", available: true})

	available := r.Available()
	if len(available) != 2 {
		t.Fatalf("expected 2 available backends, got %d", len(available))
	}

	for _, b := range available {
		if !b.Available() {
			t.Errorf("expected backend %q to be available", b.Name())
		}
	}
}

func TestBackendRegistry_Available_Empty(t *testing.T) {
	r := NewBackendRegistry()

	available := r.Available()
	if len(available) != 0 {
		t.Errorf("expected 0 available backends, got %d", len(available))
	}
}

func TestBackendRegistry_Available_NoneAvailable(t *testing.T) {
	r := NewBackendRegistry()

	r.Register(&mockBackend{name: "unavailable-1", available: false})
	r.Register(&mockBackend{name: "unavailable-2", available: false})

	available := r.Available()
	if len(available) != 0 {
		t.Errorf("expected 0 available backends, got %d", len(available))
	}
}

func TestBackendRegistry_NilGuards(t *testing.T) {
	var r *BackendRegistry

	// These should not panic
	r.Register(&mockBackend{name: "test"})

	_, ok := r.Get("test")
	if ok {
		t.Error("expected Get on nil registry to return false")
	}

	list := r.List()
	if list != nil {
		t.Error("expected List on nil registry to return nil")
	}

	available := r.Available()
	if available != nil {
		t.Error("expected Available on nil registry to return nil")
	}
}

func TestBackendRegistry_ConcurrentAccess(t *testing.T) {
	r := NewBackendRegistry()

	// Pre-populate some backends
	for i := 0; i < 5; i++ {
		r.Register(&mockBackend{name: "initial-" + string(rune('a'+i)), available: true})
	}

	done := make(chan bool)

	// Writer goroutine
	go func() {
		for i := 0; i < 100; i++ {
			r.Register(&mockBackend{name: "dynamic-backend", available: i%2 == 0})
		}
		done <- true
	}()

	// Reader goroutine - Get
	go func() {
		for i := 0; i < 100; i++ {
			r.Get("dynamic-backend")
		}
		done <- true
	}()

	// Reader goroutine - List
	go func() {
		for i := 0; i < 100; i++ {
			r.List()
		}
		done <- true
	}()

	// Reader goroutine - Available
	go func() {
		for i := 0; i < 100; i++ {
			r.Available()
		}
		done <- true
	}()

	// Wait for all goroutines
	for i := 0; i < 4; i++ {
		<-done
	}
}

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
