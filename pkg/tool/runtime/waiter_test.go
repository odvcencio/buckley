package runtime

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/odvcencio/buckley/pkg/tool"
	"github.com/odvcencio/buckley/pkg/tool/builtin"
)

func TestNewConditionWaiter(t *testing.T) {
	waiter := NewConditionWaiter("my-service", "docker-compose.yaml", "abc123")

	if waiter.serviceName != "my-service" {
		t.Errorf("serviceName = %s, want my-service", waiter.serviceName)
	}
	if waiter.composeFile != "docker-compose.yaml" {
		t.Errorf("composeFile = %s, want docker-compose.yaml", waiter.composeFile)
	}
	if waiter.containerID != "abc123" {
		t.Errorf("containerID = %s, want abc123", waiter.containerID)
	}
}

func TestConditionWaiter_WaitForReady_NoConditions(t *testing.T) {
	waiter := NewConditionWaiter("test-service", "compose.yaml", "container123")

	// No conditions should return immediately with no error
	err := waiter.WaitForReady(nil, nil)
	if err != nil {
		t.Errorf("WaitForReady() with nil conditions should succeed, got error: %v", err)
	}

	err = waiter.WaitForReady([]Condition{}, nil)
	if err != nil {
		t.Errorf("WaitForReady() with empty conditions should succeed, got error: %v", err)
	}
}

func TestConditionWaiter_WaitForReady_Success(t *testing.T) {
	waiter := NewConditionWaiter("test-service", "compose.yaml", "container123")

	// Use AlwaysTrueCondition which should succeed immediately
	conditions := []Condition{&AlwaysTrueCondition{}}

	err := waiter.WaitForReady(conditions, nil)
	if err != nil {
		t.Errorf("WaitForReady() with AlwaysTrueCondition should succeed, got error: %v", err)
	}
}

func TestConditionWaiter_WaitForReady_Timeout(t *testing.T) {
	// Skip this test by default as it takes a while due to retry logic
	// The core timeout behavior is tested in WaitForWithRetry tests
	if testing.Short() {
		t.Skip("Skipping timeout test in short mode")
	}

	waiter := NewConditionWaiter("test-service", "compose.yaml", "container123")

	// Use a condition with very short timeout that will fail
	// Note: getMaxTimeout adds 10s buffer, so this will take ~10s minimum
	cond := &AlwaysFalseCondition{BaseCondition: BaseCondition{timeout: 50 * time.Millisecond}}
	conditions := []Condition{cond}

	err := waiter.WaitForReady(conditions, nil)
	if err == nil {
		t.Error("WaitForReady() with AlwaysFalseCondition should timeout")
	}
	if !strings.Contains(err.Error(), "failed to become ready") {
		t.Errorf("Error should mention 'failed to become ready', got: %v", err)
	}
}

func TestGetMaxTimeout(t *testing.T) {
	tests := []struct {
		name       string
		conditions []Condition
		want       time.Duration
	}{
		{
			name:       "empty conditions",
			conditions: []Condition{},
			want:       40 * time.Second, // 30s default + 10s buffer
		},
		{
			name: "single condition with default timeout",
			conditions: []Condition{
				&AlwaysTrueCondition{},
			},
			want: 40 * time.Second, // 30s + 10s buffer
		},
		{
			name: "multiple conditions with varying timeouts",
			conditions: []Condition{
				&AlwaysTrueCondition{BaseCondition: BaseCondition{timeout: 20 * time.Second}},
				&AlwaysFalseCondition{BaseCondition: BaseCondition{timeout: 45 * time.Second}},
				&AlwaysTrueCondition{BaseCondition: BaseCondition{timeout: 10 * time.Second}},
			},
			want: 55 * time.Second, // 45s max + 10s buffer
		},
		{
			name: "condition exceeds default",
			conditions: []Condition{
				&AlwaysTrueCondition{BaseCondition: BaseCondition{timeout: 120 * time.Second}},
			},
			want: 130 * time.Second, // 120s + 10s buffer
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getMaxTimeout(tt.conditions)
			if got != tt.want {
				t.Errorf("getMaxTimeout() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCaptureLogs(t *testing.T) {
	// Test with nil reader
	logCh := make(chan string, 10)
	captureLogs(nil, logCh)
	// Should not panic, just return

	// Test with actual content
	content := "line1\nline2\nline3"
	reader := bytes.NewBufferString(content)
	logCh = make(chan string, 10)

	go captureLogs(reader, logCh)

	// Give it a moment to process
	time.Sleep(50 * time.Millisecond)

	// Collect logs
	var logs []string
	timeout := time.After(100 * time.Millisecond)
loop:
	for {
		select {
		case log := <-logCh:
			logs = append(logs, log)
		case <-timeout:
			break loop
		}
	}

	if len(logs) != 3 {
		t.Errorf("Expected 3 log lines, got %d", len(logs))
	}
}

func TestCaptureLogs_ChannelFull(t *testing.T) {
	// Test channel overflow handling (should not block indefinitely)
	// With 5 lines and 100ms timeout per drop, max time is 500ms
	content := strings.Repeat("log line\n", 5)
	reader := bytes.NewBufferString(content)
	logCh := make(chan string, 1) // Very small buffer

	done := make(chan struct{})
	go func() {
		captureLogs(reader, logCh)
		close(done)
	}()

	// Should complete within reasonable time (each line takes 100ms when channel full)
	select {
	case <-done:
		// Success - completed without blocking indefinitely
	case <-time.After(3 * time.Second):
		t.Error("captureLogs blocked on full channel")
	}
}

func TestFormatConditions(t *testing.T) {
	tests := []struct {
		name       string
		conditions []Condition
		want       string
	}{
		{
			name:       "empty",
			conditions: []Condition{},
			want:       "",
		},
		{
			name:       "single",
			conditions: []Condition{&AlwaysTrueCondition{}},
			want:       "AlwaysTrue",
		},
		{
			name: "multiple",
			conditions: []Condition{
				&AlwaysTrueCondition{},
				&AlwaysFalseCondition{},
			},
			want: "AlwaysTrue; AlwaysFalse",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatConditions(tt.conditions)
			if got != tt.want {
				t.Errorf("formatConditions() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEnrichResultWithWaitInfo(t *testing.T) {
	startTime := time.Now().Add(-500 * time.Millisecond)
	conditions := []Condition{
		&AlwaysTrueCondition{},
		&AlwaysFalseCondition{},
	}

	// Test with nil result
	result := EnrichResultWithWaitInfo(nil, startTime, conditions)
	if result == nil {
		t.Fatal("EnrichResultWithWaitInfo should not return nil")
	}
	if result.Data == nil {
		t.Error("Data should be initialized")
	}

	// Test with result that has nil Data
	result = &builtin.Result{}
	result = EnrichResultWithWaitInfo(result, startTime, conditions)
	if result.Data == nil {
		t.Error("Data should be initialized")
	}

	// Test with populated result
	result = &builtin.Result{
		Success: true,
		Data: map[string]any{
			"existing": "value",
		},
	}
	result = EnrichResultWithWaitInfo(result, startTime, conditions)

	if result.Data["existing"] != "value" {
		t.Error("Existing data should be preserved")
	}

	waitDuration, ok := result.Data["wait_duration"]
	if !ok {
		t.Error("wait_duration should be set")
	}
	if dur, ok := waitDuration.(int64); ok {
		if dur < 500 {
			t.Errorf("wait_duration should be at least 500ms, got %d", dur)
		}
	}

	conditionsWaited, ok := result.Data["conditions_waited"]
	if !ok {
		t.Error("conditions_waited should be set")
	}
	if conditionsWaited != 2 {
		t.Errorf("conditions_waited = %v, want 2", conditionsWaited)
	}

	waitedFor, ok := result.Data["waited_for"]
	if !ok {
		t.Error("waited_for should be set")
	}
	if waitedFor != "AlwaysTrue; AlwaysFalse" {
		t.Errorf("waited_for = %v", waitedFor)
	}
}

// mockTool implements tool.Tool for testing
type mockTool struct {
	name        string
	executeFunc func(params map[string]any) (*builtin.Result, error)
}

func (m *mockTool) Name() string {
	return m.name
}

func (m *mockTool) Description() string {
	return "Mock tool for testing"
}

func (m *mockTool) Parameters() builtin.ParameterSchema {
	return builtin.ParameterSchema{
		Type:       "object",
		Properties: map[string]builtin.PropertySchema{},
		Required:   []string{},
	}
}

func (m *mockTool) Execute(params map[string]any) (*builtin.Result, error) {
	if m.executeFunc != nil {
		return m.executeFunc(params)
	}
	return &builtin.Result{Success: true, Data: map[string]any{"output": "test"}}, nil
}

// Verify mockTool implements tool.Tool
var _ tool.Tool = (*mockTool)(nil)

func TestExecuteToolWithConditions_NoConditions(t *testing.T) {
	mock := &mockTool{
		name: "test-tool",
		executeFunc: func(params map[string]any) (*builtin.Result, error) {
			return &builtin.Result{Success: true, Data: map[string]any{"result": "success"}}, nil
		},
	}

	config := ExecuteConfig{
		Tool:       mock,
		Params:     map[string]any{},
		Conditions: nil,
	}

	result, err := ExecuteToolWithConditions(config)
	if err != nil {
		t.Errorf("ExecuteToolWithConditions() error = %v", err)
	}
	if !result.Success {
		t.Error("Result should be successful")
	}
}

func TestExecuteToolWithConditions_WithConditions(t *testing.T) {
	executed := false
	mock := &mockTool{
		name: "test-tool",
		executeFunc: func(params map[string]any) (*builtin.Result, error) {
			executed = true
			return &builtin.Result{Success: true}, nil
		},
	}

	config := ExecuteConfig{
		Tool:       mock,
		Params:     map[string]any{},
		Conditions: []Condition{&AlwaysTrueCondition{}},
		Service:    "test-service",
		Compose:    "compose.yaml",
		Container:  "container123",
	}

	result, err := ExecuteToolWithConditions(config)
	if err != nil {
		t.Errorf("ExecuteToolWithConditions() error = %v", err)
	}
	if !result.Success {
		t.Error("Result should be successful")
	}
	if !executed {
		t.Error("Tool should have been executed")
	}
}

func TestExecuteToolWithConditions_ConditionFails(t *testing.T) {
	// Skip in short mode as the timeout takes a while
	if testing.Short() {
		t.Skip("Skipping timeout test in short mode")
	}

	mock := &mockTool{
		name: "test-tool",
		executeFunc: func(params map[string]any) (*builtin.Result, error) {
			t.Error("Tool should not be executed when conditions fail")
			return nil, nil
		},
	}

	// Use a condition with very short timeout
	// Note: getMaxTimeout adds 10s buffer, so this will take ~10s minimum
	cond := &AlwaysFalseCondition{BaseCondition: BaseCondition{timeout: 50 * time.Millisecond}}

	config := ExecuteConfig{
		Tool:       mock,
		Params:     map[string]any{},
		Conditions: []Condition{cond},
		Service:    "test-service",
		Compose:    "compose.yaml",
		Container:  "container123",
	}

	_, err := ExecuteToolWithConditions(config)
	if err == nil {
		t.Error("ExecuteToolWithConditions() should fail when conditions are not met")
	}
	if !strings.Contains(err.Error(), "conditions not met") {
		t.Errorf("Error should mention 'conditions not met', got: %v", err)
	}
}

func TestRunCommand(t *testing.T) {
	ctx := context.Background()

	cmd := runCommand(ctx, "echo", "hello")
	if cmd == nil {
		t.Fatal("runCommand should return a command")
	}

	// Verify command is configured correctly
	if len(cmd.Args) != 2 || cmd.Args[0] != "echo" || cmd.Args[1] != "hello" {
		t.Errorf("Command args = %v, want [echo, hello]", cmd.Args)
	}
}

func TestConditionWaiter_CollectDiagnostics(t *testing.T) {
	waiter := NewConditionWaiter("test-service", "compose.yaml", "container123")

	conditions := []Condition{
		&AlwaysTrueCondition{},
		&AlwaysFalseCondition{},
	}

	// Create channel with logs and close it to avoid blocking
	logCh := make(chan string, 10)
	logCh <- "test log line 1"
	logCh <- "test log line 2"
	close(logCh) // Close to prevent blocking in collectDiagnostics

	done := make(chan string)
	go func() {
		diagErr := &testError{msg: "test error"}
		diagnostics := waiter.collectDiagnostics(diagErr, conditions, logCh)
		done <- diagnostics
	}()

	// Wait for completion with timeout (docker commands may take a while)
	select {
	case diagnostics := <-done:
		// Verify diagnostics contain key information
		if !strings.Contains(diagnostics, "test error") {
			t.Error("Diagnostics should contain original error")
		}
		if !strings.Contains(diagnostics, "Condition Status") {
			t.Error("Diagnostics should contain condition status section")
		}
		if !strings.Contains(diagnostics, "AlwaysTrue") {
			t.Error("Diagnostics should list condition names")
		}
		if !strings.Contains(diagnostics, "SATISFIED") || !strings.Contains(diagnostics, "NOT SATISFIED") {
			t.Error("Diagnostics should show condition satisfaction status")
		}
	case <-time.After(20 * time.Second):
		t.Error("collectDiagnostics timed out")
	}
}

type testError struct {
	msg string
}

func (e *testError) Error() string {
	return e.msg
}

// Test condition with error
type errorCondition struct {
	BaseCondition
	err error
}

func (e *errorCondition) Check(ctx context.Context) (bool, error) {
	return false, e.err
}

func (e *errorCondition) String() string {
	return "ErrorCondition"
}

func TestConditionWaiter_CollectDiagnostics_WithConditionError(t *testing.T) {
	waiter := NewConditionWaiter("test-service", "compose.yaml", "container123")

	conditions := []Condition{
		&errorCondition{err: &testError{msg: "condition check failed"}},
	}

	logCh := make(chan string, 10)
	close(logCh) // Close to prevent blocking

	done := make(chan string)
	go func() {
		diagnostics := waiter.collectDiagnostics(&testError{msg: "wait error"}, conditions, logCh)
		done <- diagnostics
	}()

	select {
	case diagnostics := <-done:
		if !strings.Contains(diagnostics, "ERROR") {
			t.Error("Diagnostics should show ERROR status for condition with error")
		}
		if !strings.Contains(diagnostics, "condition check failed") {
			t.Error("Diagnostics should include the condition error message")
		}
	case <-time.After(20 * time.Second):
		t.Error("collectDiagnostics with error condition timed out")
	}
}
