package policy

import (
	"path/filepath"
	"strings"
)

// PermissionAction is the outcome of evaluating a PermissionRule.
type PermissionAction string

const (
	// PermissionAllow lets the operation proceed without further checks.
	PermissionAllow PermissionAction = "allow"
	// PermissionAsk requires human approval (or, under a parking posture,
	// records a ParkedDecision instead of blocking).
	PermissionAsk PermissionAction = "ask"
	// PermissionDeny blocks the operation outright, in every approval mode.
	PermissionDeny PermissionAction = "deny"
)

// PermissionRule is a single glob-granular permission rule. Tool selects
// which tools the rule applies to ("*" matches every tool). ArgPattern is a
// glob matched against the relevant argument: the command string for
// run_shell/run_code, or the path for file tools. OutsideWorkspaceOnly
// restricts the rule to arguments that resolve outside the workspace root,
// used by the built-in destructive-bash default.
type PermissionRule struct {
	Tool                 string           `json:"tool" yaml:"tool"`
	ArgPattern           string           `json:"arg_pattern" yaml:"arg_pattern"`
	Action               PermissionAction `json:"action" yaml:"action"`
	OutsideWorkspaceOnly bool             `json:"outside_workspace_only,omitempty" yaml:"outside_workspace_only,omitempty"`
}

// PermissionLayer is a named, ordered list of rules evaluated as a unit.
// Within a layer, evaluation stops at the first matching rule.
type PermissionLayer struct {
	Name  string
	Rules []PermissionRule
}

// PermissionRequest describes a tool call awaiting a glob-permission
// decision.
type PermissionRequest struct {
	Tool              string // tool name, e.g. "run_shell", "read_file"
	Category          string // "shell", "file_read", "file_write", ...
	Arg               string // command string or path matched against ArgPattern
	WorkspaceRelative bool   // true when Arg resolves inside the workspace root
	Posture           string // active posture name, e.g. "interactive", "unattended"
}

// PermissionDecision is the outcome of evaluating a request against one or
// more layers.
type PermissionDecision struct {
	Action  PermissionAction
	Layer   string
	Rule    PermissionRule
	Matched bool
}

// EvaluatePermissionLayers evaluates layers in priority order (highest
// priority first, e.g. posture, project, user, built-in defaults) and
// returns the composed decision. A deny match in any layer overrides
// allow/ask matches from every other layer, regardless of layer order.
// When no layer matches, the returned decision has Matched=false.
func EvaluatePermissionLayers(req PermissionRequest, layers ...PermissionLayer) PermissionDecision {
	var firstMatch PermissionDecision
	haveFirstMatch := false

	for _, layer := range layers {
		dec, matched := evaluateLayer(req, layer)
		if !matched {
			continue
		}
		if dec.Action == PermissionDeny {
			// Deny always wins, regardless of which layer produced it.
			return dec
		}
		if !haveFirstMatch {
			firstMatch = dec
			haveFirstMatch = true
		}
	}

	if !haveFirstMatch {
		return PermissionDecision{}
	}
	return firstMatch
}

// evaluateLayer returns the first matching rule in a layer, honoring
// per-rule OutsideWorkspaceOnly scoping.
func evaluateLayer(req PermissionRequest, layer PermissionLayer) (PermissionDecision, bool) {
	for _, rule := range layer.Rules {
		if !ruleMatches(req, rule) {
			continue
		}
		return PermissionDecision{
			Action:  rule.Action,
			Layer:   layer.Name,
			Rule:    rule,
			Matched: true,
		}, true
	}
	return PermissionDecision{}, false
}

// ruleMatches reports whether a rule applies to a request.
func ruleMatches(req PermissionRequest, rule PermissionRule) bool {
	if rule.Tool != "*" && !strings.EqualFold(rule.Tool, req.Tool) {
		return false
	}
	if rule.OutsideWorkspaceOnly && req.WorkspaceRelative {
		return false
	}
	if strings.TrimSpace(rule.ArgPattern) == "" {
		return true
	}
	return matchArg(req, rule.ArgPattern)
}

// matchArg matches a glob pattern against the request argument. File-tool
// categories use doublestar-style path matching (so "**/.env" matches at
// any depth); other categories (shell commands) use simple substring-style
// glob matching via matchGlob.
func matchArg(req PermissionRequest, pattern string) bool {
	if isPathCategory(req.Category) || strings.Contains(pattern, "/") {
		return matchGlobPath(pattern, req.Arg)
	}
	return matchGlob(pattern, req.Arg)
}

func isPathCategory(category string) bool {
	switch ToolCategory(category) {
	case CategoryFileRead, CategoryFileWrite:
		return true
	default:
		return false
	}
}

// matchGlobPath matches a doublestar-style glob pattern (supporting "**" as
// a path-spanning wildcard) against a filesystem path. Both the pattern and
// the path are split on "/" and matched segment by segment; "**" consumes
// zero or more path segments.
func matchGlobPath(pattern, path string) bool {
	pattern = filepath.ToSlash(strings.TrimSpace(pattern))
	path = filepath.ToSlash(strings.TrimSpace(path))
	if pattern == "" || path == "" {
		return false
	}
	patSegs := strings.Split(strings.TrimPrefix(pattern, "/"), "/")
	pathSegs := strings.Split(strings.TrimPrefix(path, "/"), "/")
	return matchSegments(patSegs, pathSegs)
}

func matchSegments(pat, name []string) bool {
	if len(pat) == 0 {
		return len(name) == 0
	}
	if pat[0] == "**" {
		// "**" matches zero segments...
		if matchSegments(pat[1:], name) {
			return true
		}
		// ...or consumes one segment and retries.
		if len(name) > 0 && matchSegments(pat, name[1:]) {
			return true
		}
		return false
	}
	if len(name) == 0 {
		return false
	}
	ok, err := filepath.Match(pat[0], name[0])
	if err != nil || !ok {
		return false
	}
	return matchSegments(pat[1:], name[1:])
}
