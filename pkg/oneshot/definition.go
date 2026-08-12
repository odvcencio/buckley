package oneshot

import (
	"encoding/json"
	"errors"

	"m31labs.dev/buckley/pkg/tools"
)

// ContextSource describes a source of context for a oneshot command.
type ContextSource struct {
	// Type identifies the source kind.
	// Supported: "git_diff", "git_log", "git_files", "agents_md", "env", "command".
	Type string

	// Params holds source-specific parameters.
	//
	//   git_diff:  "staged" => "true" for --cached; "base" => branch name for base...HEAD
	//   git_log:   "base" => branch name for base..HEAD
	//   git_files: "staged" => "true" for --cached --name-status; "base" => branch name
	//   agents_md: (no params)
	//   env:       "name" => environment variable name
	//   command:   "cmd" => shell command string
	Params map[string]string
}

// Context holds gathered context for a oneshot command.
type Context struct {
	// Sources maps a label (typically the ContextSource.Type, but can be
	// qualified like "git_diff:staged") to the gathered content string.
	Sources map[string]string

	// Tokens is the estimated total token count across all sources.
	Tokens int
}

// Definition describes a oneshot command's shape.
// The framework uses this to assemble context, build prompts, invoke the model,
// and validate/unmarshal the structured result.
type Definition interface {
	// Name returns the command name (e.g., "commit", "pr", "review").
	Name() string

	// Tool returns the tool definition the model must call.
	Tool() tools.Definition

	// ContextSources lists the sources of context this command needs.
	ContextSources() []ContextSource

	// SystemPrompt returns the system prompt for the model.
	SystemPrompt() string

	// BuildPrompt constructs the user prompt from gathered context.
	BuildPrompt(ctx *Context) string

	// Validate checks the raw tool call result for correctness.
	Validate(result json.RawMessage) error

	// Unmarshal deserializes the raw tool call result into a typed value.
	Unmarshal(result json.RawMessage) (any, error)
}

// AgentDefinition describes a oneshot command that requires one autonomous
// tool-using agent with multi-turn access.
//
// Commands implementing this interface are dispatched through RunAgent instead
// of the single-tool invoke+retry loop used by Definition.
//
// The framework detects which interface a definition implements and routes:
//   - Definition      -> single-tool invoke+retry (commit, PR)
//   - AgentDefinition -> one multi-turn tool agent (review)
type AgentDefinition interface {
	// Name returns the command name.
	Name() string

	// SystemPrompt returns the system prompt for the agent.
	SystemPrompt() string

	// AllowedTools returns exact registry tool names the agent may use
	// (e.g., "read_file", "run_shell").
	AllowedTools() []string

	// ParseResult processes the free-form agent response into typed output.
	ParseResult(response string) (any, error)
}

// AgentEvidenceRequest is one deterministic tool invocation that the harness
// must complete before model synthesis. It is intentionally data-only so
// command definitions can describe required evidence without depending on a
// concrete tool registry.
type AgentEvidenceRequest struct {
	Tool       string
	Parameters map[string]any
}

// AgentEvidencePlanner optionally declares evidence that is required for every
// valid execution of an agent command. The framework collects this evidence
// once against the immutable snapshot and carries it across validation repairs.
type AgentEvidencePlanner interface {
	AgentEvidenceRequests() []AgentEvidenceRequest
}

// AgentExecutionBudget optionally caps model/tool iterations for commands where
// bounded sampling is part of the product contract.
type AgentExecutionBudget interface {
	MaxAgentIterations() int
}

// AgentResultValidator optionally adds semantic validation to an agent command.
// RunAgent retries responses that fail this validation, using the validation
// error as corrective guidance for the next attempt.
type AgentResultValidator interface {
	ValidateResult(result any) error
}

// AgentExecutionValidator optionally validates that claims in the parsed result
// are backed by evidence from the just-completed agent execution. Review
// definitions use this to prevent an API model from claiming passing local
// verification without successful snapshot-bound verification tool calls.
type AgentExecutionValidator interface {
	ValidateAgentExecution(result any, execution *AgentResult) error
}

// AgentExecutionEvidenceRequiredError marks a validation failure that needs new
// inspection or verification evidence. A text-only repair cannot resolve it.
type AgentExecutionEvidenceRequiredError struct {
	err error
}

func (e *AgentExecutionEvidenceRequiredError) Error() string {
	if e == nil || e.err == nil {
		return "agent execution evidence is required"
	}
	return e.err.Error()
}

func (e *AgentExecutionEvidenceRequiredError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

// RequireAgentExecutionEvidence marks an error for a bounded tool-enabled retry.
func RequireAgentExecutionEvidence(err error) error {
	if err == nil {
		return nil
	}
	return &AgentExecutionEvidenceRequiredError{err: err}
}

// IsAgentExecutionEvidenceRequired reports whether validation needs new evidence.
func IsAgentExecutionEvidenceRequired(err error) bool {
	var target *AgentExecutionEvidenceRequiredError
	return errors.As(err, &target)
}

// AgentApprovalCritic optionally requires an independent adversarial pass after
// the primary agent result has been parsed and validated. Review definitions use
// this to make an approval provisional until a fresh sub-agent has searched the
// same evidence for missed blockers and returned its own validated review.
//
// The framework only invokes the critic when RequiresApprovalCritic returns
// true. The critic result is parsed and validated through the original
// AgentDefinition and becomes the final result.
type AgentApprovalCritic interface {
	// RequiresApprovalCritic reports whether the validated primary result needs
	// an independent second pass.
	RequiresApprovalCritic(result any) bool

	// ApprovalCriticSystemPrompt returns the role prompt for the independent
	// critic. It should demand the same output contract as the primary pass.
	ApprovalCriticSystemPrompt() string

	// BuildApprovalCriticPrompt combines the original evidence and the validated
	// primary result into the critic task. Both are supplied explicitly so the
	// critic can independently verify the evidence and challenge the prior work.
	BuildApprovalCriticPrompt(originalPrompt string, primaryResult any) (string, error)
}
