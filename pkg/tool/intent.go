package tool

import (
	"fmt"
	"strings"
	"time"
)

// Intent represents a statement of what the agent is about to do
type Intent struct {
	Phase        string    // "planning", "execution", "review"
	Activity     string    // High-level activity description
	Tools        []string  // Tools that will be used
	ExpectedTime string    // Estimated time (e.g., "~30 seconds")
	Timestamp    time.Time // When intent was declared
}

// IntentBuilder helps construct intent statements
type IntentBuilder struct {
	phase    string
	activity string
	tools    []string
	params   map[string]any
}

// NewIntentBuilder creates a new intent builder
func NewIntentBuilder(phase string) *IntentBuilder {
	return &IntentBuilder{
		phase:  phase,
		tools:  []string{},
		params: make(map[string]any),
	}
}

// Activity sets the high-level activity description
func (b *IntentBuilder) Activity(activity string) *IntentBuilder {
	b.activity = activity
	return b
}

// AddTool adds a tool to the intent
func (b *IntentBuilder) AddTool(toolName string) *IntentBuilder {
	b.tools = append(b.tools, toolName)
	return b
}

// AddParam adds a parameter for template substitution
func (b *IntentBuilder) AddParam(key string, value any) *IntentBuilder {
	b.params[key] = value
	return b
}

// Build creates the intent statement
func (b *IntentBuilder) Build() Intent {
	// Substitute parameters in activity description
	activity := b.activity
	for key, value := range b.params {
		placeholder := fmt.Sprintf("{%s}", key)
		activity = strings.ReplaceAll(activity, placeholder, fmt.Sprintf("%v", value))
	}

	// Estimate time based on tool count
	expectedTime := b.estimateTime()

	return Intent{
		Phase:        b.phase,
		Activity:     activity,
		Tools:        b.tools,
		ExpectedTime: expectedTime,
		Timestamp:    time.Now(),
	}
}

// estimateTime estimates execution time based on tools
func (b *IntentBuilder) estimateTime() string {
	if len(b.tools) == 0 {
		return "~5 seconds"
	}

	// Rough heuristics
	toolCount := len(b.tools)

	if toolCount == 1 {
		return "~10 seconds"
	} else if toolCount <= 3 {
		return "~30 seconds"
	} else if toolCount <= 5 {
		return "~1 minute"
	} else {
		return "~2 minutes"
	}
}

// IntentFormatter formats intent statements for display
type IntentFormatter struct{}

// NewIntentFormatter creates a new intent formatter
func NewIntentFormatter() *IntentFormatter {
	return &IntentFormatter{}
}

// Format formats an intent for display
func (f *IntentFormatter) Format(intent Intent) string {
	return fmt.Sprintf("[Intent] %s", intent.Activity)
}

// FormatWithDetails formats an intent with detailed information
func (f *IntentFormatter) FormatWithDetails(intent Intent) string {
	var b strings.Builder

	b.WriteString(fmt.Sprintf("[Intent] %s\n", intent.Activity))

	if len(intent.Tools) > 0 {
		toolList := strings.Join(intent.Tools, ", ")
		b.WriteString(fmt.Sprintf("Expected: %s, using %s", intent.ExpectedTime, toolList))
	}

	return b.String()
}

// FormatCompact creates a compact single-line intent
func (f *IntentFormatter) FormatCompact(intent Intent) string {
	if len(intent.Tools) == 0 {
		return fmt.Sprintf("[Intent] %s", intent.Activity)
	}

	toolCount := len(intent.Tools)
	if toolCount == 1 {
		return fmt.Sprintf("[Intent] %s (using %s)", intent.Activity, intent.Tools[0])
	}

	return fmt.Sprintf("[Intent] %s (using %d tools)", intent.Activity, toolCount)
}

// IntentLibrary provides pre-built intent templates for common activities
type IntentLibrary struct{}

// NewIntentLibrary creates a new intent library
func NewIntentLibrary() *IntentLibrary {
	return &IntentLibrary{}
}

// AnalyzingCodebase creates an intent for codebase analysis
func (l *IntentLibrary) AnalyzingCodebase(phase string, target string) Intent {
	return NewIntentBuilder(phase).
		Activity(fmt.Sprintf("Analyzing %s", target)).
		AddTool("read").
		AddTool("grep").
		Build()
}

// ImplementingTask creates an intent for implementing a task
func (l *IntentLibrary) ImplementingTask(taskID int, description string) Intent {
	return NewIntentBuilder("execution").
		Activity(fmt.Sprintf("Implementing Task %d: %s", taskID, description)).
		AddTool("write").
		AddTool("edit").
		Build()
}

// RunningTests creates an intent for running tests
func (l *IntentLibrary) RunningTests(testPath string) Intent {
	return NewIntentBuilder("execution").
		Activity(fmt.Sprintf("Running tests in %s", testPath)).
		AddTool("run_tests").
		Build()
}

// ValidatingImplementation creates an intent for review validation
func (l *IntentLibrary) ValidatingImplementation(aspect string) Intent {
	return NewIntentBuilder("review").
		Activity(fmt.Sprintf("Validating %s", aspect)).
		AddTool("read").
		AddTool("analyze_complexity").
		Build()
}

// GeneratingArtifact creates an intent for artifact generation
func (l *IntentLibrary) GeneratingArtifact(artifactType string) Intent {
	return NewIntentBuilder("planning").
		Activity(fmt.Sprintf("Generating %s artifact", artifactType)).
		Build()
}

// SearchingForPattern creates an intent for pattern searching
func (l *IntentLibrary) SearchingForPattern(pattern string, scope string) Intent {
	return NewIntentBuilder("planning").
		Activity(fmt.Sprintf("Searching for '%s' in %s", pattern, scope)).
		AddTool("grep").
		AddParam("pattern", pattern).
		AddParam("scope", scope).
		Build()
}

// RefactoringCode creates an intent for refactoring
func (l *IntentLibrary) RefactoringCode(description string) Intent {
	return NewIntentBuilder("execution").
		Activity(fmt.Sprintf("Refactoring: %s", description)).
		AddTool("rename_symbol").
		AddTool("extract_function").
		Build()
}

// FixingIssue creates an intent for fixing a review issue
func (l *IntentLibrary) FixingIssue(issueID int, title string) Intent {
	return NewIntentBuilder("execution").
		Activity(fmt.Sprintf("Fixing issue #%d: %s", issueID, title)).
		AddTool("edit").
		Build()
}

// IntentHistory tracks intent statements for a session
type IntentHistory struct {
	intents []Intent
}

// NewIntentHistory creates a new intent history
func NewIntentHistory() *IntentHistory {
	return &IntentHistory{
		intents: []Intent{},
	}
}

// Add adds an intent to the history
func (h *IntentHistory) Add(intent Intent) {
	h.intents = append(h.intents, intent)
}

// GetAll returns all intents
func (h *IntentHistory) GetAll() []Intent {
	return h.intents
}

// GetByPhase returns intents for a specific phase
func (h *IntentHistory) GetByPhase(phase string) []Intent {
	var filtered []Intent
	for _, intent := range h.intents {
		if intent.Phase == phase {
			filtered = append(filtered, intent)
		}
	}
	return filtered
}

// GetRecent returns the N most recent intents
func (h *IntentHistory) GetRecent(n int) []Intent {
	if n >= len(h.intents) {
		return h.intents
	}
	return h.intents[len(h.intents)-n:]
}

// GetLatest returns the most recent intent
func (h *IntentHistory) GetLatest() *Intent {
	if len(h.intents) == 0 {
		return nil
	}
	return &h.intents[len(h.intents)-1]
}

// Clear clears the intent history
func (h *IntentHistory) Clear() {
	h.intents = []Intent{}
}

// Count returns the number of intents
func (h *IntentHistory) Count() int {
	return len(h.intents)
}
