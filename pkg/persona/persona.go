// Package persona parses tiller-compatible persona files and resolves them
// into a registry buckley's subagent manager can consume. A persona file is
// a markdown file with a YAML frontmatter block, the same shape used by
// Claude Code and Codex agent files under .claude/agents and .tiller/agents.
//
// Buckley reads this format; it does not invent a second one. The Persona
// fields below are a superset of the fields tiller's own agent files use
// today (name, description, tools, model): buckley-specific keys (mode,
// tier, permission, step_cap, color) are additive and optional, so existing
// tiller persona files parse cleanly with zero values for the fields they
// omit.
package persona

import "strings"

// Mode distinguishes a persona meant to run as the primary session driver
// from one meant to run as a delegated subagent. It mirrors OpenCode's
// "mode" field (primary | subagent).
type Mode string

const (
	// ModePrimary marks a persona that can drive a top-level session.
	ModePrimary Mode = "primary"
	// ModeSubagent marks a persona meant to be spawned as a delegated
	// child. It is the default when a persona file omits "mode".
	ModeSubagent Mode = "subagent"
)

// Persona is buckley's in-memory representation of a tiller-compatible
// agent definition file.
type Persona struct {
	// Name is the persona's identifier, used for "@name" resolution and
	// registry lookups. Required.
	Name string
	// Description explains when to delegate to this persona.
	Description string
	// Mode is ModePrimary or ModeSubagent. Defaults to ModeSubagent when
	// the source file omits "mode" (tiller's Claude Code agent files
	// never set it; they are always subagents).
	Mode Mode
	// Model is the pinned model alias, e.g. "sonnet", "opus", "fable".
	// Empty when the persona does not pin a model.
	Model string
	// Tier is the pinned tiller tier (reason | scrutiny | execute).
	// Buckley-specific and additive; tiller's own agent files never set
	// it directly today, they pin via Model instead. Empty when unset.
	Tier Tier
	// Prompt is the markdown body following the frontmatter block: the
	// persona's system prompt.
	Prompt string
	// AllowedTools is the persona's tool allowlist, parsed from the
	// comma-separated "tools" field tiller's Claude Code agent files use.
	// Nil means "inherit all tools" (the field was omitted).
	AllowedTools []string
	// PermissionOverrides carries OpenCode-style "permission" frontmatter
	// as opaque per-key strings (e.g. "edit" -> "ask", "bash" -> a
	// glob-to-verdict YAML block). pkg/policy integration will interpret
	// these later; this package treats them as data, not policy.
	PermissionOverrides map[string]string
	// StepCap bounds the persona's iteration budget when a runtime
	// supports iteration limits. Zero means unset (no cap imposed here).
	StepCap int
	// Color is an optional UI hint for the persona, matching the
	// optional Claude Code subagent "color" field.
	Color string

	// SourcePath is the file the persona was parsed from. Empty for
	// programmatically constructed personas.
	SourcePath string
}

// HasExplicitPin reports whether the persona pins a model or a tier
// explicitly, rather than leaving both to be inferred or inherited.
func (p Persona) HasExplicitPin() bool {
	return strings.TrimSpace(string(p.Tier)) != "" || strings.TrimSpace(p.Model) != ""
}

// EffectiveMode returns p.Mode, defaulting to ModeSubagent when unset.
func (p Persona) EffectiveMode() Mode {
	if p.Mode == ModePrimary {
		return ModePrimary
	}
	return ModeSubagent
}
