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
	// PermissionPark records the operation for later approval. Most callers
	// represent parking as PermissionAsk plus a posture-level parking mode,
	// but the explicit action is kept here so a policy can express the same
	// safety severity without relying on caller configuration.
	PermissionPark PermissionAction = "park"
	// PermissionDeny blocks the operation outright, in every approval mode.
	PermissionDeny PermissionAction = "deny"
)

// PermissionActionSeverity ranks policy outcomes for composition. Safety
// actions outrank an allow regardless of layer or rule order: deny is the
// strongest terminal action, while ask/park both require a human decision.
// Unknown actions have the lowest severity and retain the first-match tie
// behavior for backwards compatibility with existing policy files.
func PermissionActionSeverity(action PermissionAction) int {
	switch action {
	case PermissionDeny:
		return 3
	case PermissionAsk, PermissionPark:
		return 2
	case PermissionAllow:
		return 1
	default:
		return 0
	}
}

// PermissionRule is a single glob-granular permission rule. Tool selects
// which tools the rule applies to ("*" matches every tool). ArgPattern is a
// glob matched against the relevant argument: the command string for
// run_shell/run_code, or the path for file tools. OutsideWorkspaceOnly
// restricts the rule to arguments that resolve outside the workspace root,
// used by the built-in destructive-bash default.
type PermissionRule struct {
	ID                   string           `json:"id,omitempty" yaml:"id,omitempty"`
	Tool                 string           `json:"tool" yaml:"tool"`
	ArgPattern           string           `json:"arg_pattern" yaml:"arg_pattern"`
	CommandClass         string           `json:"command_class,omitempty" yaml:"command_class,omitempty"`
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
	CommandClass      string // deterministic shell classification, when available
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
// returns the composed decision. Safety actions (deny, ask, and park) outrank
// allow matches from every other layer, regardless of layer order. Within the
// same severity, layer order remains priority order and rule order remains
// stable. When no layer matches, the returned decision has Matched=false.
func EvaluatePermissionLayers(req PermissionRequest, layers ...PermissionLayer) PermissionDecision {
	var firstMatch PermissionDecision
	haveFirstMatch := false

	for _, layer := range layers {
		dec, matched := evaluateLayer(req, layer)
		if !matched {
			continue
		}
		if !haveFirstMatch || PermissionActionSeverity(dec.Action) > PermissionActionSeverity(firstMatch.Action) {
			firstMatch = dec
			haveFirstMatch = true
		}
	}

	if !haveFirstMatch {
		return PermissionDecision{}
	}
	return firstMatch
}

// evaluateLayer returns the highest-severity matching rule in a layer,
// honoring per-rule OutsideWorkspaceOnly scoping. Equal-severity matches keep
// the first rule, so existing ordered policy files remain deterministic while
// a safety rule cannot be hidden behind an earlier allow.
func evaluateLayer(req PermissionRequest, layer PermissionLayer) (PermissionDecision, bool) {
	var best PermissionDecision
	haveBest := false
	for _, rule := range layer.Rules {
		if !ruleMatches(req, rule) {
			continue
		}
		candidate := PermissionDecision{
			Action:  rule.Action,
			Layer:   layer.Name,
			Rule:    rule,
			Matched: true,
		}
		if !haveBest || PermissionActionSeverity(candidate.Action) > PermissionActionSeverity(best.Action) {
			best = candidate
			haveBest = true
		}
	}
	return best, haveBest
}

// ruleMatches reports whether a rule applies to a request.
func ruleMatches(req PermissionRequest, rule PermissionRule) bool {
	if rule.Tool != "*" && !strings.EqualFold(rule.Tool, req.Tool) {
		return false
	}
	if rule.OutsideWorkspaceOnly && req.WorkspaceRelative {
		return false
	}
	if rule.CommandClass != "" && !strings.EqualFold(rule.CommandClass, req.CommandClass) {
		return false
	}
	if strings.TrimSpace(rule.ArgPattern) == "" {
		if rule.Action == PermissionAllow && shellRequestHasControlText(req) {
			return false
		}
		return true
	}
	// A wildcard allow is intentionally not an authorization for shell
	// control text. The dotall matcher is still required by safety rules (for
	// example, to find `rm -rf` after an injected newline), so constrain only
	// allow matches and require every control marker in the argument to be
	// literally represented by the caller's pattern.
	if rule.Action == PermissionAllow && shellRequestHasControlText(req) && !allowPatternTypesControlText(rule.ArgPattern, req.Arg) {
		return false
	}
	return matchArg(req, rule.ArgPattern)
}

func shellRequestHasControlText(req PermissionRequest) bool {
	if !strings.EqualFold(strings.TrimSpace(req.Tool), "run_shell") &&
		!strings.EqualFold(strings.TrimSpace(req.Tool), "run_code") &&
		!strings.EqualFold(strings.TrimSpace(req.Category), "shell") {
		return false
	}
	return len(controlMarkers(req.Arg)) > 0
}

// controlMarkers returns the shell/control separators that occur in text.
// Longer operators are checked first so `&&` does not also require a lone
// ampersand to appear in a caller's explicitly typed allow pattern.
func controlMarkers(text string) []string {
	markers := make([]string, 0, 8)
	remaining := text
	for _, marker := range []string{"\r\n", "\r", "\n", "\x00", "&&", "||", ";", "|", "&", "<", ">", "`", "$("} {
		if !strings.Contains(remaining, marker) {
			continue
		}
		markers = append(markers, marker)
		remaining = strings.ReplaceAll(remaining, marker, "")
	}
	return markers
}

func allowPatternTypesControlText(pattern, arg string) bool {
	for _, marker := range controlMarkers(arg) {
		if !strings.Contains(pattern, marker) {
			return false
		}
	}
	return true
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
