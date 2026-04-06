package tool

import (
	"sort"
	"strings"

	"github.com/odvcencio/buckley/pkg/types"
)

// RequiredTierForTool infers the permission tier for a tool from its metadata.
func RequiredTierForTool(t Tool) types.PermissionTier {
	if t == nil {
		return types.TierReadOnly
	}

	meta := GetMetadata(t)
	if meta.Impact == ImpactDestructive {
		return types.TierShellExec
	}

	switch meta.Category {
	case CategoryShell, CategoryBrowser, CategoryDelegation:
		return types.TierShellExec
	}

	if meta.Impact == ImpactModifying {
		return types.TierWorkspaceWrite
	}
	return types.TierReadOnly
}

// RequiredTier returns the inferred tier for a registered tool.
func (r *Registry) RequiredTier(name string) types.PermissionTier {
	if r == nil {
		return types.TierReadOnly
	}
	t, ok := r.Get(name)
	if !ok {
		return types.TierReadOnly
	}
	return RequiredTierForTool(t)
}

// GovernedToolNames returns the tool list after skill and arbiter filtering.
func GovernedToolNames(registry *Registry, evaluator types.RuleEvaluator, role, taskType string, baseAllowed []string, budgetUtil float64) []string {
	if registry == nil {
		return nil
	}

	allowed := make(map[string]struct{}, len(baseAllowed))
	for _, name := range baseAllowed {
		name = strings.TrimSpace(name)
		if name != "" {
			allowed[name] = struct{}{}
		}
	}

	mode := "full"
	excluded := map[string]struct{}{}
	if evaluator != nil {
		result, err := evaluator.EvalStrategy("runtime/concurrency", "pool_policy", map[string]any{
			"role":        role,
			"task_type":   taskType,
			"budget_util": budgetUtil,
		})
		if err == nil {
			if resolvedMode := strings.TrimSpace(result.String("mode")); resolvedMode != "" {
				mode = resolvedMode
			}
			for _, name := range strings.Split(result.String("exclude_tools"), ",") {
				name = strings.TrimSpace(name)
				if name != "" {
					excluded[name] = struct{}{}
				}
			}
		}
	}

	names := make([]string, 0, registry.Count())
	for _, candidate := range registry.List() {
		if candidate == nil {
			continue
		}
		name := strings.TrimSpace(candidate.Name())
		if name == "" {
			continue
		}
		if len(allowed) > 0 {
			if _, ok := allowed[name]; !ok {
				continue
			}
		}
		if _, ok := excluded[name]; ok {
			continue
		}
		if !matchesPoolMode(candidate, mode) {
			continue
		}
		names = append(names, name)
	}

	sort.Strings(names)
	return names
}

// ToOpenAIFunctionsGoverned applies skill and arbiter filtering before exposing tools.
func (r *Registry) ToOpenAIFunctionsGoverned(evaluator types.RuleEvaluator, role, taskType string, baseAllowed []string, budgetUtil float64) []map[string]any {
	names := GovernedToolNames(r, evaluator, role, taskType, baseAllowed, budgetUtil)
	return r.ToOpenAIFunctionsFiltered(names)
}

func matchesPoolMode(t Tool, mode string) bool {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" || mode == "full" {
		return true
	}

	meta := GetMetadata(t)
	switch mode {
	case "read_only":
		return RequiredTierForTool(t) == types.TierReadOnly
	case "standard":
		return meta.Impact != ImpactDestructive &&
			meta.Category != CategoryDelegation &&
			meta.Category != CategoryBrowser
	case "simple":
		if RequiredTierForTool(t) == types.TierReadOnly {
			return true
		}
		switch meta.Category {
		case CategoryFilesystem, CategoryCodebase, CategoryRefactoring, CategoryAnalysis, CategoryTesting, CategoryDocumentation, CategoryPlanning, CategoryShell:
			return meta.Category != CategoryDelegation && meta.Category != CategoryBrowser
		default:
			return false
		}
	default:
		return true
	}
}
