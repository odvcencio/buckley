package model

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/odvcencio/buckley/pkg/config"
)

func TestCodexCLIProviderChatCompletionUsesExecLastMessage(t *testing.T) {
	provider := NewCodexCLIProvider(
		config.CodexConfig{
			Command: "codex",
			Models:  []string{"codex/gpt-5.4-mini-xhigh"},
		},
		config.SandboxConfig{Mode: "workspace"},
		config.ApprovalConfig{Mode: "safe"},
	)

	var got CodexCLICommand
	provider.runner = func(ctx context.Context, cmd CodexCLICommand) (CodexCLICommandResult, error) {
		got = cmd
		outputPath := argAfter(cmd.Args, "--output-last-message")
		if outputPath == "" {
			t.Fatalf("missing --output-last-message in args: %v", cmd.Args)
		}
		if err := os.WriteFile(outputPath, []byte("codex answer\n"), 0o644); err != nil {
			t.Fatalf("write codex output: %v", err)
		}
		return CodexCLICommandResult{Stdout: []byte("progress logs")}, nil
	}

	resp, err := provider.ChatCompletion(context.Background(), ChatRequest{
		Model: "codex/gpt-5.4-mini-xhigh",
		Messages: []Message{
			{Role: "system", Content: "system prompt"},
			{Role: "user", Content: "hello"},
		},
	})
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}

	if got.Name != "codex" {
		t.Fatalf("command name=%q want codex", got.Name)
	}
	if !containsArgs(got.Args, "exec", "--color", "never") {
		t.Fatalf("unexpected codex args: %v", got.Args)
	}
	if !containsSubsequence(got.Args, []string{"--model", "gpt-5.4-mini-xhigh"}) {
		t.Fatalf("codex args missing model: %v", got.Args)
	}
	if !containsSubsequence(got.Args, []string{"--sandbox", "workspace-write"}) {
		t.Fatalf("codex args missing workspace sandbox: %v", got.Args)
	}
	if !containsSubsequence(got.Args, []string{"--ask-for-approval", "never"}) {
		t.Fatalf("codex args missing approval policy: %v", got.Args)
	}
	if got.Args[len(got.Args)-1] != "-" {
		t.Fatalf("codex prompt should be read from stdin, args: %v", got.Args)
	}
	if !strings.Contains(got.Stdin, "System:\nsystem prompt") || !strings.Contains(got.Stdin, "User:\nhello") {
		t.Fatalf("stdin missing transcript: %q", got.Stdin)
	}
	if len(resp.Choices) != 1 || resp.Choices[0].Message.Content != "codex answer" {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestCodexCLIProviderDefaultModelOmitsModelArg(t *testing.T) {
	provider := NewCodexCLIProvider(config.CodexConfig{}, config.SandboxConfig{Mode: "readonly"}, config.ApprovalConfig{Mode: "ask"})

	var got CodexCLICommand
	provider.runner = func(ctx context.Context, cmd CodexCLICommand) (CodexCLICommandResult, error) {
		got = cmd
		if err := os.WriteFile(argAfter(cmd.Args, "--output-last-message"), []byte("answer"), 0o644); err != nil {
			t.Fatalf("write output: %v", err)
		}
		return CodexCLICommandResult{}, nil
	}

	if _, err := provider.ChatCompletion(context.Background(), ChatRequest{Model: "codex/default", Messages: []Message{{Role: "user", Content: "hello"}}}); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}

	if argAfter(got.Args, "--model") != "" {
		t.Fatalf("default codex model should not pass --model: %v", got.Args)
	}
	if !containsSubsequence(got.Args, []string{"--sandbox", "read-only"}) {
		t.Fatalf("codex args missing read-only sandbox: %v", got.Args)
	}
	if !containsSubsequence(got.Args, []string{"--ask-for-approval", "untrusted"}) {
		t.Fatalf("codex args missing untrusted approval: %v", got.Args)
	}
}

func TestCodexCLIProviderCatalogIncludesConfiguredModels(t *testing.T) {
	provider := NewCodexCLIProvider(
		config.CodexConfig{Models: []string{"gpt-5.4-mini-xhigh"}},
		config.SandboxConfig{},
		config.ApprovalConfig{},
	)

	catalog, err := provider.FetchCatalog()
	if err != nil {
		t.Fatalf("FetchCatalog: %v", err)
	}

	if len(catalog.Data) != 2 {
		t.Fatalf("catalog size=%d want 2: %+v", len(catalog.Data), catalog.Data)
	}
	if catalog.Data[0].ID != "codex/default" || catalog.Data[1].ID != "codex/gpt-5.4-mini-xhigh" {
		t.Fatalf("unexpected catalog: %+v", catalog.Data)
	}
	if provider.SupportsToolsForTest("codex/gpt-5.4-mini-xhigh") {
		t.Fatal("codex provider catalog should not advertise OpenAI-style tool calling")
	}
}

func (p *CodexCLIProvider) SupportsToolsForTest(modelID string) bool {
	info, err := p.GetModelInfo(modelID)
	if err != nil {
		return false
	}
	for _, param := range info.SupportedParameters {
		if param == "tools" || param == "functions" {
			return true
		}
	}
	return false
}

func argAfter(args []string, key string) string {
	for i, arg := range args {
		if arg == key && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func containsArgs(args []string, values ...string) bool {
	for _, value := range values {
		found := false
		for _, arg := range args {
			if arg == value {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func containsSubsequence(values, want []string) bool {
	if len(want) == 0 {
		return true
	}
	for i := 0; i <= len(values)-len(want); i++ {
		match := true
		for j := range want {
			if values[i+j] != want[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
