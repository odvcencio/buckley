// Package thinking provides extended reasoning mode detection and configuration.
//
// Users can request deeper reasoning using keywords in their prompts:
//   - "think" - Extra reasoning tokens, moderate depth
//   - "think hard" - Structured planning, deeper analysis
//   - "think harder" - Extended reasoning with multiple approaches
//   - "ultrathink" - Maximum computation, comprehensive analysis
package thinking

import (
	"regexp"
	"strings"
)

// Mode represents a level of extended reasoning.
type Mode int

const (
	// ModeNormal is default reasoning without extended thinking.
	ModeNormal Mode = iota

	// ModeThink adds extra reasoning tokens for moderate depth.
	// Equivalent to Claude Code's "think" keyword.
	ModeThink

	// ModeThinkHard enables structured planning and deeper analysis.
	// Equivalent to Claude Code's "think hard" keyword.
	ModeThinkHard

	// ModeThinkHarder extends reasoning with multiple approaches.
	// Equivalent to Claude Code's "think harder" keyword.
	ModeThinkHarder

	// ModeUltrathink enables maximum computation and comprehensive analysis.
	// Equivalent to Claude Code's "ultrathink" keyword.
	ModeUltrathink
)

// String returns the mode name.
func (m Mode) String() string {
	switch m {
	case ModeNormal:
		return "normal"
	case ModeThink:
		return "think"
	case ModeThinkHard:
		return "think-hard"
	case ModeThinkHarder:
		return "think-harder"
	case ModeUltrathink:
		return "ultrathink"
	default:
		return "unknown"
	}
}

// Label returns a human-readable label for UI display.
func (m Mode) Label() string {
	switch m {
	case ModeNormal:
		return ""
	case ModeThink:
		return "Thinking"
	case ModeThinkHard:
		return "Thinking Hard"
	case ModeThinkHarder:
		return "Thinking Harder"
	case ModeUltrathink:
		return "Ultrathinking"
	default:
		return ""
	}
}

// Config returns model configuration adjustments for the thinking mode.
type Config struct {
	// ReasoningEffort for models that support it (low/medium/high)
	ReasoningEffort string

	// Temperature adjustment (-1 means use default)
	Temperature float64

	// MaxTokens multiplier for response length
	MaxTokensMultiplier float64

	// SystemPromptAddition to prepend to system prompt
	SystemPromptAddition string

	// BudgetTokens for extended thinking budget (for Claude models)
	BudgetTokens int
}

// GetConfig returns the configuration for a thinking mode.
func (m Mode) GetConfig() Config {
	switch m {
	case ModeThink:
		return Config{
			ReasoningEffort:      "medium",
			Temperature:          -1, // Use default
			MaxTokensMultiplier:  1.5,
			SystemPromptAddition: thinkPrompt,
			BudgetTokens:         4000,
		}
	case ModeThinkHard:
		return Config{
			ReasoningEffort:      "high",
			Temperature:          -1,
			MaxTokensMultiplier:  2.0,
			SystemPromptAddition: thinkHardPrompt,
			BudgetTokens:         8000,
		}
	case ModeThinkHarder:
		return Config{
			ReasoningEffort:      "high",
			Temperature:          -1,
			MaxTokensMultiplier:  2.5,
			SystemPromptAddition: thinkHarderPrompt,
			BudgetTokens:         12000,
		}
	case ModeUltrathink:
		return Config{
			ReasoningEffort:      "high",
			Temperature:          -1,
			MaxTokensMultiplier:  3.0,
			SystemPromptAddition: ultrathinkPrompt,
			BudgetTokens:         16000,
		}
	default:
		return Config{
			ReasoningEffort:     "",
			Temperature:         -1,
			MaxTokensMultiplier: 1.0,
			BudgetTokens:        0,
		}
	}
}

// Detection patterns for thinking keywords
var (
	ultrathinkPattern  = regexp.MustCompile(`(?i)\bultrathink\b`)
	thinkHarderPattern = regexp.MustCompile(`(?i)\bthink\s+harder\b`)
	thinkHardPattern   = regexp.MustCompile(`(?i)\bthink\s+hard\b`)
	thinkPattern       = regexp.MustCompile(`(?i)\bthink\b`)

	// Explicit mode commands
	modeCommandPattern = regexp.MustCompile(`(?i)^/think\s*(hard|harder|ultra)?$`)
)

// DetectMode analyzes input text to detect thinking mode keywords.
// Returns the detected mode and the input with keywords removed.
func DetectMode(input string) (Mode, string) {
	// Check for ultrathink first (most specific)
	if ultrathinkPattern.MatchString(input) {
		cleaned := ultrathinkPattern.ReplaceAllString(input, "")
		return ModeUltrathink, strings.TrimSpace(cleaned)
	}

	// Check for "think harder"
	if thinkHarderPattern.MatchString(input) {
		cleaned := thinkHarderPattern.ReplaceAllString(input, "")
		return ModeThinkHarder, strings.TrimSpace(cleaned)
	}

	// Check for "think hard"
	if thinkHardPattern.MatchString(input) {
		cleaned := thinkHardPattern.ReplaceAllString(input, "")
		return ModeThinkHard, strings.TrimSpace(cleaned)
	}

	// Check for standalone "think"
	// Only match if it's clearly a thinking directive, not part of other phrases
	if isThinkingDirective(input) {
		cleaned := thinkPattern.ReplaceAllString(input, "")
		return ModeThink, strings.TrimSpace(cleaned)
	}

	return ModeNormal, input
}

// isThinkingDirective determines if "think" is used as a directive
// rather than part of natural language like "I think we should..."
func isThinkingDirective(input string) bool {
	lower := strings.ToLower(input)

	// Common phrases where "think" is NOT a directive - check these first
	naturalPhrases := []string{
		"i think",
		"don't think",
		"do you think",
		"what do you think",
		"let me think",
		"thinking about",
		"think about",
		"think of",
		"think that",
		"think it",
		"think we",
		"think this",
	}

	for _, phrase := range naturalPhrases {
		if strings.Contains(lower, phrase) {
			return false
		}
	}

	// Strong directive patterns that indicate thinking mode
	directivePatterns := []string{
		`^think[,.]?\s`,   // "think, " or "think. " at start
		`\bplease think`,  // Explicit request
		`\bnow think`,     // Sequential instruction
		`\band think`,     // Conjunction
		`\bthen think`,    // Sequential
		`\bthink[.!?]\s`,  // "think. " mid-sentence
		`\bthink[.!?]$`,   // "think." at end
		`[.!?]\s+think\b`, // After sentence boundary
	}

	for _, p := range directivePatterns {
		matched, _ := regexp.MatchString(`(?i)`+p, input)
		if matched {
			return true
		}
	}

	return false
}

// ParseModeCommand parses a /think command and returns the mode.
// Returns ModeNormal if not a valid think command.
func ParseModeCommand(input string) Mode {
	input = strings.TrimSpace(input)

	matches := modeCommandPattern.FindStringSubmatch(input)
	if matches == nil {
		return ModeNormal
	}

	if len(matches) < 2 || matches[1] == "" {
		return ModeThink
	}

	switch strings.ToLower(matches[1]) {
	case "hard":
		return ModeThinkHard
	case "harder":
		return ModeThinkHarder
	case "ultra":
		return ModeUltrathink
	default:
		return ModeThink
	}
}

// Prompt additions for each thinking mode
const thinkPrompt = `
<thinking_mode>
Extended thinking mode is active. Take extra time to reason through this problem:
- Consider multiple angles before responding
- Show your reasoning process
- Validate your conclusions
</thinking_mode>
`

const thinkHardPrompt = `
<thinking_mode>
Deep thinking mode is active. Apply structured reasoning:
1. First, understand the full scope of the problem
2. Break it down into components
3. Consider edge cases and potential issues
4. Develop a clear plan before implementing
5. Validate your approach against requirements
</thinking_mode>
`

const thinkHarderPrompt = `
<thinking_mode>
Extended deep thinking mode is active. Apply comprehensive analysis:
1. Thoroughly understand the problem from multiple perspectives
2. Consider at least 2-3 different approaches
3. Evaluate trade-offs between approaches
4. Identify potential pitfalls and mitigations
5. Select the optimal approach with clear justification
6. Plan implementation in detail before proceeding
7. Consider how to verify correctness
</thinking_mode>
`

const ultrathinkPrompt = `
<thinking_mode>
Maximum thinking mode is active. Apply exhaustive analysis:

## Phase 1: Problem Understanding
- Restate the problem in your own words
- Identify all explicit and implicit requirements
- Note any ambiguities or assumptions

## Phase 2: Exploration
- Generate at least 3 distinct approaches
- For each approach, identify:
  - Pros and cons
  - Implementation complexity
  - Risk factors
  - Resource requirements

## Phase 3: Analysis
- Compare approaches systematically
- Consider long-term implications
- Identify the optimal solution with detailed justification

## Phase 4: Planning
- Create a detailed implementation plan
- Include verification steps
- Anticipate potential issues and prepare mitigations

## Phase 5: Execution
- Proceed methodically through the plan
- Validate each step before proceeding
- Document any deviations from the plan

Take all the time and tokens needed to think through this comprehensively.
</thinking_mode>
`
