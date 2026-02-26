package github

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

const defaultGhTimeout = 30 * time.Second

//go:generate mockgen -package=github -destination=mock_runner_test.go github.com/odvcencio/buckley/pkg/github commandRunner
type commandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	output, err := cmd.Output()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return output, fmt.Errorf("%s command timed out", name)
		}
		if exitErr, ok := err.(*exec.ExitError); ok && len(exitErr.Stderr) > 0 {
			return output, fmt.Errorf("%s", strings.TrimSpace(string(exitErr.Stderr)))
		}
		return output, err
	}
	return output, nil
}

// CLI wraps the GitHub CLI (gh) tool
type CLI struct {
	authenticated bool
	timeout       time.Duration
	runner        commandRunner
}

// Issue represents a GitHub issue
type Issue struct {
	Number    int      `json:"number"`
	Title     string   `json:"title"`
	State     string   `json:"state"`
	Assignees []string `json:"assignees"`
	URL       string   `json:"url"`
}

// PullRequest represents a GitHub pull request
type PullRequest struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	State  string `json:"state"`
	URL    string `json:"url"`
	Head   string `json:"headRefName"`
	Base   string `json:"baseRefName"`
}

// NewCLI creates a new GitHub CLI wrapper with sane timeouts.
func NewCLI() *CLI {
	return &CLI{
		timeout: defaultGhTimeout,
		runner:  execRunner{},
	}
}

// EnsureAuthenticated checks if the GitHub CLI is authenticated
func (gh *CLI) EnsureAuthenticated() error {
	if gh.authenticated {
		return nil
	}
	if _, err := gh.run("auth", "status"); err != nil {
		return fmt.Errorf("github cli not authenticated; please run: gh auth login (%w)", err)
	}
	gh.authenticated = true
	return nil
}

// IsInstalled checks if the GitHub CLI is installed
func (gh *CLI) IsInstalled() bool {
	_, err := gh.run("--version")
	return err == nil
}

// CreatePR creates a pull request
func (gh *CLI) CreatePR(title, body, base string) (string, error) {
	if err := gh.EnsureAuthenticated(); err != nil {
		return "", err
	}

	args := []string{"pr", "create", "--title", title, "--body", body}
	if base != "" {
		args = append(args, "--base", base)
	}

	output, err := gh.run(args...)
	if err != nil {
		return "", fmt.Errorf("failed to create PR: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) == 0 {
		return "", fmt.Errorf("no output received from gh pr create")
	}
	prURL := lines[len(lines)-1]

	return prURL, nil
}

// ListIssues lists issues with optional filters
func (gh *CLI) ListIssues(filters map[string]string) ([]Issue, error) {
	if err := gh.EnsureAuthenticated(); err != nil {
		return nil, err
	}

	args := []string{"issue", "list", "--json", "number,title,state,assignees,url"}

	if state, ok := filters["state"]; ok {
		args = append(args, "--state", state)
	}
	if assignee, ok := filters["assignee"]; ok {
		args = append(args, "--assignee", assignee)
	}
	if label, ok := filters["label"]; ok {
		args = append(args, "--label", label)
	}

	output, err := gh.run(args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list issues: %w", err)
	}

	var issues []Issue
	if err := json.Unmarshal(output, &issues); err != nil {
		return nil, fmt.Errorf("failed to parse issues: %w", err)
	}

	return issues, nil
}

// ListPRs lists pull requests with optional filters
func (gh *CLI) ListPRs(filters map[string]string) ([]PullRequest, error) {
	if err := gh.EnsureAuthenticated(); err != nil {
		return nil, err
	}

	args := []string{"pr", "list", "--json", "number,title,state,url,headRefName,baseRefName"}

	if state, ok := filters["state"]; ok {
		args = append(args, "--state", state)
	}
	if base, ok := filters["base"]; ok {
		args = append(args, "--base", base)
	}
	if head, ok := filters["head"]; ok {
		args = append(args, "--head", head)
	}

	output, err := gh.run(args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list PRs: %w", err)
	}

	var prs []PullRequest
	if err := json.Unmarshal(output, &prs); err != nil {
		return nil, fmt.Errorf("failed to parse PRs: %w", err)
	}

	return prs, nil
}

// GetPR gets details about a specific pull request
func (gh *CLI) GetPR(number int) (*PullRequest, error) {
	if err := gh.EnsureAuthenticated(); err != nil {
		return nil, err
	}

	args := []string{"pr", "view", fmt.Sprintf("%d", number),
		"--json", "number,title,state,url,headRefName,baseRefName"}
	output, err := gh.run(args...)
	if err != nil {
		return nil, fmt.Errorf("failed to get PR: %w", err)
	}

	var pr PullRequest
	if err := json.Unmarshal(output, &pr); err != nil {
		return nil, fmt.Errorf("failed to parse PR: %w", err)
	}

	return &pr, nil
}

// MergePR merges a pull request
func (gh *CLI) MergePR(number int, method string) error {
	if err := gh.EnsureAuthenticated(); err != nil {
		return err
	}

	args := []string{"pr", "merge", fmt.Sprintf("%d", number)}
	if method != "" {
		args = append(args, "--"+method)
	}

	if _, err := gh.run(args...); err != nil {
		return fmt.Errorf("failed to merge PR: %w", err)
	}

	return nil
}

// ClosePR closes a pull request
func (gh *CLI) ClosePR(number int) error {
	if err := gh.EnsureAuthenticated(); err != nil {
		return err
	}

	if _, err := gh.run("pr", "close", fmt.Sprintf("%d", number)); err != nil {
		return fmt.Errorf("failed to close PR: %w", err)
	}

	return nil
}

func (gh *CLI) run(args ...string) ([]byte, error) {
	timeout := gh.timeout
	if timeout <= 0 {
		timeout = defaultGhTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if gh.runner == nil {
		gh.runner = execRunner{}
	}

	output, err := gh.runner.Run(ctx, "gh", args...)
	if err != nil {
		return nil, fmt.Errorf("gh %s: %w", strings.Join(args, " "), err)
	}
	return output, nil
}
