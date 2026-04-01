package gts

import "strings"

// Capability describes a gts tool's purpose and when to use it.
type Capability struct {
	Tool        string
	Description string
	UseCases    []string
}

// Capabilities returns all available gts structural analysis tools.
func Capabilities() []Capability {
	return []Capability{
		{
			Tool:        "gts map",
			Description: "Structural summary of files — functions, types, methods with signatures",
			UseCases:    []string{"understanding code structure", "onboarding to unfamiliar code", "finding entry points"},
		},
		{
			Tool:        "gts callgraph",
			Description: "Traverse resolved call graph from root functions",
			UseCases:    []string{"tracing execution flow", "understanding dependencies", "debugging call chains"},
		},
		{
			Tool:        "gts refs",
			Description: "Find all references to a symbol across the codebase",
			UseCases:    []string{"rename safety check", "understanding usage patterns", "impact assessment"},
		},
		{
			Tool:        "gts scope",
			Description: "Resolve all symbols in scope at a specific file and line",
			UseCases:    []string{"understanding available APIs", "debugging import issues", "context at a point"},
		},
		{
			Tool:        "gts context",
			Description: "Pack focused context for a symbol with configurable call depth",
			UseCases:    []string{"deep-diving a function", "understanding a component", "preparing for changes"},
		},
		{
			Tool:        "gts impact",
			Description: "Compute blast radius of changes to specified symbols",
			UseCases:    []string{"pre-change risk assessment", "refactoring safety", "PR review"},
		},
		{
			Tool:        "gts dead",
			Description: "Find callable definitions with zero incoming references",
			UseCases:    []string{"cleanup", "code review", "reducing maintenance burden"},
		},
		{
			Tool:        "gts complexity",
			Description: "AST-based complexity metrics — cyclomatic, cognitive, nesting depth",
			UseCases:    []string{"identifying refactor targets", "code review", "quality assessment"},
		},
		{
			Tool:        "gts hotspot",
			Description: "Detect hotspots from git churn, complexity, and call graph centrality",
			UseCases:    []string{"finding high-risk code", "prioritizing refactoring", "tech debt assessment"},
		},
		{
			Tool:        "gts capa",
			Description: "Detect capabilities from API/import patterns with MITRE ATT&CK mapping",
			UseCases:    []string{"security review", "dependency audit", "threat modeling"},
		},
		{
			Tool:        "gts deps",
			Description: "Analyze import dependency graph at package or file level",
			UseCases:    []string{"architecture review", "circular dependency detection", "module planning"},
		},
		{
			Tool:        "gts similarity",
			Description: "Find similar functions between or within codebases",
			UseCases:    []string{"deduplication", "pattern detection", "code reuse opportunities"},
		},
	}
}

// PromptSection generates a system prompt section describing gts capabilities
// for the given tool list (comma-separated). If tools is empty, returns "".
func PromptSection(tools string) string {
	if tools == "" {
		return ""
	}

	all := make(map[string]Capability, len(Capabilities()))
	for _, c := range Capabilities() {
		// Extract tool name: "gts map" -> "map"
		name := c.Tool
		if len(name) > 4 && name[:4] == "gts " {
			name = name[4:]
		}
		all[name] = c
	}

	var b strings.Builder
	b.WriteString("## Structural Intelligence (gts-suite)\n\n")
	b.WriteString("You have access to structural code analysis tools. Use them proactively to understand code before making changes.\n\n")

	for _, t := range strings.Split(tools, ",") {
		t = strings.TrimSpace(t)
		if cap, ok := all[t]; ok {
			b.WriteString("- **" + cap.Tool + "**: " + cap.Description + "\n")
		}
	}
	b.WriteString("\nRun these via the bash tool. All support --json for structured output.\n")
	return b.String()
}
