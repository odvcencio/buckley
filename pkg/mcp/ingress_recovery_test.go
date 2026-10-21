package mcp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestManager_MessageBudgetRecoveryDoesNotReplayCall(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "calls")
	manager := NewManager(WithRestartPolicy(RestartPolicy{InitialBackoff: 10 * time.Millisecond, MaxBackoff: 20 * time.Millisecond, MaxRestarts: 2}))
	cfg := fakeConfig(t, "large", "-tools=0", "-call-log="+logPath)
	cfg.MaxMessageBytes = 64 << 10
	manager.AddServer(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := manager.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	old, _ := manager.GetClient(cfg.Name)
	if _, err := manager.CallTool(ctx, cfg.Name, "echo", map[string]any{"text": "report"}); !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("budget error lost: %v", err)
	}
	for {
		current, ok := manager.GetClient(cfg.Name)
		if ok && current != old {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("manager did not replace rejected connection")
		case <-time.After(time.Millisecond):
		}
	}
	result, err := manager.CallTool(ctx, cfg.Name, "mixed_content", nil)
	if err != nil || result.IsError || len(result.Content) != 3 {
		t.Fatalf("healthy call after recovery failed: %+v %v", result, err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil || string(data) != "echo\nmixed_content\n" {
		t.Fatalf("call replayed or missing: %q %v", data, err)
	}
	if strings.Count(string(data), "echo") != 1 {
		t.Fatal("oversized invocation replayed")
	}
	t.Log("oversized_call_count=1 connection_replaced=true subsequent_call_ok=true")
}

func TestClient_ExplicitMessageBudget(t *testing.T) {
	cfg := fakeConfig(t, "large", "-tools=0")
	cfg.MaxMessageBytes = 64 << 10
	client, err := NewClient(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if _, err := client.CallTool(context.Background(), "echo", map[string]any{"text": "report"}); !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("explicit lower budget ignored: %v", err)
	}
	cfg.MaxMessageBytes = 16 << 20
	larger, err := NewClient(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = larger.Close() })
	result, err := larger.CallTool(context.Background(), "echo", map[string]any{"text": "report"})
	if err != nil || len(result.Content) != 32 {
		t.Fatalf("explicit larger budget failed: %v", err)
	}
}

func TestNewClient_InvalidMessageBudget(t *testing.T) {
	for _, limit := range []int{-1, 65 << 20} {
		cfg := fakeConfig(t, "normal")
		cfg.MaxMessageBytes = limit
		if client, err := NewClient(context.Background(), cfg); err == nil || client != nil {
			t.Fatalf("invalid budget accepted: %d", limit)
		}
	}
}
