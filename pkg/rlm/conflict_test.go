package rlm

import "testing"

func TestConflictDetector_ReadWrite(t *testing.T) {
	detector := NewConflictDetector()
	if err := detector.AcquireRead("task-a", "pkg/main.go"); err != nil {
		t.Fatalf("unexpected read lock error: %v", err)
	}
	if err := detector.AcquireRead("task-b", "pkg/main.go"); err != nil {
		t.Fatalf("unexpected second read lock error: %v", err)
	}
	if err := detector.AcquireWrite("task-b", "pkg/main.go"); err == nil {
		t.Fatalf("expected write lock conflict with active readers")
	}
	detector.ReleaseRead("task-a", "pkg/main.go")
	detector.ReleaseRead("task-b", "pkg/main.go")
	if err := detector.AcquireWrite("task-b", "pkg/main.go"); err != nil {
		t.Fatalf("unexpected write lock error after release: %v", err)
	}
}

func TestConflictDetector_UpgradeAndReenter(t *testing.T) {
	detector := NewConflictDetector()
	if err := detector.AcquireRead("task-a", "pkg/main.go"); err != nil {
		t.Fatalf("unexpected read lock error: %v", err)
	}
	if err := detector.AcquireWrite("task-a", "pkg/main.go"); err != nil {
		t.Fatalf("unexpected write upgrade error: %v", err)
	}
	if err := detector.AcquireWrite("task-a", "pkg/main.go"); err != nil {
		t.Fatalf("unexpected reentrant write error: %v", err)
	}
	detector.ReleaseWrite("task-a", "pkg/main.go")
	snapshot := detector.Snapshot()
	if state, ok := snapshot["pkg/main.go"]; !ok || state.Writer != "task-a" {
		t.Fatalf("expected writer to remain after partial release")
	}
	detector.ReleaseWrite("task-a", "pkg/main.go")
	snapshot = detector.Snapshot()
	if _, ok := snapshot["pkg/main.go"]; ok {
		t.Fatalf("expected lock cleared after final release")
	}
}
