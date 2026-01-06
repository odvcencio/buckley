package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/odvcencio/buckley/pkg/artifact"
	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/encoding/toon"
	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/paths"
	"github.com/odvcencio/buckley/pkg/skill"
	"github.com/odvcencio/buckley/pkg/telemetry"
	"github.com/odvcencio/buckley/pkg/tool"
)

// BuilderAgent encapsulates implementation generation, tool execution, and telemetry.
type BuilderAgent struct {
	plan         *Plan
	modelClient  ModelClient
	toolRegistry *tool.Registry
	config       *config.Config
	workflow     *WorkflowManager
	logger       *builderLogger
	resultCodec  *toon.Codec
}

// BuilderResult captures the outcome of a builder run.
type BuilderResult struct {
	Implementation string
	Files          []BuilderFile
	StartedAt      time.Time
	CompletedAt    time.Time
}

// BuilderFile summarizes a file modification emitted by the builder.
type BuilderFile struct {
	Path       string
	LinesAdded int
}

// BuilderEventType categorizes builder telemetry events.
type BuilderEventType string

const (
	builderEventStart          BuilderEventType = "builder.start"
	builderEventImplementation BuilderEventType = "builder.implementation.generated"
	builderEventApplyStart     BuilderEventType = "builder.apply.start"
	builderEventApplyComplete  BuilderEventType = "builder.apply.complete"
	builderEventToolCall       BuilderEventType = "builder.tool.call"
	builderEventToolResult     BuilderEventType = "builder.tool.result"
	builderEventCompleted      BuilderEventType = "builder.completed"
	builderEventFailed         BuilderEventType = "builder.failed"
)

// builderEvent represents a single structured log entry for overseer replay.
type builderEvent struct {
	Timestamp time.Time         `json:"timestamp"`
	PlanID    string            `json:"plan_id,omitempty"`
	TaskID    string            `json:"task_id,omitempty"`
	Type      BuilderEventType  `json:"type"`
	Details   map[string]string `json:"details,omitempty"`
}

// builderLogger persists builder events to disk for overseer supervision.
type builderLogger struct {
	path string
	mu   sync.Mutex
}

// NewBuilderAgent constructs a builder agent for the given plan.
func NewBuilderAgent(plan *Plan, cfg *config.Config, client ModelClient, registry *tool.Registry, workflow *WorkflowManager) *BuilderAgent {
	return &BuilderAgent{
		plan:         plan,
		modelClient:  client,
		toolRegistry: registry,
		config:       cfg,
		workflow:     workflow,
		logger:       newBuilderLogger(plan),
		resultCodec:  toon.New(cfg.Encoding.UseToon),
	}
}

// Build generates and applies an implementation for the provided task.
func (a *BuilderAgent) Build(task *Task) (*BuilderResult, error) {
	start := time.Now()
	a.logEvent(task.ID, builderEventStart, map[string]string{
		"title": task.Title,
	})
	a.emitBuilderEvent(task, telemetry.EventBuilderStarted, map[string]any{
		"stage": "build",
	})
	a.sendProgress("🛠️ Builder agent drafting %q", task.Title)

	if a.workflow != nil {
		a.workflow.ClearPause()
		prevAgent := a.workflow.GetActiveAgent()
		a.workflow.SetActiveAgent("Builder")
		defer a.workflow.SetActiveAgent(prevAgent)
	}

	impl, err := a.generateImplementation(task)
	if err != nil {
		a.logFailure(task.ID, "generation", err)
		a.sendProgress("⚠️ Builder failed to generate implementation for %q: %v", task.Title, err)
		a.emitBuilderEvent(task, telemetry.EventBuilderFailed, map[string]any{
			"stage": "generate",
			"error": err.Error(),
		})
		return nil, fmt.Errorf("builder failed to generate implementation: %w", err)
	}
	a.sendProgress("🧠 Draft ready for %q", task.Title)

	a.logEvent(task.ID, builderEventImplementation, map[string]string{
		"preview": summarizeSnippet(impl, 200),
	})

	files, err := parseFileBlocks(impl)
	if err != nil {
		a.logFailure(task.ID, "parse", err)
		a.sendProgress("⚠️ Builder failed to parse implementation for %q: %v", task.Title, err)
		a.emitBuilderEvent(task, telemetry.EventBuilderFailed, map[string]any{
			"stage": "parse",
			"error": err.Error(),
		})
		return nil, fmt.Errorf("builder failed to parse implementation: %w", err)
	}
	a.sendProgress("📦 Builder produced %d file block(s) for %q", len(files), task.Title)

	// Allow tasks with no file changes (e.g., analysis, command execution only)
	var applied []BuilderFile
	if len(files) > 0 {
		a.sendProgress("📁 Applying %d file(s) for %q", len(files), task.Title)
		applied, err = a.applyFiles(task, files, "primary")
		if err != nil {
			a.logFailure(task.ID, "apply", err)
			a.sendProgress("⚠️ Failed to apply changes for %q: %v", task.Title, err)
			a.emitBuilderEvent(task, telemetry.EventBuilderFailed, map[string]any{
				"stage": "apply",
				"error": err.Error(),
			})
			return nil, err
		}
	} else {
		// No files to apply - this is OK for analysis/command tasks
		a.logEvent(task.ID, builderEventCompleted, map[string]string{
			"note": "No file changes (analysis/command task)",
		})
		a.sendProgress("📝 No file changes required for %q", task.Title)
	}

	result := &BuilderResult{
		Implementation: impl,
		Files:          applied,
		StartedAt:      start,
		CompletedAt:    time.Now(),
	}
	a.sendProgress("✅ Builder agent finished %q (%d file(s))", task.Title, len(applied))

	a.logEvent(task.ID, builderEventCompleted, map[string]string{
		"files":       fmt.Sprintf("%d", len(applied)),
		"duration_ms": fmt.Sprintf("%d", result.CompletedAt.Sub(result.StartedAt).Milliseconds()),
	})
	a.emitBuilderEvent(task, telemetry.EventBuilderCompleted, map[string]any{
		"stage":       "build",
		"files":       len(applied),
		"duration_ms": result.CompletedAt.Sub(result.StartedAt).Milliseconds(),
	})

	a.recordTaskProgress(task, result)

	return result, nil
}

// ApplyImplementation applies an implementation (often from self-healing) and records telemetry.
func (a *BuilderAgent) ApplyImplementation(task *Task, implementation string, source string) ([]BuilderFile, error) {
	a.logEvent(task.ID, builderEventApplyStart, map[string]string{
		"source": source,
	})
	a.emitBuilderEvent(task, telemetry.EventBuilderStarted, map[string]any{
		"stage":  "apply",
		"source": source,
	})
	a.sendProgress("🛠️ Applying %q implementation from %s", task.Title, source)

	files, err := parseFileBlocks(implementation)
	if err != nil {
		a.logFailure(task.ID, "parse", err)
		a.sendProgress("⚠️ Failed to parse %s implementation for %q: %v", source, task.Title, err)
		a.emitBuilderEvent(task, telemetry.EventBuilderFailed, map[string]any{
			"stage":  "apply_parse",
			"source": source,
			"error":  err.Error(),
		})
		return nil, fmt.Errorf("builder failed to parse %s implementation: %w", source, err)
	}

	// Allow tasks with no file changes (e.g., analysis, command execution only)
	var applied []BuilderFile
	if len(files) > 0 {
		applied, err = a.applyFiles(task, files, source)
		if err != nil {
			a.sendProgress("⚠️ Failed to apply %s implementation for %q: %v", source, task.Title, err)
			a.emitBuilderEvent(task, telemetry.EventBuilderFailed, map[string]any{
				"stage":  "apply_apply",
				"source": source,
				"error":  err.Error(),
			})
			return nil, err
		}
	} else {
		// No files to apply - this is OK for analysis/command tasks
		a.logEvent(task.ID, builderEventApplyComplete, map[string]string{
			"source": source,
			"note":   "No file changes (analysis/command task)",
		})
	}

	a.logEvent(task.ID, builderEventApplyComplete, map[string]string{
		"source": source,
		"files":  fmt.Sprintf("%d", len(applied)),
	})
	a.sendProgress("✅ Applied %d file(s) for %q via %s", len(applied), task.Title, source)
	a.emitBuilderEvent(task, telemetry.EventBuilderCompleted, map[string]any{
		"stage":  "apply",
		"source": source,
		"files":  len(applied),
	})

	return applied, nil
}

func (a *BuilderAgent) sendProgress(format string, args ...any) {
	if a == nil || a.workflow == nil {
		return
	}
	a.workflow.SendProgress(fmt.Sprintf(format, args...))
}

func (a *BuilderAgent) emitBuilderEvent(task *Task, eventType telemetry.EventType, details map[string]any) {
	if a == nil || a.workflow == nil {
		return
	}
	if details == nil {
		details = make(map[string]any)
	}
	a.workflow.EmitBuilderEvent(task, eventType, details)
}

func (a *BuilderAgent) generateImplementation(task *Task) (string, error) {
	prompt := buildImplementationPrompt(task)

	systemMessage := "You are an expert software engineer. Use the available tools to implement tasks. Run commands with run_shell, read/write files with file tools, check git status, etc. Prefer running actual commands over generating fake output.\n\nFor analysis tasks: Just run the commands and report results - you don't need to create files.\nFor implementation tasks: After running any necessary commands, provide code in markdown blocks with filepath: headers."
	if a.workflow != nil {
		if a.workflow.skillRegistry != nil {
			if desc := strings.TrimSpace(a.workflow.skillRegistry.GetDescriptions()); desc != "" {
				systemMessage += "\n\n" + desc
			}
		}
		if persona := a.workflow.PersonaSection("builder"); persona != "" {
			systemMessage += "\n\n## Persona Voice\n" + persona
		}
	}

	messages := []model.Message{
		{
			Role:    "system",
			Content: systemMessage,
		},
	}
	if a.workflow != nil {
		for _, msg := range a.workflow.skillMessages {
			if strings.TrimSpace(msg) == "" {
				continue
			}
			messages = append(messages, model.Message{
				Role:    "system",
				Content: msg,
			})
		}
	}
	messages = append(messages, model.Message{
		Role:    "user",
		Content: prompt,
	})

	req := model.ChatRequest{
		Model:       a.config.Models.Execution,
		Messages:    messages,
		ToolChoice:  "auto",
		Temperature: 0.2,
	}

	// Use streaming to handle tool calls
	return a.generateWithTools(req, task)
}

func (a *BuilderAgent) generateWithTools(req model.ChatRequest, task *Task) (string, error) {
	ctx := context.Background()
	maxIterations := 10 // Prevent infinite loops
	messages := req.Messages
	skillState := (*skill.RuntimeState)(nil)
	var baseInjector func(string)

	if a.workflow != nil {
		skillState = a.workflow.skillState
		if skillState != nil {
			baseInjector = a.workflow.skillInjector
			skillState.SetInjector(func(content string) {
				if baseInjector != nil {
					baseInjector(content)
				}
				messages = append(messages, model.Message{
					Role:    "system",
					Content: content,
				})
			})
			defer skillState.SetInjector(baseInjector)
		}
	}

	for iter := 0; iter < maxIterations; iter++ {
		allowedTools := []string{}
		if skillState != nil {
			allowedTools = skillState.ToolFilter()
		}

		if a.toolRegistry != nil {
			tools := a.toolRegistry.ToOpenAIFunctionsFiltered(allowedTools)
			if len(tools) > 0 {
				req.Tools = tools
				req.ToolChoice = "auto"
			} else {
				req.Tools = nil
				req.ToolChoice = "none"
			}
		}

		// Update request with current messages
		req.Messages = messages

		// Call model
		resp, err := a.modelClient.ChatCompletion(ctx, req)
		if err != nil {
			if a.toolRegistry != nil && isToolUnsupportedError(err) {
				req.Tools = nil
				req.ToolChoice = "none"
				continue
			}
			return "", fmt.Errorf("model call failed: %w", err)
		}
		if len(resp.Choices) == 0 {
			return "", fmt.Errorf("no response choices from model")
		}

		choice := resp.Choices[0]

		// If no tool calls, we're done
		if len(choice.Message.ToolCalls) == 0 {
			return model.ExtractTextContent(choice.Message.Content)
		}

		// Add assistant message with tool calls
		for i := range choice.Message.ToolCalls {
			if choice.Message.ToolCalls[i].ID == "" {
				choice.Message.ToolCalls[i].ID = fmt.Sprintf("tool-%d", i+1)
			}
		}
		messages = append(messages, choice.Message)

		// Execute each tool call
		for _, tc := range choice.Message.ToolCalls {
			if !tool.IsToolAllowed(tc.Function.Name, allowedTools) {
				result := fmt.Sprintf("Error: tool %s not allowed by active skills", tc.Function.Name)
				messages = append(messages, model.Message{
					Role:       "tool",
					Content:    result,
					ToolCallID: tc.ID,
					Name:       tc.Function.Name,
				})
				continue
			}
			result, err := a.executeToolCall(tc, task)
			if err != nil {
				result = fmt.Sprintf("Error: %v", err)
			}

			// Add tool response message
			messages = append(messages, model.Message{
				Role:       "tool",
				Content:    result,
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
			})
		}
	}

	return "", fmt.Errorf("max tool calling iterations (%d) exceeded", maxIterations)
}

func (a *BuilderAgent) executeToolCall(tc model.ToolCall, task *Task) (string, error) {
	// Parse arguments
	var params map[string]any
	if err := json.Unmarshal([]byte(tc.Function.Arguments), &params); err != nil {
		return "", fmt.Errorf("failed to parse tool arguments: %w", err)
	}
	if params != nil && tc.ID != "" {
		params[tool.ToolCallIDParam] = tc.ID
	}

	// Log the tool call
	a.logEvent(task.ID, builderEventToolCall, map[string]string{
		"tool":   tc.Function.Name,
		"params": tc.Function.Arguments,
	})

	// Send progress update
	if a.workflow != nil {
		a.workflow.SendProgress(fmt.Sprintf("  🔧 %s", tc.Function.Name))
	}

	// Get and execute the tool
	tool, exists := a.toolRegistry.Get(tc.Function.Name)
	if !exists {
		return "", fmt.Errorf("tool not found: %s", tc.Function.Name)
	}

	// Authorize tool call (checks for risky operations like sudo)
	if a.workflow != nil {
		if err := a.workflow.AuthorizeToolCall(tool, params); err != nil {
			return "", fmt.Errorf("tool authorization failed: %w", err)
		}
	}

	start := time.Now()
	result, err := tool.Execute(params)
	elapsed := time.Since(start).Milliseconds()

	// Log result
	a.logEvent(task.ID, builderEventToolResult, map[string]string{
		"tool":    tc.Function.Name,
		"elapsed": fmt.Sprintf("%d", elapsed),
		"success": fmt.Sprintf("%t", err == nil),
	})

	// Send result progress
	if a.workflow != nil {
		status := "✓"
		if err != nil {
			status = "✗"
		}
		a.workflow.SendProgress(fmt.Sprintf("  %s %s (%dms)", status, tc.Function.Name, elapsed))
	}

	if err != nil {
		return "", err
	}

	// Return result as string for LLM
	if result.Success {
		data, err := a.resultCodec.Marshal(result.Data)
		if err != nil {
			return "", err
		}
		return string(data), nil
	}
	return "", fmt.Errorf("tool execution failed: %s", result.Error)
}

func (a *BuilderAgent) applyFiles(task *Task, files map[string]string, source string) ([]BuilderFile, error) {
	if len(files) == 0 {
		return nil, fmt.Errorf("no file changes found in implementation")
	}

	writeTool, ok := a.toolRegistry.Get("write_file")
	if !ok {
		return nil, fmt.Errorf("write_file tool not available")
	}

	applied := make([]BuilderFile, 0, len(files))

	for path, content := range files {
		params := map[string]any{
			"path":    path,
			"content": content,
		}

		a.logEvent(task.ID, builderEventToolCall, map[string]string{
			"tool":   writeTool.Name(),
			"path":   path,
			"source": source,
		})

		start := time.Now()
		if a.workflow != nil && a.shouldPauseForArchitecturalConflict(task, path) {
			err := a.workflow.pauseWorkflow("Architectural Conflict",
				fmt.Sprintf("Task %s scoped files %v but attempted to modify %s", task.ID, task.Files, path))
			if err != nil {
				a.logFailure(task.ID, "architectural_conflict", err)
			}
			return nil, err
		}
		if a.workflow != nil {
			if err := a.workflow.AuthorizeToolCall(writeTool, params); err != nil {
				a.logFailure(task.ID, "tool_authorization", err)
				return nil, err
			}
		}
		result, err := writeTool.Execute(params)
		end := time.Now()
		if a.workflow != nil {
			a.workflow.RecordToolCall(writeTool, params, result, start, end)
		}

		if err != nil {
			a.logFailure(task.ID, "tool_execute", err)
			return nil, fmt.Errorf("failed to write %s: %w", path, err)
		}

		if !result.Success {
			a.logFailure(task.ID, "tool_execute", fmt.Errorf("%s", result.Error))
			return nil, fmt.Errorf("failed to write %s: %s", path, result.Error)
		}

		a.logEvent(task.ID, builderEventToolResult, map[string]string{
			"tool":    writeTool.Name(),
			"path":    path,
			"source":  source,
			"elapsed": fmt.Sprintf("%d", end.Sub(start).Milliseconds()),
		})

		applied = append(applied, BuilderFile{
			Path:       path,
			LinesAdded: countLines(content),
		})
	}

	return applied, nil
}

func (a *BuilderAgent) recordTaskProgress(task *Task, result *BuilderResult) {
	if a.workflow == nil || result == nil {
		return
	}

	if a.workflow.executionTracker == nil {
		a.ensureExecutionTracker()
	}

	if a.workflow.executionTracker == nil {
		// No execution artifact initialized; nothing to record.
		return
	}

	taskID := parseTaskID(task.ID)
	completedAt := result.CompletedAt
	progress := artifact.TaskProgress{
		TaskID:              taskID,
		Description:         task.Title,
		Status:              "completed",
		StartedAt:           result.StartedAt,
		CompletedAt:         &completedAt,
		Duration:            result.CompletedAt.Sub(result.StartedAt).Round(time.Millisecond).String(),
		FilesModified:       toFileModifications(result.Files),
		ImplementationNotes: fmt.Sprintf("Builder agent touched %d files for %s", len(result.Files), task.Title),
	}

	if err := a.workflow.RecordTaskProgress(progress); err != nil {
		a.logFailure(task.ID, "artifact", err)
	}
}

func (a *BuilderAgent) ensureExecutionTracker() {
	if a.workflow == nil || a.workflow.executionTracker != nil || a.plan == nil {
		return
	}

	outputDir := a.workflow.config.Artifacts.ExecutionDir
	if strings.TrimSpace(outputDir) == "" {
		outputDir = filepath.Join("docs", "execution")
	}

	planPath := filepath.Join("docs", "plans", fmt.Sprintf("%s.md", a.plan.ID))
	tracker := artifact.NewExecutionTracker(
		outputDir,
		planPath,
		a.plan.FeatureName,
		len(a.plan.Tasks),
	)

	if err := tracker.Initialize(); err != nil {
		a.logFailure("", "execution_tracker_init", err)
		return
	}

	a.workflow.executionTracker = tracker
}

func toFileModifications(files []BuilderFile) []artifact.FileModification {
	modifications := make([]artifact.FileModification, 0, len(files))
	for _, file := range files {
		modifications = append(modifications, artifact.FileModification{
			Path:       file.Path,
			LinesAdded: file.LinesAdded,
		})
	}
	return modifications
}

func (a *BuilderAgent) logEvent(taskID string, eventType BuilderEventType, details map[string]string) {
	if a.logger == nil {
		return
	}
	a.logger.record(builderEvent{
		Timestamp: time.Now(),
		PlanID:    safePlanID(a.plan),
		TaskID:    taskID,
		Type:      eventType,
		Details:   details,
	})
}

func (a *BuilderAgent) logFailure(taskID string, stage string, err error) {
	a.logEvent(taskID, builderEventFailed, map[string]string{
		"stage": stage,
		"error": err.Error(),
	})
}

func newBuilderLogger(plan *Plan) *builderLogger {
	if plan == nil {
		return nil
	}

	identifier := SanitizeIdentifier(plan.ID)
	if identifier == "" {
		identifier = "default"
	}

	logDir := paths.BuckleyLogsDir(identifier)
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return nil
	}

	return &builderLogger{
		path: filepath.Join(logDir, "builder.jsonl"),
	}
}

func (l *builderLogger) record(event builderEvent) {
	if l == nil || l.path == "" {
		return
	}

	data, err := json.Marshal(event)
	if err != nil {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return
	}
	defer f.Close()

	if _, err := f.Write(append(data, '\n')); err != nil {
		return
	}
}

func safePlanID(plan *Plan) string {
	if plan == nil {
		return ""
	}
	return plan.ID
}

func buildImplementationPrompt(task *Task) string {
	var b strings.Builder

	b.WriteString("Implement this task:\n\n")
	b.WriteString(fmt.Sprintf("**Task:** %s\n\n", task.Title))
	b.WriteString(fmt.Sprintf("**Description:** %s\n\n", task.Description))

	if len(task.Files) > 0 {
		b.WriteString("**Files to modify:**\n")
		for _, file := range task.Files {
			b.WriteString(fmt.Sprintf("- %s\n", file))
		}
		b.WriteString("\n")
	}

	b.WriteString("Provide the complete implementation with file contents.\n\n")
	b.WriteString("Format your response as:\n")
	b.WriteString("```filepath:/path/to/file.go\n")
	b.WriteString("file contents here\n")
	b.WriteString("```\n\n")
	b.WriteString("You can provide multiple files. Each file should be in its own code block with the filepath: prefix.")

	return b.String()
}

// parseFileBlocks extracts file paths and contents from markdown code blocks.
func parseFileBlocks(text string) (map[string]string, error) {
	files := make(map[string]string)

	re := regexp.MustCompile("(?s)```(?:filepath:)?([^\\n`]+)?\\n(.*?)```")
	matches := re.FindAllStringSubmatch(text, -1)

	for _, match := range matches {
		if len(match) < 3 {
			continue
		}

		header := strings.TrimSpace(match[1])
		content := match[2]

		var filepath string
		if header != "" && !isLanguageName(header) {
			filepath = header
		} else {
			lines := strings.Split(content, "\n")
			for i := 0; i < min(5, len(lines)); i++ {
				line := strings.TrimSpace(lines[i])
				if strings.HasPrefix(line, "// File:") || strings.HasPrefix(line, "# File:") {
					filepath = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(line, "//"), "#"))
					filepath = strings.TrimPrefix(filepath, "File:")
					filepath = strings.TrimSpace(filepath)
					break
				}
			}
		}

		if filepath != "" {
			files[filepath] = strings.TrimSpace(content)
		}
	}

	return files, nil
}

func isLanguageName(s string) bool {
	languages := []string{"go", "python", "javascript", "typescript", "rust", "java", "c", "cpp", "bash", "sh", "yaml", "json", "md", "markdown"}
	lower := strings.ToLower(s)
	for _, lang := range languages {
		if lower == lang {
			return true
		}
	}
	return false
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func parseTaskID(id string) int {
	var value int
	for _, r := range id {
		if r < '0' || r > '9' {
			return 0
		}
		value = value*10 + int(r-'0')
	}
	return value
}

func summarizeSnippet(content string, limit int) string {
	if len(content) <= limit {
		return content
	}
	return content[:limit] + "..."
}

func countLines(content string) int {
	if content == "" {
		return 0
	}
	return len(strings.Split(content, "\n"))
}

func isToolUnsupportedError(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	if strings.Contains(lower, "tool") && strings.Contains(lower, "not support") {
		return true
	}
	if strings.Contains(lower, "tool") && strings.Contains(lower, "unsupported") {
		return true
	}
	if strings.Contains(lower, "does not support tool calling") {
		return true
	}
	if strings.Contains(lower, "does not support tool response") {
		return true
	}
	return false
}

func (a *BuilderAgent) shouldPauseForArchitecturalConflict(task *Task, path string) bool {
	if a.workflow == nil || a.config == nil || !a.config.Workflow.PauseOnArchitecturalConflict {
		return false
	}
	if task == nil || len(task.Files) == 0 {
		return false
	}
	return !fileWithinPlannedScope(task.Files, path)
}

func fileWithinPlannedScope(planned []string, candidate string) bool {
	if candidate == "" {
		return false
	}
	candidate = filepath.Clean(candidate)
	for _, entry := range planned {
		entry = filepath.Clean(entry)
		if entry == candidate {
			return true
		}
		if strings.HasSuffix(entry, "/...") {
			base := strings.TrimSuffix(entry, "/...")
			base = strings.TrimSuffix(base, "/")
			if strings.HasPrefix(candidate, base) {
				return true
			}
		}
		if strings.HasSuffix(entry, "/*") {
			base := strings.TrimSuffix(entry, "/*")
			if strings.HasPrefix(candidate, base+"/") {
				return true
			}
		}
	}
	return false
}
