package model

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/config"
)

const (
	codexProviderID     = "codex"
	defaultCodexCommand = "codex"
	defaultCodexModelID = "codex/default"
)

// CodexCLICommand describes one Codex CLI invocation.
type CodexCLICommand struct {
	Name  string
	Args  []string
	Stdin string
	Dir   string
}

// CodexCLICommandResult captures Codex CLI output.
type CodexCLICommandResult struct {
	Stdout []byte
	Stderr []byte
}

// CodexCLICommandRunner executes a Codex CLI command.
type CodexCLICommandRunner func(ctx context.Context, cmd CodexCLICommand) (CodexCLICommandResult, error)

// CodexCLIProvider adapts `codex exec` to Buckley's chat provider interface.
type CodexCLIProvider struct {
	command  string
	models   []ModelInfo
	sandbox  config.SandboxConfig
	approval config.ApprovalConfig
	runner   CodexCLICommandRunner
}

// NewCodexCLIProvider builds a Codex CLI-backed chat provider.
func NewCodexCLIProvider(cfg config.CodexConfig, sandboxCfg config.SandboxConfig, approvalCfg config.ApprovalConfig) *CodexCLIProvider {
	command := strings.TrimSpace(cfg.Command)
	if command == "" {
		command = defaultCodexCommand
	}
	return &CodexCLIProvider{
		command:  command,
		models:   codexModelCatalog(cfg.Models),
		sandbox:  sandboxCfg,
		approval: approvalCfg,
		runner:   runCodexCLICommand,
	}
}

// ID returns provider identifier.
func (p *CodexCLIProvider) ID() string {
	return codexProviderID
}

// FetchCatalog returns configured Codex model aliases.
func (p *CodexCLIProvider) FetchCatalog() (*ModelCatalog, error) {
	if p == nil || len(p.models) == 0 {
		return &ModelCatalog{Data: codexModelCatalog(nil)}, nil
	}
	return &ModelCatalog{Data: append([]ModelInfo(nil), p.models...)}, nil
}

// GetModelInfo returns metadata for a Codex CLI model alias.
func (p *CodexCLIProvider) GetModelInfo(modelID string) (*ModelInfo, error) {
	catalog, _ := p.FetchCatalog()
	for _, info := range catalog.Data {
		for _, candidate := range []string{modelID, codexModelID(modelID)} {
			if info.ID == candidate {
				return &info, nil
			}
		}
	}
	return nil, fmt.Errorf("codex model not found: %s", modelID)
}

// ChatCompletion runs a non-streaming Codex CLI chat turn.
func (p *CodexCLIProvider) ChatCompletion(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	if p == nil {
		return nil, fmt.Errorf("codex provider is nil")
	}

	outFile, cleanup, err := createCodexOutputFile()
	if err != nil {
		return nil, err
	}
	defer cleanup()

	workDir, _ := os.Getwd()
	args := p.buildExecArgs(req.Model, outFile, workDir)
	result, err := p.runner(ctx, CodexCLICommand{
		Name:  p.command,
		Args:  args,
		Stdin: buildCodexChatPrompt(req.Messages),
		Dir:   workDir,
	})
	if err != nil {
		return nil, formatCodexCLIError(err, result)
	}

	content := readCodexLastMessage(outFile, result.Stdout)
	if strings.TrimSpace(content) == "" {
		return nil, fmt.Errorf("codex CLI returned an empty response")
	}

	usage := estimateCodexUsage(req.Messages, content)
	return &ChatResponse{
		ID:    fmt.Sprintf("codex-%d", time.Now().UnixNano()),
		Model: codexModelID(req.Model),
		Choices: []Choice{
			{
				Index: 0,
				Message: Message{
					Role:    "assistant",
					Content: content,
				},
				FinishReason: "stop",
			},
		},
		Usage: usage,
	}, nil
}

// ChatCompletionStream emits the non-streaming result as a single chunk.
func (p *CodexCLIProvider) ChatCompletionStream(ctx context.Context, req ChatRequest) (<-chan StreamChunk, <-chan error) {
	chunkChan := make(chan StreamChunk, 1)
	errChan := make(chan error, 1)
	go func() {
		defer close(chunkChan)
		defer close(errChan)
		resp, err := p.ChatCompletion(ctx, req)
		if err != nil {
			errChan <- err
			return
		}
		if len(resp.Choices) == 0 {
			errChan <- fmt.Errorf("codex: empty response choices")
			return
		}
		finish := resp.Choices[0].FinishReason
		chunkChan <- StreamChunk{
			ID:    resp.ID,
			Model: resp.Model,
			Choices: []StreamChoice{
				{
					Index: 0,
					Delta: MessageDelta{
						Role:    "assistant",
						Content: messageContentToText(resp.Choices[0].Message.Content),
					},
					FinishReason: &finish,
				},
			},
			Usage: &resp.Usage,
		}
	}()
	return chunkChan, errChan
}

func (p *CodexCLIProvider) buildExecArgs(modelID, outputPath, workDir string) []string {
	args := []string{"exec", "--color", "never", "--ephemeral", "--output-last-message", outputPath}
	if model := codexCLIModelArg(modelID); model != "" {
		args = append(args, "--model", model)
	}
	if sandboxMode := codexSandboxMode(p.sandbox, p.approval); sandboxMode != "" {
		args = append(args, "--sandbox", sandboxMode)
	}
	if approvalPolicy := codexApprovalPolicy(p.approval.Mode); approvalPolicy != "" {
		args = append(args, "--ask-for-approval", approvalPolicy)
	}
	if strings.TrimSpace(workDir) != "" {
		args = append(args, "--cd", workDir)
	}
	args = append(args, "-")
	return args
}

func createCodexOutputFile() (string, func(), error) {
	tmp, err := os.CreateTemp("", "buckley-codex-chat-*.txt")
	if err != nil {
		return "", nil, fmt.Errorf("create codex output file: %w", err)
	}
	path := tmp.Name()
	if err := tmp.Close(); err != nil {
		_ = os.Remove(path)
		return "", nil, fmt.Errorf("close codex output file: %w", err)
	}
	return path, func() { _ = os.Remove(path) }, nil
}

func readCodexLastMessage(path string, stdout []byte) string {
	data, err := os.ReadFile(path)
	if err == nil && strings.TrimSpace(string(data)) != "" {
		return strings.TrimSpace(string(data))
	}
	return strings.TrimSpace(string(stdout))
}

func buildCodexChatPrompt(messages []Message) string {
	var b strings.Builder
	b.WriteString("Continue this Buckley chat conversation as the assistant.\n")
	b.WriteString("Return only the assistant response for the latest user request.\n\n")
	for _, msg := range messages {
		role := strings.TrimSpace(msg.Role)
		if role == "" {
			role = "message"
		}
		content := strings.TrimSpace(messageContentToText(msg.Content))
		if content == "" && len(msg.ToolCalls) == 0 {
			continue
		}
		b.WriteString(strings.ToUpper(role[:1]))
		b.WriteString(role[1:])
		b.WriteString(":\n")
		if content != "" {
			b.WriteString(content)
			b.WriteString("\n")
		}
		if len(msg.ToolCalls) > 0 {
			b.WriteString("Tool calls requested:\n")
			for _, call := range msg.ToolCalls {
				b.WriteString("- ")
				b.WriteString(call.Function.Name)
				if strings.TrimSpace(call.Function.Arguments) != "" {
					b.WriteString(" ")
					b.WriteString(call.Function.Arguments)
				}
				b.WriteString("\n")
			}
		}
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String()) + "\n"
}

func codexModelCatalog(models []string) []ModelInfo {
	if len(models) == 0 {
		models = []string{defaultCodexModelID}
	}
	seen := make(map[string]struct{}, len(models)+1)
	out := make([]ModelInfo, 0, len(models)+1)
	for _, modelID := range append([]string{defaultCodexModelID}, models...) {
		modelID = codexModelID(modelID)
		if strings.TrimSpace(modelID) == "" {
			continue
		}
		if _, ok := seen[modelID]; ok {
			continue
		}
		seen[modelID] = struct{}{}
		out = append(out, ModelInfo{
			ID:            modelID,
			Name:          strings.TrimPrefix(modelID, "codex/"),
			ContextLength: 200000,
			Architecture:  Architecture{Modality: "text"},
		})
	}
	return out
}

func codexModelsFromConfig(models config.ModelConfig) []string {
	candidates := []string{
		models.Planning,
		models.Execution,
		models.Review,
		models.Utility.Commit,
		models.Utility.PR,
		models.Utility.Compaction,
		models.Utility.TodoPlan,
	}
	out := make([]string, 0, len(candidates))
	for _, modelID := range candidates {
		modelID = strings.TrimSpace(modelID)
		switch {
		case strings.HasPrefix(modelID, "codex/"):
			out = append(out, modelID)
		case models.DefaultProvider == codexProviderID && modelID != "" && !strings.Contains(modelID, "/"):
			out = append(out, codexModelID(modelID))
		}
	}
	return out
}

func codexModelID(modelID string) string {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return defaultCodexModelID
	}
	if strings.HasPrefix(modelID, "codex/") {
		return modelID
	}
	return "codex/" + modelID
}

func codexCLIModelArg(modelID string) string {
	modelID = strings.TrimSpace(strings.TrimPrefix(modelID, "codex/"))
	if modelID == "" || modelID == "default" {
		return ""
	}
	return modelID
}

func codexSandboxMode(sandboxCfg config.SandboxConfig, approvalCfg config.ApprovalConfig) string {
	mode := strings.ToLower(strings.TrimSpace(sandboxCfg.Mode))
	switch mode {
	case "readonly", "read-only", "strict":
		return "read-only"
	case "disabled", "none", "off":
		if sandboxCfg.AllowUnsafe && strings.EqualFold(strings.TrimSpace(approvalCfg.Mode), "yolo") {
			return "danger-full-access"
		}
		return "workspace-write"
	default:
		return "workspace-write"
	}
}

func codexApprovalPolicy(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "ask", "explicit", "manual":
		return "untrusted"
	default:
		return "never"
	}
}

func estimateCodexUsage(messages []Message, output string) Usage {
	promptTokens := 0
	for _, msg := range messages {
		promptTokens += len(messageContentToText(msg.Content)) / 4
		for _, call := range msg.ToolCalls {
			promptTokens += len(call.Function.Name)/4 + len(call.Function.Arguments)/4 + 10
		}
	}
	completionTokens := len(output) / 4
	return Usage{
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      promptTokens + completionTokens,
	}
}

func formatCodexCLIError(err error, result CodexCLICommandResult) error {
	stderr := strings.TrimSpace(string(result.Stderr))
	stdout := strings.TrimSpace(string(result.Stdout))
	switch {
	case stderr != "":
		return fmt.Errorf("codex CLI failed: %w: %s", err, stderr)
	case stdout != "":
		return fmt.Errorf("codex CLI failed: %w: %s", err, stdout)
	default:
		return fmt.Errorf("codex CLI failed: %w", err)
	}
}

func runCodexCLICommand(ctx context.Context, cmd CodexCLICommand) (CodexCLICommandResult, error) {
	execCmd := exec.CommandContext(ctx, cmd.Name, cmd.Args...)
	if strings.TrimSpace(cmd.Dir) != "" {
		execCmd.Dir = cmd.Dir
	}
	if cmd.Stdin != "" {
		execCmd.Stdin = strings.NewReader(cmd.Stdin)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	execCmd.Stdout = &stdout
	execCmd.Stderr = &stderr

	err := execCmd.Run()
	return CodexCLICommandResult{
		Stdout: stdout.Bytes(),
		Stderr: stderr.Bytes(),
	}, err
}
