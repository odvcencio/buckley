package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/ipc"
	"m31labs.dev/buckley/pkg/ipc/command"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/orchestrator"
	"m31labs.dev/buckley/pkg/storage"
	"m31labs.dev/buckley/pkg/telemetry"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

type serveEncodingTestServer struct{}

func (serveEncodingTestServer) Start(context.Context) error { return nil }

func TestRunServeCommandConfiguresToolEncoding(t *testing.T) {
	t.Chdir(t.TempDir())

	origLoad := serveLoadConfigFn
	origInit := serveInitStoreFn
	origNew := serveNewServerFn
	origEncoding := encodingOverrideFlag
	origAgent := agentProfileFlag
	origModel := modelOverrideFlag
	t.Cleanup(func() {
		serveLoadConfigFn = origLoad
		serveInitStoreFn = origInit
		serveNewServerFn = origNew
		encodingOverrideFlag = origEncoding
		agentProfileFlag = origAgent
		modelOverrideFlag = origModel
		tool.SetResultEncoding(true)
	})

	serveInitStoreFn = func() (*storage.Store, error) {
		return storage.New(filepath.Join(t.TempDir(), "ipc.db"))
	}
	serveNewServerFn = func(ipc.Config, *storage.Store, *telemetry.Hub, *command.Gateway, orchestrator.PlanStore, *config.Config, *orchestrator.WorkflowManager, *model.Manager) ipcServer {
		return serveEncodingTestServer{}
	}
	agentProfileFlag = ""
	modelOverrideFlag = ""

	tests := []struct {
		name       string
		configToon bool
		override   string
		wantToon   bool
	}{
		{name: "config json", configToon: false, wantToon: false},
		{name: "config toon", configToon: true, wantToon: true},
		{name: "json override", configToon: true, override: "json", wantToon: false},
		{name: "toon override", configToon: false, override: "toon", wantToon: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.Encoding.UseToon = tt.configToon
			serveLoadConfigFn = func() (*config.Config, error) { return cfg, nil }
			encodingOverrideFlag = ""
			if tt.override != "" {
				opts, err := parseStartupOptions([]string{"--encoding=" + tt.override, "serve"})
				if err != nil {
					t.Fatalf("parseStartupOptions: %v", err)
				}
				encodingOverrideFlag = opts.encodingOverride
			}

			if err := runServeCommand([]string{
				"--bind", "0.0.0.0:0",
				"--require-token",
				"--auth-token", "test-token",
			}); err != nil {
				t.Fatalf("runServeCommand: %v", err)
			}

			encoded, err := tool.ToModelOutput(&builtin.Result{
				Success: true,
				Data:    map[string]any{"marker": "serve-encoding"},
			})
			if err != nil {
				t.Fatalf("tool.ToModelOutput: %v", err)
			}
			if tt.wantToon {
				if strings.HasPrefix(strings.TrimSpace(encoded), "{") {
					t.Fatalf("encoding = JSON, want TOON: %s", encoded)
				}
			} else {
				var payload map[string]any
				if err := json.Unmarshal([]byte(encoded), &payload); err != nil {
					t.Fatalf("encoding = %q, want JSON: %v", encoded, err)
				}
			}
			if !strings.Contains(encoded, "serve-encoding") {
				t.Fatalf("encoded result omitted marker: %s", encoded)
			}
		})
	}
}
