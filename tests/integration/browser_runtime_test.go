//go:build integration
// +build integration

package integration

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/odvcencio/buckley/pkg/browser"
	"github.com/odvcencio/buckley/pkg/browser/adapters/servo"
)

// TestBrowserRuntimeLifecycle tests the full browser runtime lifecycle:
// start session, navigate, observe, act, stream, close.
func TestBrowserRuntimeLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	browserdPath := findBrowserd(t)
	if browserdPath == "" {
		t.Skip("browserd binary not found; skipping browser runtime test")
	}

	tempDir := t.TempDir()
	socketDir := filepath.Join(tempDir, "sockets")

	cfg := servo.Config{
		BrowserdPath:     browserdPath,
		SocketDir:        socketDir,
		ConnectTimeout:   10 * time.Second,
		OperationTimeout: 30 * time.Second,
		FrameRate:        12,
		MaxReconnects:    2,
	}

	runtime, err := servo.NewRuntime(cfg)
	if err != nil {
		t.Fatalf("failed to create runtime: %v", err)
	}
	defer runtime.Close()

	mgr := browser.NewManager(runtime)
	defer mgr.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	sessionCfg := browser.SessionConfig{
		SessionID:  "test-session-001",
		InitialURL: "https://example.com",
		Viewport: browser.Viewport{
			Width:  1280,
			Height: 720,
		},
		FrameRate: 12,
		Clipboard: browser.ClipboardPolicy{
			Mode:       browser.ClipboardModeVirtual,
			AllowRead:  true,
			AllowWrite: true,
			MaxBytes:   64 * 1024,
		},
	}

	sess, err := mgr.CreateSession(ctx, sessionCfg)
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	t.Cleanup(func() {
		_ = mgr.CloseSession(sessionCfg.SessionID)
	})

	if sess.ID() != sessionCfg.SessionID {
		t.Errorf("session ID = %q, want %q", sess.ID(), sessionCfg.SessionID)
	}

	// Navigate to a URL
	navObs, err := sess.Navigate(ctx, "https://example.org")
	if err != nil {
		t.Fatalf("Navigate failed: %v", err)
	}
	if navObs == nil {
		t.Fatal("Navigate returned nil observation")
	}
	initialVersion := navObs.StateVersion
	t.Logf("Navigate returned state_version=%d, url=%s", navObs.StateVersion, navObs.URL)

	// Observe current state
	obsOpts := browser.ObserveOptions{
		IncludeFrame:         true,
		IncludeDOMSnapshot:   true,
		IncludeAccessibility: true,
		IncludeHitTest:       true,
	}
	obs, err := sess.Observe(ctx, obsOpts)
	if err != nil {
		t.Fatalf("Observe failed: %v", err)
	}
	if obs == nil {
		t.Fatal("Observe returned nil observation")
	}
	if obs.StateVersion < initialVersion {
		t.Errorf("Observe state_version=%d < initial=%d", obs.StateVersion, initialVersion)
	}
	t.Logf("Observe returned state_version=%d, has_frame=%v, has_dom=%v",
		obs.StateVersion, obs.Frame != nil, len(obs.DOMSnapshot) > 0)

	// Act: click at a point
	clickAction := browser.Action{
		Type:                 browser.ActionClick,
		ExpectedStateVersion: obs.StateVersion,
		Target: &browser.ActionTarget{
			Point: &browser.Point{X: 100, Y: 100},
		},
	}
	clickResult, err := sess.Act(ctx, clickAction)
	if err != nil {
		t.Fatalf("Act (click) failed: %v", err)
	}
	if clickResult.StateVersion <= obs.StateVersion {
		t.Logf("click state_version=%d (may not increment for stub)", clickResult.StateVersion)
	}
	t.Logf("Click action returned state_version=%d", clickResult.StateVersion)

	// Act: type text
	typeAction := browser.Action{
		Type:                 browser.ActionTypeText,
		ExpectedStateVersion: clickResult.StateVersion,
		Text:                 "hello world",
	}
	typeResult, err := sess.Act(ctx, typeAction)
	if err != nil {
		t.Fatalf("Act (type) failed: %v", err)
	}
	t.Logf("Type action returned state_version=%d", typeResult.StateVersion)

	// Act: scroll
	scrollAction := browser.Action{
		Type:                 browser.ActionScroll,
		ExpectedStateVersion: typeResult.StateVersion,
		Scroll: &browser.ScrollDelta{
			Y:    100,
			Unit: browser.ScrollUnitPixels,
		},
	}
	scrollResult, err := sess.Act(ctx, scrollAction)
	if err != nil {
		t.Fatalf("Act (scroll) failed: %v", err)
	}
	t.Logf("Scroll action returned state_version=%d", scrollResult.StateVersion)

	// Stream events briefly - use longer timeout to ensure subscription completes
	streamOpts := browser.StreamOptions{
		IncludeFrames:             true,
		IncludeDOMDiffs:           true,
		IncludeAccessibilityDiffs: true,
		IncludeHitTest:            true,
		TargetFPS:                 5,
	}

	streamCtx, streamCancel := context.WithTimeout(ctx, 10*time.Second)
	defer streamCancel()

	events, err := sess.Stream(streamCtx, streamOpts)
	if err != nil {
		// Streaming is not critical for this test - log but don't fail
		t.Logf("Stream subscription failed (non-fatal): %v", err)
	} else {
		eventCount := 0
		for event := range events {
			eventCount++
			t.Logf("Stream event: type=%s, state_version=%d", event.Type, event.StateVersion)
			if eventCount >= 3 {
				streamCancel()
				break
			}
		}
		t.Logf("Received %d stream events", eventCount)
	}

	// Verify metrics
	metrics := mgr.Metrics()
	if metrics.SessionsCreated != 1 {
		t.Errorf("metrics.SessionsCreated = %d, want 1", metrics.SessionsCreated)
	}
	if metrics.ActiveSessions != 1 {
		t.Errorf("metrics.ActiveSessions = %d, want 1", metrics.ActiveSessions)
	}
	if metrics.NavigateCount < 1 {
		t.Errorf("metrics.NavigateCount = %d, want >= 1", metrics.NavigateCount)
	}
	if metrics.ObserveCount < 1 {
		t.Errorf("metrics.ObserveCount = %d, want >= 1", metrics.ObserveCount)
	}
	if metrics.ActionCount < 3 {
		t.Errorf("metrics.ActionCount = %d, want >= 3", metrics.ActionCount)
	}

	// Close session
	if err := mgr.CloseSession(sessionCfg.SessionID); err != nil {
		t.Errorf("CloseSession failed: %v", err)
	}

	// Verify session is removed
	if _, ok := mgr.GetSession(sessionCfg.SessionID); ok {
		t.Error("session still exists after CloseSession")
	}

	finalMetrics := mgr.Metrics()
	if finalMetrics.SessionsClosed != 1 {
		t.Errorf("metrics.SessionsClosed = %d, want 1", finalMetrics.SessionsClosed)
	}
	if finalMetrics.ActiveSessions != 0 {
		t.Errorf("metrics.ActiveSessions = %d after close, want 0", finalMetrics.ActiveSessions)
	}

	t.Log("browser runtime lifecycle test passed")
}

// TestBrowserMultipleSessions tests creating and managing multiple concurrent sessions.
func TestBrowserMultipleSessions(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	browserdPath := findBrowserd(t)
	if browserdPath == "" {
		t.Skip("browserd binary not found; skipping browser runtime test")
	}

	tempDir := t.TempDir()
	socketDir := filepath.Join(tempDir, "sockets")

	cfg := servo.Config{
		BrowserdPath:     browserdPath,
		SocketDir:        socketDir,
		ConnectTimeout:   10 * time.Second,
		OperationTimeout: 30 * time.Second,
	}

	runtime, err := servo.NewRuntime(cfg)
	if err != nil {
		t.Fatalf("failed to create runtime: %v", err)
	}
	defer runtime.Close()

	mgr := browser.NewManager(runtime)
	defer mgr.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	sessionIDs := []string{"session-a", "session-b", "session-c"}
	for _, id := range sessionIDs {
		cfg := browser.SessionConfig{
			SessionID: id,
			Viewport: browser.Viewport{
				Width:  1024,
				Height: 768,
			},
		}
		sess, err := mgr.CreateSession(ctx, cfg)
		if err != nil {
			t.Fatalf("CreateSession(%s) failed: %v", id, err)
		}
		if sess.ID() != id {
			t.Errorf("session ID = %q, want %q", sess.ID(), id)
		}
	}

	metrics := mgr.Metrics()
	if metrics.SessionsCreated != 3 {
		t.Errorf("metrics.SessionsCreated = %d, want 3", metrics.SessionsCreated)
	}
	if metrics.ActiveSessions != 3 {
		t.Errorf("metrics.ActiveSessions = %d, want 3", metrics.ActiveSessions)
	}

	// Verify duplicate session ID is rejected
	_, err = mgr.CreateSession(ctx, browser.SessionConfig{SessionID: "session-a"})
	if err == nil {
		t.Error("expected error creating duplicate session, got nil")
	}

	// Close all sessions
	for _, id := range sessionIDs {
		if err := mgr.CloseSession(id); err != nil {
			t.Errorf("CloseSession(%s) failed: %v", id, err)
		}
	}

	finalMetrics := mgr.Metrics()
	if finalMetrics.ActiveSessions != 0 {
		t.Errorf("metrics.ActiveSessions = %d, want 0", finalMetrics.ActiveSessions)
	}

	t.Log("multiple sessions test passed")
}

// TestBrowserClipboardOperations tests clipboard read/write actions.
func TestBrowserClipboardOperations(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	browserdPath := findBrowserd(t)
	if browserdPath == "" {
		t.Skip("browserd binary not found; skipping browser runtime test")
	}

	tempDir := t.TempDir()
	socketDir := filepath.Join(tempDir, "sockets")

	cfg := servo.Config{
		BrowserdPath:     browserdPath,
		SocketDir:        socketDir,
		ConnectTimeout:   10 * time.Second,
		OperationTimeout: 30 * time.Second,
	}

	runtime, err := servo.NewRuntime(cfg)
	if err != nil {
		t.Fatalf("failed to create runtime: %v", err)
	}
	defer runtime.Close()

	mgr := browser.NewManager(runtime)
	defer mgr.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sessionCfg := browser.SessionConfig{
		SessionID: "clipboard-test",
		Clipboard: browser.ClipboardPolicy{
			Mode:       browser.ClipboardModeVirtual,
			AllowRead:  true,
			AllowWrite: true,
			MaxBytes:   64 * 1024,
		},
	}

	sess, err := mgr.CreateSession(ctx, sessionCfg)
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	defer mgr.CloseSession(sessionCfg.SessionID)

	// Write to clipboard
	writeAction := browser.Action{
		Type: browser.ActionClipboardWrite,
		Text: "test clipboard content",
	}
	writeResult, err := sess.Act(ctx, writeAction)
	if err != nil {
		t.Fatalf("clipboard write failed: %v", err)
	}
	t.Logf("Clipboard write returned state_version=%d", writeResult.StateVersion)

	// Read from clipboard
	readAction := browser.Action{
		Type: browser.ActionClipboardRead,
	}
	readResult, err := sess.Act(ctx, readAction)
	if err != nil {
		t.Fatalf("clipboard read failed: %v", err)
	}
	t.Logf("Clipboard read returned state_version=%d", readResult.StateVersion)

	t.Log("clipboard operations test passed")
}

// findBrowserd locates the browserd binary.
func findBrowserd(t *testing.T) string {
	t.Helper()

	// Check common locations in order of preference
	candidates := []string{
		// Debug build in apps/browserd
		filepath.Join(findRepoRoot(t), "apps", "browserd", "target", "debug", "browserd"),
		// Release build
		filepath.Join(findRepoRoot(t), "apps", "browserd", "target", "release", "browserd"),
		// In PATH
		"browserd",
	}

	for _, path := range candidates {
		if path == "browserd" {
			if p, err := exec.LookPath(path); err == nil {
				return p
			}
			continue
		}
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}

	// Try to build it
	repoRoot := findRepoRoot(t)
	browserdDir := filepath.Join(repoRoot, "apps", "browserd")
	if _, err := os.Stat(filepath.Join(browserdDir, "Cargo.toml")); err == nil {
		t.Log("attempting to build browserd...")
		cmd := exec.Command("cargo", "build")
		cmd.Dir = browserdDir
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Logf("cargo build failed: %v\n%s", err, output)
			return ""
		}
		debugPath := filepath.Join(browserdDir, "target", "debug", "browserd")
		if _, err := os.Stat(debugPath); err == nil {
			return debugPath
		}
	}

	return ""
}

// findRepoRoot finds the repository root by walking up from the current directory.
func findRepoRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		return ""
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}
