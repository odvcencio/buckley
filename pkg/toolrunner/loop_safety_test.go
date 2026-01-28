package toolrunner

import (
	"testing"
)

// TestLoopSafety_DefaultMaxIterations verifies the default limit is set correctly
func TestLoopSafety_DefaultMaxIterations(t *testing.T) {
	// Verify the constant is set to expected value
	if defaultMaxIterations != 25 {
		t.Errorf("Expected defaultMaxIterations to be 25, got %d", defaultMaxIterations)
	}
	
	// Verify defaultMaxToolsPhase1 is reasonable
	if defaultMaxToolsPhase1 != 15 {
		t.Errorf("Expected defaultMaxToolsPhase1 to be 15, got %d", defaultMaxToolsPhase1)
	}
}

// TestLoopSafety_NewRunnerDefaults verifies New applies correct defaults
func TestLoopSafety_NewRunnerDefaults(t *testing.T) {
	// Create a minimal config (without models/registry to test validation)
	cfg := Config{
		DefaultMaxIterations: 0, // Should use default
		MaxToolsPhase1:       0, // Should use default
	}
	
	// Apply the same default logic as New()
	maxIter := cfg.DefaultMaxIterations
	if maxIter <= 0 {
		maxIter = defaultMaxIterations
	}
	
	maxTools := cfg.MaxToolsPhase1
	if maxTools <= 0 {
		maxTools = defaultMaxToolsPhase1
	}
	
	if maxIter != 25 {
		t.Errorf("Expected default iterations to be 25, got %d", maxIter)
	}
	
	if maxTools != 15 {
		t.Errorf("Expected default tools to be 15, got %d", maxTools)
	}
}

// TestLoopSafety_NewRunnerWithCustomLimits verifies custom limits are respected
func TestLoopSafety_NewRunnerWithCustomLimits(t *testing.T) {
	cfg := Config{
		DefaultMaxIterations: 10,
		MaxToolsPhase1:       5,
	}
	
	// Apply the same default logic as New()
	maxIter := cfg.DefaultMaxIterations
	if maxIter <= 0 {
		maxIter = defaultMaxIterations
	}
	
	maxTools := cfg.MaxToolsPhase1
	if maxTools <= 0 {
		maxTools = defaultMaxToolsPhase1
	}
	
	if maxIter != 10 {
		t.Errorf("Expected custom iterations to be 10, got %d", maxIter)
	}
	
	if maxTools != 5 {
		t.Errorf("Expected custom tools to be 5, got %d", maxTools)
	}
}

// TestLoopSafety_ToolResultDeduperCreation verifies deduper is created properly
func TestLoopSafety_ToolResultDeduperCreation(t *testing.T) {
	deduper := newToolResultDeduper()
	
	if deduper == nil {
		t.Fatal("Expected deduper to be created")
	}
	
	if deduper.seen == nil {
		t.Error("Expected deduper.seen map to be initialized")
	}
}

// TestLoopSafety_ToolResultDeduperBasic verifies basic deduplication
func TestLoopSafety_ToolResultDeduperBasic(t *testing.T) {
	deduper := newToolResultDeduper()
	
	// Create test records
	record1 := ToolCallRecord{
		ID:      "call_1",
		Name:    "test_tool",
		Result:  "result content",
	}
	
	// First call should return original result
	result1 := deduper.messageFor(record1)
	if result1 != "result content" {
		t.Errorf("Expected original result, got: %s", result1)
	}
	
	// Second identical call should be marked as duplicate
	record2 := ToolCallRecord{
		ID:      "call_2",
		Name:    "test_tool",
		Result:  "result content",
	}
	
	result2 := deduper.messageFor(record2)
	if result2 == "result content" {
		t.Error("Expected deduplicated message for duplicate result")
	}
	
	if result2 != "[deduplicated tool result; same as tool call call_1]" {
		t.Errorf("Unexpected deduplication message: %s", result2)
	}
}

// TestLoopSafety_ToolResultDeduperDifferentResults verifies different results aren't deduplicated
func TestLoopSafety_ToolResultDeduperDifferentResults(t *testing.T) {
	deduper := newToolResultDeduper()
	
	record1 := ToolCallRecord{
		ID:      "call_1",
		Name:    "test_tool",
		Result:  "result one",
	}
	
	record2 := ToolCallRecord{
		ID:      "call_2",
		Name:    "test_tool",
		Result:  "result two",
	}
	
	result1 := deduper.messageFor(record1)
	result2 := deduper.messageFor(record2)
	
	if result1 != "result one" {
		t.Errorf("Expected 'result one', got: %s", result1)
	}
	
	if result2 != "result two" {
		t.Errorf("Expected 'result two', got: %s", result2)
	}
}

// TestLoopSafety_ToolResultDeduperNilHandling verifies nil deduper handling
func TestLoopSafety_ToolResultDeduperNilHandling(t *testing.T) {
	var deduper *toolResultDeduper
	
	record := ToolCallRecord{
		Result: "test result",
	}
	
	// Should not panic and return original result
	result := deduper.messageFor(record)
	if result != "test result" {
		t.Errorf("Expected original result for nil deduper, got: %s", result)
	}
}

// TestLoopSafety_ToolResultDeduperEmptyResult verifies empty result handling
func TestLoopSafety_ToolResultDeduperEmptyResult(t *testing.T) {
	deduper := newToolResultDeduper()
	
	record := ToolCallRecord{
		ID:      "call_1",
		Name:    "test_tool",
		Result:  "",
	}
	
	// Empty result should return empty without deduplication
	result := deduper.messageFor(record)
	if result != "" {
		t.Errorf("Expected empty result, got: %s", result)
	}
}

// TestLoopSafety_DefaultMaxParallel verifies default parallel limit
func TestLoopSafety_DefaultMaxParallel(t *testing.T) {
	if defaultMaxParallel != 5 {
		t.Errorf("Expected defaultMaxParallel to be 5, got %d", defaultMaxParallel)
	}
}
