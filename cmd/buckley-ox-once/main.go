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
	flag.StringVar(&promptPath, "prompt", "", "repo-relative prompt blob committed at --head")
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
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	rule, prompt, err := formTrackedOSSPrompt(ctx, canonicalRoot, exactHead, promptPath)
	if err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load Buckley config: %w", err)
	}
	provider := cfg.Providers.OpenRouter
	client, err := model.NewOneUseOSSOpenRouterClient(provider.APIKey, provider.BaseURL, model.OxAlphaOpenRouterModelID, rule, prompt)
	if err != nil {
		return err
	}
	defer client.Close()

	response, err := client.CompletePatch(ctx)
	if err != nil {
		return fmt.Errorf("Ox Alpha request failed: %w", err)
	}
	content, err := oxAlphaPatchText(response)
	if err != nil {
		return err
	}
	if err := writeExclusiveOutput(outputPath, []byte(content)); err != nil {
		return err
	}
	fmt.Printf("saved one Ox Alpha response for %s at %s\n", exactHead, outputPath)
	return nil
}

func formTrackedOSSPrompt(ctx context.Context, canonicalRoot, exactHead, promptPath string) (*workspaceevidence.OSSBlobRule, []byte, error) {
	evidence, err := workspaceevidence.InspectRootLicenseBlob(ctx, canonicalRoot, exactHead)
	if err != nil {
		return nil, nil, fmt.Errorf("inspect root license: %w", err)
	}
	rule, prompt, err := workspaceevidence.MintTrackedPromptOSSBlobRule(ctx, evidence, promptPath)
	if err != nil {
		return nil, nil, fmt.Errorf("form tracked OSS prompt authority: %w", err)
	}
	return rule, prompt, nil
}

func oxAlphaPatchText(response *model.ChatResponse) (string, error) {
	if response == nil {
		return "", errors.New("Ox Alpha returned a nil response")
	}
	if len(response.Choices) != 1 {
		return "", fmt.Errorf("Ox Alpha returned %d choices, want exactly one", len(response.Choices))
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
		return "", fmt.Errorf("Ox Alpha returned no patch text (finish_reason=%q)", choice.FinishReason)
	}
	return content, nil
}

func writeExclusiveOutput(path string, content []byte) error {
	output, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("output already exists: %s", path)
		}
		return fmt.Errorf("create Ox Alpha output: %w", err)
	}
	createdInfo, _ := output.Stat()
	keep := false
	defer func() {
		if keep {
			return
		}
		_ = output.Close()
		currentInfo, err := os.Lstat(path)
		if err == nil && createdInfo != nil && os.SameFile(createdInfo, currentInfo) {
			_ = os.Remove(path)
		}
	}()

	if _, err := output.Write(content); err != nil {
		return fmt.Errorf("write Ox Alpha output: %w", err)
	}
	if err := output.Close(); err != nil {
		return fmt.Errorf("close Ox Alpha output: %w", err)
	}
	keep = true
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
