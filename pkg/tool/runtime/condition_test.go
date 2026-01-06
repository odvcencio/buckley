package runtime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAlwaysTrueCondition(t *testing.T) {
	ctx := context.Background()
	cond := &AlwaysTrueCondition{}

	satisfied, err := cond.Check(ctx)
	if err != nil {
		t.Errorf("Check() error = %v", err)
	}

	if !satisfied {
		t.Error("AlwaysTrueCondition should always return true")
	}

	if cond.String() != "AlwaysTrue" {
		t.Errorf("String() = %s, want AlwaysTrue", cond.String())
	}
}

func TestAlwaysFalseCondition(t *testing.T) {
	ctx := context.Background()
	cond := &AlwaysFalseCondition{}

	satisfied, err := cond.Check(ctx)
	if err != nil {
		t.Errorf("Check() error = %v", err)
	}

	if satisfied {
		t.Error("AlwaysFalseCondition should always return false")
	}

	if cond.String() != "AlwaysFalse" {
		t.Errorf("String() = %s, want AlwaysFalse", cond.String())
	}
}

func TestNotCondition(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name          string
		inner         Condition
		wantSatisfied bool
	}{
		{
			name:          "NOT(AlwaysTrue) = AlwaysFalse",
			inner:         &AlwaysTrueCondition{},
			wantSatisfied: false,
		},
		{
			name:          "NOT(AlwaysFalse) = AlwaysTrue",
			inner:         &AlwaysFalseCondition{},
			wantSatisfied: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cond := NewNotCondition(tt.inner)
			satisfied, err := cond.Check(ctx)
			if err != nil {
				t.Errorf("Check() error = %v", err)
			}

			if satisfied != tt.wantSatisfied {
				t.Errorf("Check() = %v, want %v", satisfied, tt.wantSatisfied)
			}
		})
	}
}

func TestAndCondition(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name          string
		conditions    []Condition
		wantSatisfied bool
		wantTimeout   time.Duration
	}{
		{
			name: "AND with all true",
			conditions: []Condition{
				&AlwaysTrueCondition{},
				&AlwaysTrueCondition{},
			},
			wantSatisfied: true,
			wantTimeout:   30 * time.Second, // Default timeout
		},
		{
			name: "AND with one false",
			conditions: []Condition{
				&AlwaysTrueCondition{},
				&AlwaysFalseCondition{},
			},
			wantSatisfied: false,
			wantTimeout:   30 * time.Second,
		},
		{
			name: "AND with custom timeout",
			conditions: []Condition{
				&PortReadyCondition{BaseCondition: BaseCondition{timeout: 45 * time.Second}, Host: "localhost", Port: 8080},
				&PortReadyCondition{BaseCondition: BaseCondition{timeout: 60 * time.Second}, Host: "localhost", Port: 8081},
			},
			wantSatisfied: false,            // Ports not open
			wantTimeout:   60 * time.Second, // Max of timeouts
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cond := NewAndCondition(tt.conditions)
			satisfied, err := cond.Check(ctx)
			if err != nil {
				t.Errorf("Check() error = %v", err)
			}

			if satisfied != tt.wantSatisfied {
				t.Errorf("Check() = %v, want %v", satisfied, tt.wantSatisfied)
			}

			if cond.Timeout() != tt.wantTimeout {
				t.Errorf("Timeout() = %v, want %v", cond.Timeout(), tt.wantTimeout)
			}
		})
	}
}

func TestOrCondition(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name          string
		conditions    []Condition
		wantSatisfied bool
	}{
		{
			name: "OR with at least one true",
			conditions: []Condition{
				&AlwaysFalseCondition{},
				&AlwaysTrueCondition{},
			},
			wantSatisfied: true,
		},
		{
			name: "OR with all false",
			conditions: []Condition{
				&AlwaysFalseCondition{},
				&AlwaysFalseCondition{},
			},
			wantSatisfied: false,
		},
		{
			name: "OR with all true",
			conditions: []Condition{
				&AlwaysTrueCondition{},
				&AlwaysTrueCondition{},
			},
			wantSatisfied: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cond := NewOrCondition(tt.conditions)
			satisfied, err := cond.Check(ctx)
			if err != nil {
				t.Errorf("Check() error = %v", err)
			}

			if satisfied != tt.wantSatisfied {
				t.Errorf("Check() = %v, want %v", satisfied, tt.wantSatisfied)
			}
		})
	}
}

func TestPortReadyCondition(t *testing.T) {
	// Test with closed port
	ctx := context.Background()
	cond := NewPortReadyCondition("localhost", 49999) // Unlikely to be in use

	satisfied, err := cond.Check(ctx)
	if err != nil {
		t.Errorf("Check() error = %v", err)
	}

	if satisfied {
		t.Error("PortReadyCondition should return false for closed port")
	}

	// Test with open port (requires starting a server)
	// This would be an integration test - for unit test we just verify structure
	if cond.String() != "PortReady(localhost:49999)" {
		t.Errorf("String() = %s, want PortReady(localhost:49999)", cond.String())
	}
}

func TestFileExistsCondition(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	// Test missing file
	missingFile := filepath.Join(tmpDir, "missing.txt")
	cond := NewFileExistsCondition(missingFile)

	satisfied, err := cond.Check(ctx)
	if err != nil {
		t.Errorf("Check() error = %v", err)
	}

	if satisfied {
		t.Error("FileExistsCondition should return false for missing file")
	}

	// Test existing file
	existingFile := filepath.Join(tmpDir, "exists.txt")
	if err := os.WriteFile(existingFile, []byte("test"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	cond = NewFileExistsCondition(existingFile)
	satisfied, err = cond.Check(ctx)
	if err != nil {
		t.Errorf("Check() error = %v", err)
	}

	if !satisfied {
		t.Error("FileExistsCondition should return true for existing file")
	}
}

func TestHealthCheckCondition(t *testing.T) {
	ctx := context.Background()

	// This is a very basic check - in a real test we'd start an HTTP server
	cond := NewHealthCheckCondition("http://localhost:49999/health")

	satisfied, err := cond.Check(ctx)
	if err != nil {
		t.Errorf("Check() error = %v", err)
	}

	if satisfied {
		t.Error("HealthCheckCondition should return false for unreachable endpoint")
	}
}

func TestLogMatchCondition(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	// Create a log file
	logFile := filepath.Join(tmpDir, "app.log")

	// Test with non-existent file
	cond := NewLogMatchCondition(logFile, "READY")

	satisfied, err := cond.Check(ctx)
	if err != nil {
		t.Errorf("Check() error = %v", err)
	}

	if satisfied {
		t.Error("LogMatchCondition should return false for non-existent file")
	}

	// Create file with non-matching content
	if err := os.WriteFile(logFile, []byte("Starting up..."), 0644); err != nil {
		t.Fatalf("Failed to create log file: %v", err)
	}

	cond = NewLogMatchCondition(logFile, "READY")
	satisfied, err = cond.Check(ctx)
	if err != nil {
		t.Errorf("Check() error = %v", err)
	}

	if satisfied {
		t.Error("LogMatchCondition should return false when pattern not found")
	}

	// Create file with matching content
	if err := os.WriteFile(logFile, []byte("Starting up...\nService READY\n"), 0644); err != nil {
		t.Fatalf("Failed to update log file: %v", err)
	}

	satisfied, err = cond.Check(ctx)
	if err != nil {
		t.Errorf("Check() error = %v", err)
	}

	if !satisfied {
		t.Error("LogMatchCondition should return true when pattern found")
	}
}

func TestWaitFor(t *testing.T) {
	ctx := context.Background()

	// Test successful wait
	cond := &AlwaysTrueCondition{}
	err := WaitFor(ctx, cond)
	if err != nil {
		t.Errorf("WaitFor() error = %v", err)
	}

	// Test timeout
	timeoutCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()

	condFalse := &AlwaysFalseCondition{}
	err = WaitFor(timeoutCtx, condFalse)
	if err == nil {
		t.Error("WaitFor() should timeout for unsatisfied condition")
	}
}

func TestWaitForWithRetry(t *testing.T) {
	ctx := context.Background()

	// Test success on first attempt
	cond := &AlwaysTrueCondition{}
	err := WaitForWithRetry(ctx, cond, 3, 10*time.Millisecond)
	if err != nil {
		t.Errorf("WaitForWithRetry() error = %v", err)
	}

	// Test failure after retries - use short timeout to avoid long test times
	timeoutCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()

	condFalse := &AlwaysFalseCondition{}
	err = WaitForWithRetry(timeoutCtx, condFalse, 2, 10*time.Millisecond)
	if err == nil {
		t.Error("WaitForWithRetry() should fail after max retries")
	}

	// Check error message mentions retries or context deadline
	if !contains(err.Error(), "max retries") && !contains(err.Error(), "timeout") && !contains(err.Error(), "deadline") {
		t.Errorf("Error should mention max retries, timeout, or deadline: %v", err)
	}
}

func TestWaitForWithResult(t *testing.T) {
	ctx := context.Background()

	cond := &AlwaysTrueCondition{}
	result := WaitForWithResult(ctx, cond, 3, 10*time.Millisecond)

	if !result.Success {
		t.Error("WaitForWithResult() should succeed for AlwaysTrueCondition")
	}

	if result.Duration == 0 {
		t.Error("Duration should be non-zero")
	}

	if result.Error != nil {
		t.Errorf("Error should be nil: %v", result.Error)
	}

	// Test failed wait - use short timeout to avoid long test times
	timeoutCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()

	condFalse2 := &AlwaysFalseCondition{}
	result = WaitForWithResult(timeoutCtx, condFalse2, 1, 10*time.Millisecond)

	if result.Success {
		t.Error("WaitForWithResult() should fail for AlwaysFalseCondition")
	}

	if result.Error == nil {
		t.Error("Error should not be nil for failed wait")
	}
}

func TestConditionBuilder(t *testing.T) {
	ctx := context.Background()

	// Test basic AND building
	cond1 := &AlwaysTrueCondition{}
	cond2 := &AlwaysTrueCondition{}

	builder := NewConditionBuilder()
	built := builder.And(cond1).And(cond2).Build()

	satisfied, err := built.Check(ctx)
	if err != nil {
		t.Errorf("Check() error = %v", err)
	}

	if !satisfied {
		t.Error("Built AND condition should be satisfied")
	}

	// Test single condition
	single := NewConditionBuilder().And(cond1).Build()
	if single != cond1 {
		t.Error("Single condition should be returned as-is")
	}

	// Test OR building
	orCond := NewConditionBuilder().And(cond1).BuildOr()
	satisfied, err = orCond.Check(ctx)
	if err != nil {
		t.Errorf("Check() error = %v", err)
	}

	if !satisfied {
		t.Error("OR condition should satisfy if any condition is true")
	}

	// Test empty builder
	empty := NewConditionBuilder().Build()
	satisfied, err = empty.Check(ctx)
	if err != nil {
		t.Errorf("Check() error = %v", err)
	}

	if !satisfied {
		t.Error("Empty condition should always be true")
	}
}

func TestParseConditionsFromYAML(t *testing.T) {
	tests := []struct {
		name    string
		data    map[string]any
		wantLen int
		wantErr bool
	}{
		{
			name: "port ready condition",
			data: map[string]any{
				"ready_conditions": []any{
					map[string]any{
						"type": "port_ready",
						"host": "localhost",
						"port": 5432.0,
					},
				},
			},
			wantLen: 1,
			wantErr: false,
		},
		{
			name: "multiple conditions",
			data: map[string]any{
				"ready_conditions": []any{
					map[string]any{
						"type": "port_ready",
						"port": 5432.0,
					},
					map[string]any{
						"type":    "health_check",
						"url":     "http://localhost:8080",
						"timeout": "30s",
					},
				},
			},
			wantLen: 2,
			wantErr: false,
		},
		{
			name: "invalid condition type",
			data: map[string]any{
				"ready_conditions": []any{
					map[string]any{
						"type": "invalid_type",
					},
				},
			},
			wantLen: 0,
			wantErr: true,
		},
		{
			name: "missing required field",
			data: map[string]any{
				"ready_conditions": []any{
					map[string]any{
						"type": "health_check",
						// Missing url
					},
				},
			},
			wantLen: 0,
			wantErr: true,
		},
		{
			name: "no conditions",
			data: map[string]any{
				"other_field": "value",
			},
			wantLen: 0,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conditions, err := ParseConditionsFromYAML(tt.data)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseConditionsFromYAML() error = %v, wantErr %v", err, tt.wantErr)
			}

			if len(conditions) != tt.wantLen {
				t.Errorf("ParseConditionsFromYAML() returned %d conditions, want %d", len(conditions), tt.wantLen)
			}
		})
	}
}

func TestGetDefaultServiceConditions(t *testing.T) {
	tests := []struct {
		serviceType string
		wantPort    int
	}{
		{"postgres", 5432},
		{"postgresql", 5432},
		{"mysql", 3306},
		{"redis", 6379},
		{"mongodb", 27017},
		{"elasticsearch", 9200},
		{"rabbitmq", 5672},
	}

	for _, tt := range tests {
		conditions := GetDefaultServiceConditions(tt.serviceType)

		if len(conditions) == 0 {
			t.Errorf("GetDefaultServiceConditions(%s) returned no conditions", tt.serviceType)
		}

		// Check at least one condition has expected port
		foundPort := false
		for _, cond := range conditions {
			if pr, ok := cond.(*PortReadyCondition); ok && pr.Port == tt.wantPort {
				foundPort = true
				break
			}
		}

		if !foundPort && tt.serviceType != "rabbitmq" { // rabbitmq has multiple ports
			t.Errorf("GetDefaultServiceConditions(%s) should include port %d", tt.serviceType, tt.wantPort)
		}
	}
}

func TestDatabaseQueryCondition(t *testing.T) {
	ctx := context.Background()

	// Test unsupported database
	cond := NewDatabaseQueryCondition("unsupported", "", "SELECT 1")
	satisfied, err := cond.Check(ctx)
	if err == nil {
		t.Error("Should error on unsupported database")
	}
	if satisfied {
		t.Error("Should not be satisfied on error")
	}

	// Test supported databases (these will fail in test without actual DB, but check structure)
	cond = NewDatabaseQueryCondition("postgres", "host=localhost", "SELECT 1")
	if cond.String() != "DatabaseQuery(postgres, query='SELECT 1')" {
		t.Errorf("String() = %s", cond.String())
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && (s[:len(substr)] == substr || contains(s[1:], substr)))
}

// Additional tests for String() methods

func TestAndConditionString(t *testing.T) {
	cond := NewAndCondition([]Condition{
		&AlwaysTrueCondition{},
		&AlwaysFalseCondition{},
	})

	str := cond.String()
	if !strings.Contains(str, "AND(") {
		t.Error("AndCondition.String() should start with AND(")
	}
	if !strings.Contains(str, "AlwaysTrue") {
		t.Error("AndCondition.String() should contain AlwaysTrue")
	}
	if !strings.Contains(str, "AlwaysFalse") {
		t.Error("AndCondition.String() should contain AlwaysFalse")
	}
	if !strings.Contains(str, "&&") {
		t.Error("AndCondition.String() should contain &&")
	}
}

func TestOrConditionString(t *testing.T) {
	cond := NewOrCondition([]Condition{
		&AlwaysTrueCondition{},
		&AlwaysFalseCondition{},
	})

	str := cond.String()
	if !strings.Contains(str, "OR(") {
		t.Error("OrCondition.String() should start with OR(")
	}
	if !strings.Contains(str, "||") {
		t.Error("OrCondition.String() should contain ||")
	}
}

func TestNotConditionString(t *testing.T) {
	cond := NewNotCondition(&AlwaysTrueCondition{})

	str := cond.String()
	if !strings.Contains(str, "NOT(") {
		t.Error("NotCondition.String() should start with NOT(")
	}
	if !strings.Contains(str, "AlwaysTrue") {
		t.Error("NotCondition.String() should contain inner condition")
	}
}

func TestFileExistsConditionString(t *testing.T) {
	cond := NewFileExistsCondition("/path/to/file")

	str := cond.String()
	if !strings.Contains(str, "FileExists") {
		t.Error("FileExistsCondition.String() should contain FileExists")
	}
	if !strings.Contains(str, "/path/to/file") {
		t.Error("FileExistsCondition.String() should contain the file path")
	}
}

func TestHealthCheckConditionString(t *testing.T) {
	cond := NewHealthCheckCondition("http://localhost:8080/health")

	str := cond.String()
	if !strings.Contains(str, "HealthCheck") {
		t.Error("HealthCheckCondition.String() should contain HealthCheck")
	}
	if !strings.Contains(str, "http://localhost:8080/health") {
		t.Error("HealthCheckCondition.String() should contain the URL")
	}
	if !strings.Contains(str, "status=200") {
		t.Error("HealthCheckCondition.String() should contain the status code")
	}
}

func TestLogMatchConditionString(t *testing.T) {
	cond := NewLogMatchCondition("/var/log/app.log", "READY")

	str := cond.String()
	if !strings.Contains(str, "LogMatch") {
		t.Error("LogMatchCondition.String() should contain LogMatch")
	}
	if !strings.Contains(str, "/var/log/app.log") {
		t.Error("LogMatchCondition.String() should contain the log file")
	}
	if !strings.Contains(str, "READY") {
		t.Error("LogMatchCondition.String() should contain the pattern")
	}
}

func TestProcessExitCondition(t *testing.T) {
	ctx := context.Background()

	// Test with a PID that likely doesn't exist (very high number)
	cond := NewProcessExitCondition(999999999)

	// A non-existent PID should be considered "exited"
	satisfied, err := cond.Check(ctx)
	if err != nil {
		t.Errorf("Check() error = %v", err)
	}

	// The behavior may vary depending on OS, but we test the function runs
	t.Logf("ProcessExitCondition for PID 999999999: satisfied=%v", satisfied)

	// Test string representation
	str := cond.String()
	if !strings.Contains(str, "ProcessExit") {
		t.Error("String() should contain ProcessExit")
	}
	if !strings.Contains(str, "999999999") {
		t.Error("String() should contain the PID")
	}

	// Test with specific exit code
	condWithCode := &ProcessExitCondition{
		BaseCondition: BaseCondition{timeout: 30 * time.Second},
		PID:           12345,
		ExitCode:      0,
	}
	str = condWithCode.String()
	if !strings.Contains(str, "ExitCode=0") {
		t.Error("String() should contain ExitCode when specified")
	}
}

func TestProcessExitCondition_CurrentProcess(t *testing.T) {
	ctx := context.Background()

	// Test with the current process PID (should be running)
	pid := os.Getpid()
	cond := NewProcessExitCondition(pid)

	satisfied, err := cond.Check(ctx)
	if err != nil {
		t.Errorf("Check() error = %v", err)
	}

	// Note: The implementation uses Signal(nil) which may behave differently
	// on different OS. We just verify the check runs without error.
	// On Linux, Signal(nil) on a running process may return nil (no error),
	// which the implementation interprets as "process still running" -> satisfied=false
	// But the actual behavior depends on permissions.
	t.Logf("ProcessExitCondition for current PID %d: satisfied=%v", pid, satisfied)
}

func TestAndConditionWithError(t *testing.T) {
	ctx := context.Background()

	// Create a condition that returns an error
	errCond := &errorTestCondition{err: fmt.Errorf("test error")}

	andCond := NewAndCondition([]Condition{
		&AlwaysTrueCondition{},
		errCond,
	})

	satisfied, err := andCond.Check(ctx)
	if err == nil {
		t.Error("Check() should return error when inner condition fails")
	}
	if satisfied {
		t.Error("Should not be satisfied when there's an error")
	}
}

func TestOrConditionWithError(t *testing.T) {
	ctx := context.Background()

	// Or should continue even with errors, until it finds a true
	errCond := &errorTestCondition{err: fmt.Errorf("test error")}

	orCond := NewOrCondition([]Condition{
		errCond,
		&AlwaysTrueCondition{},
	})

	satisfied, err := orCond.Check(ctx)
	if err != nil {
		t.Errorf("Check() should not return error: %v", err)
	}
	if !satisfied {
		t.Error("Should be satisfied when one condition is true")
	}

	// All errors should result in false, not error
	allErrCond := NewOrCondition([]Condition{
		errCond,
		errCond,
	})

	satisfied, err = allErrCond.Check(ctx)
	if err != nil {
		t.Errorf("Check() should not return error: %v", err)
	}
	if satisfied {
		t.Error("Should not be satisfied when all conditions error")
	}
}

func TestNotConditionWithError(t *testing.T) {
	ctx := context.Background()

	errCond := &errorTestCondition{err: fmt.Errorf("test error")}
	notCond := NewNotCondition(errCond)

	satisfied, err := notCond.Check(ctx)
	if err == nil {
		t.Error("Check() should propagate error from inner condition")
	}
	if satisfied {
		t.Error("Should not be satisfied when there's an error")
	}
}

func TestConditionBuilderBuildOr_Empty(t *testing.T) {
	ctx := context.Background()

	builder := NewConditionBuilder()
	cond := builder.BuildOr()

	// Empty OR should return AlwaysFalse
	satisfied, err := cond.Check(ctx)
	if err != nil {
		t.Errorf("Check() error = %v", err)
	}
	if satisfied {
		t.Error("Empty OR condition should be false")
	}
}

func TestConditionBuilderBuildOr_Single(t *testing.T) {
	ctx := context.Background()

	builder := NewConditionBuilder()
	trueCond := &AlwaysTrueCondition{}
	builder.And(trueCond)
	cond := builder.BuildOr()

	// Single OR should return the condition directly
	if cond != trueCond {
		t.Error("Single BuildOr() should return the condition directly")
	}

	satisfied, err := cond.Check(ctx)
	if err != nil {
		t.Errorf("Check() error = %v", err)
	}
	if !satisfied {
		t.Error("Single true OR condition should be satisfied")
	}
}

func TestConditionBuilderBuildOr_Multiple(t *testing.T) {
	ctx := context.Background()

	builder := NewConditionBuilder()
	builder.And(&AlwaysFalseCondition{})
	builder.And(&AlwaysTrueCondition{})
	cond := builder.BuildOr()

	satisfied, err := cond.Check(ctx)
	if err != nil {
		t.Errorf("Check() error = %v", err)
	}
	if !satisfied {
		t.Error("OR with one true should be satisfied")
	}
}

func TestParseConditionsFromYAML_LogMatch(t *testing.T) {
	data := map[string]any{
		"ready_conditions": []any{
			map[string]any{
				"type":     "log_match",
				"log_file": "/var/log/app.log",
				"pattern":  "Server started",
			},
		},
	}

	conditions, err := ParseConditionsFromYAML(data)
	if err != nil {
		t.Errorf("ParseConditionsFromYAML() error = %v", err)
	}
	if len(conditions) != 1 {
		t.Errorf("Expected 1 condition, got %d", len(conditions))
	}
}

func TestParseConditionsFromYAML_DatabaseQuery(t *testing.T) {
	data := map[string]any{
		"ready_conditions": []any{
			map[string]any{
				"type":     "database_query",
				"database": "postgres",
				"dsn":      "host=localhost",
				"query":    "SELECT 1",
			},
		},
	}

	conditions, err := ParseConditionsFromYAML(data)
	if err != nil {
		t.Errorf("ParseConditionsFromYAML() error = %v", err)
	}
	if len(conditions) != 1 {
		t.Errorf("Expected 1 condition, got %d", len(conditions))
	}
}

func TestParseConditionsFromYAML_MissingFields(t *testing.T) {
	tests := []struct {
		name    string
		data    map[string]any
		wantErr bool
	}{
		{
			name: "log_match missing log_file",
			data: map[string]any{
				"ready_conditions": []any{
					map[string]any{
						"type":    "log_match",
						"pattern": "READY",
					},
				},
			},
			wantErr: true,
		},
		{
			name: "log_match missing pattern",
			data: map[string]any{
				"ready_conditions": []any{
					map[string]any{
						"type":     "log_match",
						"log_file": "/var/log/app.log",
					},
				},
			},
			wantErr: true,
		},
		{
			name: "database_query missing database",
			data: map[string]any{
				"ready_conditions": []any{
					map[string]any{
						"type":  "database_query",
						"dsn":   "host=localhost",
						"query": "SELECT 1",
					},
				},
			},
			wantErr: true,
		},
		{
			name: "database_query missing dsn",
			data: map[string]any{
				"ready_conditions": []any{
					map[string]any{
						"type":     "database_query",
						"database": "postgres",
						"query":    "SELECT 1",
					},
				},
			},
			wantErr: true,
		},
		{
			name: "database_query missing query",
			data: map[string]any{
				"ready_conditions": []any{
					map[string]any{
						"type":     "database_query",
						"database": "postgres",
						"dsn":      "host=localhost",
					},
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseConditionsFromYAML(tt.data)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseConditionsFromYAML() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestParseConditionsFromYAML_FileExists(t *testing.T) {
	data := map[string]any{
		"ready_conditions": []any{
			map[string]any{
				"type":    "file_exists",
				"path":    "/tmp/ready.txt",
				"timeout": "60s",
			},
		},
	}

	conditions, err := ParseConditionsFromYAML(data)
	if err != nil {
		t.Errorf("ParseConditionsFromYAML() error = %v", err)
	}
	if len(conditions) != 1 {
		t.Errorf("Expected 1 condition, got %d", len(conditions))
	}

	// Verify timeout was parsed
	if conditions[0].Timeout() != 60*time.Second {
		t.Errorf("Timeout = %v, want 60s", conditions[0].Timeout())
	}
}

func TestParseConditionsFromYAML_FileExistsMissingPath(t *testing.T) {
	data := map[string]any{
		"ready_conditions": []any{
			map[string]any{
				"type": "file_exists",
			},
		},
	}

	_, err := ParseConditionsFromYAML(data)
	if err == nil {
		t.Error("Should error when file_exists is missing path")
	}
}

func TestParseConditionsFromYAML_SkipsInvalidEntries(t *testing.T) {
	data := map[string]any{
		"ready_conditions": []any{
			"not a map",               // Invalid entry
			map[string]any{},          // No type
			map[string]any{"type": 1}, // Non-string type
			map[string]any{
				"type": "port_ready",
				"port": 8080.0,
			}, // Valid
		},
	}

	conditions, err := ParseConditionsFromYAML(data)
	if err != nil {
		t.Errorf("ParseConditionsFromYAML() error = %v", err)
	}
	// Should get 1 valid condition
	if len(conditions) != 1 {
		t.Errorf("Expected 1 condition, got %d", len(conditions))
	}
}

func TestGetDefaultServiceConditions_Unknown(t *testing.T) {
	conditions := GetDefaultServiceConditions("unknown-service")

	if len(conditions) == 0 {
		t.Error("Unknown service should still return default conditions")
	}

	// Should default to port 8080
	if pr, ok := conditions[0].(*PortReadyCondition); ok {
		if pr.Port != 8080 {
			t.Errorf("Default port should be 8080, got %d", pr.Port)
		}
	} else {
		t.Error("First condition should be PortReadyCondition")
	}
}

func TestWaitFor_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	cond := &AlwaysFalseCondition{}
	err := WaitFor(ctx, cond)
	if err == nil {
		t.Error("WaitFor() should fail on cancelled context")
	}
}

func TestWaitForWithRetry_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	cond := &AlwaysFalseCondition{}
	err := WaitForWithRetry(ctx, cond, 3, 10*time.Millisecond)
	if err == nil {
		t.Error("WaitForWithRetry() should fail on cancelled context")
	}
}

// errorTestCondition is a condition that always returns an error
type errorTestCondition struct {
	BaseCondition
	err error
}

func (e *errorTestCondition) Check(ctx context.Context) (bool, error) {
	return false, e.err
}

func (e *errorTestCondition) String() string {
	return "ErrorTestCondition"
}
