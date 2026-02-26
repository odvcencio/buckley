package main

import (
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/conversation"
)

// promptBuilder accumulates prompt sections within a token budget.
// It is used by both the ACP and one-shot system-prompt builders to avoid
// exceeding the model's context window.
type promptBuilder struct {
	b      strings.Builder
	used   int
	budget int
}

// newPromptBuilder creates a builder that tracks token usage against budget.
// A budget <= 0 means unlimited (required sections are always appended,
// optional sections are skipped).
func newPromptBuilder(budget int) *promptBuilder {
	return &promptBuilder{budget: budget}
}

// appendSection appends content if it fits within the remaining budget.
// Required sections are always appended regardless of budget.
func (pb *promptBuilder) appendSection(content string, required bool) {
	if strings.TrimSpace(content) == "" {
		return
	}
	if !required && pb.budget <= 0 {
		return
	}
	tokens := conversation.CountTokens(content)
	if pb.budget > 0 && !required && pb.used+tokens > pb.budget {
		return
	}
	pb.b.WriteString(content)
	pb.used += tokens
}

// remaining returns the number of budget tokens left, or 0 if unlimited.
func (pb *promptBuilder) remaining() int {
	if pb.budget <= 0 {
		return 0
	}
	r := pb.budget - pb.used
	if r < 0 {
		return 0
	}
	return r
}

// String returns the accumulated prompt text.
func (pb *promptBuilder) String() string {
	return pb.b.String()
}

// sessionInfo contains summary data parsed from a Ralph session log.
// Used by list, resume, and cross-project session views.
type sessionInfo struct {
	Project   string
	ID        string
	StartTime time.Time
	EndTime   time.Time
	Status    string
	Prompt    string
	Iters     int
	Cost      float64
}
