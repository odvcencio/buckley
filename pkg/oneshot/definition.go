package oneshot

import (
	"encoding/json"

	"github.com/odvcencio/buckley/pkg/tools"
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

// RLMDefinition describes a oneshot command that requires full RLM sub-agent
// execution with multi-turn tool access (read, write, bash, grep, glob).
//
// Commands implementing this interface are dispatched through RunRLM instead
// of the single-tool invoke+retry loop used by Definition.
//
// The framework detects which interface a definition implements and routes:
//   - Definition     -> single-tool invoke+retry (commit, PR)
//   - RLMDefinition  -> full RLM sub-agent execution (review)
type RLMDefinition interface {
	// Name returns the command name.
	Name() string

	// SystemPrompt returns the system prompt for the RLM agent.
	SystemPrompt() string

	// AllowedTools returns tool names the agent may use (e.g., "read", "bash").
	AllowedTools() []string

	// ParseResult processes the free-form agent response into typed output.
	ParseResult(response string) (any, error)
}
