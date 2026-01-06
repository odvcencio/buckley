package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/tool/builtin"
)

// PlanningCoordinator manages the brainstorm → refine → commit flow
type PlanningCoordinator struct {
	config           *config.PlanningConfig
	complexityDetect *ComplexityDetector
	todoTool         *builtin.TodoTool
	modelClient      ModelClient
	decisionLog      *DecisionLog

	// State
	mu             sync.RWMutex
	currentSession string
	pendingResult  *PlanningResult
	awaitingUser   bool
}

// PlanningResult contains the outcome of a planning phase
type PlanningResult struct {
	Phase       PlanningPhase
	Approaches  []builtin.Approach
	Recommended int
	Reasoning   string
	Todos       []builtin.TodoInput
	Error       error
	AutoDecided bool // True if decision was made automatically in long-run mode
}

// PlanningPhase represents the current phase of planning
type PlanningPhase int

const (
	PhaseIdle PlanningPhase = iota
	PhaseBrainstorming
	PhaseAwaitingSelection
	PhaseRefining
	PhaseAwaitingConfirmation
	PhaseCommitting
	PhaseComplete
)

// Decision represents a recorded decision for long-run mode
type Decision struct {
	Timestamp   time.Time `json:"timestamp"`
	SessionID   string    `json:"session_id"`
	Context     string    `json:"context"`
	Options     []string  `json:"options"`
	Selected    int       `json:"selected"`
	Reasoning   string    `json:"reasoning"`
	AutoDecided bool      `json:"auto_decided"`
}

// DecisionLog persists decisions for review
type DecisionLog struct {
	mu        sync.Mutex
	decisions []Decision
	sessionID string
}

// NewPlanningCoordinator creates a new planning coordinator
func NewPlanningCoordinator(
	cfg *config.PlanningConfig,
	todoTool *builtin.TodoTool,
	modelClient ModelClient,
) *PlanningCoordinator {
	threshold := cfg.ComplexityThreshold
	if threshold <= 0 {
		threshold = 0.6
	}

	return &PlanningCoordinator{
		config:           cfg,
		complexityDetect: &ComplexityDetector{Threshold: threshold},
		todoTool:         todoTool,
		modelClient:      modelClient,
		decisionLog:      &DecisionLog{},
	}
}

// SetSession updates the current session ID
func (p *PlanningCoordinator) SetSession(sessionID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.currentSession = sessionID
	if p.decisionLog != nil {
		p.decisionLog.sessionID = sessionID
	}
}

// AnalyzeComplexity checks if input warrants planning mode
func (p *PlanningCoordinator) AnalyzeComplexity(input string, ctx *AnalysisContext) *ComplexitySignal {
	if !p.config.Enabled {
		return &ComplexitySignal{
			Score:       0,
			Recommended: DirectExecution,
		}
	}
	return p.complexityDetect.Analyze(input, ctx)
}

// ShouldPlan returns true if planning mode is warranted for the input
func (p *PlanningCoordinator) ShouldPlan(input string, ctx *AnalysisContext) bool {
	signal := p.AnalyzeComplexity(input, ctx)
	return signal.IsComplex()
}

// StartBrainstorm initiates the brainstorming phase
func (p *PlanningCoordinator) StartBrainstorm(ctx context.Context, task string, taskContext map[string]any) (*PlanningResult, error) {
	p.mu.Lock()
	sessionID := p.currentSession
	p.mu.Unlock()

	if p.todoTool == nil {
		return nil, fmt.Errorf("todo tool not configured")
	}

	result, err := p.todoTool.Execute(map[string]any{
		"action":     "brainstorm",
		"session_id": sessionID,
		"task":       task,
		"context":    taskContext,
	})

	if err != nil {
		return &PlanningResult{Phase: PhaseBrainstorming, Error: err}, err
	}

	if !result.Success {
		err := fmt.Errorf("brainstorm failed: %s", result.Error)
		return &PlanningResult{Phase: PhaseBrainstorming, Error: err}, err
	}

	// Extract approaches from result
	approaches := extractApproaches(result.Data["approaches"])
	recommended := 0
	if rec, ok := result.Data["recommended"].(int); ok {
		recommended = rec
	} else if rec, ok := result.Data["recommended"].(float64); ok {
		recommended = int(rec)
	}
	reasoning := ""
	if r, ok := result.Data["reasoning"].(string); ok {
		reasoning = r
	}

	planResult := &PlanningResult{
		Phase:       PhaseAwaitingSelection,
		Approaches:  approaches,
		Recommended: recommended,
		Reasoning:   reasoning,
	}

	// In long-run mode, auto-select if there's a clear winner
	if p.config.LongRunEnabled && p.hasCleanWinner(approaches, recommended) {
		planResult.AutoDecided = true
		p.recordDecision(task, approaches, recommended, reasoning, true)

		// Automatically proceed to refine
		return p.ContinueWithApproach(ctx, task, recommended, approaches, "")
	}

	p.mu.Lock()
	p.pendingResult = planResult
	p.awaitingUser = true
	p.mu.Unlock()

	return planResult, nil
}

// ContinueWithApproach proceeds with the selected approach
func (p *PlanningCoordinator) ContinueWithApproach(
	ctx context.Context,
	task string,
	approachIndex int,
	approaches []builtin.Approach,
	adjustments string,
) (*PlanningResult, error) {
	p.mu.Lock()
	sessionID := p.currentSession
	p.awaitingUser = false
	p.mu.Unlock()

	// Record user's decision
	if !p.config.LongRunEnabled {
		p.recordDecision(task, approaches, approachIndex, "user selected", false)
	}

	// Refine the selected approach into TODOs
	result, err := p.todoTool.Execute(map[string]any{
		"action":         "refine",
		"session_id":     sessionID,
		"approach_index": float64(approachIndex),
		"approaches":     approaches,
		"adjustments":    adjustments,
	})

	if err != nil {
		return &PlanningResult{Phase: PhaseRefining, Error: err}, err
	}

	if !result.Success {
		err := fmt.Errorf("refine failed: %s", result.Error)
		return &PlanningResult{Phase: PhaseRefining, Error: err}, err
	}

	// Extract TODOs from result
	todos := extractTodos(result.Data["todos"])

	planResult := &PlanningResult{
		Phase:       PhaseAwaitingConfirmation,
		Approaches:  approaches,
		Recommended: approachIndex,
		Todos:       todos,
	}

	// In long-run mode, auto-commit
	if p.config.LongRunEnabled {
		return p.CommitTodos(ctx, todos)
	}

	p.mu.Lock()
	p.pendingResult = planResult
	p.awaitingUser = true
	p.mu.Unlock()

	return planResult, nil
}

// CommitTodos finalizes the TODOs
func (p *PlanningCoordinator) CommitTodos(ctx context.Context, todos []builtin.TodoInput) (*PlanningResult, error) {
	p.mu.Lock()
	sessionID := p.currentSession
	p.awaitingUser = false
	p.mu.Unlock()

	result, err := p.todoTool.Execute(map[string]any{
		"action":     "commit",
		"session_id": sessionID,
		"todos":      todos,
	})

	if err != nil {
		return &PlanningResult{Phase: PhaseCommitting, Error: err}, err
	}

	if !result.Success {
		err := fmt.Errorf("commit failed: %s", result.Error)
		return &PlanningResult{Phase: PhaseCommitting, Error: err}, err
	}

	planResult := &PlanningResult{
		Phase: PhaseComplete,
		Todos: todos,
	}

	p.mu.Lock()
	p.pendingResult = nil
	p.mu.Unlock()

	return planResult, nil
}

// IsAwaitingUser returns true if coordinator is waiting for user input
func (p *PlanningCoordinator) IsAwaitingUser() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.awaitingUser
}

// GetPendingResult returns the current pending result, if any
func (p *PlanningCoordinator) GetPendingResult() *PlanningResult {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.pendingResult
}

// GetDecisions returns all recorded decisions
func (p *PlanningCoordinator) GetDecisions() []Decision {
	if p.decisionLog == nil {
		return nil
	}
	p.decisionLog.mu.Lock()
	defer p.decisionLog.mu.Unlock()
	result := make([]Decision, len(p.decisionLog.decisions))
	copy(result, p.decisionLog.decisions)
	return result
}

// hasCleanWinner determines if there's a clear best approach
func (p *PlanningCoordinator) hasCleanWinner(approaches []builtin.Approach, recommended int) bool {
	if len(approaches) < 2 {
		return true
	}

	rec := approaches[recommended]

	// Low risk recommended approach is a clean winner
	if rec.Risk == "low" {
		return true
	}

	// Check if recommended has significantly fewer steps
	for i, a := range approaches {
		if i == recommended {
			continue
		}
		if a.Risk == "low" && rec.Risk != "low" {
			return false // Another option has lower risk
		}
		if a.Steps < rec.Steps && a.Risk != "high" {
			return false // Another option has fewer steps with acceptable risk
		}
	}

	return true
}

// recordDecision logs a decision
func (p *PlanningCoordinator) recordDecision(task string, approaches []builtin.Approach, selected int, reasoning string, auto bool) {
	if !p.config.LongRunLogDecisions || p.decisionLog == nil {
		return
	}

	options := make([]string, len(approaches))
	for i, a := range approaches {
		options[i] = a.Name
	}

	decision := Decision{
		Timestamp:   time.Now(),
		SessionID:   p.decisionLog.sessionID,
		Context:     task,
		Options:     options,
		Selected:    selected,
		Reasoning:   reasoning,
		AutoDecided: auto,
	}

	p.decisionLog.mu.Lock()
	p.decisionLog.decisions = append(p.decisionLog.decisions, decision)
	p.decisionLog.mu.Unlock()
}

// Helper functions

func extractApproaches(data any) []builtin.Approach {
	if data == nil {
		return nil
	}

	// Try direct type assertion
	if approaches, ok := data.([]builtin.Approach); ok {
		return approaches
	}

	// Try JSON round-trip for interface{} arrays
	jsonBytes, err := json.Marshal(data)
	if err != nil {
		return nil
	}

	var approaches []builtin.Approach
	if err := json.Unmarshal(jsonBytes, &approaches); err != nil {
		return nil
	}

	return approaches
}

func extractTodos(data any) []builtin.TodoInput {
	if data == nil {
		return nil
	}

	// Try direct type assertion
	if todos, ok := data.([]builtin.TodoInput); ok {
		return todos
	}

	// Try JSON round-trip
	jsonBytes, err := json.Marshal(data)
	if err != nil {
		return nil
	}

	var todos []builtin.TodoInput
	if err := json.Unmarshal(jsonBytes, &todos); err != nil {
		return nil
	}

	return todos
}

// FormatApproachesDisplay formats approaches for user display
func FormatApproachesDisplay(approaches []builtin.Approach, recommended int, reasoning string) string {
	if len(approaches) == 0 {
		return "No approaches generated"
	}

	var result string
	result = "📋 **Planning Analysis**\n\n"

	for i, a := range approaches {
		marker := "  "
		if i == recommended {
			marker = "→ "
		}
		result += fmt.Sprintf("%s**%d. %s** (%d steps, %s risk)\n",
			marker, i+1, a.Name, a.Steps, a.Risk)
		result += fmt.Sprintf("   %s\n", a.Description)
		if len(a.Tradeoffs) > 0 {
			for _, t := range a.Tradeoffs {
				result += fmt.Sprintf("   • %s\n", t)
			}
		}
		result += "\n"
	}

	result += fmt.Sprintf("**Recommendation:** %s\n", approaches[recommended].Name)
	result += fmt.Sprintf("**Reasoning:** %s\n", reasoning)
	result += "\nReply with a number (1-" + fmt.Sprintf("%d", len(approaches)) + ") to select an approach, or describe adjustments."

	return result
}

// FormatTodosDisplay formats TODOs for user confirmation
func FormatTodosDisplay(todos []builtin.TodoInput, approachName string) string {
	if len(todos) == 0 {
		return "No TODOs generated"
	}

	result := fmt.Sprintf("📝 **Generated TODOs for \"%s\"**\n\n", approachName)

	for i, t := range todos {
		result += fmt.Sprintf("%d. %s\n", i+1, t.Content)
	}

	result += "\nReply 'yes' to commit these TODOs, or describe changes."

	return result
}
