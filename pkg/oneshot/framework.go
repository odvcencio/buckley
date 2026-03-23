package oneshot

import (
	"context"
	"fmt"
	"strings"

	"github.com/odvcencio/buckley/pkg/rules"
	"github.com/odvcencio/buckley/pkg/transparency"
)

const defaultMaxRetries = 3

// Framework provides a single execution engine for all oneshot commands.
// It replaces the duplicated Runner types in commit/, pr/, and rlm/.
type Framework struct {
	invoker *DefaultInvoker
	engine  *rules.Engine
}

// NewFramework creates a new oneshot framework.
func NewFramework(invoker *DefaultInvoker, engine *rules.Engine) *Framework {
	return &Framework{
		invoker: invoker,
		engine:  engine,
	}
}

// RunOpts configures a single framework execution.
type RunOpts struct {
	// ContextOpts controls context gathering behavior.
	ContextOpts ContextOpts

	// MaxRetries overrides the default retry count.
	// If zero, uses arbiter strategy or default (3).
	MaxRetries int

	// Guidance is optional extra text appended to the user prompt on retry
	// when the model fails to call the tool.
	Guidance string
}

// RunResult contains the outcome of a framework execution.
type RunResult struct {
	// Value is the unmarshalled result (typed per Definition).
	Value any

	// Trace contains transparency data from the invocation.
	Trace *transparency.Trace

	// ContextAudit shows what context was gathered.
	ContextAudit *transparency.ContextAudit
}

// Run executes a oneshot command using the unified pipeline:
//  1. Build context from the definition's sources
//  2. Build system and user prompts
//  3. Query arbiter for retry config (if engine available)
//  4. Invoke model in a retry loop with validation
//  5. Return the validated, unmarshalled result
func (f *Framework) Run(ctx context.Context, def Definition, opts RunOpts) (*RunResult, error) {
	if f.invoker == nil {
		return nil, fmt.Errorf("invoker is required")
	}

	// 1. Build context from definition's sources
	gathered, err := BuildContext(def.ContextSources(), opts.ContextOpts)
	if err != nil {
		return nil, fmt.Errorf("build context: %w", err)
	}

	// Build a transparency audit from gathered sources
	audit := transparency.NewContextAudit()
	for label, content := range gathered.Sources {
		audit.Add(label, contextEstimateTokens(content))
	}

	// 2. Build prompts
	systemPrompt := def.SystemPrompt()
	baseUserPrompt := def.BuildPrompt(gathered)

	// 3. Determine retry config from arbiter
	maxRetries := f.resolveMaxRetries(def.Name(), opts.MaxRetries)

	// 4. Invoke with retry loop
	tool := def.Tool()
	userPrompt := baseUserPrompt
	var lastTrace *transparency.Trace

	for attempt := 0; attempt < maxRetries; attempt++ {
		result, trace, invokeErr := f.invoker.Invoke(ctx, systemPrompt, userPrompt, tool, audit)
		lastTrace = trace

		if invokeErr != nil {
			return nil, fmt.Errorf("invoke failed: %w", invokeErr)
		}

		// Check if model called the tool
		if result == nil || result.ToolCall == nil {
			userPrompt = baseUserPrompt + "\n\nIMPORTANT: You MUST call the " + tool.Name + " tool. Do not reply with plain text."
			continue
		}

		// 5. Validate
		if err := def.Validate(result.ToolCall.Arguments); err != nil {
			userPrompt = baseUserPrompt + "\n\nThe previous response failed validation: " + strings.TrimSpace(err.Error()) + ". Fix and call " + tool.Name + " again."
			continue
		}

		// Unmarshal
		value, err := def.Unmarshal(result.ToolCall.Arguments)
		if err != nil {
			userPrompt = baseUserPrompt + "\n\nThe tool call arguments must be valid JSON matching the schema for " + tool.Name + "."
			continue
		}

		return &RunResult{
			Value:        value,
			Trace:        trace,
			ContextAudit: audit,
		}, nil
	}

	// All retries exhausted
	return &RunResult{
		Trace:        lastTrace,
		ContextAudit: audit,
	}, fmt.Errorf("failed after %d attempts for command %q", maxRetries, def.Name())
}

// resolveMaxRetries determines the retry count from opts, arbiter, or defaults.
func (f *Framework) resolveMaxRetries(cmdName string, optsRetries int) int {
	if optsRetries > 0 {
		return optsRetries
	}

	// Try arbiter engine
	if f.engine != nil {
		result, err := f.engine.EvalStrategy("oneshot", "oneshot_policy", map[string]any{
			"command": cmdName,
		})
		if err == nil {
			if v, ok := result.Params["max_retries"]; ok {
				switch n := v.(type) {
				case float64:
					if int(n) > 0 {
						return int(n)
					}
				case int:
					if n > 0 {
						return n
					}
				}
			}
		}
	}

	return defaultMaxRetries
}
