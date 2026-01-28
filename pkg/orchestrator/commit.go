package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/model"
)

type CommitGenerator struct {
	modelClient ModelClient
	cfg         *config.Config
	ctx         context.Context
}

func NewCommitGenerator(mgr ModelClient, cfg *config.Config) *CommitGenerator {
	return &CommitGenerator{
		modelClient: mgr,
		cfg:         cfg,
		ctx:         context.Background(),
	}
}

func (cg *CommitGenerator) baseContext() context.Context {
	if cg == nil || cg.ctx == nil {
		return context.Background()
	}
	return cg.ctx
}

// SetContext updates the base context for commit generation.
func (cg *CommitGenerator) SetContext(ctx context.Context) {
	if cg == nil || ctx == nil {
		return
	}
	cg.ctx = ctx
}

type CommitInfo struct {
	Type     string   // add, fix, update, refactor, etc.
	Scope    string   // optional scope
	Subject  string   // short summary
	Body     string   // detailed description
	Breaking bool     // breaking change
	Issues   []string // related issue numbers
	Files    []string // files changed
	Diff     string   // git diff output
}

var commitSystemPrompt = `You are a git commit message generator using action(scope): summary format.

Generate commit messages in this format:

<action>[optional scope]: <summary>

[body]

[optional footer(s)]

Use a clear action verb (e.g., add, fix, update, improve)

Rules:
- Summary: concise, no period, ~50 chars
- Body: REQUIRED. Explain what and why, not how. Prefer a bullet list and match detail to diff size.
- Breaking changes: add BREAKING CHANGE: in footer
- Reference issues: Closes #123

Output JSON:
{
  "action": "add",
  "scope": "api",
  "summary": "user authentication",
  "body": "- Implement JWT-based authentication\n- Add middleware for protected routes",
  "breaking": false,
  "issues": ["123"]
}`

func (cg *CommitGenerator) Generate(task *Task) (*CommitInfo, error) {
	if task == nil {
		return nil, fmt.Errorf("task cannot be nil")
	}

	// Get git diff
	diff, err := cg.getDiff()
	if err != nil {
		return nil, fmt.Errorf("failed to get diff: %w", err)
	}

	// Get changed files
	files, err := cg.getChangedFiles()
	if err != nil {
		return nil, fmt.Errorf("failed to get changed files: %w", err)
	}

	if len(files) == 0 {
		return nil, fmt.Errorf("no staged files to commit")
	}

	stats, err := cg.getDiffStats()
	if err != nil {
		stats.Files = len(files)
	} else if stats.Files == 0 {
		stats.Files = len(files)
	}
	detail := commitMessageDetailForStats(stats)

	// Generate commit message
	prompt := cg.buildCommitPrompt(task, diff, files, stats, detail)

	reqCtx, cancel := context.WithTimeout(cg.baseContext(), 30*time.Second)
	defer cancel()

	req := model.ChatRequest{
		Model: cg.getUtilityModel(),
		Messages: []model.Message{
			{Role: "system", Content: commitSystemPrompt},
			{Role: "user", Content: prompt},
		},
		Temperature: 0.2,
	}

	resp, err := cg.modelClient.ChatCompletion(reqCtx, req)
	if err != nil {
		return nil, fmt.Errorf("commit generation failed: %w", err)
	}

	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("no response from model")
	}

	// Parse response into CommitInfo
	content, err := model.ExtractTextContent(resp.Choices[0].Message.Content)
	if err != nil {
		return nil, fmt.Errorf("failed to extract content: %w", err)
	}
	commit, err := cg.parseCommitMessage(content)
	if err != nil {
		return nil, fmt.Errorf("failed to parse commit message: %w", err)
	}

	commit.Files = files
	commit.Diff = diff

	return commit, nil
}

type diffStats struct {
	Files        int
	Insertions   int
	Deletions    int
	TotalChanges int
	BinaryFiles  int
}

type commitMessageDetail struct {
	MinBodyLines    int
	TargetBodyLines int
	MaxBodyLines    int
}

func commitMessageDetailForStats(stats diffStats) commitMessageDetail {
	total := stats.TotalChanges
	files := stats.Files

	var detail commitMessageDetail
	switch {
	case total <= 20 && files <= 2:
		detail = commitMessageDetail{MinBodyLines: 1, TargetBodyLines: 2, MaxBodyLines: 3}
	case total <= 80 && files <= 10:
		detail = commitMessageDetail{MinBodyLines: 2, TargetBodyLines: 3, MaxBodyLines: 5}
	case total <= 200 && files <= 25:
		detail = commitMessageDetail{MinBodyLines: 3, TargetBodyLines: 5, MaxBodyLines: 7}
	case total <= 500 && files <= 40:
		detail = commitMessageDetail{MinBodyLines: 5, TargetBodyLines: 6, MaxBodyLines: 9}
	default:
		detail = commitMessageDetail{MinBodyLines: 6, TargetBodyLines: 8, MaxBodyLines: 12}
	}

	switch {
	case files >= 50 && detail.MinBodyLines < 6:
		detail.MinBodyLines = 6
	case files >= 25 && detail.MinBodyLines < 5:
		detail.MinBodyLines = 5
	case files >= 10 && detail.MinBodyLines < 4:
		detail.MinBodyLines = 4
	}

	if detail.TargetBodyLines < detail.MinBodyLines {
		detail.TargetBodyLines = detail.MinBodyLines
	}
	if detail.MaxBodyLines < detail.TargetBodyLines {
		detail.MaxBodyLines = detail.TargetBodyLines
	}
	if detail.MaxBodyLines > 12 {
		detail.MaxBodyLines = 12
	}
	if detail.TargetBodyLines > detail.MaxBodyLines {
		detail.TargetBodyLines = detail.MaxBodyLines
	}

	return detail
}

func (cg *CommitGenerator) buildCommitPrompt(task *Task, diff string, files []string, stats diffStats, detail commitMessageDetail) string {
	var b strings.Builder

	b.WriteString("Generate an action-style commit message for this change:\n\n")
	b.WriteString(fmt.Sprintf("**Task:** %s\n\n", task.Title))
	b.WriteString(fmt.Sprintf("**Description:** %s\n\n", task.Description))

	b.WriteString("**Diff Summary:**\n")
	b.WriteString(fmt.Sprintf("- Files changed: %d\n", stats.Files))
	b.WriteString(fmt.Sprintf("- Insertions: %d\n", stats.Insertions))
	b.WriteString(fmt.Sprintf("- Deletions: %d\n", stats.Deletions))
	b.WriteString(fmt.Sprintf("- Total changed lines: %d\n", stats.TotalChanges))
	b.WriteString(fmt.Sprintf("- Commit body detail: target ~%d lines (min %d, max %d)\n\n", detail.TargetBodyLines, detail.MinBodyLines, detail.MaxBodyLines))

	b.WriteString("**Files changed:**\n")
	for _, file := range files {
		b.WriteString(fmt.Sprintf("- %s\n", file))
	}
	b.WriteString("\n")

	// Include abbreviated diff if not too long
	if len(diff) < 2000 {
		b.WriteString("**Diff:**\n```\n")
		b.WriteString(diff)
		b.WriteString("\n```\n\n")
	} else {
		b.WriteString("**Diff:** (too large, see file list)\n\n")
	}

	b.WriteString(fmt.Sprintf("Generate an action-style commit message following the specification.\nBody must be included and should have at least %d non-empty line(s) (prefer \"- \" bullets).\n", detail.MinBodyLines))

	return b.String()
}

func (cg *CommitGenerator) parseCommitMessage(content string) (*CommitInfo, error) {
	commit := &CommitInfo{}

	// Try to parse as JSON first
	jsonStr := extractJSON(content)
	if jsonStr != "" {
		var jsonData struct {
			Action   string   `json:"action"`
			Type     string   `json:"type"`
			Scope    string   `json:"scope"`
			Summary  string   `json:"summary"`
			Subject  string   `json:"subject"`
			Body     string   `json:"body"`
			Breaking bool     `json:"breaking"`
			Issues   []string `json:"issues"`
		}

		if err := json.Unmarshal([]byte(jsonStr), &jsonData); err == nil {
			action := jsonData.Action
			if action == "" {
				action = jsonData.Type
			}
			summary := jsonData.Summary
			if summary == "" {
				summary = jsonData.Subject
			}
			commit.Type = normalizeCommitAction(action)
			commit.Scope = strings.TrimSpace(jsonData.Scope)
			commit.Subject = strings.TrimSpace(summary)
			commit.Body = jsonData.Body
			commit.Breaking = jsonData.Breaking
			commit.Issues = jsonData.Issues
			return commit, nil
		}
	}

	// Fallback: parse as action-style commit text format
	lines := strings.Split(content, "\n")
	if len(lines) > 0 {
		// Parse first line: action(scope): summary
		firstLine := strings.TrimSpace(lines[0])

		// Skip markdown code block markers
		if strings.HasPrefix(firstLine, "```") {
			if len(lines) > 1 {
				firstLine = strings.TrimSpace(lines[1])
			}
		}

		if strings.Contains(firstLine, ":") {
			parts := strings.SplitN(firstLine, ":", 2)
			typeScope := strings.TrimSpace(parts[0])
			commit.Subject = strings.TrimSpace(parts[1])

			if strings.Contains(typeScope, "(") {
				// Has scope
				typeParts := strings.SplitN(typeScope, "(", 2)
				commit.Type = strings.TrimSpace(typeParts[0])
				commit.Scope = strings.TrimSpace(strings.TrimSuffix(typeParts[1], ")"))
			} else {
				commit.Type = typeScope
			}
		}
	}

	// Parse body (lines after first blank line)
	inBody := false
	var bodyLines []string
	for i := 1; i < len(lines); i++ {
		line := lines[i]

		// Skip code block markers
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			continue
		}

		if line == "" && !inBody {
			inBody = true
			continue
		}
		if inBody {
			// Check for BREAKING CHANGE
			if strings.HasPrefix(line, "BREAKING CHANGE:") {
				commit.Breaking = true
			}
			// Check for issue references
			if strings.Contains(line, "Closes #") || strings.Contains(line, "Fixes #") {
				// Extract issue numbers
				words := strings.Fields(line)
				for _, word := range words {
					if strings.HasPrefix(word, "#") {
						commit.Issues = append(commit.Issues, strings.TrimPrefix(word, "#"))
					}
				}
			}
			bodyLines = append(bodyLines, line)
		}
	}

	commit.Body = strings.Join(bodyLines, "\n")

	// Ensure we have at least a type and subject
	commit.Type = normalizeCommitAction(commit.Type)
	if commit.Type == "" {
		commit.Type = "update"
	}
	if commit.Subject == "" {
		commit.Subject = "staged changes"
	}

	return commit, nil
}

var commitActionAliases = map[string]string{
	"feat":        "add",
	"feature":     "add",
	"fix":         "fix",
	"docs":        "update",
	"doc":         "update",
	"style":       "format",
	"refactor":    "refactor",
	"perf":        "improve",
	"performance": "improve",
	"test":        "test",
	"tests":       "test",
	"build":       "build",
	"ci":          "update",
	"chore":       "update",
	"revert":      "revert",
}

func normalizeCommitAction(action string) string {
	action = strings.ToLower(strings.TrimSpace(action))
	if action == "" {
		return ""
	}
	if mapped, ok := commitActionAliases[action]; ok {
		return mapped
	}
	return action
}

// extractJSON finds JSON in a markdown code block or raw text
func extractJSON(text string) string {
	// Try to find JSON in code blocks
	if strings.Contains(text, "```json") {
		start := strings.Index(text, "```json") + 7
		end := strings.Index(text[start:], "```")
		if end > 0 {
			return strings.TrimSpace(text[start : start+end])
		}
	}

	// Try to find raw JSON object
	start := strings.Index(text, "{")
	if start >= 0 {
		end := strings.LastIndex(text, "}")
		if end > start {
			return strings.TrimSpace(text[start : end+1])
		}
	}

	return ""
}

func (cg *CommitGenerator) getDiff() (string, error) {
	// Stage all changes automatically
	addCmd := exec.Command("git", "add", "-A")
	if err := addCmd.Run(); err != nil {
		return "", fmt.Errorf("failed to stage changes: %w", err)
	}

	// Get diff of staged changes
	cmd := exec.Command("git", "diff", "--staged")
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}

	diff := string(output)
	if diff == "" {
		return "", fmt.Errorf("no changes to commit after staging")
	}

	return diff, nil
}

func (cg *CommitGenerator) getChangedFiles() ([]string, error) {
	cmd := exec.Command("git", "diff", "--staged", "--name-only")
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	files := []string{}
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			files = append(files, line)
		}
	}

	return files, nil
}

func (cg *CommitGenerator) getDiffStats() (diffStats, error) {
	cmd := exec.Command("git", "diff", "--staged", "--numstat")
	output, err := cmd.Output()
	if err != nil {
		return diffStats{}, err
	}

	stats := diffStats{}
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) < 3 {
			continue
		}
		stats.Files++

		insertions, okIns := parseNumstatField(parts[0])
		deletions, okDel := parseNumstatField(parts[1])
		if !okIns || !okDel {
			stats.BinaryFiles++
		}
		stats.Insertions += insertions
		stats.Deletions += deletions
	}
	stats.TotalChanges = stats.Insertions + stats.Deletions
	return stats, nil
}

func parseNumstatField(field string) (int, bool) {
	field = strings.TrimSpace(field)
	if field == "-" || field == "" {
		return 0, false
	}
	value, err := strconv.Atoi(field)
	if err != nil || value < 0 {
		return 0, false
	}
	return value, true
}

func (cg *CommitGenerator) FormatCommitMessage(commit *CommitInfo) string {
	var b strings.Builder

	// First line: action(scope): summary
	if commit.Scope != "" {
		b.WriteString(fmt.Sprintf("%s(%s): %s\n", commit.Type, commit.Scope, commit.Subject))
	} else {
		b.WriteString(fmt.Sprintf("%s: %s\n", commit.Type, commit.Subject))
	}

	// Blank line
	if commit.Body != "" || commit.Breaking || len(commit.Issues) > 0 {
		b.WriteString("\n")
	}

	// Body
	if commit.Body != "" {
		b.WriteString(commit.Body)
		b.WriteString("\n")
	}

	// Footer
	if commit.Breaking {
		b.WriteString("\nBREAKING CHANGE: ")
		b.WriteString(commit.Body)
		b.WriteString("\n")
	}

	if len(commit.Issues) > 0 {
		b.WriteString("\n")
		for _, issue := range commit.Issues {
			b.WriteString(fmt.Sprintf("Closes #%s\n", issue))
		}
	}

	return b.String()
}

func (cg *CommitGenerator) Commit(commit *CommitInfo) error {
	// Stage files
	for _, file := range commit.Files {
		cmd := exec.Command("git", "add", file)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to stage %s: %w", file, err)
		}
	}

	// Create commit
	message := cg.FormatCommitMessage(commit)
	tmp, err := os.CreateTemp("", "buckley-commit-*.txt")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(tmpPath)

	if err := os.WriteFile(tmpPath, []byte(message+"\n"), 0o600); err != nil {
		return fmt.Errorf("write commit message: %w", err)
	}

	cmd := exec.Command("git", "commit", "-F", tmpPath)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to commit: %w", err)
	}

	return nil
}

// getUtilityModel returns the configured utility model for commit messages
func (cg *CommitGenerator) getUtilityModel() string {
	if cg.cfg != nil {
		return cg.cfg.GetUtilityCommitModel()
	}
	return config.DefaultUtilityModel
}
