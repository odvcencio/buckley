package persona

import (
	"fmt"
	"strings"
)

// Tier names mirror tiller's tier taxonomy exactly (internal/tier,
// defaults/models.toml): reason is the most capable and most expensive
// tier, scrutiny is the mid tier, execute is the cheapest operational
// tier.
type Tier string

const (
	TierReason   Tier = "reason"
	TierScrutiny Tier = "scrutiny"
	TierExecute  Tier = "execute"
)

// DefaultTier is the tier an unpinned persona resolves to instead of
// silently inheriting a reason-tier parent's model. It mirrors tiller's
// cheapest, safest default. A future config layer may make this
// project-configurable (see pkg/config); this foundation slice keeps it
// fixed to avoid reaching into pkg/config merge files.
const DefaultTier Tier = TierExecute

// tierWeight orders tiers from cheapest to most capable, so ValidateEscalation
// can detect a child persona asking for more than its parent's budget.
var tierWeight = map[Tier]int{
	TierExecute:  1,
	TierScrutiny: 2,
	TierReason:   3,
}

// Valid reports whether t is one of tiller's known tier names.
func (t Tier) Valid() bool {
	_, ok := tierWeight[t]
	return ok
}

func (t Tier) weight() int {
	return tierWeight[t]
}

// modelTierAliases maps known model aliases to their tiller tier, mirroring
// tiller's embedded defaults/models.toml [ambient.claude-code] and
// [ambient.codex] tables. It is a name-based heuristic, not a parse of
// tiller's models.toml: a project-level override file is a later increment.
var modelTierAliases = map[string]Tier{
	"fable":                     TierReason,
	"claude-fable-5":            TierReason,
	"opus":                      TierScrutiny,
	"claude-opus-4-8":           TierScrutiny,
	"sonnet":                    TierExecute,
	"claude-sonnet-4-6":         TierExecute,
	"haiku":                     TierExecute,
	"claude-haiku-4-5":          TierExecute,
	"claude-haiku-4-5-20251001": TierExecute,
	"5.5 xhigh":                 TierReason,
	"gpt-5.5 xhigh":             TierReason,
	"5.5 high":                  TierExecute,
	"5.5 medium":                TierExecute,
	"5.5 low":                   TierExecute,
	"gpt-5.5 high":              TierExecute,
	"gpt-5.5 medium":            TierExecute,
	"gpt-5.5 low":               TierExecute,
}

// TierFor returns the effective tier for p: its explicit Tier pin when set
// and valid, otherwise a tier inferred from its Model pin, otherwise "" when
// neither resolves to a known tier.
func TierFor(p Persona) Tier {
	if explicit := Tier(strings.ToLower(strings.TrimSpace(string(p.Tier)))); explicit.Valid() {
		return explicit
	}
	if tier, ok := modelTierAliases[strings.ToLower(strings.TrimSpace(p.Model))]; ok {
		return tier
	}
	return ""
}

// EscalationError is returned by ValidateEscalation when a child persona
// pins a tier more capable than its parent's tier. Callers must surface it
// for user approval rather than spawning silently or denying outright.
type EscalationError struct {
	Parent     Persona
	Child      Persona
	ParentTier Tier
	ChildTier  Tier
}

func (e *EscalationError) Error() string {
	parentName := e.Parent.Name
	if parentName == "" {
		parentName = "(unnamed parent)"
	}
	return fmt.Sprintf(
		"persona %q pins tier %q, which is higher than parent %q's tier %q; requires approval",
		e.Child.Name, e.ChildTier, parentName, e.ParentTier,
	)
}

// ValidateEscalation implements tiller's DenyImplicitReasonInheritance
// discipline for spawning child under parent.
//
// When child carries no explicit model or tier pin, it must not silently
// inherit parent's tier (in particular a reason-tier parent's model);
// ValidateEscalation resolves it to DefaultTier instead and returns no
// error. This is the "implicit inheritance" branch of the guard.
//
// When child carries an explicit pin, ValidateEscalation resolves it to
// TierFor(child). If that tier is no more capable than parent's tier, it is
// returned with no error. If it exceeds parent's tier, ValidateEscalation
// returns a typed *EscalationError instead of the tier: the caller must
// surface this for approval rather than spawn the child automatically.
func ValidateEscalation(parent, child Persona) (Tier, error) {
	if !child.HasExplicitPin() {
		return DefaultTier, nil
	}

	childTier := TierFor(child)
	if !childTier.Valid() {
		// Explicit pin present but not a recognized tier/model alias
		// (e.g. a bespoke model name). There is nothing to compare
		// against parent's tier, so let the persona's own Model pin
		// carry the resolution.
		return childTier, nil
	}

	parentTier := TierFor(parent)
	if parentTier.Valid() && childTier.weight() > parentTier.weight() {
		return "", &EscalationError{
			Parent:     parent,
			Child:      child,
			ParentTier: parentTier,
			ChildTier:  childTier,
		}
	}
	return childTier, nil
}
