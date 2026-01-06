package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunRemoteCommandGuards(t *testing.T) {
	if err := runRemoteCommand(nil); err == nil {
		t.Fatalf("expected usage error")
	}
	if err := runRemoteCommand([]string{"nope"}); err == nil || !strings.Contains(err.Error(), "unknown remote") {
		t.Fatalf("expected unknown subcommand error, got %v", err)
	}
}

func TestParseRemoteFlagsAndClientURLs(t *testing.T) {
	// attach flags require url and session
	if _, err := parseRemoteAttachFlags([]string{"--url", "https://x"}); err == nil {
		t.Fatalf("expected missing session error")
	}
	if _, err := parseRemoteAttachFlags([]string{"--session", "s1"}); err == nil {
		t.Fatalf("expected missing url error")
	}

	t.Setenv("BUCKLEY_IPC_TOKEN", "tok")
	opts, err := parseRemoteAttachFlags([]string{"--url", "buckley.example.com", "--session", "s1"})
	if err != nil {
		t.Fatalf("parseRemoteAttachFlags: %v", err)
	}
	if opts.BaseURL != "buckley.example.com" || opts.SessionID != "s1" || opts.IPCAuthToken != "tok" {
		t.Fatalf("unexpected attach opts: %+v", opts)
	}

	// base flags require url
	if _, err := parseRemoteBaseFlags("sessions", nil); err == nil {
		t.Fatalf("expected base flags missing url error")
	}

	// login flags require url
	if _, err := parseRemoteLoginFlags(nil); err == nil {
		t.Fatalf("expected login missing url error")
	}

	// console flags require url + session
	if _, err := parseRemoteConsoleFlags([]string{"--url", "https://x"}); err == nil {
		t.Fatalf("expected console missing session error")
	}

	base := remoteBaseOptions{
		BaseURL:      "buckley.example.com/root",
		IPCAuthToken: "tok",
		BasicUser:    "u",
		BasicPass:    "p",
	}
	client, err := newRemoteClient(base)
	if err != nil {
		t.Fatalf("newRemoteClient: %v", err)
	}
	if client.baseURL.Scheme != "https" || client.baseURL.Path != "/root" {
		t.Fatalf("unexpected normalized base url: %v", client.baseURL)
	}

	api := client.apiURL("/sessions")
	if !strings.Contains(api, "/api/sessions") || strings.Contains(api, "token=tok") {
		t.Fatalf("unexpected apiURL: %q", api)
	}

	ws := client.wsURL("")
	if !strings.HasPrefix(ws, "wss://") || !strings.Contains(ws, "/ws") || strings.Contains(ws, "token=tok") {
		t.Fatalf("unexpected wsURL: %q", ws)
	}

	pty := client.ptyURL("s1", 40, 120, "bash")
	if !strings.Contains(pty, "/ws/pty") || !strings.Contains(pty, "sessionId=s1") || !strings.Contains(pty, "cmd=bash") || strings.Contains(pty, "token=tok") {
		t.Fatalf("unexpected ptyURL: %q", pty)
	}

	// persistCookies no-op on nil receiver
	if err := (*remoteClient)(nil).persistCookies(); err != nil {
		t.Fatalf("persistCookies nil: %v", err)
	}
}

func TestCheckConfigEnvFileReadsKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".buckley"), 0o755); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	envPath := filepath.Join(home, ".buckley", "config.env")
	if err := os.WriteFile(envPath, []byte("export OPENROUTER_API_KEY=abc123\n"), 0o644); err != nil {
		t.Fatalf("write config.env: %v", err)
	}
	if got := checkConfigEnvFile(); got != "abc123" {
		t.Fatalf("checkConfigEnvFile=%q want abc123", got)
	}
}

func TestRunConfigCommandDispatchAndCompletion(t *testing.T) {
	if err := runConfigCommand([]string{"unknown"}); err == nil {
		t.Fatalf("expected unknown config command error")
	}
	// show/path/check should not error with default config.
	if err := runConfigCommand([]string{"path"}); err != nil {
		t.Fatalf("runConfigCommand path: %v", err)
	}
	if err := runConfigCommand([]string{"show"}); err != nil {
		t.Fatalf("runConfigCommand show: %v", err)
	}

	out := captureStdout(t, func() {
		if err := runCompletionCommand(nil); err != nil {
			t.Fatalf("runCompletionCommand: %v", err)
		}
	})
	if !strings.Contains(out, "Generate shell completions") {
		t.Fatalf("unexpected completion help output: %q", out)
	}
}

func TestBuildViewProviderBestEffort(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	oldWd, _ := os.Getwd()
	root := t.TempDir()
	_ = os.Chdir(root)
	t.Cleanup(func() { _ = os.Chdir(oldWd) })

	prov, cleanup, err := buildViewProvider()
	if err != nil {
		t.Fatalf("buildViewProvider: %v", err)
	}
	if prov == nil {
		t.Fatalf("expected provider")
	}
	cleanup()

	// Ensure provider can build state for missing session without panic.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, _ = prov.BuildSessionState(ctx, "missing")
}
