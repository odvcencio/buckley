package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/setup"
	"github.com/odvcencio/buckley/pkg/telemetry"
)

func TestResolveDependenciesWhenNothingMissing(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "dummy")
	checker := setup.NewChecker()
	if err := resolveDependencies(checker); err != nil {
		t.Fatalf("expected no missing deps, got %v", err)
	}
}

func TestStartEmbeddedIPCServerGuardsAndTokenError(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.IPC.Enabled = false
	stop, url, err := startEmbeddedIPCServer(cfg, nil, nil, nil, nil, nil, nil)
	if err != nil || stop != nil || url != "" {
		t.Fatalf("expected disabled ipc to noop, got stopSet=%v url=%q err=%v", stop != nil, url, err)
	}

	cfg = config.DefaultConfig()
	cfg.IPC.Enabled = true
	cfg.IPC.Bind = ""
	stop, url, err = startEmbeddedIPCServer(cfg, nil, nil, nil, nil, nil, nil)
	if err != nil || stop != nil || url != "" {
		t.Fatalf("expected empty bind to noop, got stopSet=%v url=%q err=%v", stop != nil, url, err)
	}

	cfg = config.DefaultConfig()
	cfg.IPC.Enabled = true
	cfg.IPC.RequireToken = true
	cfg.IPC.Bind = "127.0.0.1:4488"
	t.Setenv("BUCKLEY_IPC_TOKEN", "")
	_, _, err = startEmbeddedIPCServer(cfg, nil, nil, nil, nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "IPC token required") {
		t.Fatalf("expected token required error, got %v", err)
	}
}

func TestStartACPServerGuardsAndErrors(t *testing.T) {
	cfg := config.DefaultConfig()
	stop, err := startACPServer(cfg, nil, nil, nil)
	if err != nil || stop != nil {
		t.Fatalf("expected empty listen to noop, got stopSet=%v err=%v", stop != nil, err)
	}

	cfg = config.DefaultConfig()
	cfg.ACP.Listen = "0.0.0.0:5555"
	_, err = startACPServer(cfg, nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "requires mTLS") {
		t.Fatalf("expected tls/loopback guard error, got %v", err)
	}
}

func TestInitDependenciesFailsWithoutProviderKeys(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	oldWd, _ := os.Getwd()
	tmp := t.TempDir()
	_ = os.Chdir(tmp)
	t.Cleanup(func() { _ = os.Chdir(oldWd) })

	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	_, _, _, err := initDependencies()
	if err == nil || !strings.Contains(err.Error(), "no providers configured") {
		t.Fatalf("expected provider key error, got %v", err)
	}
}

func TestCompletionsProduceOutput(t *testing.T) {
	bash := captureStdout(t, func() { printBashCompletion() })
	if !strings.Contains(bash, "complete -F") {
		t.Fatalf("expected bash completion output, got %q", bash)
	}

	zsh := captureStdout(t, func() { printZshCompletion() })
	if !strings.Contains(zsh, "#compdef buckley") {
		t.Fatalf("expected zsh completion output, got %q", zsh)
	}

	fish := captureStdout(t, func() { printFishCompletion() })
	if !strings.Contains(fish, "complete -c buckley") {
		t.Fatalf("expected fish completion output, got %q", fish)
	}
}

func TestRunConfigCheckAndMigrateSmoke(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	var checkErr error
	out := captureStdout(t, func() {
		checkErr = runConfigCheck()
	})
	if !strings.Contains(out, "Checking Buckley configuration") {
		t.Fatalf("unexpected config check output: %q", out)
	}
	if checkErr == nil {
		t.Fatalf("expected runConfigCheck to fail without provider keys")
	}
	if exitCodeForError(checkErr) != 2 {
		t.Fatalf("exitCode=%d want 2 (got err=%v)", exitCodeForError(checkErr), checkErr)
	}

	if err := runMigrateCommand(); err != nil {
		t.Fatalf("runMigrateCommand: %v", err)
	}
}

func TestRemoteEventFormattingHelpers(t *testing.T) {
	detail := formatTelemetryDetail(string(telemetry.EventTaskStarted), map[string]any{"taskId": "1"})
	if !strings.Contains(detail, "Task") {
		t.Fatalf("unexpected telemetry detail: %q", detail)
	}

	msgPayload, _ := json.Marshal(map[string]string{"role": "assistant", "content": "hi"})
	evt := remoteEvent{Type: "message.created", Payload: msgPayload, Timestamp: time.Now()}
	out := captureStdout(t, func() { printRemoteEvent(evt) })
	if !strings.Contains(out, "ASSISTANT") || !strings.Contains(out, "hi") {
		t.Fatalf("expected message.created output, got %q", out)
	}

	// openBrowser should noop on empty URL
	if err := openBrowser(""); err != nil {
		t.Fatalf("openBrowser empty: %v", err)
	}

	// readLineWithContext returns context error when cancelled
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := readLineWithContext(ctx, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled error, got %v", err)
	}
}
