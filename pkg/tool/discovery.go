package tool

import (
	"sort"
	"strings"
	"unicode"

	"m31labs.dev/buckley/pkg/tool/builtin"
)

// DefaultDiscoveryTools is the small, general-purpose tool prefix sent on
// every request. Specialized tools remain installed and can be exposed by
// discover_tools without bloating every model turn.
var DefaultDiscoveryTools = []string{
	"discover_tools",
	"read_file",
	"search_text",
	"find_files",
	"run_shell",
	"write_file",
	"patch_file",
	"run_tests",
	"activate_skill",
	"compact_context",
}

// EnableDynamicDiscovery limits model-visible schemas to a working set. It is
// safe to call before plugins are loaded because discovery reads the registry
// at execution time.
func (r *Registry) EnableDynamicDiscovery(core []string) {
	if r == nil {
		return
	}
	if core == nil {
		core = DefaultDiscoveryTools
	}
	r.mu.Lock()
	r.discoveryEnabled = true
	r.discoveryCore = make(map[string]struct{}, len(core)+1)
	r.discoveryExposed = make(map[string]struct{}, len(core)+1)
	for _, name := range core {
		if name = strings.TrimSpace(name); name != "" {
			r.discoveryCore[name] = struct{}{}
			r.discoveryExposed[name] = struct{}{}
		}
	}
	r.discoveryCore["discover_tools"] = struct{}{}
	r.discoveryExposed["discover_tools"] = struct{}{}
	r.tools["discover_tools"] = &DiscoveryTool{registry: r}
	r.mu.Unlock()
}

func (r *Registry) filterDiscoveredToolNames(governed []string) []string {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if !r.discoveryEnabled {
		return governed
	}
	visible := make([]string, 0, len(governed))
	for _, name := range governed {
		if _, ok := r.discoveryExposed[name]; ok {
			visible = append(visible, name)
		}
	}
	return visible
}

func (r *Registry) exposeTools(names ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.discoveryExposed = make(map[string]struct{}, len(r.discoveryCore)+len(names))
	for name := range r.discoveryCore {
		r.discoveryExposed[name] = struct{}{}
	}
	for _, name := range names {
		if _, ok := r.tools[name]; ok {
			r.discoveryExposed[name] = struct{}{}
		}
	}
}

// DiscoveryTool searches installed tool metadata and exposes the best matches
// for subsequent model turns.
type DiscoveryTool struct {
	registry *Registry
}

func (t *DiscoveryTool) Name() string { return "discover_tools" }

func (t *DiscoveryTool) Description() string {
	return "Find and enable specialized tools only when needed. Search by capability, task, or exact tool name."
}

func (t *DiscoveryTool) Parameters() builtin.ParameterSchema {
	return builtin.ParameterSchema{
		Type: "object",
		Properties: map[string]builtin.PropertySchema{
			"query": {Type: "string", Description: "Capability to find, such as git history, browser, refactor, or delegation"},
			"names": {Type: "array", Description: "Exact tool names to enable", Items: &builtin.PropertySchema{Type: "string", Description: "Tool name"}},
			"limit": {Type: "integer", Description: "Maximum search matches to enable", Default: 5},
		},
	}
}

type discoveryMatch struct {
	tool  Tool
	score int
}

func (t *DiscoveryTool) Execute(params map[string]any) (*builtin.Result, error) {
	if t == nil || t.registry == nil {
		return &builtin.Result{Success: false, Error: "tool registry unavailable"}, nil
	}
	query, _ := params["query"].(string)
	wanted := stringSlice(params["names"])
	limit := intParam(params["limit"], 5)
	if limit < 1 {
		limit = 1
	}
	if limit > 12 {
		limit = 12
	}

	matches := t.find(query, wanted)
	if len(matches) > limit {
		matches = matches[:limit]
	}
	names := make([]string, 0, len(matches))
	items := make([]map[string]any, 0, len(matches))
	for _, match := range matches {
		name := match.tool.Name()
		names = append(names, name)
		items = append(items, map[string]any{
			"name":        name,
			"description": match.tool.Description(),
		})
	}
	t.registry.exposeTools(names...)
	if len(items) == 0 {
		return &builtin.Result{Success: false, Error: "no matching tools found"}, nil
	}
	return &builtin.Result{Success: true, Data: map[string]any{
		"enabled": items,
		"hint":    "The enabled tool schemas will be available on the next model turn.",
	}}, nil
}

func (t *DiscoveryTool) find(query string, exact []string) []discoveryMatch {
	exactSet := make(map[string]struct{}, len(exact))
	for _, name := range exact {
		exactSet[strings.ToLower(strings.TrimSpace(name))] = struct{}{}
	}
	terms := discoveryTerms(query)
	matches := make([]discoveryMatch, 0)
	for _, candidate := range t.registry.snapshotTools() {
		if candidate == nil || candidate.Name() == t.Name() {
			continue
		}
		name := strings.ToLower(candidate.Name())
		_, exactHit := exactSet[name]
		score := 0
		if exactHit {
			score = 1000
		}
		haystack := name + " " + strings.ToLower(candidate.Description()) + " " + string(GetMetadata(candidate).Category)
		for _, term := range terms {
			if name == term {
				score += 100
			} else if strings.Contains(name, term) {
				score += 25
			} else if strings.Contains(haystack, term) {
				score += 5
			}
		}
		if score > 0 {
			matches = append(matches, discoveryMatch{tool: candidate, score: score})
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score > matches[j].score
		}
		return matches[i].tool.Name() < matches[j].tool.Name()
	})
	return matches
}

func discoveryTerms(query string) []string {
	return strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_'
	})
}

func stringSlice(value any) []string {
	values, ok := value.([]any)
	if !ok {
		if typed, ok := value.([]string); ok {
			return typed
		}
		return nil
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
			result = append(result, text)
		}
	}
	return result
}

func intParam(value any, fallback int) int {
	switch value := value.(type) {
	case int:
		return value
	case float64:
		return int(value)
	default:
		return fallback
	}
}
