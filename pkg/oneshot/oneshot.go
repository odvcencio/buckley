// Package oneshot provides the framework for one-shot CLI commands.
//
// One-shot commands (commit, review, plan, etc.) follow a consistent pattern:
//  1. Assemble context (transparent token accounting)
//  2. Invoke model with a tool contract
//  3. Receive structured response (no parsing)
//  4. Present with full transparency
//  5. Let user decide (approve, regenerate, edit)
//
// This package provides the shared infrastructure for all one-shot commands.
package oneshot

import (
	"context"

	"github.com/odvcencio/buckley/pkg/tools"
	"github.com/odvcencio/buckley/pkg/transparency"
)

// Options configures one-shot command behavior.
type Options struct {
	// Model to use (overrides default)
	Model string

	// Verbose shows expanded reasoning
	Verbose bool

	// Trace shows raw API request/response
	Trace bool

	// Quiet minimizes output
	Quiet bool

	// DryRun prevents side effects
	DryRun bool

	// MaxContextTokens limits total context size
	MaxContextTokens int
}

// Result captures the outcome of a one-shot invocation.
type Result struct {
	// Trace contains full transparency data
	Trace *transparency.Trace

	// ToolCall is the structured response (if model called a tool)
	ToolCall *tools.ToolCall

	// TextContent is raw text (if model didn't use tools)
	TextContent string
}

// HasToolCall returns true if the model called a tool.
func (r *Result) HasToolCall() bool {
	return r.ToolCall != nil
}

// Invoker executes one-shot commands.
type Invoker interface {
	// Invoke runs a one-shot command with the given tool.
	Invoke(ctx context.Context, prompt string, tool tools.Definition, opts Options) (*Result, error)
}

// Presenter renders one-shot results to the user.
type Presenter interface {
	// ShowContext displays the context audit
	ShowContext(audit *transparency.ContextAudit)

	// ShowReasoning displays model reasoning (collapsible)
	ShowReasoning(reasoning string, collapsed bool)

	// ShowResult displays the structured result
	ShowResult(name string, data any)

	// ShowCost displays token/cost information
	ShowCost(trace *transparency.Trace)

	// ShowError displays an error with context
	ShowError(err error, trace *transparency.Trace)
}

// ActionHandler processes user actions on results.
type ActionHandler interface {
	// PromptAction shows action choices and handles user selection.
	// Returns the selected action or an error.
	PromptAction(result *Result) (Action, error)
}

// Action represents a user action on a one-shot result.
type Action string

const (
	ActionApprove    Action = "approve"
	ActionRegenerate Action = "regenerate"
	ActionEdit       Action = "edit"
	ActionGuidance   Action = "guidance"
	ActionCopy       Action = "copy"
	ActionQuit       Action = "quit"
)
