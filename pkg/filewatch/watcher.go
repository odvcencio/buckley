package filewatch

import (
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
)

// ChangeType describes the kind of file change observed.
type ChangeType string

const (
	ChangeCreated  ChangeType = "created"
	ChangeModified ChangeType = "modified"
	ChangeDeleted  ChangeType = "deleted"
	ChangeRenamed  ChangeType = "renamed"
)

const defaultMaxHistory = 100

// FileChange records a tool-driven change to a file.
type FileChange struct {
	Path     string
	Type     ChangeType
	OldPath  string
	Size     int64
	ModTime  time.Time
	ToolName string
	CallID   string
}

// FileChangeHandler receives file change notifications.
type FileChangeHandler func(change FileChange)

// Subscription binds a pattern to a handler.
type Subscription struct {
	ID      string
	Pattern string
	Handler FileChangeHandler
}

// FileWatcher tracks tool-emitted file changes.
type FileWatcher struct {
	mu            sync.RWMutex
	subscriptions map[string]*Subscription
	recentChanges []FileChange
	maxHistory    int
}

// NewFileWatcher creates a watcher with bounded history.
func NewFileWatcher(maxHistory int) *FileWatcher {
	if maxHistory <= 0 {
		maxHistory = defaultMaxHistory
	}
	return &FileWatcher{
		subscriptions: make(map[string]*Subscription),
		maxHistory:    maxHistory,
	}
}

// Subscribe registers a file change handler for a glob pattern.
func (fw *FileWatcher) Subscribe(pattern string, handler FileChangeHandler) string {
	if fw == nil || handler == nil {
		return ""
	}
	id := ulid.Make().String()
	sub := &Subscription{
		ID:      id,
		Pattern: strings.TrimSpace(pattern),
		Handler: handler,
	}
	fw.mu.Lock()
	if fw.subscriptions == nil {
		fw.subscriptions = make(map[string]*Subscription)
	}
	fw.subscriptions[id] = sub
	fw.mu.Unlock()
	return id
}

// Unsubscribe removes a subscription.
func (fw *FileWatcher) Unsubscribe(id string) {
	if fw == nil || strings.TrimSpace(id) == "" {
		return
	}
	fw.mu.Lock()
	delete(fw.subscriptions, id)
	fw.mu.Unlock()
}

// Notify publishes a file change event.
func (fw *FileWatcher) Notify(change FileChange) {
	if fw == nil {
		return
	}
	fw.mu.Lock()
	fw.ensureHistoryLocked()
	fw.recentChanges = append(fw.recentChanges, change)
	if len(fw.recentChanges) > fw.maxHistory {
		fw.recentChanges = fw.recentChanges[len(fw.recentChanges)-fw.maxHistory:]
	}
	subs := make([]*Subscription, 0, len(fw.subscriptions))
	for _, sub := range fw.subscriptions {
		subs = append(subs, sub)
	}
	fw.mu.Unlock()

	for _, sub := range subs {
		if sub == nil || sub.Handler == nil {
			continue
		}
		if matchesPattern(sub.Pattern, change.Path) {
			sub.Handler(change)
		}
	}
}

// RecentChanges returns the most recent changes (newest first).
func (fw *FileWatcher) RecentChanges(limit int) []FileChange {
	if fw == nil {
		return nil
	}
	fw.mu.RLock()
	defer fw.mu.RUnlock()
	if limit <= 0 || limit > len(fw.recentChanges) {
		limit = len(fw.recentChanges)
	}
	out := make([]FileChange, 0, limit)
	for i := len(fw.recentChanges) - 1; i >= len(fw.recentChanges)-limit; i-- {
		out = append(out, fw.recentChanges[i])
	}
	return out
}

func (fw *FileWatcher) ensureHistoryLocked() {
	if fw.maxHistory <= 0 {
		fw.maxHistory = defaultMaxHistory
	}
}

func matchesPattern(pattern, filePath string) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" || pattern == "*" {
		return true
	}
	cleanPath := filepath.ToSlash(strings.TrimSpace(filePath))
	cleanPattern := filepath.ToSlash(pattern)
	if ok, _ := path.Match(cleanPattern, cleanPath); ok {
		return true
	}
	if !strings.Contains(cleanPattern, "/") {
		base := path.Base(cleanPath)
		if ok, _ := path.Match(cleanPattern, base); ok {
			return true
		}
	}
	return false
}
