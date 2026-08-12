package protocol

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	artifactv1 "m31labs.dev/buckley/pkg/artifact/v1"
	"m31labs.dev/buckley/pkg/types"
)

// TaskRequest contains task facts relevant to execution shape. It deliberately
// excludes raw prompts and source content so protocol receipts are safe to
// persist or inspect.
type TaskRequest struct {
	TaskID            string   `json:"task_id,omitempty"`
	Phase             string   `json:"phase"`
	TaskClass         string   `json:"task_class"`
	Complexity        int      `json:"complexity"`
	Risk              string   `json:"risk"`
	Parallelizable    bool     `json:"parallelizable"`
	NeedsArtifact     bool     `json:"needs_artifact"`
	BudgetUtilization float64  `json:"budget_utilization"`
	LatencyBudgetMS   int64    `json:"latency_budget_ms,omitempty"`
	CandidateTools    []string `json:"candidate_tools,omitempty"`
}

// CompilerConfig controls a rollout without changing policy source. Mode is
// intentionally explicit: legacy produces a conservative one-stage protocol,
// shadow emits a receipt without asking hosts to apply it, and dynamic permits
// the Arbiter-selected protocol to drive a host.
type CompilerConfig struct {
	Mode            string
	PolicyVersion   string
	MaxFanout       int
	AutoCodeMode    bool
	DefaultToolList []string
}

const (
	ModeLegacy  = "legacy"
	ModeShadow  = "shadow"
	ModeDynamic = "dynamic"
)

// Compiler owns only protocol compilation. It reads a RuleEvaluator port so
// policy remains replaceable and auditable rather than becoming nested Go
// conditionals.
type Compiler struct {
	evaluator types.RuleEvaluator
	config    CompilerConfig
}

func NewCompiler(evaluator types.RuleEvaluator, config CompilerConfig) *Compiler {
	if strings.TrimSpace(config.Mode) == "" {
		config.Mode = ModeLegacy
	}
	config.Mode = strings.ToLower(strings.TrimSpace(config.Mode))
	if config.Mode != ModeLegacy && config.Mode != ModeShadow && config.Mode != ModeDynamic {
		config.Mode = ModeLegacy
	}
	if strings.TrimSpace(config.PolicyVersion) == "" {
		config.PolicyVersion = "adaptive-protocol-v1"
	}
	if config.MaxFanout <= 0 {
		config.MaxFanout = 4
	}
	config.DefaultToolList = normalizedTools(config.DefaultToolList)
	return &Compiler{evaluator: evaluator, config: config}
}

// Protocol is the cross-surface execution plan. Hosts may display it in the
// TUI or ACP, pass its narrowed tool list to a child, and persist its receipt
// next to the run that applied it.
type Protocol struct {
	SchemaVersion string                    `json:"schema_version"`
	ProtocolID    string                    `json:"protocol_id"`
	Mode          string                    `json:"mode"`
	ModelID       string                    `json:"model_id"`
	ProfileClass  ModelClass                `json:"profile_class"`
	VisibleTools  []string                  `json:"visible_tools,omitempty"`
	Stages        []Stage                   `json:"stages"`
	Output        artifactv1.OutputContract `json:"output"`
	Receipt       Receipt                   `json:"receipt"`
}

// Stage is a bounded unit of work within a protocol.
type Stage struct {
	Name                 string `json:"name"`
	Role                 string `json:"role"`
	MaxTurns             int    `json:"max_turns"`
	MaxFanout            int    `json:"max_fanout"`
	ContextSource        string `json:"context_source"`
	CodeMode             string `json:"code_mode"`
	Continuation         bool   `json:"continuation"`
	VerificationDepth    string `json:"verification_depth"`
	ArchitectEditorSplit bool   `json:"architect_editor_split"`
}

// Receipt makes compilation explainable and replayable. There is no timestamp
// here by design: the ID is deterministic for pinned facts, policy, and
// configuration.
type Receipt struct {
	CompilerVersion string         `json:"compiler_version"`
	PolicyVersion   string         `json:"policy_version"`
	PolicySource    string         `json:"policy_source"`
	PolicyOutcome   string         `json:"policy_outcome"`
	ProfileVersion  string         `json:"profile_version"`
	ProfileDigest   string         `json:"profile_digest"`
	Facts           map[string]any `json:"facts"`
	FactsDigest     string         `json:"facts_digest"`
}

// Compile resolves exactly one protocol from a profile and task. A failed
// Arbiter evaluation has an explicit conservative fallback rather than hidden
// behavior; its use is recorded in Receipt.PolicySource.
func (c *Compiler) Compile(request TaskRequest, profile BehaviorProfile) (Protocol, error) {
	if c == nil {
		return Protocol{}, fmt.Errorf("protocol compiler is unavailable")
	}
	profile = profile.Normalize()
	if err := profile.Validate(); err != nil {
		return Protocol{}, err
	}
	request, err := normalizeRequest(request)
	if err != nil {
		return Protocol{}, err
	}
	profileDigest, err := profile.Digest()
	if err != nil {
		return Protocol{}, err
	}
	class := profile.ResolvedClass()
	facts := protocolFacts(request, profile, class, c.config)
	choice, source, err := c.resolveChoice(facts, class)
	if err != nil {
		return Protocol{}, err
	}
	if c.config.Mode == ModeLegacy {
		choice = conservativeChoice()
		source = "legacy"
	}
	choice = clampChoice(choice, request, profile, c.config)
	tools := selectTools(request.CandidateTools, c.config.DefaultToolList, choice.ToolOrder, choice.VisibleToolCount)
	output := artifactv1.NegotiatedOutputDescriptor(artifactv1.ProviderCapabilities{
		NativeJSONSchema: profile.Capabilities.NativeJSONSchema,
		ToolCalls:        profile.Capabilities.ToolCalls,
	})
	if !request.NeedsArtifact {
		// Protocols still publish a contract descriptor so surfaces can show
		// the selected fallback; callers simply need not demand a terminal
		// artifact for a conversational task.
		output.Prompt = "Artifact output is optional for this task. " + output.Prompt
	}
	stage := Stage{
		Name:                 "execute",
		Role:                 "agent",
		MaxTurns:             choice.MaxTurns,
		MaxFanout:            choice.MaxFanout,
		ContextSource:        choice.ContextSource,
		CodeMode:             choice.CodeMode,
		Continuation:         choice.Continuation,
		VerificationDepth:    choice.VerificationDepth,
		ArchitectEditorSplit: choice.ArchitectEditorSplit,
	}
	stages := []Stage{stage}
	if choice.ArchitectEditorSplit {
		stages = []Stage{
			{
				Name:                 "architect",
				Role:                 "architect",
				MaxTurns:             maxProtocol(1, choice.MaxTurns/3),
				MaxFanout:            1,
				ContextSource:        choice.ContextSource,
				CodeMode:             "off",
				VerificationDepth:    "plan",
				ArchitectEditorSplit: true,
			},
			stage,
		}
		stages[1].Role = "editor"
	}
	receipt := Receipt{
		CompilerVersion: "buckley.protocol-compiler/v1",
		PolicyVersion:   c.config.PolicyVersion,
		PolicySource:    source,
		PolicyOutcome:   choice.Name,
		ProfileVersion:  profile.Version,
		ProfileDigest:   profileDigest,
		Facts:           cloneFacts(facts),
	}
	receipt.FactsDigest, err = digestValue(receipt.Facts)
	if err != nil {
		return Protocol{}, err
	}
	protocol := Protocol{
		SchemaVersion: ProtocolSchemaVersion,
		Mode:          c.config.Mode,
		ModelID:       profile.ModelID,
		ProfileClass:  class,
		VisibleTools:  tools,
		Stages:        stages,
		Output:        output,
		Receipt:       receipt,
	}
	protocol.ProtocolID, err = protocolID(protocol)
	if err != nil {
		return Protocol{}, err
	}
	return protocol, nil
}

type protocolChoice struct {
	Name                 string
	VisibleToolCount     int
	ToolOrder            []string
	MaxTurns             int
	MaxFanout            int
	ContextSource        string
	CodeMode             string
	Continuation         bool
	VerificationDepth    string
	ArchitectEditorSplit bool
}

func (c *Compiler) resolveChoice(facts map[string]any, class ModelClass) (protocolChoice, string, error) {
	if c.evaluator == nil {
		return fallbackChoice(class), "fallback_no_policy", nil
	}
	result, err := c.evaluator.EvalStrategy("runtime/protocol", "compile", facts)
	if err != nil {
		return fallbackChoice(class), "fallback_policy_error", nil
	}
	name := firstNonEmpty(strings.TrimSpace(result.String("name")), "arbiter")
	choice := protocolChoice{
		Name:                 name,
		VisibleToolCount:     result.Int("visible_tool_count"),
		ToolOrder:            toolOrderForOutcome(name),
		MaxTurns:             result.Int("max_turns"),
		MaxFanout:            result.Int("max_fanout"),
		ContextSource:        firstNonEmpty(strings.TrimSpace(result.String("context_source")), "canopy_ranked"),
		CodeMode:             firstNonEmpty(strings.TrimSpace(result.String("code_mode")), "suggest"),
		Continuation:         result.Bool("continuation"),
		VerificationDepth:    firstNonEmpty(strings.TrimSpace(result.String("verification_depth")), "focused"),
		ArchitectEditorSplit: result.Bool("architect_editor_split"),
	}
	return choice, "arbiter", nil
}

// fallbackChoice intentionally refuses to infer frontier privileges from a
// profile when policy is unavailable. It is a transparent safe mode, not an
// alternate policy engine.
func fallbackChoice(_ ModelClass) protocolChoice {
	return protocolChoice{
		Name:                 "policy_fallback",
		VisibleToolCount:     4,
		ToolOrder:            codingCoreToolOrder,
		MaxTurns:             10,
		MaxFanout:            1,
		ContextSource:        "canopy_ranked",
		CodeMode:             "suggest",
		VerificationDepth:    "focused",
		ArchitectEditorSplit: true,
	}
}

func conservativeChoice() protocolChoice {
	return protocolChoice{Name: "legacy", VisibleToolCount: 0, MaxTurns: 0, MaxFanout: 1, ContextSource: "legacy", CodeMode: "off", VerificationDepth: "legacy"}
}

func clampChoice(choice protocolChoice, request TaskRequest, profile BehaviorProfile, config CompilerConfig) protocolChoice {
	if choice.VisibleToolCount < 0 {
		choice.VisibleToolCount = 0
	}
	if profile.Capabilities.SafeVisibleToolCount > 0 && choice.VisibleToolCount > profile.Capabilities.SafeVisibleToolCount {
		choice.VisibleToolCount = profile.Capabilities.SafeVisibleToolCount
	}
	if choice.MaxTurns < 0 {
		choice.MaxTurns = 0
	}
	if choice.MaxFanout < 1 {
		choice.MaxFanout = 1
	}
	if choice.MaxFanout > config.MaxFanout {
		choice.MaxFanout = config.MaxFanout
	}
	if !request.Parallelizable || request.Risk == "high" || !profile.Capabilities.ParallelToolCalls || profile.Metrics.ParallelCallReliability < 0.80 {
		choice.MaxFanout = 1
	}
	if !profile.Capabilities.Continuation || profile.Metrics.ContinuationReliability < 0.75 {
		choice.Continuation = false
	}
	if !profile.Capabilities.CodeMode || !config.AutoCodeMode || config.Mode != ModeDynamic {
		if choice.CodeMode == "auto_read_only" {
			choice.CodeMode = "suggest"
		}
	}
	return choice
}

func normalizeRequest(request TaskRequest) (TaskRequest, error) {
	request.TaskID = strings.TrimSpace(request.TaskID)
	request.Phase = strings.TrimSpace(strings.ToLower(request.Phase))
	if request.Phase == "" {
		request.Phase = "execution"
	}
	request.TaskClass = strings.TrimSpace(strings.ToLower(request.TaskClass))
	if request.TaskClass == "" {
		request.TaskClass = "coding"
	}
	request.Risk = strings.TrimSpace(strings.ToLower(request.Risk))
	if request.Risk == "" {
		request.Risk = "medium"
	}
	if request.Risk != "low" && request.Risk != "medium" && request.Risk != "high" {
		return TaskRequest{}, fmt.Errorf("protocol task risk must be low, medium, or high")
	}
	if request.Complexity < 0 || request.Complexity > 100 {
		return TaskRequest{}, fmt.Errorf("protocol task complexity must be between 0 and 100")
	}
	if request.BudgetUtilization < 0 || request.BudgetUtilization > 1 || request.LatencyBudgetMS < 0 {
		return TaskRequest{}, fmt.Errorf("protocol task budget values are invalid")
	}
	request.CandidateTools = normalizedTools(request.CandidateTools)
	return request, nil
}

func protocolFacts(request TaskRequest, profile BehaviorProfile, class ModelClass, config CompilerConfig) map[string]any {
	return map[string]any{
		"model.class":                         string(class),
		"model.tool_calls":                    profile.Capabilities.ToolCalls,
		"model.native_json_schema":            profile.Capabilities.NativeJSONSchema,
		"model.parallel_tool_calls":           profile.Capabilities.ParallelToolCalls,
		"model.continuation":                  profile.Capabilities.Continuation,
		"model.code_mode":                     profile.Capabilities.CodeMode,
		"model.tool_reliability":              profile.Metrics.ToolReliability,
		"model.structured_output_reliability": profile.Metrics.StructuredOutputReliability,
		"model.parallel_call_reliability":     profile.Metrics.ParallelCallReliability,
		"model.continuation_reliability":      profile.Metrics.ContinuationReliability,
		"model.sample_size":                   profile.SampleSize,
		"model.confidence":                    profile.Confidence,
		"task.phase":                          request.Phase,
		"task.class":                          request.TaskClass,
		"task.complexity":                     request.Complexity,
		"task.risk":                           request.Risk,
		"task.parallelizable":                 request.Parallelizable,
		"task.needs_artifact":                 request.NeedsArtifact,
		"budget.utilization":                  request.BudgetUtilization,
		"budget.latency_ms":                   request.LatencyBudgetMS,
		"rollout.dynamic":                     config.Mode == ModeDynamic,
		"rollout.auto_code_mode":              config.AutoCodeMode,
	}
}

func selectTools(requestTools, defaults, preferred []string, limit int) []string {
	tools := requestTools
	if len(tools) == 0 {
		tools = defaults
	}
	tools = normalizedTools(tools)
	if limit <= 0 || len(tools) == 0 {
		return nil
	}
	capacity := limit
	if len(tools) < capacity {
		capacity = len(tools)
	}
	selected := make([]string, 0, capacity)
	appendAvailable := func(tool string) bool {
		index := sort.SearchStrings(tools, tool)
		if index >= len(tools) || tools[index] != tool {
			return false
		}
		for _, selectedTool := range selected {
			if selectedTool == tool {
				return false
			}
		}
		selected = append(selected, tool)
		return len(selected) == limit
	}
	for _, tool := range preferred {
		if appendAvailable(tool) {
			return selected
		}
	}
	for _, tool := range tools {
		if appendAvailable(tool) {
			return selected
		}
	}
	return selected
}

// Outcome tool bundles are execution vocabulary; Arbiter owns the governed
// choice of outcome. Reusing immutable slices keeps protocol compilation free
// of repeated policy-payload parsing.
var (
	evidenceCoreToolOrder = []string{"read_file", "search_text", "code_refs", "git_diff", "find_files", "code_impact", "git_status", "code_callgraph", "run_tests", "apply_patch", "exec_program"}
	codingCoreToolOrder   = []string{"read_file", "search_text", "apply_patch", "run_tests", "find_files", "code_refs", "git_diff", "git_status", "code_impact", "exec_program", "run_shell"}
	balancedToolOrder     = []string{"read_file", "search_text", "apply_patch", "run_tests", "find_files", "code_refs", "git_diff", "git_status", "code_impact", "code_callgraph", "exec_program", "run_shell"}
	frontierParallelTools = []string{"exec_program", "read_file", "search_text", "code_impact", "code_refs", "apply_patch", "run_tests", "git_diff", "git_status", "find_files", "code_callgraph", "spawn_subagent", "run_shell", "edit_file"}
	frontierHorizonTools  = []string{"exec_program", "read_file", "search_text", "code_impact", "code_refs", "apply_patch", "run_tests", "git_diff", "git_status", "find_files", "code_callgraph", "run_shell", "edit_file"}
)

func toolOrderForOutcome(name string) []string {
	switch name {
	case "weak_evidence_stages":
		return evidenceCoreToolOrder
	case "weak_typed_stages", "policy_fallback":
		return codingCoreToolOrder
	case "frontier_parallel":
		return frontierParallelTools
	case "frontier_horizon":
		return frontierHorizonTools
	case "balanced_protocol":
		return balancedToolOrder
	default:
		return nil
	}
}

func normalizedTools(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	tools := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		tools = append(tools, value)
	}
	sort.Strings(tools)
	return tools
}

func protocolID(protocol Protocol) (string, error) {
	protocol.ProtocolID = ""
	encoded, err := json.Marshal(protocol)
	if err != nil {
		return "", fmt.Errorf("encode protocol receipt: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return "protocol_" + hex.EncodeToString(digest[:12]), nil
}

func digestValue(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode protocol facts: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func cloneFacts(facts map[string]any) map[string]any {
	out := make(map[string]any, len(facts))
	for key, value := range facts {
		out[key] = value
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func maxProtocol(left, right int) int {
	if left > right {
		return left
	}
	return right
}
