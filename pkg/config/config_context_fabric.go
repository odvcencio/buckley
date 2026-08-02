package config

import "time"

// ContextFabricConfig controls the Canopy-selected, LX-rendered context
// compilation pipeline described by the M31 Context Fabric spec. Every field
// defaults to the spec's initial baseline (feature disabled) and no runtime
// code reads this struct yet; it is scaffolding only.
type ContextFabricConfig struct {
	Enabled         bool                          `yaml:"enabled" env:"BUCKLEY_CONTEXT_FABRIC_ENABLED"`
	Shadow          bool                          `yaml:"shadow" env:"BUCKLEY_CONTEXT_FABRIC_SHADOW"`
	PolicyVersion   string                        `yaml:"policy_version"`
	Renderer        string                        `yaml:"renderer"`
	OutputFormat    string                        `yaml:"output_format"`
	BudgetTolerance float64                       `yaml:"budget_tolerance"`
	ReceiptReuse    bool                          `yaml:"receipt_reuse"`
	AutoExpand      bool                          `yaml:"auto_expand"`
	RemoteInputs    bool                          `yaml:"remote_inputs"`
	Pressure        ContextFabricPressureConfig   `yaml:"pressure"`
	Evidence        ContextFabricEvidenceConfig   `yaml:"evidence"`
	Checkpoint      ContextFabricCheckpointConfig `yaml:"checkpoint"`
}

// ContextFabricPressureConfig sets the context-window pressure thresholds
// that gate dedupe, checkpoint, compaction, and emergency behavior.
type ContextFabricPressureConfig struct {
	Dedupe     float64 `yaml:"dedupe"`
	Checkpoint float64 `yaml:"checkpoint"`
	Compact    float64 `yaml:"compact"`
	Emergency  float64 `yaml:"emergency"`
}

// ContextFabricEvidenceConfig controls the durable evidence store backing
// context receipts.
type ContextFabricEvidenceConfig struct {
	StoreEnabled  bool `yaml:"store_enabled"`
	InlineBytes   int  `yaml:"inline_bytes"`
	RetentionDays int  `yaml:"retention_days"`
	Compress      bool `yaml:"compress"`
}

// ContextFabricCheckpointConfig controls automatic task checkpointing under
// context pressure.
type ContextFabricCheckpointConfig struct {
	Enabled     bool `yaml:"enabled"`
	ModelPolish bool `yaml:"model_polish"`
}

// AgentControllerConfig selects the next-action policy that drives the
// durable agent runtime.
type AgentControllerConfig struct {
	Mode          string                             `yaml:"mode" env:"BUCKLEY_AGENT_CONTROLLER_MODE"` // legacy | shadow | dynamic
	PolicyVersion string                             `yaml:"policy_version"`
	Critic        AgentControllerCriticConfig        `yaml:"critic"`
	EmergencyFuse AgentControllerEmergencyFuseConfig `yaml:"emergency_fuse"`
}

// AgentControllerCriticConfig configures the optional cheap-model critic used
// on ambiguity or a failed strategy.
type AgentControllerCriticConfig struct {
	Enabled             bool    `yaml:"enabled"`
	Model               string  `yaml:"model"`
	ConfidenceThreshold float64 `yaml:"confidence_threshold"`
}

// AgentControllerEmergencyFuseConfig sets the distant emergency fuses that
// bound a run regardless of the active controller policy.
type AgentControllerEmergencyFuseConfig struct {
	ModelRequests  int           `yaml:"model_requests"`
	ToolExecutions int           `yaml:"tool_executions"`
	WallTime       time.Duration `yaml:"wall_time"`
}

// AgentOperationsConfig gates high-impact side effects the interactive agent
// may take, per the no-implicit-side-effect design decision.
type AgentOperationsConfig struct {
	ReviewChanges     bool `yaml:"review_changes"`
	ReviewPullRequest bool `yaml:"review_pull_request"`
	PostReview        bool `yaml:"post_review"`
	PrepareCommit     bool `yaml:"prepare_commit"`
	CommitChanges     bool `yaml:"commit_changes"`
	PushChanges       bool `yaml:"push_changes"`
}

// MetricsConfig controls local-first measurement and optional aggregate
// export. Raw source and transcripts stay local by default.
type MetricsConfig struct {
	Enabled           bool `yaml:"enabled" env:"BUCKLEY_METRICS_ENABLED"`
	Export            bool `yaml:"export" env:"BUCKLEY_METRICS_EXPORT"`
	IncludeRawContent bool `yaml:"include_raw_content"`
}
