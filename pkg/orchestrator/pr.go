package orchestrator

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/model"
)

type PRCreator struct {
	modelClient ModelClient
	cfg         *config.Config
}

func NewPRCreator(mgr ModelClient, cfg *config.Config) *PRCreator {
	return &PRCreator{
		modelClient: mgr,
		cfg:         cfg,
	}
}

type PRInfo struct {
	Title       string
	Description string
	BaseBranch  string
	HeadBranch  string
	Labels      []string
	Reviewers   []string
	URL         string
}

var prSystemPrompt = `You are a pull request description generator.

Generate a comprehensive PR description with:
- Clear summary of changes
- Motivation and context
- Type of change (feature, bugfix, etc.)
- Testing performed
- Checklist items

Format in markdown. Be specific and detailed.`

func (pc *PRCreator) GeneratePR(plan *Plan) (*PRInfo, error) {
	if plan == nil {
		return nil, fmt.Errorf("plan cannot be nil")
	}

	// Get git info
	baseBranch, err := pc.getBaseBranch()
	if err != nil {
		baseBranch = "main"
	}

	headBranch, err := pc.getCurrentBranch()
	if err != nil {
		return nil, fmt.Errorf("failed to get current branch: %w", err)
	}

	if headBranch == baseBranch {
		return nil, fmt.Errorf("cannot create PR from %s to itself, please create a feature branch first", baseBranch)
	}

	// Get commit history
	commits, err := pc.getCommitsSince(baseBranch)
	if err != nil {
		commits = []string{}
	}

	if len(commits) == 0 {
		return nil, fmt.Errorf("no commits to create PR from")
	}

	// Generate PR description
	description, err := pc.generateDescription(plan, commits)
	if err != nil {
		return nil, fmt.Errorf("failed to generate description: %w", err)
	}

	// Generate title from plan
	title := fmt.Sprintf("feat: %s", plan.FeatureName)

	pr := &PRInfo{
		Title:       title,
		Description: description,
		BaseBranch:  baseBranch,
		HeadBranch:  headBranch,
		Labels:      pc.suggestLabels(plan),
	}

	return pr, nil
}

func (pc *PRCreator) generateDescription(plan *Plan, commits []string) (string, error) {
	prompt := pc.buildPRPrompt(plan, commits)

	reqCtx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	req := model.ChatRequest{
		Model: pc.getUtilityModel(),
		Messages: []model.Message{
			{Role: "system", Content: prSystemPrompt},
			{Role: "user", Content: prompt},
		},
		Temperature: 0.3,
	}

	resp, err := pc.modelClient.ChatCompletion(reqCtx, req)
	if err != nil {
		return "", fmt.Errorf("PR description generation failed: %w", err)
	}

	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("no response from model")
	}

	return model.ExtractTextContent(resp.Choices[0].Message.Content)
}

func (pc *PRCreator) buildPRPrompt(plan *Plan, commits []string) string {
	var b strings.Builder

	b.WriteString("Generate a pull request description for this feature:\n\n")
	b.WriteString(fmt.Sprintf("**Feature:** %s\n\n", plan.FeatureName))
	b.WriteString(fmt.Sprintf("**Description:** %s\n\n", plan.Description))
	if plan.Logs.BaseDir != "" {
		b.WriteString(fmt.Sprintf("**Agent Logs:** %s (builder/review/research)\n\n", plan.Logs.BaseDir))
	}

	b.WriteString(fmt.Sprintf("**Tasks completed:** %d\n", len(plan.Tasks)))
	b.WriteString("**Task breakdown:**\n")
	for _, task := range plan.Tasks {
		b.WriteString(fmt.Sprintf("- %s\n", task.Title))
	}
	b.WriteString("\n")

	if summary := strings.TrimSpace(plan.Context.ResearchSummary); summary != "" {
		b.WriteString("**Research Summary:**\n")
		b.WriteString(summary)
		b.WriteString("\n\n")
		if len(plan.Context.ResearchRisks) > 0 {
			b.WriteString("**Top Risks:**\n")
			for _, risk := range plan.Context.ResearchRisks {
				b.WriteString(fmt.Sprintf("- %s\n", risk))
			}
			b.WriteString("\n")
		}
		if plan.Context.ResearchLogPath != "" {
			b.WriteString(fmt.Sprintf("_Research log_: %s\n\n", plan.Context.ResearchLogPath))
		}
	}

	if len(commits) > 0 {
		b.WriteString("**Commits:**\n")
		for _, commit := range commits {
			b.WriteString(fmt.Sprintf("- %s\n", commit))
		}
		b.WriteString("\n")
	}

	b.WriteString("Generate a detailed PR description with summary, testing, and checklist.")

	return b.String()
}

func (pc *PRCreator) suggestLabels(plan *Plan) []string {
	labels := []string{}

	// Determine labels based on task types
	hasTests := false
	hasDocs := false

	for _, task := range plan.Tasks {
		taskLower := strings.ToLower(task.Title)
		if strings.Contains(taskLower, "test") {
			hasTests = true
		}
		if strings.Contains(taskLower, "doc") || strings.Contains(taskLower, "readme") {
			hasDocs = true
		}
	}

	labels = append(labels, "enhancement")

	if hasTests {
		labels = append(labels, "tests")
	}

	if hasDocs {
		labels = append(labels, "documentation")
	}

	return labels
}

func (pc *PRCreator) CreatePR(pr *PRInfo) error {
	// Push current branch
	cmd := exec.Command("git", "push", "-u", "origin", pr.HeadBranch)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to push branch: %w", err)
	}

	// Create PR using gh CLI
	args := []string{
		"pr", "create",
		"--title", pr.Title,
		"--body", pr.Description,
		"--base", pr.BaseBranch,
	}

	// Add labels
	for _, label := range pr.Labels {
		args = append(args, "--label", label)
	}

	// Add reviewers
	for _, reviewer := range pr.Reviewers {
		args = append(args, "--reviewer", reviewer)
	}

	cmd = exec.Command("gh", args...)
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to create PR: %w", err)
	}

	// Extract PR URL from output
	pr.URL = strings.TrimSpace(string(output))

	return nil
}

func (pc *PRCreator) getBaseBranch() (string, error) {
	// Try to determine base branch from git config or common names
	branches := []string{"main", "master", "develop"}

	for _, branch := range branches {
		cmd := exec.Command("git", "rev-parse", "--verify", branch)
		if err := cmd.Run(); err == nil {
			return branch, nil
		}
	}

	return "", fmt.Errorf("could not determine base branch")
}

func (pc *PRCreator) getCurrentBranch() (string, error) {
	cmd := exec.Command("git", "branch", "--show-current")
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func (pc *PRCreator) getCommitsSince(baseBranch string) ([]string, error) {
	cmd := exec.Command("git", "log", fmt.Sprintf("%s..HEAD", baseBranch), "--oneline")
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	commits := []string{}
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			commits = append(commits, line)
		}
	}

	return commits, nil
}

// getUtilityModel returns the configured utility model for PR descriptions
func (pc *PRCreator) getUtilityModel() string {
	if pc.cfg != nil {
		return pc.cfg.GetUtilityPRModel()
	}
	return config.DefaultUtilityModel
}
