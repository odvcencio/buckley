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

func TestConflictDetector_ExclusiveConflictsWithStructuredLocks(t *testing.T) {
	detector := NewConflictDetector()
	if err := detector.AcquireWrite("writer", "pkg/main.go"); err != nil {
		t.Fatalf("AcquireWrite: %v", err)
	}
	if err := detector.AcquireExclusive("shell"); err == nil {
		t.Fatal("AcquireExclusive succeeded with another task's write lock")
	}
	detector.ReleaseWrite("writer", "pkg/main.go")

	if err := detector.AcquireRead("reader", "pkg/main.go"); err != nil {
		t.Fatalf("AcquireRead: %v", err)
	}
	if err := detector.AcquireExclusive("shell"); err == nil {
		t.Fatal("AcquireExclusive succeeded with another task's read lock")
	}
	detector.ReleaseRead("reader", "pkg/main.go")

	if err := detector.AcquireExclusive("shell"); err != nil {
		t.Fatalf("AcquireExclusive after release: %v", err)
	}
	if err := detector.AcquireExclusive("shell"); err != nil {
		t.Fatalf("reentrant AcquireExclusive: %v", err)
	}
	if err := detector.AcquireRead("reader", "pkg/main.go"); err == nil {
		t.Fatal("AcquireRead succeeded while another task holds exclusive lock")
	}
	if err := detector.AcquireWrite("writer", "pkg/main.go"); err == nil {
		t.Fatal("AcquireWrite succeeded while another task holds exclusive lock")
	}
	detector.ReleaseExclusive("shell")
	if err := detector.AcquireRead("reader", "pkg/main.go"); err == nil {
		t.Fatal("AcquireRead succeeded after partial exclusive release")
	}
	detector.ReleaseExclusive("shell")
	if err := detector.AcquireWrite("writer", "pkg/main.go"); err != nil {
		t.Fatalf("AcquireWrite after exclusive release: %v", err)
	}
}

func TestConflictDetector_ReleaseAllClearsExclusiveLock(t *testing.T) {
	detector := NewConflictDetector()
	if err := detector.AcquireExclusive("shell"); err != nil {
		t.Fatalf("AcquireExclusive: %v", err)
	}
	detector.ReleaseAll("shell")
	if err := detector.AcquireRead("reader", "pkg/main.go"); err != nil {
		t.Fatalf("AcquireRead after ReleaseAll: %v", err)
	}
}
