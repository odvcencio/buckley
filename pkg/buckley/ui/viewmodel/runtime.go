package viewmodel

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/odvcencio/buckley/pkg/telemetry"
	"github.com/odvcencio/buckley/pkg/touch"
)

const maxRecentFiles = 5

// RuntimeStateTracker maintains real-time session state from telemetry events.
// It provides runtime state that complements the storage-based state in the assembler.
type RuntimeStateTracker struct {
	hub      *telemetry.Hub
	mu       sync.RWMutex
	sessions map[string]*sessionRuntime
	eventCh  <-chan telemetry.Event
	cancel   context.CancelFunc
	wg       sync.WaitGroup
}

// sessionRuntime holds live state for a single session.
type sessionRuntime struct {
	isStreaming   bool
	runningTools  map[string]ToolCall
	recentFiles   []FileTouch
	activeTouches map[string]CodeTouch
}

// NewRuntimeStateTracker creates a tracker that subscribes to telemetry events.
func NewRuntimeStateTracker(hub *telemetry.Hub) *RuntimeStateTracker {
	t := &RuntimeStateTracker{
		hub:      hub,
		sessions: make(map[string]*sessionRuntime),
	}
	return t
}

// Start begins processing telemetry events.
func (t *RuntimeStateTracker) Start(ctx context.Context) {
	if t.hub == nil {
		return
	}

	ctx, t.cancel = context.WithCancel(ctx)
	t.eventCh, _ = t.hub.Subscribe()

	t.wg.Add(1)
	go t.processEvents(ctx)
}

// Stop ceases event processing.
func (t *RuntimeStateTracker) Stop() {
	if t.cancel != nil {
		t.cancel()
	}
	t.wg.Wait()
}

// GetRuntimeState returns the current runtime state for a session.
func (t *RuntimeStateTracker) GetRuntimeState(sessionID string) (isStreaming bool, tools []ToolCall, files []FileTouch, touches []CodeTouch) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	sess := t.sessions[sessionID]
	if sess == nil {
		return false, nil, nil, nil
	}

	isStreaming = sess.isStreaming

	tools = make([]ToolCall, 0, len(sess.runningTools))
	for _, tc := range sess.runningTools {
		tools = append(tools, tc)
	}

	files = make([]FileTouch, len(sess.recentFiles))
	copy(files, sess.recentFiles)

	now := time.Now()
	touches = make([]CodeTouch, 0, len(sess.activeTouches))
	for id, touch := range sess.activeTouches {
		if !touch.ExpiresAt.IsZero() && now.After(touch.ExpiresAt) {
			delete(sess.activeTouches, id)
			continue
		}
		touches = append(touches, touch)
	}

	return isStreaming, tools, files, touches
}

// SetStreaming directly sets the streaming state for a session.
// This is called by the orchestrator/controller when streaming starts/ends.
func (t *RuntimeStateTracker) SetStreaming(sessionID string, streaming bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	sess := t.getOrCreateSession(sessionID)
	sess.isStreaming = streaming
}

// processEvents handles telemetry events in a loop.
func (t *RuntimeStateTracker) processEvents(ctx context.Context) {
	defer t.wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-t.eventCh:
			if !ok {
				return
			}
			t.handleEvent(event)
		}
	}
}

// handleEvent processes a single telemetry event.
func (t *RuntimeStateTracker) handleEvent(event telemetry.Event) {
	t.mu.Lock()
	defer t.mu.Unlock()

	sess := t.getOrCreateSession(event.SessionID)

	switch event.Type {
	case telemetry.EventModelStreamStarted:
		sess.isStreaming = true
	case telemetry.EventModelStreamEnded:
		sess.isStreaming = false

	// Shell commands
	case telemetry.EventShellCommandStarted:
		cmd := getString(event.Data, "command")
		sess.runningTools[event.TaskID] = ToolCall{
			ID:        event.TaskID,
			Name:      "shell",
			Status:    "running",
			Command:   truncateString(cmd, 50),
			StartedAt: event.Timestamp,
		}
	case telemetry.EventShellCommandCompleted:
		delete(sess.runningTools, event.TaskID)
	case telemetry.EventShellCommandFailed:
		delete(sess.runningTools, event.TaskID)

	// Builder
	case telemetry.EventBuilderStarted:
		sess.runningTools[event.TaskID] = ToolCall{
			ID:        event.TaskID,
			Name:      "builder",
			Status:    "running",
			Command:   "Building...",
			StartedAt: event.Timestamp,
		}
	case telemetry.EventBuilderCompleted, telemetry.EventBuilderFailed:
		delete(sess.runningTools, event.TaskID)

	// Research
	case telemetry.EventResearchStarted:
		query := getString(event.Data, "query")
		sess.runningTools["research"] = ToolCall{
			ID:        "research",
			Name:      "research",
			Status:    "running",
			Command:   truncateString(query, 50),
			StartedAt: event.Timestamp,
		}
	case telemetry.EventResearchCompleted, telemetry.EventResearchFailed:
		delete(sess.runningTools, "research")

	// Index
	case telemetry.EventIndexStarted:
		sess.runningTools["index"] = ToolCall{
			ID:        "index",
			Name:      "index",
			Status:    "running",
			Command:   "Indexing...",
			StartedAt: event.Timestamp,
		}
	case telemetry.EventIndexCompleted, telemetry.EventIndexFailed:
		delete(sess.runningTools, "index")

	// Editor events -> recent files
	case telemetry.EventEditorApply, telemetry.EventEditorInline:
		for _, path := range extractPaths(event.Data) {
			t.addRecentFile(sess, path, "write", event.Timestamp)
		}
	case telemetry.EventEditorPropose:
		for _, path := range extractPaths(event.Data) {
			t.addRecentFile(sess, path, "read", event.Timestamp)
		}
	case telemetry.EventToolStarted:
		if event.TaskID != "" {
			toolName := getString(event.Data, "toolName")
			if toolName == "" {
				toolName = getString(event.Data, "tool")
			}
			desc := getString(event.Data, "description")
			if desc == "" {
				desc = getString(event.Data, "command")
			}
			if desc == "" {
				desc = getString(event.Data, "filePath")
			}
			sess.runningTools[event.TaskID] = ToolCall{
				ID:        event.TaskID,
				Name:      firstNonEmpty(toolName, "tool"),
				Status:    "running",
				Command:   truncateString(desc, 50),
				StartedAt: event.Timestamp,
			}
		}
		touch := touchFromEvent(event)
		if touch.FilePath != "" {
			if sess.activeTouches == nil {
				sess.activeTouches = make(map[string]CodeTouch)
			}
			sess.activeTouches[touch.ID] = touch
		}
	case telemetry.EventToolCompleted, telemetry.EventToolFailed:
		if event.TaskID != "" {
			delete(sess.runningTools, event.TaskID)
		}
		if sess.activeTouches != nil {
			delete(sess.activeTouches, event.TaskID)
		}
	}
}

// getOrCreateSession returns the session runtime, creating if needed.
func (t *RuntimeStateTracker) getOrCreateSession(sessionID string) *sessionRuntime {
	if sessionID == "" {
		sessionID = "_global"
	}
	sess := t.sessions[sessionID]
	if sess == nil {
		sess = &sessionRuntime{
			runningTools:  make(map[string]ToolCall),
			activeTouches: make(map[string]CodeTouch),
		}
		t.sessions[sessionID] = sess
	}
	return sess
}

// addRecentFile adds a file to the recent files list.
func (t *RuntimeStateTracker) addRecentFile(sess *sessionRuntime, path, op string, ts time.Time) {
	// Remove if already exists
	for i, f := range sess.recentFiles {
		if f.Path == path {
			sess.recentFiles = append(sess.recentFiles[:i], sess.recentFiles[i+1:]...)
			break
		}
	}

	// Add to front
	touch := FileTouch{
		Path:      path,
		Operation: op,
		TouchedAt: ts,
	}
	sess.recentFiles = append([]FileTouch{touch}, sess.recentFiles...)

	// Cap size
	if len(sess.recentFiles) > maxRecentFiles {
		sess.recentFiles = sess.recentFiles[:maxRecentFiles]
	}
}

// Helper functions

func getString(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func extractPaths(data map[string]any) []string {
	if data == nil {
		return nil
	}
	if path := getString(data, "path"); path != "" {
		return []string{path}
	}
	if path := getString(data, "filePath"); path != "" {
		return []string{path}
	}
	if raw, ok := data["files"]; ok {
		switch v := raw.(type) {
		case []string:
			return v
		case []any:
			out := make([]string, 0, len(v))
			for _, item := range v {
				if s, ok := item.(string); ok && s != "" {
					out = append(out, s)
				}
			}
			return out
		}
	}
	return nil
}

func truncateString(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func touchFromEvent(event telemetry.Event) CodeTouch {
	touch := CodeTouch{
		ID:        event.TaskID,
		ToolName:  getString(event.Data, "toolName"),
		Operation: getString(event.Data, "operationType"),
		FilePath:  getString(event.Data, "filePath"),
		StartedAt: event.Timestamp,
	}
	touch.Ranges = extractRanges(event.Data)
	touch.ExpiresAt = extractTime(event.Data, "expiresAt")
	if touch.ExpiresAt.IsZero() && touch.Operation != "" {
		touch.ExpiresAt = event.Timestamp.Add(defaultTouchTTL(touch.Operation))
	}
	if touch.ID == "" {
		touch.ID = touch.FilePath + ":" + touch.Operation
	}
	return touch
}

func extractRanges(data map[string]any) []LineRange {
	if data == nil {
		return nil
	}
	if ranges, ok := data["ranges"].([]LineRange); ok {
		return ranges
	}
	if ranges, ok := data["ranges"].([]touch.LineRange); ok {
		out := make([]LineRange, 0, len(ranges))
		for _, r := range ranges {
			out = append(out, LineRange{Start: r.Start, End: r.End})
		}
		return out
	}
	raw, ok := data["ranges"].([]any)
	if !ok {
		return nil
	}
	out := make([]LineRange, 0, len(raw))
	for _, item := range raw {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		start := getInt(entry, "start")
		end := getInt(entry, "end")
		if start == 0 && end == 0 {
			continue
		}
		if end == 0 {
			end = start
		}
		out = append(out, LineRange{Start: start, End: end})
	}
	return out
}

func extractTime(data map[string]any, key string) time.Time {
	if data == nil {
		return time.Time{}
	}
	switch v := data[key].(type) {
	case time.Time:
		return v
	case string:
		if parsed, err := time.Parse(time.RFC3339, v); err == nil {
			return parsed
		}
	}
	return time.Time{}
}

func getInt(m map[string]any, key string) int {
	if v, ok := m[key]; ok {
		switch val := v.(type) {
		case int:
			return val
		case int32:
			return int(val)
		case int64:
			return int(val)
		case float64:
			return int(val)
		}
	}
	return 0
}

func defaultTouchTTL(operation string) time.Duration {
	switch operation {
	case "read", "shell:read", "git:read":
		return 2 * time.Minute
	case "write", "delete", "shell:write", "git:write":
		return 10 * time.Minute
	case "shell:network", "network":
		return 5 * time.Minute
	default:
		return 3 * time.Minute
	}
}
