package experiment

import "time"

// Experiment groups variants for a single comparison run.
type Experiment struct {
	ID          string
	Name        string
	Description string
	Hypothesis  string
	Task        Task
	Variants    []Variant
	Criteria    []SuccessCriterion
	Status      ExperimentStatus
	CreatedAt   time.Time
	CompletedAt *time.Time
}

// Task describes what each variant should execute.
type Task struct {
	Prompt     string
	Context    map[string]string
	WorkingDir string
	Timeout    time.Duration
	Files      []string // Explicit file paths for scope conflict detection
	Scope      []string // Glob patterns for scope conflict detection (e.g., "pkg/auth/...")
}

// Variant describes a model configuration to test.
type Variant struct {
	ID           string
	Name         string
	ModelID      string
	ProviderID   string
	SystemPrompt *string
	Temperature  *float64
	MaxTokens    *int
	ToolsAllowed []string
	CustomConfig map[string]any
	Files        []string // Override task-level file scope for this variant
	Scope        []string // Override task-level glob scope for this variant
}

// Run captures a single execution of a variant.
type Run struct {
	ID           string
	ExperimentID string
	VariantID    string
	SessionID    string
	Branch       string
	Status       RunStatus
	Output       string
	Files        []string
	Metrics      RunMetrics
	Error        *string
	StartedAt    time.Time
	CompletedAt  *time.Time
}

// RunMetrics captures measurable outcomes.
type RunMetrics struct {
	DurationMs       int64
	PromptTokens     int
	CompletionTokens int
	TotalCost        float64
	ToolCalls        int
	ToolSuccesses    int
	ToolFailures     int
	FilesModified    int
	LinesChanged     int
}

// SuccessCriterion defines how to evaluate a run.
type SuccessCriterion struct {
	ID     int64
	Name   string
	Type   CriterionType
	Target string
	Weight float64
}

// CriterionEvaluation records evaluation results for a run.
type CriterionEvaluation struct {
	ID          int64
	RunID       string
	CriterionID int64
	Passed      bool
	Score       float64
	Details     string
	EvaluatedAt time.Time
}

// ExperimentStatus captures lifecycle state for an experiment.
type ExperimentStatus string

const (
	ExperimentPending   ExperimentStatus = "pending"
	ExperimentRunning   ExperimentStatus = "running"
	ExperimentCompleted ExperimentStatus = "completed"
	ExperimentFailed    ExperimentStatus = "failed"
	ExperimentCancelled ExperimentStatus = "cancelled"
)

// RunStatus captures lifecycle state for a run.
type RunStatus string

const (
	RunPending   RunStatus = "pending"
	RunRunning   RunStatus = "running"
	RunCompleted RunStatus = "completed"
	RunFailed    RunStatus = "failed"
	RunCancelled RunStatus = "cancelled"
)

// CriterionType defines supported evaluation types.
type CriterionType string

const (
	CriterionTestPass   CriterionType = "test_pass"
	CriterionFileExists CriterionType = "file_exists"
	CriterionContains   CriterionType = "contains"
	CriterionCommand    CriterionType = "command"
	CriterionManual     CriterionType = "manual"
)
