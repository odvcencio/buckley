package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/odvcencio/buckley/pkg/config"
	projectcontext "github.com/odvcencio/buckley/pkg/context"
	"github.com/odvcencio/buckley/pkg/conversation"
	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/oneshot"
	"github.com/odvcencio/buckley/pkg/orchestrator"
	"github.com/odvcencio/buckley/pkg/storage"
	"github.com/odvcencio/buckley/pkg/telemetry"
	buckleyterm "github.com/odvcencio/buckley/pkg/terminal"
	"github.com/odvcencio/buckley/pkg/tool"
	"github.com/odvcencio/buckley/pkg/toolrunner"
	"github.com/odvcencio/buckley/pkg/transparency"
)

// executeOneShot executes a single prompt and exits
func executeOneShot(prompt string, cfg *config.Config, mgr *model.Manager, store *storage.Store, projectContext *projectcontext.ProjectContext, planStore orchestrator.PlanStore, verbose bool) int {
	stderrIsTTY := term.IsTerminal(int(os.Stderr.Fd()))
	minimalOutput := oneshotMinimalOutputEnabled()
	if !minimalOutput {
		if cwd, err := os.Getwd(); err == nil {
			fmt.Fprintf(os.Stderr, "workdir: %s\n", cwd)
		}
		if modelID := strings.TrimSpace(cfg.Models.Execution); modelID != "" {
			fmt.Fprintf(os.Stderr, "model: %s\n", modelID)
		}
	}
	if mgr != nil {
		mgr.SetRequestTimeout(0)
	}
	if cfg == nil || mgr == nil {
		fmt.Fprintln(os.Stderr, "Error: missing configuration or model manager")
		return 1
	}

	// Get model ID
	modelID := cfg.Models.Execution
	if modelID == "" {
		modelID = defaultFallbackModel
	}

	// Build system prompt with budgeted project context
	budget := promptBudget(cfg, mgr, modelID)
	if budget > 0 {
		budget -= estimateMessageTokens("user", prompt)
		if budget < 0 {
			budget = 0
		}
	}
	systemPrompt := buildOneShotSystemPrompt(projectContext, budget)

	// Build messages
	messages := []model.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: prompt},
	}

	// Tool registry for one-shot runs (full access)
	registry := tool.NewRegistry()
	applyToolDefaults(registry, cfg, nil, "")
	if err := registry.LoadDefaultPlugins(); err != nil {
		if !minimalOutput {
			fmt.Fprintf(os.Stderr, "Warning: failed to load some plugins: %v\n", err)
		}
	}
	if cwd, err := os.Getwd(); err == nil {
		registry.SetWorkDir(cwd)
		registry.ConfigureContainers(cfg, cwd)
		registry.ConfigureDockerSandbox(cfg, cwd)
	}
	registerMCPTools(cfg, registry)

	// Set up spinner (used for both progress display and tool activity)
	var spinner *buckleyterm.Spinner
	if !minimalOutput && stderrIsTTY {
		spinner = buckleyterm.NewSpinnerWithOutput(os.Stderr, "processing")
		spinner.Start()
	}

	// Create cancellable context with signal handling
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	mode := cfg.OneshotMode()
	if mode == config.ExecutionModeRLM {
		runner := oneshot.NewRLMRunner(oneshot.RLMRunnerConfig{
			Models:   mgr,
			Registry: registry,
			ModelID:  modelID,
		})
		result, err := runner.Run(ctx, systemPrompt, prompt, nil)
		if spinner != nil {
			spinner.Stop()
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "\nError: %v\n", err)
			return 1
		}
		if result != nil && result.Response != "" {
			fmt.Print(result.Response)
		}
		fmt.Println()
		if result != nil && result.Trace != nil && !minimalOutput && stderrIsTTY {
			printOneShotCost(result.Trace)
		}
		return 0
	}

	runner, err := toolrunner.New(toolrunner.Config{
		Models:   mgr,
		Registry: registry,
	})
	if err != nil {
		if spinner != nil {
			spinner.Stop()
		}
		fmt.Fprintf(os.Stderr, "\nError: %v\n", err)
		return 1
	}

	streamHandler := &oneshotStreamHandler{
		spinner: spinner,
		verbose: verbose && stderrIsTTY && !minimalOutput,
	}
	runner.SetStreamHandler(streamHandler)
	result, err := runner.Run(ctx, toolrunner.Request{
		Messages:        messages,
		SelectionPrompt: prompt,
		Model:           modelID,
	})
	streamHandler.ensureSpinnerStopped()
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nError: %v\n", err)
		return 1
	}
	if result != nil && result.Content != "" && !streamHandler.HasOutput() {
		fmt.Print(result.Content)
	}
	fmt.Println()

	// Show cost/token summary
	if result != nil && !minimalOutput && stderrIsTTY {
		printOneShotUsage(result.Usage, modelID, mgr)
	}
	return 0
}

// printOneShotUsage prints token/cost summary for toolrunner results.
func printOneShotUsage(usage model.Usage, modelID string, mgr *model.Manager) {
	total := usage.TotalTokens
	if total == 0 {
		total = usage.PromptTokens + usage.CompletionTokens
	}
	if total == 0 {
		return
	}
	tokensLine := fmt.Sprintf("Tokens: %d in · %d out = %d total",
		usage.PromptTokens, usage.CompletionTokens, total)
	fmt.Fprintf(os.Stderr, "\033[2m%s\033[0m\n", tokensLine)

	if mgr != nil {
		if cost, err := mgr.CalculateCost(modelID, usage); err == nil && cost > 0 {
			costLine := fmt.Sprintf("Cost: $%.4f", cost)
			fmt.Fprintf(os.Stderr, "\033[2m%s\033[0m\n", costLine)
		}
	}
}

// printOneShotCost prints token/cost summary for RLM results that have a Trace.
func printOneShotCost(trace *transparency.Trace) {
	tokens := trace.Tokens
	total := tokens.Total()
	if total == 0 {
		return
	}
	var tokensLine string
	if tokens.Reasoning > 0 {
		tokensLine = fmt.Sprintf("Tokens: %d in · %d out · %d reasoning = %d total",
			tokens.Input, tokens.Output, tokens.Reasoning, total)
	} else {
		tokensLine = fmt.Sprintf("Tokens: %d in · %d out = %d total",
			tokens.Input, tokens.Output, total)
	}
	fmt.Fprintf(os.Stderr, "\033[2m%s\033[0m\n", tokensLine)
	if trace.Cost > 0 {
		costLine := fmt.Sprintf("Cost: $%.4f", trace.Cost)
		fmt.Fprintf(os.Stderr, "\033[2m%s\033[0m\n", costLine)
	}
}

type oneshotStreamHandler struct {
	mu          sync.Mutex
	wrote       bool
	spinner     *buckleyterm.Spinner
	stopOnce    sync.Once
	verbose     bool
	hasReasoned bool
}

func (h *oneshotStreamHandler) HasOutput() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.wrote
}

// ensureSpinnerStopped stops the spinner exactly once, clearing the line.
func (h *oneshotStreamHandler) ensureSpinnerStopped() {
	h.stopOnce.Do(func() {
		if h.spinner != nil {
			h.spinner.Stop()
		}
	})
}

func (h *oneshotStreamHandler) OnText(text string) {
	if text == "" {
		return
	}
	h.ensureSpinnerStopped()
	h.mu.Lock()
	h.wrote = true
	h.mu.Unlock()
	fmt.Print(text)
}

func (h *oneshotStreamHandler) OnReasoning(reasoning string) {
	if reasoning == "" || !h.verbose {
		return
	}
	h.ensureSpinnerStopped()
	h.mu.Lock()
	if !h.hasReasoned {
		h.hasReasoned = true
		h.mu.Unlock()
		fmt.Fprint(os.Stderr, "\033[2m") // dim
	} else {
		h.mu.Unlock()
	}
	fmt.Fprint(os.Stderr, reasoning)
}

func (h *oneshotStreamHandler) OnReasoningEnd() {
	h.mu.Lock()
	reasoned := h.hasReasoned
	h.mu.Unlock()
	if reasoned {
		fmt.Fprint(os.Stderr, "\033[0m\n") // reset + newline
	}
}

func (h *oneshotStreamHandler) OnToolStart(name string, _ string) {
	if h.spinner != nil {
		h.spinner.SetMessage(fmt.Sprintf("running: %s", name))
	}
}

func (h *oneshotStreamHandler) OnToolEnd(_ string, _ string, _ error) {
	if h.spinner != nil {
		h.spinner.SetMessage("processing")
	}
}

func (h *oneshotStreamHandler) OnError(err error) {
	if err == nil {
		return
	}
	h.ensureSpinnerStopped()
	fmt.Fprintf(os.Stderr, "\nError: %v\n", err)
}

func (h *oneshotStreamHandler) OnComplete(result *toolrunner.Result) {}

func buildOneShotSystemPrompt(projectContext *projectcontext.ProjectContext, budgetTokens int) string {
	base := "You are Buckley, an AI development assistant. Be concise and helpful.\n\n"
	pb := newPromptBuilder(budgetTokens)

	pb.appendSection(base, true)

	if projectContext != nil && projectContext.Loaded {
		rawProject := strings.TrimSpace(projectContext.RawContent)
		projectSummary := buildProjectContextSummary(projectContext)
		if budgetTokens > 0 && (rawProject != "" || projectSummary != "") {
			projectSection := ""
			if rawProject != "" {
				projectSection = "Project Context:\n" + rawProject + "\n\n"
			}
			summarySection := ""
			if projectSummary != "" {
				summarySection = "Project Context (summary):\n" + projectSummary + "\n\n"
			}

			remaining := pb.remaining()
			if remaining > 0 {
				if projectSection != "" && conversation.CountTokens(projectSection) <= remaining {
					pb.appendSection(projectSection, false)
				} else if summarySection != "" && conversation.CountTokens(summarySection) <= remaining {
					pb.appendSection(summarySection, false)
				}
			}
		}
	}

	return strings.TrimSpace(pb.String())
}

func applyToolDefaults(registry *tool.Registry, cfg *config.Config, hub *telemetry.Hub, sessionID string) {
	if registry == nil {
		return
	}
	defaults := tool.DefaultRegistryConfig()
	if cfg != nil {
		defaults.MaxOutputBytes = cfg.ToolMiddleware.MaxResultBytes
		defaults.Middleware.DefaultTimeout = cfg.ToolMiddleware.DefaultTimeout
		defaults.Middleware.PerToolTimeouts = copyDurationMap(cfg.ToolMiddleware.PerToolTimeouts)
		defaults.Middleware.MaxResultBytes = cfg.ToolMiddleware.MaxResultBytes
		defaults.Middleware.RetryConfig = tool.RetryConfig{
			MaxAttempts:  cfg.ToolMiddleware.Retry.MaxAttempts,
			InitialDelay: cfg.ToolMiddleware.Retry.InitialDelay,
			MaxDelay:     cfg.ToolMiddleware.Retry.MaxDelay,
			Multiplier:   cfg.ToolMiddleware.Retry.Multiplier,
			Jitter:       cfg.ToolMiddleware.Retry.Jitter,
		}
	}
	defaults.TelemetryHub = hub
	defaults.TelemetrySessionID = strings.TrimSpace(sessionID)
	tool.ApplyRegistryConfig(registry, defaults)
	if cfg != nil {
		registry.SetSandboxConfig(cfg.Sandbox.ToSandboxConfig(""))
	}
}

func copyDurationMap(src map[string]time.Duration) map[string]time.Duration {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]time.Duration, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}
