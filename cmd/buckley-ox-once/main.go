package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/workspaceevidence"
)

const maxPromptBytes = 96 << 10

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "buckley-ox-once:", err)
		os.Exit(1)
	}
}

func run() error {
	var repoRoot, exactHead, promptPath, outputPath string
	flag.StringVar(&repoRoot, "repo", "", "canonical clean Git worktree root")
	flag.StringVar(&exactHead, "head", "", "exact full HEAD commit OID")
	flag.StringVar(&promptPath, "prompt", "", "bounded prompt file")
	flag.StringVar(&outputPath, "output", "", "exclusive output file")
	flag.Parse()

	if repoRoot == "" || exactHead == "" || promptPath == "" || outputPath == "" {
		return errors.New("--repo, --head, --prompt, and --output are required")
	}
	canonicalRoot, err := canonicalDirectory(repoRoot)
	if err != nil {
		return err
	}
	if err := requireExactCleanHead(canonicalRoot, exactHead); err != nil {
		return err
	}
	prompt, err := os.ReadFile(promptPath)
	if err != nil {
		return fmt.Errorf("read prompt: %w", err)
	}
	if len(prompt) == 0 || len(prompt) > maxPromptBytes {
		return fmt.Errorf("prompt size %d is outside 1..%d bytes", len(prompt), maxPromptBytes)
	}
	if _, err := os.Stat(outputPath); err == nil {
		return fmt.Errorf("output already exists: %s", outputPath)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect output: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	evidence, err := workspaceevidence.InspectRootLicenseBlob(ctx, canonicalRoot, exactHead)
	if err != nil {
		return fmt.Errorf("inspect root license: %w", err)
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load Buckley config: %w", err)
	}
	provider := cfg.Providers.OpenRouter
	client, err := model.NewOneUseOSSOpenRouterClient(ctx, provider.APIKey, provider.BaseURL, model.OxAlphaOpenRouterModelID, evidence)
	if err != nil {
		return err
	}
	defer client.Close()

	response, err := client.ChatCompletion(ctx, model.ChatRequest{
		Model:               model.OxAlphaOpenRouterModelID,
		Messages:            []model.Message{{Role: "user", Content: string(prompt)}},
		MaxCompletionTokens: 16384,
		Reasoning:           &model.ReasoningConfig{Effort: "medium"},
	})
	if err != nil {
		return fmt.Errorf("Ox Alpha request failed: %w", err)
	}
	if response == nil || len(response.Choices) != 1 {
		return fmt.Errorf("Ox Alpha returned %d choices, want exactly one", len(response.Choices))
	}
	choice := response.Choices[0]
	content, _ := choice.Message.Content.(string)
	if strings.TrimSpace(content) == "" {
		content = choice.Message.Reasoning
	}
	if strings.TrimSpace(content) == "" {
		var recovered strings.Builder
		for _, detail := range choice.Message.ReasoningDetails {
			if detail.Text != "" {
				recovered.WriteString(detail.Text)
				recovered.WriteByte('\n')
			}
			if detail.Summary != "" {
				recovered.WriteString(detail.Summary)
				recovered.WriteByte('\n')
			}
		}
		content = recovered.String()
	}
	if strings.TrimSpace(content) == "" {
		return fmt.Errorf("Ox Alpha returned no patch text (finish_reason=%q)", choice.FinishReason)
	}
	if err := os.WriteFile(outputPath, []byte(content), 0o600); err != nil {
		return fmt.Errorf("write Ox Alpha output: %w", err)
	}
	fmt.Printf("saved one Ox Alpha response for %s at %s\n", exactHead, outputPath)
	return nil
}

func canonicalDirectory(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve repository root: %w", err)
	}
	abs = filepath.Clean(abs)
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve repository root: %w", err)
	}
	if resolved != abs {
		return "", errors.New("repository root must be canonical and symlink-free")
	}
	return abs, nil
}

func requireExactCleanHead(root, exactHead string) error {
	if len(exactHead) != 40 || strings.ToLower(exactHead) != exactHead {
		return errors.New("head must be an exact lowercase SHA-1 commit OID")
	}
	top, err := gitOutput(root, "rev-parse", "--show-toplevel")
	if err != nil || strings.TrimSpace(string(top)) != root {
		return errors.New("repository root is not the canonical Git top-level")
	}
	head, err := gitOutput(root, "rev-parse", "HEAD")
	if err != nil || strings.TrimSpace(string(head)) != exactHead {
		return errors.New("worktree HEAD does not match the exact requested commit")
	}
	status, err := gitOutput(root, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return fmt.Errorf("inspect worktree status: %w", err)
	}
	if len(bytes.TrimSpace(status)) != 0 {
		return errors.New("worktree must be clean before Ox dispatch")
	}
	return nil
}

func gitOutput(root string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	return cmd.Output()
}
