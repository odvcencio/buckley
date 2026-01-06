package tool

// Impact represents the impact level of a tool operation
type Impact string

const (
	ImpactReadOnly    Impact = "readonly"    // read, grep, glob - no modifications
	ImpactModifying   Impact = "modifying"   // edit, write - modifies files
	ImpactDestructive Impact = "destructive" // delete, git reset - destructive operations
)

// Cost represents the resource cost of a tool operation
type Cost string

const (
	CostFree      Cost = "free"      // bash, read - no API calls
	CostCheap     Cost = "cheap"     // grep, glob - minimal processing
	CostExpensive Cost = "expensive" // search (embeddings), compaction - uses LLM
)

// Category represents the functional category of a tool
type Category string

const (
	CategoryCodebase      Category = "codebase"      // grep, find_symbol, search
	CategoryGit           Category = "git"           // git_status, git_diff, git_log
	CategoryTesting       Category = "testing"       // run_tests, generate_test
	CategoryFilesystem    Category = "filesystem"    // read, write, edit, list
	CategoryRefactoring   Category = "refactoring"   // rename_symbol, extract_function
	CategoryDocumentation Category = "documentation" // generate_docstring, explain_code
	CategoryAnalysis      Category = "analysis"      // analyze_complexity, find_duplicates
	CategoryDelegation    Category = "delegation"    // buckley, subagent, codex
	CategoryBrowser       Category = "browser"       // headless_browse
	CategoryShell         Category = "shell"         // shell_command
	CategoryPlanning      Category = "planning"      // todo, checkpoint
)

// ToolMetadata contains rich metadata for enhanced UI display
type ToolMetadata struct {
	Category     Category // Functional category
	Intent       string   // Template for intent display: "Searching for {pattern} in {path}"
	Summary      string   // Template for result summary: "Found {count} matches in {files} files"
	Impact       Impact   // Operation impact level
	Cost         Cost     // Resource cost
	ExampleUsage string   // Example of how to use this tool
}

// DefaultMetadata returns default metadata for tools without explicit metadata
func DefaultMetadata() ToolMetadata {
	return ToolMetadata{
		Category: CategoryFilesystem,
		Intent:   "Executing {tool}",
		Summary:  "Completed {tool}",
		Impact:   ImpactReadOnly,
		Cost:     CostFree,
	}
}

// RichTool is an optional interface that tools can implement for enhanced UI
// Tools that don't implement this will use default metadata
type RichTool interface {
	Tool
	Metadata() ToolMetadata
}

// GetMetadata returns metadata for a tool, with fallback to defaults
func GetMetadata(t Tool) ToolMetadata {
	if rt, ok := t.(RichTool); ok {
		return rt.Metadata()
	}
	return inferMetadata(t)
}

// inferMetadata attempts to infer reasonable metadata from tool name and description
func inferMetadata(t Tool) ToolMetadata {
	metadata := DefaultMetadata()
	name := t.Name()

	// Infer category from name
	switch {
	case contains(name, "git"):
		metadata.Category = CategoryGit
		metadata.Intent = "Running git operation"
		metadata.Summary = "Git operation completed"
	case contains(name, "test"):
		metadata.Category = CategoryTesting
		metadata.Intent = "Running tests"
		metadata.Summary = "Tests completed"
	case contains(name, "search", "grep", "find"):
		metadata.Category = CategoryCodebase
		metadata.Impact = ImpactReadOnly
		metadata.Intent = "Searching codebase"
		metadata.Summary = "Search completed"
	case contains(name, "read", "list", "get", "status"):
		metadata.Category = CategoryFilesystem
		metadata.Impact = ImpactReadOnly
		metadata.Intent = "Reading file"
		metadata.Summary = "Read completed"
	case contains(name, "write", "patch", "edit"):
		metadata.Category = CategoryFilesystem
		metadata.Impact = ImpactModifying
		metadata.Intent = "Modifying file"
		metadata.Summary = "File modified"
	case contains(name, "delete", "remove"):
		metadata.Category = CategoryFilesystem
		metadata.Impact = ImpactDestructive
		metadata.Intent = "Deleting file"
		metadata.Summary = "File deleted"
	case contains(name, "rename", "extract", "refactor"):
		metadata.Category = CategoryRefactoring
		metadata.Impact = ImpactModifying
		metadata.Intent = "Refactoring code"
		metadata.Summary = "Refactoring completed"
	case contains(name, "generate_docstring", "explain"):
		metadata.Category = CategoryDocumentation
		metadata.Intent = "Generating documentation"
		metadata.Summary = "Documentation generated"
	case contains(name, "analyze", "complexity", "duplicate"):
		metadata.Category = CategoryAnalysis
		metadata.Intent = "Analyzing code"
		metadata.Summary = "Analysis completed"
	case contains(name, "buckley", "subagent", "codex", "claude"):
		metadata.Category = CategoryDelegation
		metadata.Cost = CostExpensive
		metadata.Intent = "Delegating to agent"
		metadata.Summary = "Agent task completed"
	case contains(name, "browse", "browser"):
		metadata.Category = CategoryBrowser
		metadata.Intent = "Browsing URL"
		metadata.Summary = "Browser task completed"
	case contains(name, "shell", "command"):
		metadata.Category = CategoryShell
		metadata.Impact = ImpactDestructive // Shell commands can do anything
		metadata.Intent = "Executing shell command"
		metadata.Summary = "Command executed"
	case contains(name, "todo", "checkpoint"):
		metadata.Category = CategoryPlanning
		metadata.Intent = "Managing tasks"
		metadata.Summary = "Task list updated"
	}

	return metadata
}

// contains checks if any of the substrings are in the string (case insensitive)
func contains(s string, substrings ...string) bool {
	lower := toLower(s)
	for _, substr := range substrings {
		if containsSubstring(lower, toLower(substr)) {
			return true
		}
	}
	return false
}

func toLower(s string) string {
	result := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			result[i] = c + ('a' - 'A')
		} else {
			result[i] = c
		}
	}
	return string(result)
}

func containsSubstring(s, substr string) bool {
	if len(substr) == 0 {
		return true
	}
	if len(s) < len(substr) {
		return false
	}
	for i := 0; i <= len(s)-len(substr); i++ {
		match := true
		for j := 0; j < len(substr); j++ {
			if s[i+j] != substr[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
