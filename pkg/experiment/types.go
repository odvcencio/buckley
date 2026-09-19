package experiment

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/transparency"
)

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
	ID            string
	ExperimentID  string
	VariantID     string
	SessionID     string
	Branch        string
	Status        RunStatus
	Output        string
	Files         []string
	Metrics       RunMetrics
	Error         *string
	StartedAt     time.Time
	CompletedAt   *time.Time
	InputManifest *RunInputManifest
	// ModelExecutions records observed model-response identities for this run.
	// Empty means execution identity evidence was unavailable; requested input
	// provenance remains in InputManifest.
	ModelExecutions []model.ExecutionIdentity
}

// RunInputManifest freezes the requested inputs for a run. It is an input
// provenance record only: it does not prove the actual provider, backend
// revision, tool versions, or model release that executed the task.
type RunInputManifest struct {
	Version        string                 `json:"version"`
	InputDigest    string                 `json:"input_digest"`
	WorkloadDigest string                 `json:"workload_digest"`
	Task           RunManifestTask        `json:"task"`
	Variant        RunManifestVariant     `json:"variant"`
	Criteria       []RunManifestCriterion `json:"criteria"`
}

type RunManifestTask struct {
	PromptHash         string `json:"prompt_hash"`
	ContextHash        string `json:"context_hash"`
	WorkingDir         string `json:"working_dir,omitempty"`
	EffectiveTimeoutMs int64  `json:"effective_timeout_ms"`
	FilesHash          string `json:"files_hash"`
	ScopeHash          string `json:"scope_hash"`
}

type RunManifestVariant struct {
	ID                  string   `json:"id,omitempty"`
	Name                string   `json:"name"`
	RequestedModelID    string   `json:"requested_model_id"`
	RequestedProviderID string   `json:"requested_provider_id,omitempty"`
	SystemPromptHash    string   `json:"system_prompt_hash"`
	Temperature         *float64 `json:"temperature,omitempty"`
	MaxTokens           *int     `json:"max_tokens,omitempty"`
	ToolsAllowedHash    string   `json:"tools_allowed_hash"`
	CustomConfigHash    string   `json:"custom_config_hash"`
	FilesHash           string   `json:"files_hash"`
	ScopeHash           string   `json:"scope_hash"`
}

type RunManifestCriterion struct {
	ID         int64         `json:"id,omitempty"`
	Name       string        `json:"name"`
	Type       CriterionType `json:"type"`
	Weight     float64       `json:"weight"`
	TargetHash string        `json:"target_hash"`
}

// RunMetrics captures measurable outcomes.
type RunMetrics struct {
	DurationMs       int64
	PromptTokens     int
	CompletionTokens int
	TotalCost        float64
	Usage            *transparency.TokenUsage `json:"Usage,omitempty"`
	CostUnknown      bool                     `json:"CostUnknown,omitempty"`
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

	// targetKnownNonempty is supplied only by validated redacted-input
	// projections, never by task configuration.
	targetKnownNonempty bool
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

const runInputManifestVersion = "experiment-run-input-v1"

const maxExecutionIdentityFieldRunes = 2048

func cloneModelExecutions(input []model.ExecutionIdentity) []model.ExecutionIdentity {
	if input == nil {
		return nil
	}
	out := make([]model.ExecutionIdentity, len(input))
	copy(out, input)
	return out
}

func validateModelExecutions(identities []model.ExecutionIdentity) error {
	for i, identity := range identities {
		for _, field := range []struct {
			name  string
			value string
		}{
			{"requested_model", identity.RequestedModel},
			{"selected_model", identity.SelectedModel},
			{"provider_id", identity.ProviderID},
			{"response_model", identity.ResponseModel},
			{"response_id", identity.ResponseID},
		} {
			if err := validateExecutionIdentityField(i, field.name, field.value); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateExecutionIdentityField(index int, name, value string) error {
	if value == "" {
		return nil
	}
	if strings.TrimSpace(value) != value {
		return fmt.Errorf("model execution identity %d %s has leading or trailing whitespace", index, name)
	}
	if len([]rune(value)) > maxExecutionIdentityFieldRunes {
		return fmt.Errorf("model execution identity %d %s is too long", index, name)
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("model execution identity %d %s contains control characters", index, name)
		}
	}
	return nil
}

func buildRunInputManifest(exp *Experiment, variant Variant, effectiveTimeout time.Duration) (*RunInputManifest, error) {
	if exp == nil {
		return nil, fmt.Errorf("experiment is nil")
	}
	if effectiveTimeout < 0 {
		effectiveTimeout = 0
	}

	files := variant.Files
	if len(files) == 0 {
		files = exp.Task.Files
	}
	scope := variant.Scope
	if len(scope) == 0 {
		scope = exp.Task.Scope
	}
	systemPrompt := ""
	if variant.SystemPrompt != nil {
		systemPrompt = strings.TrimSpace(*variant.SystemPrompt)
	}

	criteria := make([]RunManifestCriterion, 0, len(exp.Criteria))
	for _, criterion := range exp.Criteria {
		weight := criterion.Weight
		if weight <= 0 {
			weight = 1
		}
		criteria = append(criteria, RunManifestCriterion{
			ID:         criterion.ID,
			Name:       criterion.Name,
			Type:       criterion.Type,
			Weight:     weight,
			TargetHash: hashText(criterion.Target),
		})
	}

	customConfig, err := canonicalAnyMap(variant.CustomConfig)
	if err != nil {
		return nil, fmt.Errorf("custom config is not JSON-serializable: %w", err)
	}
	contextHash, err := hashCanonical(normalizeStringMap(exp.Task.Context))
	if err != nil {
		return nil, fmt.Errorf("hash task context: %w", err)
	}
	taskFilesHash, err := hashCanonical(normalizeStringSlice(exp.Task.Files))
	if err != nil {
		return nil, fmt.Errorf("hash task files: %w", err)
	}
	taskScopeHash, err := hashCanonical(normalizeStringSlice(exp.Task.Scope))
	if err != nil {
		return nil, fmt.Errorf("hash task scope: %w", err)
	}
	toolsHash, err := hashCanonical(normalizeStringSlice(variant.ToolsAllowed))
	if err != nil {
		return nil, fmt.Errorf("hash requested tools: %w", err)
	}
	customConfigHash, err := hashCanonical(customConfig)
	if err != nil {
		return nil, fmt.Errorf("hash custom config: %w", err)
	}
	variantFilesHash, err := hashCanonical(normalizeStringSlice(files))
	if err != nil {
		return nil, fmt.Errorf("hash requested files: %w", err)
	}
	variantScopeHash, err := hashCanonical(normalizeStringSlice(scope))
	if err != nil {
		return nil, fmt.Errorf("hash requested scope: %w", err)
	}
	manifest := &RunInputManifest{
		Version: runInputManifestVersion,
		Task: RunManifestTask{
			PromptHash:         hashText(exp.Task.Prompt),
			ContextHash:        contextHash,
			WorkingDir:         exp.Task.WorkingDir,
			EffectiveTimeoutMs: effectiveTimeout.Milliseconds(),
			FilesHash:          taskFilesHash,
			ScopeHash:          taskScopeHash,
		},
		Variant: RunManifestVariant{
			ID:                  variant.ID,
			Name:                variantName(&variant),
			RequestedModelID:    variant.ModelID,
			RequestedProviderID: variant.ProviderID,
			SystemPromptHash:    hashText(systemPrompt),
			Temperature:         copyFloatPtr(variant.Temperature),
			MaxTokens:           copyIntPtr(variant.MaxTokens),
			ToolsAllowedHash:    toolsHash,
			CustomConfigHash:    customConfigHash,
			FilesHash:           variantFilesHash,
			ScopeHash:           variantScopeHash,
		},
		Criteria: criteria,
	}
	manifest.WorkloadDigest, err = manifest.workloadDigest()
	if err != nil {
		return nil, err
	}
	manifest.InputDigest, err = manifest.inputDigest()
	if err != nil {
		return nil, err
	}
	return manifest, nil
}

func (m *RunInputManifest) Validate() error {
	if m == nil {
		return nil
	}
	if m.Version != runInputManifestVersion {
		return fmt.Errorf("unsupported run input manifest version %q", m.Version)
	}
	wantWorkload, err := m.workloadDigest()
	if err != nil {
		return err
	}
	if m.WorkloadDigest != wantWorkload {
		return fmt.Errorf("run input manifest workload digest mismatch")
	}
	wantInput, err := m.inputDigest()
	if err != nil {
		return err
	}
	if m.InputDigest != wantInput {
		return fmt.Errorf("run input manifest digest mismatch")
	}
	return nil
}

func (m *RunInputManifest) criteriaDefinitions() []SuccessCriterion {
	if m == nil {
		return nil
	}
	out := make([]SuccessCriterion, 0, len(m.Criteria))
	for _, criterion := range m.Criteria {
		digest, err := hex.DecodeString(criterion.TargetHash)
		out = append(out, SuccessCriterion{
			targetKnownNonempty: err == nil && len(digest) == sha256.Size && !strings.EqualFold(criterion.TargetHash, hashText("")),
			ID:                  criterion.ID,
			Name:                criterion.Name,
			Type:                criterion.Type,
			Weight:              criterion.Weight,
		})
	}
	return out
}

func (m *RunInputManifest) workloadDigest() (string, error) {
	if m == nil {
		return "", nil
	}
	payload := struct {
		Task     RunManifestTask        `json:"task"`
		Criteria []RunManifestCriterion `json:"criteria"`
	}{
		Task:     m.Task,
		Criteria: criteriaWithoutStorageIDs(m.Criteria),
	}
	return hashCanonical(payload)
}

func (m *RunInputManifest) inputDigest() (string, error) {
	if m == nil {
		return "", nil
	}
	payload := struct {
		Version        string                 `json:"version"`
		WorkloadDigest string                 `json:"workload_digest"`
		Task           RunManifestTask        `json:"task"`
		Variant        RunManifestVariant     `json:"variant"`
		Criteria       []RunManifestCriterion `json:"criteria"`
	}{
		Version:        m.Version,
		WorkloadDigest: m.WorkloadDigest,
		Task:           m.Task,
		Variant:        variantWithoutStorageID(m.Variant),
		Criteria:       criteriaWithoutStorageIDs(m.Criteria),
	}
	return hashCanonical(payload)
}

func criteriaWithoutStorageIDs(criteria []RunManifestCriterion) []RunManifestCriterion {
	out := make([]RunManifestCriterion, 0, len(criteria))
	for _, criterion := range criteria {
		criterion.ID = 0
		out = append(out, criterion)
	}
	return out
}

func variantWithoutStorageID(variant RunManifestVariant) RunManifestVariant {
	variant.ID = ""
	return variant
}

func hashText(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func hashCanonical(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return hashText(string(data)), nil
}

func normalizeStringMap(input map[string]string) map[string]string {
	out := make(map[string]string, len(input))
	for k, v := range input {
		out[k] = v
	}
	return out
}

func canonicalAnyMap(input map[string]any) (map[string]any, error) {
	if len(input) == 0 {
		return map[string]any{}, nil
	}
	data, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&out); err != nil {
		return nil, err
	}
	if out == nil {
		out = map[string]any{}
	}
	return out, nil
}

func normalizeStringSlice(input []string) []string {
	out := append([]string(nil), input...)
	sort.Strings(out)
	return out
}

func copyFloatPtr(value *float64) *float64 {
	if value == nil {
		return nil
	}
	out := *value
	return &out
}

func copyIntPtr(value *int) *int {
	if value == nil {
		return nil
	}
	out := *value
	return &out
}
