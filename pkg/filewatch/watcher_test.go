package filewatch

import "testing"

func TestFileWatcher_SubscribeAndNotify(t *testing.T) {
	watcher := NewFileWatcher(10)
	var calls []FileChange
	watcher.Subscribe("*.go", func(change FileChange) {
		calls = append(calls, change)
	})

	watcher.Notify(FileChange{Path: "pkg/main.go", Type: ChangeModified})
	watcher.Notify(FileChange{Path: "README.md", Type: ChangeModified})

	if len(calls) != 1 {
		t.Fatalf("expected 1 matching change, got %d", len(calls))
	}
	if calls[0].Path != "pkg/main.go" {
		t.Fatalf("unexpected path %q", calls[0].Path)
	}
}

func TestFileWatcher_RecentChangesLimit(t *testing.T) {
	watcher := NewFileWatcher(2)
	watcher.Notify(FileChange{Path: "a", Type: ChangeModified})
	watcher.Notify(FileChange{Path: "b", Type: ChangeModified})
	watcher.Notify(FileChange{Path: "c", Type: ChangeModified})

	recent := watcher.RecentChanges(2)
	if len(recent) != 2 {
		t.Fatalf("expected 2 recent changes, got %d", len(recent))
	}
	if recent[0].Path != "c" || recent[1].Path != "b" {
		t.Fatalf("unexpected recent order: %q, %q", recent[0].Path, recent[1].Path)
	}
}

func TestFileWatcher_Unsubscribe(t *testing.T) {
	watcher := NewFileWatcher(10)
	called := false
	id := watcher.Subscribe("*.go", func(change FileChange) {
		called = true
	})
	watcher.Unsubscribe(id)
	watcher.Notify(FileChange{Path: "main.go", Type: ChangeModified})
	if called {
		t.Fatalf("expected handler to be unsubscribed")
	}
}
