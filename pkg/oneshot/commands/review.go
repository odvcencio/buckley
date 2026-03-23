package commands

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/odvcencio/buckley/pkg/oneshot"
	"github.com/odvcencio/buckley/pkg/tools"
)

// ReviewDefinition implements oneshot.Definition for code review.
//
// Review is more complex than commit/pr: it produces a free-form markdown
// review rather than a structured tool call. The tool is used to wrap the
// review output so we get structured data (grade, findings, verdict).
type ReviewDefinition struct {
	// BaseBranch overrides automatic base branch detection.
	BaseBranch string
}

// ReviewResult is the structured output from the generate_review tool.
type ReviewResult struct {
	Grade        string   `json:"grade"`
	Summary      string   `json:"summary"`
	FindingsJSON string   `json:"findings_json,omitempty"`
	Remarks      []string `json:"remarks,omitempty"`
	Approved     bool     `json:"approved"`
	Blockers     []string `json:"blockers,omitempty"`
	Suggestions  []string `json:"suggestions,omitempty"`
	Review       string   `json:"review"`
}

// Findings parses the JSON findings string into structured data.
func (rr ReviewResult) Findings() ([]ReviewFinding, error) {
	if rr.FindingsJSON == "" {
		return nil, nil
	}
	var findings []ReviewFinding
	if err := json.Unmarshal([]byte(rr.FindingsJSON), &findings); err != nil {
		return nil, fmt.Errorf("parse findings: %w", err)
	}
	return findings, nil
}

// ReviewFinding is a single issue found during review.
type ReviewFinding struct {
	ID       string `json:"id"`
	Severity string `json:"severity"` // CRITICAL, MAJOR, MINOR
	Title    string `json:"title"`
	File     string `json:"file,omitempty"`
	Line     int    `json:"line,omitempty"`
	Evidence string `json:"evidence,omitempty"`
	Impact   string `json:"impact,omitempty"`
	Fix      string `json:"fix,omitempty"`
}

func (ReviewDefinition) Name() string { return "review" }

func (ReviewDefinition) Tool() tools.Definition {
	return tools.Definition{
		Name:        "generate_review",
		Description: "Generate a structured code review with grade, findings, and verdict",
		Parameters: tools.ObjectSchema(
			map[string]tools.Property{
				"grade": tools.StringEnumProperty(
					"Overall code quality grade",
					"A", "B", "C", "D", "F",
				),
				"summary": tools.StringProperty(
					"2-4 sentence summary of the review",
				),
				"findings_json": tools.StringProperty(
					`JSON array of findings. Each finding: {"id":"FINDING-001","severity":"CRITICAL|MAJOR|MINOR","title":"...","file":"...","line":0,"evidence":"...","impact":"...","fix":"..."}`,
				),
				"remarks": tools.ArrayProperty(
					"Positive observations or minor notes",
					tools.StringProperty("A single remark"),
				),
				"approved": tools.BoolProperty(
					"Whether the code is approved for merge",
				),
				"blockers": tools.ArrayProperty(
					"Finding IDs that block approval",
					tools.StringProperty("Finding ID"),
				),
				"suggestions": tools.ArrayProperty(
					"Finding IDs that are suggestions only",
					tools.StringProperty("Finding ID"),
				),
				"review": tools.StringProperty(
					"Full review in markdown format",
				),
			},
			"grade", "summary", "approved", "review",
		),
	}
}

func (d ReviewDefinition) ContextSources() []oneshot.ContextSource {
	base := d.BaseBranch
	if base == "" {
		base = "main"
	}
	return []oneshot.ContextSource{
		{Type: "git_diff", Params: map[string]string{"base": base}},
		{Type: "git_log", Params: map[string]string{"base": base}},
		{Type: "git_files", Params: map[string]string{"base": base}},
		{Type: "agents_md"},
	}
}

func (ReviewDefinition) SystemPrompt() string {
	return `You are a senior code reviewer. Analyze the changes thoroughly and produce a structured review.

IMPORTANT: You MUST call the generate_review tool with your response.

Review criteria:
1. Correctness: Does the code do what it claims?
2. Security: Any vulnerabilities or unsafe patterns?
3. Performance: Any unnecessary allocations, N+1 queries, etc.?
4. Maintainability: Is the code clear, well-structured, and testable?
5. Architecture: Does it follow project conventions?

For each finding:
- Provide evidence (quote the actual code)
- Explain the business/technical impact
- Suggest a concrete fix

Grade the code:
- A: Excellent, ready to merge
- B: Good, minor improvements possible
- C: Acceptable, has issues that should be addressed
- D: Needs work, significant problems
- F: Critical issues, do not merge

Be precise and evidence-based. Avoid vague observations.`
}

func (ReviewDefinition) BuildPrompt(ctx *oneshot.Context) string {
	var b strings.Builder

	b.WriteString("Review the following changes.\n\n")

	if log, ok := ctx.Sources["git_log:main"]; ok && log != "" {
		b.WriteString("## Commits\n\n```\n")
		b.WriteString(log)
		b.WriteString("\n```\n\n")
	}

	if files, ok := ctx.Sources["git_files:main"]; ok && files != "" {
		b.WriteString("## Changed Files\n\n")
		b.WriteString(files)
		b.WriteString("\n\n")
	}

	if diff, ok := ctx.Sources["git_diff:main"]; ok && diff != "" {
		b.WriteString("## Full Diff\n\n```diff\n")
		b.WriteString(diff)
		b.WriteString("\n```\n\n")
	}

	if agents, ok := ctx.Sources["agents_md"]; ok && agents != "" {
		b.WriteString("## Project Guidelines (AGENTS.md)\n\n")
		b.WriteString(agents)
		b.WriteString("\n\n")
	}

	b.WriteString("Call the generate_review tool with the complete review.")

	return b.String()
}

func (ReviewDefinition) Validate(result json.RawMessage) error {
	var rr ReviewResult
	if err := json.Unmarshal(result, &rr); err != nil {
		return fmt.Errorf("unmarshal: %w", err)
	}
	if rr.Grade == "" {
		return fmt.Errorf("grade is required")
	}
	validGrades := map[string]bool{"A": true, "B": true, "C": true, "D": true, "F": true}
	if !validGrades[rr.Grade] {
		return fmt.Errorf("invalid grade: %s (expected A-F)", rr.Grade)
	}
	if strings.TrimSpace(rr.Summary) == "" {
		return fmt.Errorf("summary is required")
	}
	if strings.TrimSpace(rr.Review) == "" {
		return fmt.Errorf("review text is required")
	}
	return nil
}

func (ReviewDefinition) Unmarshal(result json.RawMessage) (any, error) {
	var rr ReviewResult
	if err := json.Unmarshal(result, &rr); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	return &rr, nil
}
