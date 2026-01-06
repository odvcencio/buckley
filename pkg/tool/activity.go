package tool

import (
	"fmt"
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/tool/builtin"
)

// ActivityGroupingConfig holds configuration for activity grouping
type ActivityGroupingConfig struct {
	WindowSeconds int // Time window for grouping (default: 30)
	Enabled       bool
}

// DefaultActivityGroupingConfig returns sensible defaults
func DefaultActivityGroupingConfig() ActivityGroupingConfig {
	return ActivityGroupingConfig{
		WindowSeconds: 30,
		Enabled:       true,
	}
}

// ActivityGroup represents a group of related tool calls
type ActivityGroup struct {
	Category  Category   // Category of tools in this group
	StartTime time.Time  // When first tool was called
	EndTime   time.Time  // When last tool was completed
	ToolCalls []ToolCall // Individual tool calls in this group
	Summary   string     // Human-readable summary
}

// ToolCall represents a single tool invocation with timing
type ToolCall struct {
	Tool      Tool
	Params    map[string]any
	Result    *builtin.Result
	StartTime time.Time
	EndTime   time.Time
	Duration  time.Duration
	Metadata  ToolMetadata
}

// ActivityTracker tracks tool calls and groups them for display
type ActivityTracker struct {
	config ActivityGroupingConfig
	calls  []ToolCall
	groups []ActivityGroup
}

// NewActivityTracker creates a new activity tracker
func NewActivityTracker(config ActivityGroupingConfig) *ActivityTracker {
	return &ActivityTracker{
		config: config,
		calls:  []ToolCall{},
		groups: []ActivityGroup{},
	}
}

// RecordCall records a tool call for potential grouping
func (t *ActivityTracker) RecordCall(tool Tool, params map[string]any, result *builtin.Result, startTime, endTime time.Time) {
	call := ToolCall{
		Tool:      tool,
		Params:    params,
		Result:    result,
		StartTime: startTime,
		EndTime:   endTime,
		Duration:  endTime.Sub(startTime),
		Metadata:  GetMetadata(tool),
	}

	t.calls = append(t.calls, call)

	// Try to add to existing group or create new group
	if t.config.Enabled {
		t.tryGroup(call)
	}
}

// tryGroup attempts to add a call to an existing group or creates a new group
func (t *ActivityTracker) tryGroup(call ToolCall) {
	windowDuration := time.Duration(t.config.WindowSeconds) * time.Second

	// Check if we can add to most recent group
	if len(t.groups) > 0 {
		lastGroup := &t.groups[len(t.groups)-1]

		// Can group if:
		// 1. Same category
		// 2. Within time window
		if lastGroup.Category == call.Metadata.Category &&
			call.StartTime.Sub(lastGroup.EndTime) <= windowDuration {
			lastGroup.ToolCalls = append(lastGroup.ToolCalls, call)
			lastGroup.EndTime = call.EndTime
			lastGroup.Summary = t.generateGroupSummary(lastGroup)
			return
		}
	}

	// Create new group
	group := ActivityGroup{
		Category:  call.Metadata.Category,
		StartTime: call.StartTime,
		EndTime:   call.EndTime,
		ToolCalls: []ToolCall{call},
	}
	group.Summary = t.generateGroupSummary(&group)
	t.groups = append(t.groups, group)
}

// GetGroups returns all activity groups
func (t *ActivityTracker) GetGroups() []ActivityGroup {
	return t.groups
}

// GetLatestGroup returns the most recent activity group
func (t *ActivityTracker) GetLatestGroup() *ActivityGroup {
	if len(t.groups) == 0 {
		return nil
	}
	return &t.groups[len(t.groups)-1]
}

// generateGroupSummary creates a human-readable summary for a group
func (t *ActivityTracker) generateGroupSummary(group *ActivityGroup) string {
	if len(group.ToolCalls) == 0 {
		return ""
	}

	// Special handling for single tool call
	if len(group.ToolCalls) == 1 {
		call := group.ToolCalls[0]
		return t.formatSingleCall(call)
	}

	// Group multiple calls
	switch group.Category {
	case CategoryCodebase:
		return t.summarizeCodebaseActivity(group)
	case CategoryFilesystem:
		return t.summarizeFilesystemActivity(group)
	case CategoryGit:
		return t.summarizeGitActivity(group)
	case CategoryTesting:
		return t.summarizeTestingActivity(group)
	default:
		return t.summarizeGenericActivity(group)
	}
}

// formatSingleCall formats a single tool call
func (t *ActivityTracker) formatSingleCall(call ToolCall) string {
	// Try to use tool's summary template
	summary := call.Metadata.Summary

	// Replace common placeholders
	summary = replacePlaceholders(summary, call.Params, call.Result)

	return summary
}

// summarizeCodebaseActivity creates summary for codebase operations
func (t *ActivityTracker) summarizeCodebaseActivity(group *ActivityGroup) string {
	searches := 0
	patterns := []string{}
	filesSearched := 0

	for _, call := range group.ToolCalls {
		if contains(call.Tool.Name(), "search", "grep", "find") {
			searches++
			if pattern, ok := call.Params["pattern"].(string); ok {
				patterns = append(patterns, pattern)
			}
			// Try to extract file count from result
			if call.Result != nil && call.Result.Data != nil {
				if count, ok := call.Result.Data["file_count"].(int); ok {
					filesSearched += count
				}
			}
		}
	}

	if searches == 1 {
		return fmt.Sprintf("Searched for '%s'", patterns[0])
	}

	return fmt.Sprintf("Searched for %d patterns across codebase", len(patterns))
}

// summarizeFilesystemActivity creates summary for filesystem operations
func (t *ActivityTracker) summarizeFilesystemActivity(group *ActivityGroup) string {
	reads := 0
	writes := 0
	filesRead := []string{}
	filesWritten := []string{}

	for _, call := range group.ToolCalls {
		name := call.Tool.Name()

		if contains(name, "read") {
			reads++
			if path, ok := call.Params["file_path"].(string); ok {
				filesRead = append(filesRead, shortPath(path))
			}
		}

		if contains(name, "write", "edit", "patch") {
			writes++
			if path, ok := call.Params["file_path"].(string); ok {
				filesWritten = append(filesWritten, shortPath(path))
			}
		}
	}

	parts := []string{}

	if reads > 0 {
		if reads == 1 && len(filesRead) > 0 {
			parts = append(parts, fmt.Sprintf("Read %s", filesRead[0]))
		} else {
			parts = append(parts, fmt.Sprintf("Read %d files", reads))
		}
	}

	if writes > 0 {
		if writes == 1 && len(filesWritten) > 0 {
			parts = append(parts, fmt.Sprintf("Modified %s", filesWritten[0]))
		} else {
			parts = append(parts, fmt.Sprintf("Modified %d files", writes))
		}
	}

	return strings.Join(parts, ", ")
}

// summarizeGitActivity creates summary for git operations
func (t *ActivityTracker) summarizeGitActivity(group *ActivityGroup) string {
	operations := []string{}

	for _, call := range group.ToolCalls {
		name := call.Tool.Name()
		if contains(name, "status") {
			operations = append(operations, "checked status")
		} else if contains(name, "diff") {
			operations = append(operations, "viewed diff")
		} else if contains(name, "log") {
			operations = append(operations, "viewed log")
		} else if contains(name, "blame") {
			operations = append(operations, "blamed file")
		}
	}

	if len(operations) == 1 {
		return fmt.Sprintf("Git: %s", operations[0])
	}

	return fmt.Sprintf("Git operations: %s", strings.Join(operations, ", "))
}

// summarizeTestingActivity creates summary for testing operations
func (t *ActivityTracker) summarizeTestingActivity(group *ActivityGroup) string {
	testsRun := 0
	testsPassed := 0
	testsFailed := 0

	for _, call := range group.ToolCalls {
		if call.Result != nil && call.Result.Data != nil {
			if count, ok := call.Result.Data["tests_run"].(int); ok {
				testsRun += count
			}
			if count, ok := call.Result.Data["tests_passed"].(int); ok {
				testsPassed += count
			}
			if count, ok := call.Result.Data["tests_failed"].(int); ok {
				testsFailed += count
			}
		}
	}

	if testsRun > 0 {
		if testsFailed == 0 {
			return fmt.Sprintf("Ran %d tests (all passed)", testsRun)
		}
		return fmt.Sprintf("Ran %d tests (%d passed, %d failed)", testsRun, testsPassed, testsFailed)
	}

	return "Ran tests"
}

// summarizeGenericActivity creates summary for other operations
func (t *ActivityTracker) summarizeGenericActivity(group *ActivityGroup) string {
	if len(group.ToolCalls) == 1 {
		return t.formatSingleCall(group.ToolCalls[0])
	}

	return fmt.Sprintf("%d %s operations", len(group.ToolCalls), group.Category)
}

// Helper functions

// replacePlaceholders replaces placeholders in summary templates
func replacePlaceholders(template string, params map[string]any, result *builtin.Result) string {
	s := template

	// Replace parameter placeholders
	for key, value := range params {
		placeholder := fmt.Sprintf("{%s}", key)
		s = strings.ReplaceAll(s, placeholder, fmt.Sprintf("%v", value))
	}

	// Replace result placeholders
	if result != nil && result.Data != nil {
		for key, value := range result.Data {
			placeholder := fmt.Sprintf("{%s}", key)
			s = strings.ReplaceAll(s, placeholder, fmt.Sprintf("%v", value))
		}
	}

	return s
}

// shortPath returns a shortened version of a file path
func shortPath(path string) string {
	parts := strings.Split(path, "/")
	if len(parts) > 2 {
		return fmt.Sprintf(".../%s", parts[len(parts)-1])
	}
	return path
}

// FormatActivityLog formats an activity group for display
func FormatActivityLog(group *ActivityGroup) string {
	var b strings.Builder

	b.WriteString(fmt.Sprintf("[%s] %s\n", group.StartTime.Format("15:04:05"), formatCategoryTitle(group.Category)))

	for _, call := range group.ToolCalls {
		b.WriteString(fmt.Sprintf("├─ %s", call.Tool.Name()))

		// Add key parameters
		keyParams := extractKeyParams(call.Params)
		if keyParams != "" {
			b.WriteString(fmt.Sprintf(" %s", keyParams))
		}

		b.WriteString("\n")
	}

	b.WriteString(fmt.Sprintf("└─ Summary: %s\n", group.Summary))

	return b.String()
}

// formatCategoryTitle formats a category name for display
func formatCategoryTitle(cat Category) string {
	s := string(cat)
	return strings.ToUpper(s[:1]) + s[1:] + " Operations"
}

// extractKeyParams extracts key parameters for display
func extractKeyParams(params map[string]any) string {
	// Extract commonly interesting parameters
	parts := []string{}

	if path, ok := params["file_path"].(string); ok {
		parts = append(parts, shortPath(path))
	}

	if pattern, ok := params["pattern"].(string); ok {
		parts = append(parts, fmt.Sprintf("'%s'", pattern))
	}

	if command, ok := params["command"].(string); ok {
		if len(command) > 50 {
			command = command[:47] + "..."
		}
		parts = append(parts, command)
	}

	return strings.Join(parts, " ")
}
