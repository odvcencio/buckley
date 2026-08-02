package policy

import (
	"m31labs.dev/buckley/pkg/types"
)

// BuiltinDefaultsDomain and BuiltinDefaultsStrategy identify the arbiter
// rule domain/strategy that mirrors BuiltinDefaultRules. Both paths must
// agree on every input; see the parity test in builtin_defaults_test.go.
const (
	BuiltinDefaultsDomain   = "permissions/builtin_defaults"
	BuiltinDefaultsStrategy = "builtin_defaults_policy"
)

// BuiltinDefaultRules returns the built-in permission defaults: deny reads
// of .env-shaped and credential-shaped paths, and ask before destructive
// bash commands run outside the workspace.
func BuiltinDefaultRules() []PermissionRule {
	return []PermissionRule{
		{Tool: "*", ArgPattern: "**/.env", Action: PermissionDeny},
		{Tool: "*", ArgPattern: "**/.env.*", Action: PermissionDeny},
		{Tool: "*", ArgPattern: "**/*credentials*", Action: PermissionDeny},
		{Tool: "*", ArgPattern: "**/id_rsa*", Action: PermissionDeny},
		{Tool: "run_shell", ArgPattern: "*rm -rf*", Action: PermissionAsk, OutsideWorkspaceOnly: true},
		{Tool: "run_shell", ArgPattern: "*rm -r -f*", Action: PermissionAsk, OutsideWorkspaceOnly: true},
		{Tool: "run_shell", ArgPattern: "*git push*--force*", Action: PermissionAsk, OutsideWorkspaceOnly: true},
		{Tool: "run_shell", ArgPattern: "*git push*-f*", Action: PermissionAsk, OutsideWorkspaceOnly: true},
		{Tool: "run_shell", ArgPattern: "*curl*|*sh*", Action: PermissionAsk, OutsideWorkspaceOnly: true},
		{Tool: "run_shell", ArgPattern: "*curl*|*bash*", Action: PermissionAsk, OutsideWorkspaceOnly: true},
		{Tool: "run_code", ArgPattern: "*rm -rf*", Action: PermissionAsk, OutsideWorkspaceOnly: true},
	}
}

// BuiltinDefaultsLayer wraps BuiltinDefaultRules as a named PermissionLayer,
// ready to compose with posture/project/user layers.
func BuiltinDefaultsLayer() PermissionLayer {
	return PermissionLayer{Name: "built-in defaults", Rules: BuiltinDefaultRules()}
}

// EvaluateBuiltinDefaultsGo evaluates the built-in defaults using the
// deterministic Go glob layer only (no arbiter). This is the fallback used
// when a rules engine is unavailable, and the reference implementation the
// arbiter strategy must match.
func EvaluateBuiltinDefaultsGo(req PermissionRequest) PermissionDecision {
	dec, _ := evaluateLayer(req, BuiltinDefaultsLayer())
	return dec
}

// EvaluateBuiltinDefaultsArbiter evaluates the built-in defaults via the
// embedded rules engine's permissions/builtin_defaults strategy. It returns
// ok=false when the strategy could not be evaluated (nil evaluator, missing
// domain, or an evaluation error), signaling the caller to fall back to
// EvaluateBuiltinDefaultsGo.
func EvaluateBuiltinDefaultsArbiter(evaluator types.RuleEvaluator, req PermissionRequest) (PermissionDecision, bool) {
	if evaluator == nil {
		return PermissionDecision{}, false
	}
	result, err := evaluator.EvalStrategy(BuiltinDefaultsDomain, BuiltinDefaultsStrategy, map[string]any{
		"tool":               req.Tool,
		"category":           req.Category,
		"arg":                req.Arg,
		"workspace_relative": req.WorkspaceRelative,
		"posture":            req.Posture,
	})
	if err != nil {
		return PermissionDecision{}, false
	}
	action, _ := result.Params["action"].(string)
	if action == "" {
		return PermissionDecision{}, true
	}
	return PermissionDecision{
		Action:  PermissionAction(action),
		Layer:   "built-in defaults (arbiter)",
		Matched: true,
	}, true
}

// EvaluateBuiltinDefaults evaluates the built-in defaults via the arbiter
// strategy when a rules evaluator is available, falling back to the
// deterministic Go glob layer otherwise. Both paths implement the same
// rule set and must agree (see the parity test).
func EvaluateBuiltinDefaults(evaluator types.RuleEvaluator, req PermissionRequest) PermissionDecision {
	if dec, ok := EvaluateBuiltinDefaultsArbiter(evaluator, req); ok {
		return dec
	}
	return EvaluateBuiltinDefaultsGo(req)
}

// EvaluatePermissionLayersWithBuiltins composes caller-supplied layers
// (highest priority first, e.g. posture, project, user) with the built-in
// defaults layer (lowest priority, evaluated via EvaluateBuiltinDefaults so
// it prefers the arbiter strategy and falls back to the Go glob layer). The
// same deny-always-wins / first-match-by-priority semantics as
// EvaluatePermissionLayers apply across every layer, built-in defaults
// included.
func EvaluatePermissionLayersWithBuiltins(evaluator types.RuleEvaluator, req PermissionRequest, layers ...PermissionLayer) PermissionDecision {
	var firstMatch PermissionDecision
	haveFirstMatch := false

	for _, layer := range layers {
		dec, matched := evaluateLayer(req, layer)
		if !matched {
			continue
		}
		if dec.Action == PermissionDeny {
			return dec
		}
		if !haveFirstMatch {
			firstMatch = dec
			haveFirstMatch = true
		}
	}

	if builtinDec := EvaluateBuiltinDefaults(evaluator, req); builtinDec.Matched {
		if builtinDec.Action == PermissionDeny {
			return builtinDec
		}
		if !haveFirstMatch {
			firstMatch = builtinDec
			haveFirstMatch = true
		}
	}

	if !haveFirstMatch {
		return PermissionDecision{}
	}
	return firstMatch
}
