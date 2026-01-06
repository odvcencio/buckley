package artifact

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// CompactionConfig holds configuration for artifact compaction
type CompactionConfig struct {
	ContextThreshold  float64  // Trigger at 80% of context window
	TaskInterval      int      // Also trigger every N tasks
	TokenThreshold    int      // Also trigger when artifact exceeds N tokens
	TargetReduction   float64  // Aim to reduce by 70%
	PreserveCommands  bool     // Never summarize tool calls
	PreserveDecisions bool     // Never lose architectural choices
	Models            []string // Compaction models to try (in order)
}

// DefaultCompactionConfig returns sensible defaults
func DefaultCompactionConfig() CompactionConfig {
	return CompactionConfig{
		ContextThreshold:  0.80,
		TaskInterval:      20,
		TokenThreshold:    15000,
		TargetReduction:   0.70,
		PreserveCommands:  true,
		PreserveDecisions: true,
		Models: []string{
			"deepseek/deepseek-chat",
			"moonshot/kimi-k2-thinking",
			"openai/gpt-4o-mini",
			"google/gemini-flash-1.5",
		},
	}
}

// Compactor handles artifact compaction
type Compactor struct {
	config       CompactionConfig
	modelClient  ModelClient // Interface to call compaction models
	tokenCounter TokenCounter
}

// ModelClient is an interface for calling LLM models
//
//go:generate mockgen -package=artifact -destination=mock_model_client_test.go github.com/odvcencio/buckley/pkg/artifact ModelClient
type ModelClient interface {
	// Complete sends a prompt to the model and returns the response
	Complete(ctx context.Context, model string, prompt string) (string, error)
}

// TokenCounter is an interface for counting tokens in text
//
//go:generate mockgen -package=artifact -destination=mock_token_counter_test.go github.com/odvcencio/buckley/pkg/artifact TokenCounter
type TokenCounter interface {
	// Count returns the number of tokens in the given text
	Count(text string) (int, error)
}

// NewCompactor creates a new artifact compactor
func NewCompactor(config CompactionConfig, modelClient ModelClient, tokenCounter TokenCounter) *Compactor {
	return &Compactor{
		config:       config,
		modelClient:  modelClient,
		tokenCounter: tokenCounter,
	}
}

// ShouldCompact determines if an execution artifact should be compacted
func (c *Compactor) ShouldCompact(artifact *ExecutionArtifact, contextWindowSize int) (bool, string) {
	// Count tokens in artifact
	content := c.artifactToString(artifact)
	tokens, err := c.tokenCounter.Count(content)
	if err != nil {
		// Fallback to length/4 heuristic
		tokens = len(content) / 4
	}

	// Check context window threshold
	contextUsage := float64(tokens) / float64(contextWindowSize)
	if contextUsage >= c.config.ContextThreshold {
		return true, fmt.Sprintf("context usage %.1f%% >= threshold %.1f%%",
			contextUsage*100, c.config.ContextThreshold*100)
	}

	// Check task interval
	completedTasks := countCompletedTasks(artifact.ProgressLog)
	if completedTasks >= c.config.TaskInterval && completedTasks%c.config.TaskInterval == 0 {
		return true, fmt.Sprintf("completed %d tasks (interval: %d)",
			completedTasks, c.config.TaskInterval)
	}

	// Check token threshold
	if tokens >= c.config.TokenThreshold {
		return true, fmt.Sprintf("artifact tokens %d >= threshold %d",
			tokens, c.config.TokenThreshold)
	}

	return false, ""
}

// Compact performs compaction on an execution artifact
// Returns compacted tasks section and compaction result
func (c *Compactor) Compact(ctx context.Context, artifact *ExecutionArtifact, tasksToCompact []TaskProgress) (*CompactionResult, string, error) {
	startTime := time.Now()

	// Extract command logs from tasks
	commands := c.extractCommands(tasksToCompact)

	// Extract key decisions and deviations
	decisions := c.extractDecisions(tasksToCompact)

	// Calculate original token count
	originalContent := c.tasksToString(tasksToCompact)
	originalTokens, _ := c.tokenCounter.Count(originalContent)
	if originalTokens == 0 {
		originalTokens = len(originalContent) / 4
	}

	// Generate compaction prompt
	prompt := c.generateCompactionPrompt(tasksToCompact, commands, decisions)

	// Try compaction models in order
	var compactedSummary string
	var modelUsed string
	var err error

	for _, model := range c.config.Models {
		compactedSummary, err = c.modelClient.Complete(ctx, model, prompt)
		if err == nil {
			modelUsed = model
			break
		}
	}

	if err != nil {
		return nil, "", fmt.Errorf("all compaction models failed: %w", err)
	}

	// Assemble final compacted section
	compacted := c.assembleCompactedSection(tasksToCompact, compactedSummary, commands, decisions)

	// Calculate final token count
	compactedTokens, _ := c.tokenCounter.Count(compacted)
	if compactedTokens == 0 {
		compactedTokens = len(compacted) / 4
	}

	result := &CompactionResult{
		OriginalTokens:    originalTokens,
		CompactedTokens:   compactedTokens,
		ReductionPercent:  float64(originalTokens-compactedTokens) / float64(originalTokens) * 100,
		TasksCompacted:    len(tasksToCompact),
		CommandsPreserved: len(commands),
		Model:             modelUsed,
		Duration:          time.Since(startTime),
	}

	return result, compacted, nil
}

// extractCommands pulls all tool calls from task progress
func (c *Compactor) extractCommands(tasks []TaskProgress) []CommandLog {
	var commands []CommandLog

	for _, task := range tasks {
		// Extract from file modifications
		for _, file := range task.FilesModified {
			commands = append(commands, CommandLog{
				Timestamp: task.StartedAt,
				Tool:      determineToolFromFileChange(file),
				Args: map[string]any{
					"path":          file.Path,
					"lines_added":   file.LinesAdded,
					"lines_deleted": file.LinesDeleted,
				},
				Impact: "modifying",
			})
		}

		// Extract from test results (implicit bash commands)
		for _, test := range task.TestsAdded {
			commands = append(commands, CommandLog{
				Tool:   "bash",
				Args:   map[string]any{"command": fmt.Sprintf("go test -run %s", test.Name)},
				Result: test.Status,
				Impact: "readonly",
			})
		}
	}

	return commands
}

// extractDecisions pulls key architectural decisions from tasks
func (c *Compactor) extractDecisions(tasks []TaskProgress) []Deviation {
	var decisions []Deviation

	for _, task := range tasks {
		for _, deviation := range task.Deviations {
			// Only include medium/high impact deviations
			if deviation.Impact == "Medium" || deviation.Impact == "High" {
				decisions = append(decisions, deviation)
			}
		}
	}

	return decisions
}

// generateCompactionPrompt creates the prompt for the compaction model
func (c *Compactor) generateCompactionPrompt(tasks []TaskProgress, commands []CommandLog, decisions []Deviation) string {
	var b strings.Builder

	b.WriteString("You are summarizing execution logs while preserving resumability.\n\n")
	b.WriteString("CRITICAL: You must preserve ALL commands executed exactly as shown. ")
	b.WriteString("A future agent will resume this work and needs the complete command history.\n\n")

	b.WriteString(fmt.Sprintf("Input: Tasks %d-%d with verbose narratives\n\n", tasks[0].TaskID, tasks[len(tasks)-1].TaskID))

	b.WriteString("Tasks:\n")
	for _, task := range tasks {
		b.WriteString(fmt.Sprintf("- Task %d: %s (%s)\n", task.TaskID, task.Description, task.Status))
		if task.ImplementationNotes != "" {
			b.WriteString(fmt.Sprintf("  Notes: %s\n", truncate(task.ImplementationNotes, 200)))
		}
	}

	b.WriteString("\nOutput a compact summary with:\n")
	b.WriteString("1. High-level summary paragraph (what was accomplished)\n")
	b.WriteString("2. Key decisions made (especially deviations from plan)\n")
	b.WriteString("3. Aggregate metrics (files changed, test results, duration)\n\n")

	b.WriteString("Do NOT summarize:\n")
	b.WriteString("- Architectural decisions\n")
	b.WriteString("- Deviations from plan\n")
	b.WriteString("- Test results\n\n")

	b.WriteString("DO summarize:\n")
	b.WriteString("- Implementation narratives\n")
	b.WriteString("- Reasoning explanations\n")
	b.WriteString("- Verbose descriptions\n\n")

	b.WriteString(fmt.Sprintf("Target: Reduce narrative by %.0f%% while keeping 100%% of mechanical facts.\n", c.config.TargetReduction*100))

	return b.String()
}

// assembleCompactedSection combines the AI summary with preserved commands
func (c *Compactor) assembleCompactedSection(tasks []TaskProgress, summary string, commands []CommandLog, decisions []Deviation) string {
	var b strings.Builder

	firstTask := tasks[0].TaskID
	lastTask := tasks[len(tasks)-1].TaskID

	b.WriteString(fmt.Sprintf("## Tasks %d-%d: Compacted ✓\n\n", firstTask, lastTask))
	b.WriteString(fmt.Sprintf("**Summary:** %s\n\n", summary))

	// Commands executed (preserved verbatim)
	b.WriteString("**Commands Executed (resumable log):**\n```sh\n")
	for _, cmd := range commands {
		if cmd.Tool == "bash" {
			b.WriteString(fmt.Sprintf("bash: %v\n", cmd.Args["command"]))
		} else if cmd.Tool == "write" || cmd.Tool == "edit" {
			path := cmd.Args["path"]
			linesAdded := cmd.Args["lines_added"]
			b.WriteString(fmt.Sprintf("%s %v (+%v lines)\n", cmd.Tool, path, linesAdded))
		}
	}
	b.WriteString("```\n\n")

	// Key decisions (preserved)
	if len(decisions) > 0 {
		b.WriteString("**Key Decisions (preserve for review):**\n")
		for _, decision := range decisions {
			b.WriteString(fmt.Sprintf("- Task %d: %s (%s)\n", decision.TaskID, decision.Description, decision.Rationale))
		}
		b.WriteString("\n")
	}

	// Aggregate metrics
	totalFiles := countUniqueFiles(tasks)
	totalLines := sumLinesChanged(tasks)
	totalDuration := calculateTotalDuration(tasks)
	passedTests, totalTests := countTests(tasks)

	b.WriteString(fmt.Sprintf("**Files Changed:** %d files, +%d lines\n", totalFiles, totalLines))
	b.WriteString(fmt.Sprintf("**Duration:** %s\n", totalDuration))
	b.WriteString(fmt.Sprintf("**Tests:** %d/%d passing\n\n", passedTests, totalTests))

	return b.String()
}

// Helper functions

func (c *Compactor) artifactToString(artifact *ExecutionArtifact) string {
	// Simple serialization for token counting
	var b strings.Builder
	for _, task := range artifact.ProgressLog {
		b.WriteString(task.Description)
		b.WriteString(task.ImplementationNotes)
		b.WriteString(task.CodeSnippet)
	}
	return b.String()
}

func (c *Compactor) tasksToString(tasks []TaskProgress) string {
	var b strings.Builder
	for _, task := range tasks {
		b.WriteString(task.Description)
		b.WriteString(task.ImplementationNotes)
		b.WriteString(task.CodeSnippet)
	}
	return b.String()
}

func countCompletedTasks(tasks []TaskProgress) int {
	count := 0
	for _, task := range tasks {
		if task.Status == "completed" {
			count++
		}
	}
	return count
}

func determineToolFromFileChange(file FileModification) string {
	if file.LinesAdded > 0 && file.LinesDeleted == 0 {
		return "write"
	}
	return "edit"
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func countUniqueFiles(tasks []TaskProgress) int {
	files := make(map[string]bool)
	for _, task := range tasks {
		for _, file := range task.FilesModified {
			files[file.Path] = true
		}
	}
	return len(files)
}

func sumLinesChanged(tasks []TaskProgress) int {
	total := 0
	for _, task := range tasks {
		for _, file := range task.FilesModified {
			total += file.LinesAdded
		}
	}
	return total
}

func calculateTotalDuration(tasks []TaskProgress) string {
	if len(tasks) == 0 {
		return "0s"
	}
	first := tasks[0].StartedAt
	last := tasks[len(tasks)-1].StartedAt
	if tasks[len(tasks)-1].CompletedAt != nil {
		last = *tasks[len(tasks)-1].CompletedAt
	}
	duration := last.Sub(first)
	return duration.Round(time.Second).String()
}

func countTests(tasks []TaskProgress) (passed, total int) {
	for _, task := range tasks {
		for _, test := range task.TestsAdded {
			total++
			if test.Status == "pass" {
				passed++
			}
		}
	}
	return
}
