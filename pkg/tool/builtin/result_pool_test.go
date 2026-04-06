package builtin

import (
	"fmt"
	"testing"
)

func TestAcquireResult_Basic(t *testing.T) {
	r := AcquireResult()
	if r == nil {
		t.Fatal("expected non-nil result")
	}

	// Verify it's reset
	if r.Success {
		t.Error("expected Success to be false")
	}
	if r.Error != "" {
		t.Errorf("expected empty Error, got %q", r.Error)
	}
	if r.Data != nil {
		t.Error("expected nil Data")
	}
}

func TestReleaseResult_Basic(t *testing.T) {
	r := AcquireResult()
	r.Success = true
	r.Error = "test error"
	r.Data = map[string]any{"key": "value"}
	r.DisplayData = map[string]any{"display": "data"}

	// Release should reset
	ReleaseResult(r)

	// Acquire again - should be reset
	r2 := AcquireResult()
	if r2.Success {
		t.Error("expected Success to be false after release/acquire")
	}
	if r2.Error != "" {
		t.Errorf("expected empty Error, got %q", r2.Error)
	}
}

func TestReleaseResult_Nil(t *testing.T) {
	// Should not panic
	ReleaseResult(nil)
}

func TestResult_Reset(t *testing.T) {
	r := &Result{
		Success:       true,
		Data:          map[string]any{"key": "value", "other": 123},
		Error:         "error message",
		DisplayData:   map[string]any{"display": "data"},
		ShouldAbridge: true,
		NeedsApproval: true,
		ApprovalFunc:  func(bool) {},
	}

	r.Reset()

	if r.Success {
		t.Error("expected Success to be false")
	}
	if r.Error != "" {
		t.Errorf("expected Error to be empty, got %q", r.Error)
	}
	if r.ShouldAbridge {
		t.Error("expected ShouldAbridge to be false")
	}
	if r.NeedsApproval {
		t.Error("expected NeedsApproval to be false")
	}
	if r.ApprovalFunc != nil {
		t.Error("expected ApprovalFunc to be nil")
	}
	if r.DiffPreview != nil {
		t.Error("expected DiffPreview to be nil")
	}

	// Maps should be cleared but not nil (preserving capacity)
	if r.Data == nil {
		t.Error("expected Data to be non-nil (preserving capacity)")
	}
	if len(r.Data) != 0 {
		t.Errorf("expected Data to be empty, got %d items", len(r.Data))
	}
	if r.DisplayData == nil {
		t.Error("expected DisplayData to be non-nil (preserving capacity)")
	}
	if len(r.DisplayData) != 0 {
		t.Errorf("expected DisplayData to be empty, got %d items", len(r.DisplayData))
	}
}

func TestResult_Reset_Nil(t *testing.T) {
	var r *Result
	// Should not panic
	r.Reset()
}

func TestResult_Reset_PreservesCapacity(t *testing.T) {
	// Create maps with initial capacity
	dataMap := make(map[string]any, 100)
	displayMap := make(map[string]any, 50)

	r := &Result{
		Data:        dataMap,
		DisplayData: displayMap,
	}

	// Add some data
	for i := 0; i < 10; i++ {
		r.Data[fmt.Sprintf("key%d", i)] = i
		r.DisplayData[fmt.Sprintf("display%d", i)] = i
	}

	r.Reset()

	// Maps should still be usable
	r.Data["new"] = "value"
	r.DisplayData["display"] = "data"

	if len(r.Data) != 1 {
		t.Errorf("expected 1 item in Data, got %d", len(r.Data))
	}
	if len(r.DisplayData) != 1 {
		t.Errorf("expected 1 item in DisplayData, got %d", len(r.DisplayData))
	}
}

func TestAcquireResultSlice_Basic(t *testing.T) {
	s := AcquireResultSlice()
	if s == nil {
		t.Fatal("expected non-nil slice")
	}
	if len(s) != 0 {
		t.Errorf("expected empty slice, got len=%d", len(s))
	}
	if cap(s) < 1 {
		t.Error("expected some capacity")
	}
}

func TestReleaseResultSlice_Basic(t *testing.T) {
	s := AcquireResultSlice()
	s = append(s, &Result{Success: true})
	s = append(s, &Result{Success: false})

	ReleaseResultSlice(s)

	// Should be able to acquire again
	s2 := AcquireResultSlice()
	if len(s2) != 0 {
		t.Errorf("expected empty slice after release, got len=%d", len(s2))
	}
}

func TestReleaseResultSlice_Nil(t *testing.T) {
	// Should not panic
	ReleaseResultSlice(nil)
}

func TestReleaseResultSlice_LargeSlice(t *testing.T) {
	// Large slices should not be pooled
	largeSlice := make([]*Result, 0, 2048)
	ReleaseResultSlice(largeSlice)
	// Should be dropped, not pooled
}
