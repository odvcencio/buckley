//go:build integration
// +build integration

package browser_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/odvcencio/buckley/pkg/browser"
	"github.com/odvcencio/buckley/pkg/browser/adapters/servo"
)

// TestBrowserRuntimeLifecycle tests the full browser session lifecycle with stub engine.
func TestBrowserRuntimeLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	browserdPath := findBrowserdPath(t)
	socketDir := t.TempDir()

	cfg := servo.Config{
		BrowserdPath:   browserdPath,
		SocketDir:      socketDir,
		FrameRate:      12,
		ConnectTimeout: 5 * time.Second,
	}

	runtime, err := servo.NewRuntime(cfg)
	if err != nil {
		t.Fatalf("failed to create runtime: %v", err)
	}

	manager := browser.NewManager(runtime)
	defer manager.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sessionCfg := browser.DefaultSessionConfig()
	sessionCfg.SessionID = "test-session-1"
	sessionCfg.InitialURL = "about:blank"
	sessionCfg.Clipboard.AllowRead = true
	sessionCfg.Clipboard.AllowWrite = true

	sess, err := manager.CreateSession(ctx, sessionCfg)
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	t.Run("initial_observe", func(t *testing.T) {
		obs, err := sess.Observe(ctx, browser.ObserveOptions{
			IncludeDOMSnapshot:   true,
			IncludeAccessibility: true,
		})
		if err != nil {
			t.Fatalf("observe failed: %v", err)
		}
		if obs.StateVersion == 0 {
			t.Error("expected non-zero state version")
		}
		if obs.URL == "" {
			t.Error("expected non-empty URL")
		}
		if len(obs.DOMSnapshot) == 0 {
			t.Error("expected non-empty DOM snapshot")
		}
		if len(obs.AccessibilityTree) == 0 {
			t.Error("expected non-empty accessibility tree")
		}
		t.Logf("initial observation: state_version=%d url=%s", obs.StateVersion, obs.URL)
	})

	t.Run("navigate", func(t *testing.T) {
		obs, err := sess.Navigate(ctx, "https://example.com")
		if err != nil {
			t.Fatalf("navigate failed: %v", err)
		}
		if obs.StateVersion < 2 {
			t.Errorf("expected state version >= 2 after navigate, got %d", obs.StateVersion)
		}
		if obs.URL != "https://example.com" {
			t.Errorf("expected URL https://example.com, got %s", obs.URL)
		}
		t.Logf("after navigate: state_version=%d url=%s", obs.StateVersion, obs.URL)
	})

	var stateVersionAfterClick browser.StateVersion

	t.Run("click_action", func(t *testing.T) {
		action := browser.Action{
			Type: browser.ActionClick,
			Target: &browser.ActionTarget{
				Point: &browser.Point{X: 640, Y: 120},
			},
		}
		result, err := sess.Act(ctx, action)
		if err != nil {
			t.Fatalf("click action failed: %v", err)
		}
		if result.StateVersion == 0 {
			t.Error("expected non-zero state version after click")
		}
		stateVersionAfterClick = result.StateVersion
		t.Logf("after click: state_version=%d effects=%d", result.StateVersion, len(result.Effects))
	})

	t.Run("type_action", func(t *testing.T) {
		action := browser.Action{
			Type: browser.ActionTypeText,
			Target: &browser.ActionTarget{
				NodeID: 3, // INPUT_NODE_ID in stub
			},
			Text: "hello world",
		}
		result, err := sess.Act(ctx, action)
		if err != nil {
			t.Fatalf("type action failed: %v", err)
		}
		if result.StateVersion <= stateVersionAfterClick {
			t.Errorf("expected state version > %d, got %d", stateVersionAfterClick, result.StateVersion)
		}
		t.Logf("after type: state_version=%d", result.StateVersion)
	})

	t.Run("scroll_action", func(t *testing.T) {
		action := browser.Action{
			Type: browser.ActionScroll,
			Scroll: &browser.ScrollDelta{
				X:    0,
				Y:    100,
				Unit: browser.ScrollUnitPixels,
			},
		}
		result, err := sess.Act(ctx, action)
		if err != nil {
			t.Fatalf("scroll action failed: %v", err)
		}
		if result.StateVersion == 0 {
			t.Error("expected non-zero state version after scroll")
		}
		t.Logf("after scroll: state_version=%d", result.StateVersion)
	})

	t.Run("stream_events", func(t *testing.T) {
		// NOTE: Streaming requires a second connection to browserd. The current
		// browserd implementation handles one connection at a time per socket.
		// This test is skipped until multi-connection support is added.
		// The streaming infrastructure (protobuf, IPC, event loop) is tested
		// in other ways and the code paths are exercised by the Rust tests.
		t.Skip("streaming requires multi-connection browserd support")
	})

	t.Run("clipboard_write_read", func(t *testing.T) {
		writeAction := browser.Action{
			Type: browser.ActionClipboardWrite,
			Text: "clipboard test data",
		}
		writeResult, err := sess.Act(ctx, writeAction)
		if err != nil {
			t.Fatalf("clipboard write failed: %v", err)
		}
		t.Logf("clipboard write: state_version=%d", writeResult.StateVersion)

		readAction := browser.Action{
			Type: browser.ActionClipboardRead,
		}
		readResult, err := sess.Act(ctx, readAction)
		if err != nil {
			t.Fatalf("clipboard read failed: %v", err)
		}
		var foundText string
		for _, effect := range readResult.Effects {
			if effect.Kind == "clipboard_read" {
				if text, ok := effect.Metadata["text"].(string); ok {
					foundText = text
					break
				}
			}
		}
		if foundText != "clipboard test data" {
			t.Errorf("expected clipboard text 'clipboard test data', got %q", foundText)
		}
		t.Logf("clipboard read: state_version=%d text=%q", readResult.StateVersion, foundText)
	})

	t.Run("state_version_increments", func(t *testing.T) {
		obs1, err := sess.Observe(ctx, browser.ObserveOptions{})
		if err != nil {
			t.Fatalf("observe 1 failed: %v", err)
		}
		v1 := obs1.StateVersion

		_, err = sess.Act(ctx, browser.Action{
			Type: browser.ActionClick,
			Target: &browser.ActionTarget{
				Point: &browser.Point{X: 100, Y: 100},
			},
		})
		if err != nil {
			t.Fatalf("click failed: %v", err)
		}

		obs2, err := sess.Observe(ctx, browser.ObserveOptions{})
		if err != nil {
			t.Fatalf("observe 2 failed: %v", err)
		}
		v2 := obs2.StateVersion

		if v2 <= v1 {
			t.Errorf("state version should increment: v1=%d v2=%d", v1, v2)
		}
		t.Logf("state version incremented: %d -> %d", v1, v2)
	})

	t.Run("close_session", func(t *testing.T) {
		err := manager.CloseSession(sessionCfg.SessionID)
		if err != nil {
			t.Fatalf("close session failed: %v", err)
		}

		_, ok := manager.GetSession(sessionCfg.SessionID)
		if ok {
			t.Error("session should not exist after close")
		}
		t.Log("session closed successfully")
	})
}

// TestBrowserRuntimeMultipleSessions tests managing multiple concurrent sessions.
func TestBrowserRuntimeMultipleSessions(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	browserdPath := findBrowserdPath(t)
	socketDir := t.TempDir()

	cfg := servo.Config{
		BrowserdPath:   browserdPath,
		SocketDir:      socketDir,
		FrameRate:      12,
		ConnectTimeout: 5 * time.Second,
	}

	runtime, err := servo.NewRuntime(cfg)
	if err != nil {
		t.Fatalf("failed to create runtime: %v", err)
	}

	manager := browser.NewManager(runtime)
	defer manager.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sessionIDs := []string{"multi-session-1", "multi-session-2", "multi-session-3"}

	for _, sessionID := range sessionIDs {
		sessionCfg := browser.DefaultSessionConfig()
		sessionCfg.SessionID = sessionID

		_, err := manager.CreateSession(ctx, sessionCfg)
		if err != nil {
			t.Fatalf("failed to create session %s: %v", sessionID, err)
		}
	}

	for _, sessionID := range sessionIDs {
		sess, ok := manager.GetSession(sessionID)
		if !ok {
			t.Errorf("session %s not found", sessionID)
			continue
		}
		obs, err := sess.Observe(ctx, browser.ObserveOptions{})
		if err != nil {
			t.Errorf("observe failed for session %s: %v", sessionID, err)
			continue
		}
		if obs.StateVersion == 0 {
			t.Errorf("session %s has zero state version", sessionID)
		}
	}

	for _, sessionID := range sessionIDs {
		err := manager.CloseSession(sessionID)
		if err != nil {
			t.Errorf("failed to close session %s: %v", sessionID, err)
		}
	}

	t.Logf("created and closed %d sessions successfully", len(sessionIDs))
}

// TestBrowserRuntimeDuplicateSession tests that creating a duplicate session fails.
func TestBrowserRuntimeDuplicateSession(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	browserdPath := findBrowserdPath(t)
	socketDir := t.TempDir()

	cfg := servo.Config{
		BrowserdPath:   browserdPath,
		SocketDir:      socketDir,
		FrameRate:      12,
		ConnectTimeout: 5 * time.Second,
	}

	runtime, err := servo.NewRuntime(cfg)
	if err != nil {
		t.Fatalf("failed to create runtime: %v", err)
	}

	manager := browser.NewManager(runtime)
	defer manager.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sessionCfg := browser.DefaultSessionConfig()
	sessionCfg.SessionID = "dup-session"

	_, err = manager.CreateSession(ctx, sessionCfg)
	if err != nil {
		t.Fatalf("failed to create first session: %v", err)
	}

	_, err = manager.CreateSession(ctx, sessionCfg)
	if err == nil {
		t.Error("expected error when creating duplicate session")
	} else {
		t.Logf("correctly rejected duplicate session: %v", err)
	}
}

func findBrowserdPath(t *testing.T) string {
	t.Helper()

	candidates := []string{
		os.Getenv("BROWSERD_PATH"),
		"../../apps/browserd/target/release/browserd",
		"../../apps/browserd/target/debug/browserd",
		"../../../apps/browserd/target/release/browserd",
		"../../../apps/browserd/target/debug/browserd",
		"apps/browserd/target/release/browserd",
		"apps/browserd/target/debug/browserd",
	}

	cwd, _ := os.Getwd()

	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}

		var absPath string
		if filepath.IsAbs(candidate) {
			absPath = candidate
		} else {
			absPath = filepath.Join(cwd, candidate)
		}

		if info, err := os.Stat(absPath); err == nil && !info.IsDir() {
			return absPath
		}
	}

	if cwd != "" {
		for dir := cwd; dir != "/" && dir != "."; dir = filepath.Dir(dir) {
			for _, rel := range []string{
				"apps/browserd/target/release/browserd",
				"apps/browserd/target/debug/browserd",
			} {
				absPath := filepath.Join(dir, rel)
				if info, err := os.Stat(absPath); err == nil && !info.IsDir() {
					return absPath
				}
			}
		}
	}

	t.Skip("browserd binary not found; build with 'cargo build --release' in apps/browserd")
	return ""
}
