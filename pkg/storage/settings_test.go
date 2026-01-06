package storage

import (
	"path/filepath"
	"testing"
)

func TestSettingsLifecycle(t *testing.T) {
	dir := t.TempDir()
	store, err := New(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// Test setting a value
	if err := store.SetSetting("theme", "dark"); err != nil {
		t.Fatalf("failed to set setting: %v", err)
	}

	// Test getting a single setting
	settings, err := store.GetSettings([]string{"theme"})
	if err != nil {
		t.Fatalf("failed to get settings: %v", err)
	}
	if settings["theme"] != "dark" {
		t.Errorf("expected theme=dark, got %q", settings["theme"])
	}

	// Test updating a setting
	if err := store.SetSetting("theme", "light"); err != nil {
		t.Fatalf("failed to update setting: %v", err)
	}
	settings, err = store.GetSettings([]string{"theme"})
	if err != nil {
		t.Fatalf("failed to get settings after update: %v", err)
	}
	if settings["theme"] != "light" {
		t.Errorf("expected theme=light, got %q", settings["theme"])
	}

	// Test deleting a setting (empty value)
	if err := store.SetSetting("theme", ""); err != nil {
		t.Fatalf("failed to delete setting: %v", err)
	}
	settings, err = store.GetSettings([]string{"theme"})
	if err != nil {
		t.Fatalf("failed to get settings after delete: %v", err)
	}
	if _, exists := settings["theme"]; exists {
		t.Errorf("expected theme to be deleted, but it exists")
	}
}

func TestGetSettingsMultiple(t *testing.T) {
	dir := t.TempDir()
	store, err := New(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// Set multiple settings
	if err := store.SetSetting("key1", "value1"); err != nil {
		t.Fatalf("failed to set key1: %v", err)
	}
	if err := store.SetSetting("key2", "value2"); err != nil {
		t.Fatalf("failed to set key2: %v", err)
	}
	if err := store.SetSetting("key3", "value3"); err != nil {
		t.Fatalf("failed to set key3: %v", err)
	}

	// Get multiple settings
	settings, err := store.GetSettings([]string{"key1", "key2", "key3", "nonexistent"})
	if err != nil {
		t.Fatalf("failed to get settings: %v", err)
	}

	if len(settings) != 3 {
		t.Errorf("expected 3 settings, got %d", len(settings))
	}
	if settings["key1"] != "value1" {
		t.Errorf("expected key1=value1, got %q", settings["key1"])
	}
	if settings["key2"] != "value2" {
		t.Errorf("expected key2=value2, got %q", settings["key2"])
	}
	if settings["key3"] != "value3" {
		t.Errorf("expected key3=value3, got %q", settings["key3"])
	}
	if _, exists := settings["nonexistent"]; exists {
		t.Errorf("expected nonexistent key to not be in results")
	}
}

func TestGetSettingsEmpty(t *testing.T) {
	dir := t.TempDir()
	store, err := New(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// Get with empty keys list
	settings, err := store.GetSettings([]string{})
	if err != nil {
		t.Fatalf("failed to get empty settings: %v", err)
	}
	if len(settings) != 0 {
		t.Errorf("expected empty map, got %d items", len(settings))
	}
}

func TestSetSettingEdgeCases(t *testing.T) {
	dir := t.TempDir()
	store, err := New(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// Empty key should be ignored
	if err := store.SetSetting("", "value"); err != nil {
		t.Fatalf("unexpected error for empty key: %v", err)
	}

	// Whitespace key should be trimmed and ignored
	if err := store.SetSetting("   ", "value"); err != nil {
		t.Fatalf("unexpected error for whitespace key: %v", err)
	}

	// Whitespace value should be trimmed
	if err := store.SetSetting("key", "  value  "); err != nil {
		t.Fatalf("failed to set key with whitespace value: %v", err)
	}
	settings, err := store.GetSettings([]string{"key"})
	if err != nil {
		t.Fatalf("failed to get settings: %v", err)
	}
	if settings["key"] != "value" {
		t.Errorf("expected value to be trimmed, got %q", settings["key"])
	}
}

func TestAuditLogLifecycle(t *testing.T) {
	dir := t.TempDir()
	store, err := New(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// Record an audit log with no payload
	if err := store.RecordAuditLog("user1", "settings", "update", nil); err != nil {
		t.Fatalf("failed to record audit log: %v", err)
	}

	// Record an audit log with payload
	payload := map[string]string{"key": "theme", "value": "dark"}
	if err := store.RecordAuditLog("user2", "settings", "create", payload); err != nil {
		t.Fatalf("failed to record audit log with payload: %v", err)
	}

	// List audit logs
	logs, err := store.ListAuditLogs(10)
	if err != nil {
		t.Fatalf("failed to list audit logs: %v", err)
	}
	if len(logs) != 2 {
		t.Fatalf("expected 2 audit logs, got %d", len(logs))
	}

	// Verify the logs (should be in reverse chronological order)
	if logs[0]["actor"] != "user2" {
		t.Errorf("expected first log actor=user2, got %q", logs[0]["actor"])
	}
	if logs[0]["scope"] != "settings" {
		t.Errorf("expected first log scope=settings, got %q", logs[0]["scope"])
	}
	if logs[0]["action"] != "create" {
		t.Errorf("expected first log action=create, got %q", logs[0]["action"])
	}
	if logs[0]["payload"] == nil {
		t.Errorf("expected first log to have payload")
	}

	if logs[1]["actor"] != "user1" {
		t.Errorf("expected second log actor=user1, got %q", logs[1]["actor"])
	}
	if logs[1]["payload"] != nil {
		t.Errorf("expected second log to have nil payload, got %v", logs[1]["payload"])
	}
}

func TestListAuditLogsLimit(t *testing.T) {
	dir := t.TempDir()
	store, err := New(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// Record multiple audit logs
	for i := 0; i < 10; i++ {
		if err := store.RecordAuditLog("user", "test", "action", nil); err != nil {
			t.Fatalf("failed to record audit log: %v", err)
		}
	}

	// Test default limit
	logs, err := store.ListAuditLogs(0)
	if err != nil {
		t.Fatalf("failed to list audit logs: %v", err)
	}
	if len(logs) != 10 {
		t.Errorf("expected default limit to return all 10 logs, got %d", len(logs))
	}

	// Test custom limit
	logs, err = store.ListAuditLogs(5)
	if err != nil {
		t.Fatalf("failed to list audit logs with limit: %v", err)
	}
	if len(logs) != 5 {
		t.Errorf("expected 5 logs, got %d", len(logs))
	}

	// Test limit exceeding max (should cap at 100 or configured max)
	logs, err = store.ListAuditLogs(1000)
	if err != nil {
		t.Fatalf("failed to list audit logs with high limit: %v", err)
	}
	if len(logs) != 10 {
		t.Errorf("expected 10 logs (all available), got %d", len(logs))
	}
}

func TestSettingsClosedStore(t *testing.T) {
	dir := t.TempDir()
	store, err := New(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	_ = store.Close()

	// Test operations on closed store - should return an error
	if err := store.SetSetting("key", "value"); err == nil {
		t.Errorf("expected error for closed store, got nil")
	}

	_, err = store.GetSettings([]string{"key"})
	if err == nil {
		t.Errorf("expected error for closed store, got nil")
	}

	if err := store.RecordAuditLog("user", "scope", "action", nil); err == nil {
		t.Errorf("expected error for closed store, got nil")
	}

	_, err = store.ListAuditLogs(10)
	if err == nil {
		t.Errorf("expected error for closed store, got nil")
	}
}
