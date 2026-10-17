package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/oneshot"
	"m31labs.dev/buckley/pkg/oneshot/commands"
)

const fakeCodexCommitReply = `{"action":"add","scope":"cli","subject":"Route codex provider through the CLI","body":["Constrain the reply with an output schema."]}`

// writeFakeCodex installs a codex stand-in that records its argv, drains the
// prompt on stdin, and prints a schema-shaped reply.
func writeFakeCodex(t *testing.T) (script, argvFile string) {
	t.Helper()
	dir := t.TempDir()
	script = filepath.Join(dir, "codex")
	argvFile = filepath.Join(dir, "argv")
	body := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > '%s'\ncat > /dev/null\nprintf '%%s' '%s'\n", argvFile, fakeCodexCommitReply)
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}
	return script, argvFile
}

// blockRealCodex puts a failing codex first on PATH so a test whose command
// resolution regresses to the bare name fails fast instead of running the
// real CLI against the working tree.
func blockRealCodex(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	stub := "#!/bin/sh\necho 'tests must not run the real codex CLI' >&2\nexit 97\n"
	if err := os.WriteFile(filepath.Join(dir, "codex"), []byte(stub), 0o755); err != nil {
		t.Fatalf("write codex guard: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestNewOneshotToolInvoker_CodexProviderRouteUsesSchemaConstrainedCLI(t *testing.T) {
	tests := []struct {
		name            string
		requested       string
		selected        string
		defaultProvider string
		reasoning       string
		viaEnv          bool
		wantModel       string
	}{
		{name: "provider prefix is stripped", requested: "codex/gpt-6-luna", wantModel: "gpt-6-luna"},
		{name: "default model omits the model flag", requested: "codex/default"},
		{name: "routing selection wins over the request", requested: "route-alias", selected: "codex/gpt-6.1-sol", wantModel: "gpt-6.1-sol"},
		{name: "blank provider command falls back to the environment", requested: "codex/gpt-6-luna", viaEnv: true, wantModel: "gpt-6-luna"},
		{name: "unprefixed model uses default provider", requested: "gpt-6-luna", defaultProvider: "codex", wantModel: "gpt-6-luna"},
		{name: "max effort is preserved", requested: "codex/gpt-6-luna", reasoning: "max", wantModel: "gpt-6-luna"},
		{name: "disabled effort omits override", requested: "codex/gpt-6-luna", reasoning: "off", wantModel: "gpt-6-luna"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			blockRealCodex(t)
			script, argvFile := writeFakeCodex(t)
			cfg := config.DefaultConfig()
			cfg.Models.Reasoning = "xhigh"
			if tt.reasoning != "" {
				cfg.Models.Reasoning = tt.reasoning
			}
			if tt.defaultProvider != "" {
				cfg.Models.DefaultProvider = tt.defaultProvider
			}
			cfg.Providers.Codex.Enabled = true
			cfg.Providers.Codex.Models = []string{"codex/gpt-6-luna", "codex/gpt-6.1-sol"}
			if tt.viaEnv {
				cfg.Providers.Codex.Command = ""
				t.Setenv(envCodexCommand, script)
			} else {
				cfg.Providers.Codex.Command = script
				t.Setenv(envCodexCommand, filepath.Join(t.TempDir(), "must-not-run"))
			}
			mgr, err := model.NewManager(cfg)
			if err != nil {
				t.Fatalf("NewManager: %v", err)
			}
			if tt.selected != "" {
				mgr.RoutingHooks().Register(func(decision *model.RoutingDecision) *model.RoutingDecision {
					if decision != nil && decision.RequestedModel == tt.requested {
						decision.SelectedModel = tt.selected
					}
					return decision
				})
			}

			invoker, err := newOneshotToolInvoker(oneshotBackendAPI, "commit", tt.requested, cfg, mgr, nil)
			if err != nil {
				t.Fatalf("newOneshotToolInvoker: %v", err)
			}
			if _, ok := invoker.(*oneshot.CLIInvoker); !ok {
				t.Fatalf("invoker = %T, want *oneshot.CLIInvoker so the reply is schema constrained", invoker)
			}

			result, _, err := invoker.Invoke(context.Background(), "system", "user", commands.CommitDefinition{}.Tool(), nil)
			if err != nil {
				t.Fatalf("Invoke: %v", err)
			}
			if result == nil || result.ToolCall == nil {
				t.Fatalf("result = %+v, want a generate_commit tool call", result)
			}
			if result.ToolCall.Name != "generate_commit" || string(result.ToolCall.Arguments) != fakeCodexCommitReply {
				t.Fatalf("tool call = %s %s, want generate_commit with the reply object", result.ToolCall.Name, result.ToolCall.Arguments)
			}

			raw, err := os.ReadFile(argvFile)
			if err != nil {
				t.Fatalf("read recorded argv: %v", err)
			}
			args := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
			assertCodexArg(t, args, "--output-schema", "")
			assertCodexArg(t, args, "--sandbox", "read-only")
			assertCodexArg(t, args, "--ephemeral", "")
			assertCodexArg(t, args, "-c", `approval_policy="never"`)
			if tt.reasoning == "off" {
				for _, arg := range args {
					if strings.HasPrefix(arg, "model_reasoning_effort=") {
						t.Fatalf("argv = %q, want no reasoning effort override", args)
					}
				}
			} else {
				assertCodexArg(t, args, "-c", fmt.Sprintf("model_reasoning_effort=%q", cfg.Models.Reasoning))
			}
			if tt.wantModel == "" {
				for _, arg := range args {
					if arg == "--model" {
						t.Fatalf("argv = %q, want no --model flag", args)
					}
				}
			} else {
				assertCodexArg(t, args, "--model", tt.wantModel)
			}
		})
	}
}

// assertCodexArg requires flag in args. A non-empty want must also follow it.
func assertCodexArg(t *testing.T, args []string, flag, want string) {
	t.Helper()
	for i, arg := range args {
		if arg != flag {
			continue
		}
		if want == "" {
			return
		}
		if i+1 < len(args) && args[i+1] == want {
			return
		}
	}
	t.Fatalf("argv = %q, want %s %s", args, flag, want)
}

func TestNewOneshotToolInvoker_NonCodexRouteStaysOnAPIInvoker(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Models.DefaultProvider = "openai_compatible"
	cfg.Providers.OpenAICompatible.Enabled = true
	cfg.Providers.OpenAICompatible.BaseURL = "https://example.invalid/v1"
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	invoker, err := newOneshotToolInvoker(oneshotBackendAPI, "commit", "vendor/test", cfg, mgr, nil)
	if err != nil {
		t.Fatalf("newOneshotToolInvoker: %v", err)
	}
	if _, ok := invoker.(*oneshot.DefaultInvoker); !ok {
		t.Fatalf("invoker = %T, want *oneshot.DefaultInvoker for a non-codex provider", invoker)
	}
}
