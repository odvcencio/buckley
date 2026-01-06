package artifact

import "time"

// ArtifactType represents the type of artifact being generated
type ArtifactType string

const (
	ArtifactTypePlanning  ArtifactType = "planning"
	ArtifactTypeExecution ArtifactType = "execution"
	ArtifactTypeReview    ArtifactType = "review"
)

// Phase represents the current workflow phase
type Phase string

const (
	PhasePlanning  Phase = "planning"
	PhaseExecution Phase = "execution"
	PhaseReview    Phase = "review"
)

// Artifact represents a base artifact with common fields
type Artifact struct {
	Type      ArtifactType
	Feature   string    // Feature name (e.g., "user-auth")
	FilePath  string    // Full path to artifact file
	CreatedAt time.Time // When artifact was created
	UpdatedAt time.Time // When artifact was last updated
	Status    string    // "in_progress", "completed", "archived"
}

// PlanningArtifact represents a planning phase artifact
type PlanningArtifact struct {
	Artifact
	Context           ContextSection
	Decisions         []ArchitectureDecision
	CodeContracts     []CodeContract
	LayerMap          LayerMap
	Tasks             []TaskBreakdown
	CrossCuttingScope CrossCuttingConcerns
}

// ContextSection captures the codebase context analyzed during planning
type ContextSection struct {
	ExistingPatterns  []string  // Patterns detected in codebase
	ArchitectureStyle string    // "DDD", "Pragmatic CRUD", "Layered", etc.
	RelevantFiles     []string  // Files analyzed for context
	UserGoal          string    // User's stated goal
	ResearchSummary   string    // Condensed research brief summary
	ResearchRisks     []string  // Top risks discovered during research
	ResearchLogPath   string    // Path to research log file
	ResearchLoggedAt  time.Time // Timestamp of research brief generation
}

// ArchitectureDecision represents an ADR embedded in the planning artifact
type ArchitectureDecision struct {
	Title        string   // Decision title
	Alternatives []string // Alternatives considered
	Rationale    string   // Why this choice was made
	TradeOffs    []string // Trade-offs of this decision
	LayerImpact  []string // Which layers are impacted
}

// CodeContract represents interface definitions and type signatures
type CodeContract struct {
	Layer       string // "domain", "application", "infrastructure", "interface"
	FilePath    string // Where this contract lives
	Code        string // Go code for interface/type
	Description string // What this contract does
}

// LayerMap shows the file → layer → dependencies mapping
type LayerMap struct {
	Layers []Layer
}

// Layer represents a single architectural layer
type Layer struct {
	Name         string   // "Domain", "Application", "Infrastructure", "Interface"
	Files        []string // Files in this layer
	Dependencies []string // Layers this depends on
}

// TaskBreakdown represents a single task with implementation guidance
type TaskBreakdown struct {
	ID              int      // Task number
	Description     string   // What to implement
	FilePath        string   // File to create/modify
	Pseudocode      string   // Algorithmic approach
	Complexity      string   // Time/space complexity notes
	Maintainability string   // Maintainability assessment
	Dependencies    []int    // Task IDs this depends on
	Verification    []string // How to verify this task
}

// CrossCuttingConcerns captures system-wide concerns
type CrossCuttingConcerns struct {
	ErrorHandling string   // Error handling strategy
	Logging       string   // Logging approach
	Testing       string   // Testing requirements
	Security      []string // Security considerations
}

// ExecutionArtifact tracks implementation progress
type ExecutionArtifact struct {
	Artifact
	PlanningArtifactPath string
	StartedAt            time.Time
	CurrentTask          int // Which task is in progress
	TotalTasks           int
	ProgressLog          []TaskProgress
	Pauses               []ExecutionPause
	DeviationSummary     []Deviation
	ReviewChecklist      []string // High-risk areas for review
}

// TaskProgress represents progress on a single task
type TaskProgress struct {
	TaskID              int
	Description         string
	Status              string // "pending", "in_progress", "completed", "failed"
	StartedAt           time.Time
	CompletedAt         *time.Time
	Duration            string
	FilesModified       []FileModification
	ImplementationNotes string
	Deviations          []Deviation
	TestsAdded          []TestResult
	CodeSnippet         string
}

// FileModification tracks changes to a file
type FileModification struct {
	Path          string
	LinesAdded    int
	LinesDeleted  int
	LinesModified int
}

// Deviation tracks deviations from the plan
type Deviation struct {
	TaskID      int
	Type        string // "Added", "Changed", "Removed"
	Description string
	Rationale   string
	Impact      string // "Low", "Medium", "High"
}

// TestResult captures test execution results
type TestResult struct {
	Name     string
	Status   string // "pass", "fail"
	Coverage float64
}

// ExecutionPause represents when execution paused for user input
type ExecutionPause struct {
	Number       int
	TaskID       int
	Reason       string // "Business Logic Ambiguity", "Architectural Conflict", etc.
	Question     string
	UserResponse string
	Resolution   string
	Timestamp    time.Time
}

// ReviewArtifact captures review validation and results
type ReviewArtifact struct {
	Artifact
	PlanningArtifactPath      string
	ExecutionArtifactPath     string
	ReviewedAt                time.Time
	ReviewerModel             string
	Status                    string // "in_progress", "changes_requested", "approved"
	ValidationStrategy        ValidationStrategy
	ValidationResults         []ValidationResult
	IssuesFound               []Issue
	Iterations                []ReviewIteration
	OpportunisticImprovements []Improvement
	Approval                  *Approval
}

// ValidationStrategy describes how review will validate the implementation
type ValidationStrategy struct {
	CriticalPath  []string // Ordered list of validation priorities
	HighRiskAreas []string // Areas flagged from execution
}

// ValidationResult represents results for a validation category
type ValidationResult struct {
	Category string // "Security", "Correctness", "Conventions", etc.
	Status   string // "pass", "fail", "concern"
	Checks   []ValidationCheck
}

// ValidationCheck is an individual validation item
type ValidationCheck struct {
	Name        string
	Status      string // "pass", "fail", "concern"
	Description string
	Issue       *Issue // If check failed or raised concern
}

// Issue represents a problem found during review
type Issue struct {
	ID          int
	Severity    string // "critical", "quality", "nit"
	Category    string // "Security", "Correctness", etc.
	Title       string
	Description string
	Location    string // File and line number
	Fix         string // Suggested fix
}

// ReviewIteration tracks review cycles
type ReviewIteration struct {
	Number      int
	Timestamp   time.Time
	IssuesFound int
	Status      string // "changes_requested", "approved"
	Notes       string
}

// Improvement represents opportunistic improvements found during review
type Improvement struct {
	Category    string // "Codebase Quality", "Architecture", "Performance", "Documentation"
	Title       string
	Observation string
	Suggestion  string
	Impact      string // Effort and benefit assessment
	Files       []string
}

// Approval represents final review approval
type Approval struct {
	Status        string // "approved", "approved_with_nits"
	Timestamp     time.Time
	RemainingWork []string // Nits deferred to future
	ReadyForPR    bool
	Summary       string
}

// CompactionResult represents the result of artifact compaction
type CompactionResult struct {
	OriginalTokens    int
	CompactedTokens   int
	ReductionPercent  float64
	TasksCompacted    int
	CommandsPreserved int
	Model             string
	Duration          time.Duration
}

// CommandLog represents a preserved command from execution
type CommandLog struct {
	Timestamp time.Time
	Tool      string
	Args      map[string]any
	Result    string
	Impact    string // "readonly", "modifying", "destructive"
}
