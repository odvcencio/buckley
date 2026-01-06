package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/model"
)

// PlanningClient defines the LLM interface for brainstorming and planning
type PlanningClient interface {
	ChatCompletion(ctx context.Context, req model.ChatRequest) (*model.ChatResponse, error)
}

// TodoTool manages task lists for systematic plan execution
type TodoTool struct {
	Store         TodoStore
	PlanningModel string         // Model ID for planning operations
	LLMClient     PlanningClient // Optional: enables brainstorm/refine actions
}

// Approach represents a possible way to tackle a task
type Approach struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Steps       int      `json:"steps"`
	Risk        string   `json:"risk"` // low, medium, high
	Tradeoffs   []string `json:"tradeoffs,omitempty"`
}

// BrainstormOutput contains the result of analyzing a task
type BrainstormOutput struct {
	Approaches  []Approach `json:"approaches"`
	Recommended int        `json:"recommended"`
	Reasoning   string     `json:"reasoning"`
}

// RefineOutput contains concrete TODOs generated from an approach
type RefineOutput struct {
	Todos        []TodoInput         `json:"todos"`
	Dependencies map[string][]string `json:"dependencies,omitempty"`
}

// TodoInput represents a TODO item for creation
type TodoInput struct {
	Content    string `json:"content"`
	ActiveForm string `json:"activeForm"`
	Status     string `json:"status"`
	Metadata   string `json:"metadata,omitempty"`
}

// TodoStore interface for storage operations
//
//go:generate mockgen -package=builtin -destination=mock_todo_store_test.go github.com/odvcencio/buckley/pkg/tool/builtin TodoStore
type TodoStore interface {
	CreateTodo(todo *TodoItem) error
	UpdateTodoStatus(id int64, status string, errorMessage string) error
	GetTodos(sessionID string) ([]TodoItem, error)
	GetActiveTodo(sessionID string) (*TodoItem, error)
	DeleteTodos(sessionID string) error
	CreateCheckpoint(checkpoint *TodoCheckpointData) error
	GetLatestCheckpoint(sessionID string) (*TodoCheckpointData, error)
	EnsureSession(sessionID string) error
}

// TodoItem represents a task
type TodoItem struct {
	ID           int64
	SessionID    string
	Content      string
	ActiveForm   string
	Status       string
	OrderIndex   int
	ParentID     *int64
	CreatedAt    time.Time
	UpdatedAt    time.Time
	CompletedAt  *time.Time
	ErrorMessage string
	Metadata     string
}

// TodoCheckpointData represents a checkpoint snapshot
type TodoCheckpointData struct {
	ID                  int64
	SessionID           string
	CheckpointType      string
	TodoCount           int
	CompletedCount      int
	ConversationSummary string
	ConversationTokens  int
	CreatedAt           time.Time
	Metadata            string
}

func (t *TodoTool) Name() string {
	return "todo"
}

func (t *TodoTool) Description() string {
	return "**ALWAYS USE FOR MULTI-STEP WORK** - Manage TODO lists for systematic task execution. For complex tasks, use action='brainstorm' to analyze approaches before committing. Actions: brainstorm (analyze task, propose approaches), refine (generate TODOs from approach), commit (create TODOs), create, update, list, checkpoint, clear, get_active. This is MANDATORY for organized work."
}

func (t *TodoTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"action": {
				Type:        "string",
				Description: "Action to perform: brainstorm, refine, commit, create, update, list, checkpoint, clear, get_active",
			},
			"session_id": {
				Type:        "string",
				Description: "Session ID for the TODO list",
			},
			// Brainstorm parameters
			"task": {
				Type:        "string",
				Description: "Task description for 'brainstorm' action",
			},
			"context": {
				Type:        "object",
				Description: "Additional context for 'brainstorm' action (files, constraints, etc.)",
			},
			// Refine parameters
			"approach_index": {
				Type:        "integer",
				Description: "Index of selected approach for 'refine' action (from brainstorm result)",
			},
			"approaches": {
				Type:        "array",
				Description: "Approaches from brainstorm result for 'refine' action",
			},
			"adjustments": {
				Type:        "string",
				Description: "User adjustments to the approach for 'refine' action",
			},
			// Create/commit parameters
			"todos": {
				Type:        "array",
				Description: "Array of TODO items for 'create' or 'commit' action. Each item should have content, activeForm, and status fields",
			},
			// Update parameters
			"todo_id": {
				Type:        "integer",
				Description: "ID of the TODO to update (for 'update' action)",
			},
			"status": {
				Type:        "string",
				Description: "New status for 'update' action: pending, in_progress, completed, failed",
			},
			"error_message": {
				Type:        "string",
				Description: "Error message if status is 'failed'",
			},
			// Checkpoint parameters
			"checkpoint_type": {
				Type:        "string",
				Description: "Type of checkpoint: auto, manual, compaction",
			},
			"conversation_summary": {
				Type:        "string",
				Description: "Summary of conversation for checkpoint",
			},
			"conversation_tokens": {
				Type:        "integer",
				Description: "Token count for checkpoint",
			},
		},
		Required: []string{"action", "session_id"},
	}
}

func (t *TodoTool) Execute(params map[string]any) (*Result, error) {
	if t.Store == nil {
		return &Result{
			Success: false,
			Error:   "TODO store not initialized",
		}, nil
	}

	action, ok := params["action"].(string)
	if !ok || action == "" {
		return &Result{
			Success: false,
			Error:   "action parameter must be a non-empty string",
		}, nil
	}

	sessionID, ok := params["session_id"].(string)
	if !ok || sessionID == "" {
		return &Result{
			Success: false,
			Error:   "session_id parameter must be a non-empty string",
		}, nil
	}

	switch action {
	case "brainstorm":
		return t.handleBrainstorm(sessionID, params)
	case "refine":
		return t.handleRefine(sessionID, params)
	case "commit":
		return t.handleCommit(sessionID, params)
	case "create":
		return t.handleCreate(sessionID, params)
	case "update":
		return t.handleUpdate(params)
	case "list":
		return t.handleList(sessionID)
	case "checkpoint":
		return t.handleCheckpoint(sessionID, params)
	case "clear":
		return t.handleClear(sessionID)
	case "get_active":
		return t.handleGetActive(sessionID)
	default:
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("unknown action: %s", action),
		}, nil
	}
}

func (t *TodoTool) handleCreate(sessionID string, params map[string]any) (*Result, error) {
	// Ensure session exists (auto-create if needed for FK constraint)
	if err := t.Store.EnsureSession(sessionID); err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("failed to ensure session exists: %v", err),
		}, nil
	}

	// Clear existing TODOs first (fresh plan)
	if err := t.Store.DeleteTodos(sessionID); err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("failed to clear existing TODOs: %v", err),
		}, nil
	}

	todosRaw, ok := params["todos"]
	if !ok {
		return &Result{
			Success: false,
			Error:   "todos parameter is required for create action",
		}, nil
	}

	// Parse todos array
	todosJSON, err := json.Marshal(todosRaw)
	if err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("failed to marshal todos: %v", err),
		}, nil
	}

	var todoInputs []struct {
		Content    string `json:"content"`
		ActiveForm string `json:"activeForm"`
		Status     string `json:"status"`
		Metadata   string `json:"metadata,omitempty"`
	}

	if err := json.Unmarshal(todosJSON, &todoInputs); err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("failed to parse todos: %v", err),
		}, nil
	}

	now := time.Now()
	createdTodos := []TodoItem{}

	for i, input := range todoInputs {
		todo := &TodoItem{
			SessionID:  sessionID,
			Content:    input.Content,
			ActiveForm: input.ActiveForm,
			Status:     input.Status,
			OrderIndex: i,
			CreatedAt:  now,
			UpdatedAt:  now,
			Metadata:   input.Metadata,
		}

		if err := t.Store.CreateTodo(todo); err != nil {
			return &Result{
				Success: false,
				Error:   fmt.Sprintf("failed to create TODO: %v", err),
			}, nil
		}

		createdTodos = append(createdTodos, *todo)
	}

	// Format summary
	summary := t.formatTodoList(createdTodos)

	result := &Result{
		Success: true,
		Data: map[string]any{
			"session_id": sessionID,
			"count":      len(createdTodos),
			"todos":      createdTodos,
			"summary":    summary,
		},
		ShouldAbridge: true,
	}

	result.DisplayData = map[string]any{
		"summary": fmt.Sprintf("✓ Created %d TODOs", len(createdTodos)),
		"preview": summary,
	}

	return result, nil
}

func (t *TodoTool) handleUpdate(params map[string]any) (*Result, error) {
	todoID, ok := params["todo_id"].(float64)
	if !ok {
		return &Result{
			Success: false,
			Error:   "todo_id parameter must be a number",
		}, nil
	}

	status, ok := params["status"].(string)
	if !ok || status == "" {
		return &Result{
			Success: false,
			Error:   "status parameter must be a non-empty string",
		}, nil
	}

	errorMessage := ""
	if msg, ok := params["error_message"].(string); ok {
		errorMessage = msg
	}

	if err := t.Store.UpdateTodoStatus(int64(todoID), status, errorMessage); err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("failed to update TODO: %v", err),
		}, nil
	}

	result := &Result{
		Success: true,
		Data: map[string]any{
			"todo_id": int64(todoID),
			"status":  status,
		},
		ShouldAbridge: true,
	}

	statusIcon := "✓"
	if status == "failed" {
		statusIcon = "✗"
	} else if status == "in_progress" {
		statusIcon = "⚙"
	}

	result.DisplayData = map[string]any{
		"summary": fmt.Sprintf("%s TODO #%d → %s", statusIcon, int64(todoID), status),
	}

	return result, nil
}

func (t *TodoTool) handleList(sessionID string) (*Result, error) {
	todos, err := t.Store.GetTodos(sessionID)
	if err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("failed to list TODOs: %v", err),
		}, nil
	}

	summary := t.formatTodoList(todos)
	completed := 0
	for _, todo := range todos {
		if todo.Status == "completed" {
			completed++
		}
	}

	result := &Result{
		Success: true,
		Data: map[string]any{
			"session_id": sessionID,
			"count":      len(todos),
			"completed":  completed,
			"todos":      todos,
			"summary":    summary,
		},
		ShouldAbridge: true,
	}

	result.DisplayData = map[string]any{
		"summary": fmt.Sprintf("📋 %d/%d TODOs completed", completed, len(todos)),
		"preview": summary,
	}

	return result, nil
}

func (t *TodoTool) handleCheckpoint(sessionID string, params map[string]any) (*Result, error) {
	checkpointType := "manual"
	if ct, ok := params["checkpoint_type"].(string); ok && ct != "" {
		checkpointType = ct
	}

	summary := ""
	if s, ok := params["conversation_summary"].(string); ok {
		summary = s
	}

	tokens := 0
	if t, ok := params["conversation_tokens"].(float64); ok {
		tokens = int(t)
	}

	// Get current TODO count
	todos, err := t.Store.GetTodos(sessionID)
	if err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("failed to get TODOs: %v", err),
		}, nil
	}

	completed := 0
	for _, todo := range todos {
		if todo.Status == "completed" {
			completed++
		}
	}

	checkpoint := &TodoCheckpointData{
		SessionID:           sessionID,
		CheckpointType:      checkpointType,
		TodoCount:           len(todos),
		CompletedCount:      completed,
		ConversationSummary: summary,
		ConversationTokens:  tokens,
		CreatedAt:           time.Now(),
	}

	if err := t.Store.CreateCheckpoint(checkpoint); err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("failed to create checkpoint: %v", err),
		}, nil
	}

	result := &Result{
		Success: true,
		Data: map[string]any{
			"checkpoint_id": checkpoint.ID,
			"type":          checkpointType,
			"todo_count":    len(todos),
			"completed":     completed,
		},
		ShouldAbridge: true,
	}

	result.DisplayData = map[string]any{
		"summary": fmt.Sprintf("💾 Checkpoint created (%d/%d completed)", completed, len(todos)),
	}

	return result, nil
}

func (t *TodoTool) handleClear(sessionID string) (*Result, error) {
	if err := t.Store.DeleteTodos(sessionID); err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("failed to clear TODOs: %v", err),
		}, nil
	}

	return &Result{
		Success: true,
		Data: map[string]any{
			"session_id": sessionID,
		},
		ShouldAbridge: true,
		DisplayData: map[string]any{
			"summary": "✓ Cleared all TODOs",
		},
	}, nil
}

func (t *TodoTool) handleGetActive(sessionID string) (*Result, error) {
	todo, err := t.Store.GetActiveTodo(sessionID)
	if err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("failed to get active TODO: %v", err),
		}, nil
	}

	if todo == nil {
		return &Result{
			Success: true,
			Data: map[string]any{
				"session_id": sessionID,
				"active":     nil,
			},
			ShouldAbridge: true,
			DisplayData: map[string]any{
				"summary": "No active TODO",
			},
		}, nil
	}

	return &Result{
		Success: true,
		Data: map[string]any{
			"session_id": sessionID,
			"active":     todo,
		},
		ShouldAbridge: true,
		DisplayData: map[string]any{
			"summary": fmt.Sprintf("⚙ Active: %s", todo.Content),
		},
	}, nil
}

func (t *TodoTool) formatTodoList(todos []TodoItem) string {
	var b strings.Builder
	for i, todo := range todos {
		icon := "☐"
		if todo.Status == "completed" {
			icon = "☑"
		} else if todo.Status == "in_progress" {
			icon = "⚙"
		} else if todo.Status == "failed" {
			icon = "✗"
		}

		b.WriteString(fmt.Sprintf("%d. %s %s\n", i+1, icon, todo.Content))
	}
	return strings.TrimRight(b.String(), "\n")
}

// handleBrainstorm analyzes a task and proposes 2-3 approaches
func (t *TodoTool) handleBrainstorm(sessionID string, params map[string]any) (*Result, error) {
	if t.LLMClient == nil {
		return &Result{
			Success: false,
			Error:   "LLM client not configured - brainstorm action requires planning capabilities",
		}, nil
	}

	task, ok := params["task"].(string)
	if !ok || task == "" {
		return &Result{
			Success: false,
			Error:   "task parameter is required for brainstorm action",
		}, nil
	}

	// Build context string from optional context parameter
	contextStr := ""
	if ctx, ok := params["context"].(map[string]any); ok {
		ctxBytes, _ := json.Marshal(ctx)
		contextStr = string(ctxBytes)
	}

	prompt := t.buildBrainstormPrompt(task, contextStr)

	req := model.ChatRequest{
		Model: t.getPlanningModel(),
		Messages: []model.Message{
			{Role: "system", Content: brainstormSystemPrompt},
			{Role: "user", Content: prompt},
		},
		Temperature: 0.7, // Some creativity for exploring approaches
	}

	ctx := context.Background()
	resp, err := t.LLMClient.ChatCompletion(ctx, req)
	if err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("brainstorm LLM call failed: %v", err),
		}, nil
	}

	if len(resp.Choices) == 0 {
		return &Result{
			Success: false,
			Error:   "no response from LLM",
		}, nil
	}

	content := model.ExtractTextContentOrEmpty(resp.Choices[0].Message.Content)
	output, err := t.parseBrainstormResponse(content)
	if err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("failed to parse brainstorm response: %v", err),
		}, nil
	}

	// Format display summary
	var summary strings.Builder
	summary.WriteString(fmt.Sprintf("🧠 Analyzed: %s\n\n", truncate(task, 50)))
	for i, approach := range output.Approaches {
		marker := "  "
		if i == output.Recommended {
			marker = "→ "
		}
		summary.WriteString(fmt.Sprintf("%s%d. %s (%d steps, %s risk)\n",
			marker, i+1, approach.Name, approach.Steps, approach.Risk))
	}
	summary.WriteString(fmt.Sprintf("\nRecommended: %s\nReasoning: %s",
		output.Approaches[output.Recommended].Name, output.Reasoning))

	return &Result{
		Success: true,
		Data: map[string]any{
			"approaches":  output.Approaches,
			"recommended": output.Recommended,
			"reasoning":   output.Reasoning,
		},
		DisplayData: map[string]any{
			"summary": summary.String(),
		},
	}, nil
}

// handleRefine generates concrete TODOs from a selected approach
func (t *TodoTool) handleRefine(sessionID string, params map[string]any) (*Result, error) {
	if t.LLMClient == nil {
		return &Result{
			Success: false,
			Error:   "LLM client not configured - refine action requires planning capabilities",
		}, nil
	}

	approachIndex := 0
	if idx, ok := params["approach_index"].(float64); ok {
		approachIndex = int(idx)
	}

	approachesRaw, ok := params["approaches"]
	if !ok {
		return &Result{
			Success: false,
			Error:   "approaches parameter is required for refine action",
		}, nil
	}

	approachesJSON, err := json.Marshal(approachesRaw)
	if err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("failed to marshal approaches: %v", err),
		}, nil
	}

	var approaches []Approach
	if err := json.Unmarshal(approachesJSON, &approaches); err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("failed to parse approaches: %v", err),
		}, nil
	}

	if approachIndex < 0 || approachIndex >= len(approaches) {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("approach_index %d out of range (0-%d)", approachIndex, len(approaches)-1),
		}, nil
	}

	selectedApproach := approaches[approachIndex]
	adjustments := ""
	if adj, ok := params["adjustments"].(string); ok {
		adjustments = adj
	}

	prompt := t.buildRefinePrompt(selectedApproach, adjustments)

	req := model.ChatRequest{
		Model: t.getPlanningModel(),
		Messages: []model.Message{
			{Role: "system", Content: refineSystemPrompt},
			{Role: "user", Content: prompt},
		},
		Temperature: 0.3, // Lower temp for concrete planning
	}

	ctx := context.Background()
	resp, err := t.LLMClient.ChatCompletion(ctx, req)
	if err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("refine LLM call failed: %v", err),
		}, nil
	}

	if len(resp.Choices) == 0 {
		return &Result{
			Success: false,
			Error:   "no response from LLM",
		}, nil
	}

	content := model.ExtractTextContentOrEmpty(resp.Choices[0].Message.Content)
	output, err := t.parseRefineResponse(content)
	if err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("failed to parse refine response: %v", err),
		}, nil
	}

	// Format display summary
	var summary strings.Builder
	summary.WriteString(fmt.Sprintf("📋 Refined '%s' into %d TODOs:\n\n", selectedApproach.Name, len(output.Todos)))
	for i, todo := range output.Todos {
		summary.WriteString(fmt.Sprintf("%d. %s\n", i+1, todo.Content))
	}

	return &Result{
		Success: true,
		Data: map[string]any{
			"todos":        output.Todos,
			"dependencies": output.Dependencies,
			"approach":     selectedApproach.Name,
		},
		DisplayData: map[string]any{
			"summary": summary.String(),
		},
	}, nil
}

// handleCommit creates TODOs from refined output (similar to create but preserves planning context)
func (t *TodoTool) handleCommit(sessionID string, params map[string]any) (*Result, error) {
	// Ensure session exists
	if err := t.Store.EnsureSession(sessionID); err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("failed to ensure session exists: %v", err),
		}, nil
	}

	// Clear existing TODOs
	if err := t.Store.DeleteTodos(sessionID); err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("failed to clear existing TODOs: %v", err),
		}, nil
	}

	todosRaw, ok := params["todos"]
	if !ok {
		return &Result{
			Success: false,
			Error:   "todos parameter is required for commit action",
		}, nil
	}

	todosJSON, err := json.Marshal(todosRaw)
	if err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("failed to marshal todos: %v", err),
		}, nil
	}

	var todoInputs []TodoInput
	if err := json.Unmarshal(todosJSON, &todoInputs); err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("failed to parse todos: %v", err),
		}, nil
	}

	now := time.Now()
	createdTodos := []TodoItem{}
	var createdIDs []int64

	for i, input := range todoInputs {
		status := input.Status
		if status == "" {
			status = "pending"
		}

		todo := &TodoItem{
			SessionID:  sessionID,
			Content:    input.Content,
			ActiveForm: input.ActiveForm,
			Status:     status,
			OrderIndex: i,
			CreatedAt:  now,
			UpdatedAt:  now,
			Metadata:   input.Metadata,
		}

		if err := t.Store.CreateTodo(todo); err != nil {
			return &Result{
				Success: false,
				Error:   fmt.Sprintf("failed to create TODO: %v", err),
			}, nil
		}

		createdTodos = append(createdTodos, *todo)
		createdIDs = append(createdIDs, todo.ID)
	}

	summary := t.formatTodoList(createdTodos)

	return &Result{
		Success: true,
		Data: map[string]any{
			"session_id":  sessionID,
			"count":       len(createdTodos),
			"created_ids": createdIDs,
			"todos":       createdTodos,
			"summary":     summary,
		},
		ShouldAbridge: true,
		DisplayData: map[string]any{
			"summary": fmt.Sprintf("✅ Committed %d TODOs from planning", len(createdTodos)),
			"preview": summary,
		},
	}, nil
}

// Helper functions for brainstorm/refine

const brainstormSystemPrompt = `You are a planning assistant that analyzes tasks and proposes implementation approaches.

Given a task description and context, you must:
1. Analyze the complexity and requirements
2. Propose exactly 2-3 distinct approaches
3. Recommend the best approach with reasoning

Respond ONLY with valid JSON in this exact format:
{
  "approaches": [
    {
      "name": "Short name",
      "description": "One sentence description",
      "steps": 5,
      "risk": "low|medium|high",
      "tradeoffs": ["pro: ...", "con: ..."]
    }
  ],
  "recommended": 0,
  "reasoning": "Why this approach is best..."
}

Keep approach names short (2-4 words). Steps should be realistic estimates.`

const refineSystemPrompt = `You are a planning assistant that converts high-level approaches into concrete TODO items.

Given an approach, generate a list of actionable TODO items that can be executed sequentially.

Respond ONLY with valid JSON in this exact format:
{
  "todos": [
    {
      "content": "What to do (imperative form)",
      "activeForm": "What is being done (present continuous)",
      "status": "pending"
    }
  ],
  "dependencies": {
    "1": [],
    "2": ["1"],
    "3": ["1", "2"]
  }
}

Each TODO should be:
- Atomic (single action)
- Verifiable (clear done condition)
- Independent where possible

Dependencies are optional - use them when order matters.`

func (t *TodoTool) getPlanningModel() string {
	if t.PlanningModel != "" {
		return t.PlanningModel
	}
	return config.DefaultUtilityModel // Default to configured utility model for planning
}

func (t *TodoTool) buildBrainstormPrompt(task, contextStr string) string {
	var b strings.Builder
	b.WriteString("Analyze this task and propose 2-3 implementation approaches:\n\n")
	b.WriteString("## Task\n")
	b.WriteString(task)
	b.WriteString("\n\n")

	if contextStr != "" {
		b.WriteString("## Context\n")
		b.WriteString(contextStr)
		b.WriteString("\n\n")
	}

	b.WriteString("Respond with JSON only.")
	return b.String()
}

func (t *TodoTool) buildRefinePrompt(approach Approach, adjustments string) string {
	var b strings.Builder
	b.WriteString("Convert this approach into concrete TODO items:\n\n")
	b.WriteString(fmt.Sprintf("## Approach: %s\n", approach.Name))
	b.WriteString(fmt.Sprintf("Description: %s\n", approach.Description))
	b.WriteString(fmt.Sprintf("Estimated steps: %d\n", approach.Steps))
	b.WriteString(fmt.Sprintf("Risk level: %s\n\n", approach.Risk))

	if adjustments != "" {
		b.WriteString("## User Adjustments\n")
		b.WriteString(adjustments)
		b.WriteString("\n\n")
	}

	b.WriteString("Respond with JSON only.")
	return b.String()
}

func (t *TodoTool) parseBrainstormResponse(content string) (*BrainstormOutput, error) {
	// Try to extract JSON from content (may have markdown wrapper)
	content = extractJSON(content)

	var output BrainstormOutput
	if err := json.Unmarshal([]byte(content), &output); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}

	if len(output.Approaches) < 2 {
		return nil, fmt.Errorf("expected at least 2 approaches, got %d", len(output.Approaches))
	}

	if output.Recommended < 0 || output.Recommended >= len(output.Approaches) {
		output.Recommended = 0 // Default to first if invalid
	}

	return &output, nil
}

func (t *TodoTool) parseRefineResponse(content string) (*RefineOutput, error) {
	content = extractJSON(content)

	var output RefineOutput
	if err := json.Unmarshal([]byte(content), &output); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}

	if len(output.Todos) == 0 {
		return nil, fmt.Errorf("no TODOs generated")
	}

	// Ensure all todos have required fields
	for i := range output.Todos {
		if output.Todos[i].Status == "" {
			output.Todos[i].Status = "pending"
		}
		if output.Todos[i].ActiveForm == "" {
			// Generate activeForm from content
			output.Todos[i].ActiveForm = toActiveForm(output.Todos[i].Content)
		}
	}

	return &output, nil
}

// extractJSON attempts to extract JSON from a string that may contain markdown code blocks
func extractJSON(s string) string {
	s = strings.TrimSpace(s)

	// Remove markdown code block wrapper if present
	if strings.HasPrefix(s, "```json") {
		s = strings.TrimPrefix(s, "```json")
		if idx := strings.LastIndex(s, "```"); idx != -1 {
			s = s[:idx]
		}
	} else if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```")
		if idx := strings.LastIndex(s, "```"); idx != -1 {
			s = s[:idx]
		}
	}

	return strings.TrimSpace(s)
}

// toActiveForm converts imperative "Add feature" to continuous "Adding feature"
func toActiveForm(content string) string {
	words := strings.SplitN(content, " ", 2)
	if len(words) < 2 {
		return content + "..."
	}

	verb := strings.ToLower(words[0])
	rest := words[1]

	var activeVerb string
	// Common verb transformations
	switch verb {
	case "add":
		activeVerb = "Adding"
	case "create":
		activeVerb = "Creating"
	case "update":
		activeVerb = "Updating"
	case "fix":
		activeVerb = "Fixing"
	case "remove", "delete":
		activeVerb = "Removing"
	case "implement":
		activeVerb = "Implementing"
	case "write":
		activeVerb = "Writing"
	case "test":
		activeVerb = "Testing"
	case "refactor":
		activeVerb = "Refactoring"
	case "move":
		activeVerb = "Moving"
	case "rename":
		activeVerb = "Renaming"
	case "configure":
		activeVerb = "Configuring"
	case "set":
		activeVerb = "Setting"
	case "run":
		activeVerb = "Running"
	case "check":
		activeVerb = "Checking"
	case "verify":
		activeVerb = "Verifying"
	case "review":
		activeVerb = "Reviewing"
	default:
		// Default: add "ing" if ends in 'e', or just append "ing"
		if strings.HasSuffix(verb, "e") {
			activeVerb = strings.TrimSuffix(verb, "e") + "ing"
		} else {
			activeVerb = verb + "ing"
		}
		// Capitalize first letter
		if len(activeVerb) > 0 {
			activeVerb = strings.ToUpper(activeVerb[:1]) + activeVerb[1:]
		}
	}

	return activeVerb + " " + rest
}

// truncate shortens a string to maxLen with ellipsis
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
