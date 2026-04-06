package review

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/odvcencio/buckley/pkg/tools"
)

// PRTools provides tools for PR review verification.
type PRTools struct {
	prCtx *PRContext
}

// NewPRTools creates PR tools with the given context.
func NewPRTools(prCtx *PRContext) *PRTools {
	return &PRTools{prCtx: prCtx}
}

// Definitions returns the tool definitions for PR review.
func (p *PRTools) Definitions() []tools.Definition {
	return []tools.Definition{
		{
			Name:        "get_file_content",
			Description: "Get the content of a file from the PR's head branch. Use this to see the full context of changed code.",
			Parameters: tools.Schema{
				Type: "object",
				Properties: map[string]tools.Property{
					"path": {
						Type:        "string",
						Description: "Path to the file in the repository",
					},
				},
				Required: []string{"path"},
			},
		},
		{
			Name:        "get_ci_logs",
			Description: "Get logs from a specific CI check. Use this to understand why a check failed.",
			Parameters: tools.Schema{
				Type: "object",
				Properties: map[string]tools.Property{
					"check_name": {
						Type:        "string",
						Description: "Name of the CI check to get logs for",
					},
				},
				Required: []string{"check_name"},
			},
		},
		{
			Name:        "search_codebase",
			Description: "Search the codebase for a pattern. Use this to find related code, usages, or definitions.",
			Parameters: tools.Schema{
				Type: "object",
				Properties: map[string]tools.Property{
					"pattern": {
						Type:        "string",
						Description: "Search pattern (regex supported)",
					},
					"file_pattern": {
						Type:        "string",
						Description: "Optional: glob pattern for files (e.g., '*.go')",
					},
				},
				Required: []string{"pattern"},
			},
		},
		{
			Name:        "get_pr_review_comments",
			Description: "Get inline review comments on specific files. Use this to see existing feedback on code lines.",
			Parameters: tools.Schema{
				Type: "object",
				Properties: map[string]tools.Property{},
			},
		},
		{
			Name:        "check_merge_conflicts",
			Description: "Check if the PR has merge conflicts with the base branch.",
			Parameters: tools.Schema{
				Type: "object",
				Properties: map[string]tools.Property{},
			},
		},
	}
}

// Execute runs a PR tool and returns its output.
func (p *PRTools) Execute(name string, args json.RawMessage) (string, error) {
	var params map[string]any
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}

	switch name {
	case "get_file_content":
		return p.getFileContent(params)
	case "get_ci_logs":
		return p.getCILogs(params)
	case "search_codebase":
		return p.searchCodebase(params)
	case "get_pr_review_comments":
		return p.getPRReviewComments()
	case "check_merge_conflicts":
		return p.checkMergeConflicts()
	default:
		return "", fmt.Errorf("unknown tool: %s", name)
	}
}

func (p *PRTools) getFileContent(params map[string]any) (string, error) {
	path, _ := params["path"].(string)
	if path == "" {
		return "", fmt.Errorf("path is required")
	}

	// Use gh to get file content from the PR's head
	prNum := strconv.Itoa(p.prCtx.PR.Number)
	cmd := exec.Command("gh", "pr", "view", prNum, "--json", "headRefName,headRepository,headRepositoryOwner")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get PR info: %w", err)
	}

	var prData struct {
		HeadRefName         string `json:"headRefName"`
		HeadRepository      struct{ Name string } `json:"headRepository"`
		HeadRepositoryOwner struct{ Login string } `json:"headRepositoryOwner"`
	}
	if err := json.Unmarshal(output, &prData); err != nil {
		return "", fmt.Errorf("failed to parse PR info: %w", err)
	}

	// Get file content from the branch
	ref := prData.HeadRefName
	cmd = exec.Command("gh", "api", fmt.Sprintf("repos/{owner}/{repo}/contents/%s?ref=%s", path, ref), "--jq", ".content")
	output, err = cmd.Output()
	if err != nil {
		// Fallback: try to read from local if we have the repo
		cmd = exec.Command("git", "show", fmt.Sprintf("origin/%s:%s", ref, path))
		output, err = cmd.Output()
		if err != nil {
			return "", fmt.Errorf("failed to get file content: %w", err)
		}
		return fmt.Sprintf("File: %s\n\n%s", path, string(output)), nil
	}

	// Decode base64 content
	content := strings.TrimSpace(string(output))
	// gh api with --jq already decodes, but just in case
	return fmt.Sprintf("File: %s\n\n%s", path, content), nil
}

func (p *PRTools) getCILogs(params map[string]any) (string, error) {
	checkName, _ := params["check_name"].(string)
	if checkName == "" {
		return "", fmt.Errorf("check_name is required")
	}

	// Find the check in our context
	var targetCheck *PRCheck
	for _, c := range p.prCtx.Checks {
		if strings.EqualFold(c.Name, checkName) || strings.Contains(strings.ToLower(c.Name), strings.ToLower(checkName)) {
			targetCheck = &c
			break
		}
	}

	if targetCheck == nil {
		available := make([]string, len(p.prCtx.Checks))
		for i, c := range p.prCtx.Checks {
			available[i] = c.Name
		}
		return fmt.Sprintf("Check '%s' not found. Available checks: %s", checkName, strings.Join(available, ", ")), nil
	}

	// Get check run details - gh doesn't have a direct way to get logs, but we can get the URL
	prNum := strconv.Itoa(p.prCtx.PR.Number)
	cmd := exec.Command("gh", "pr", "checks", prNum, "--json", "name,detailsUrl")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Sprintf("Check: %s\nStatus: %s\nConclusion: %s\n\n(Unable to fetch detailed logs)", targetCheck.Name, targetCheck.Status, targetCheck.Conclusion), nil
	}

	var checks []struct {
		Name       string `json:"name"`
		DetailsURL string `json:"detailsUrl"`
	}
	if err := json.Unmarshal(output, &checks); err != nil {
		return fmt.Sprintf("Check: %s\nStatus: %s\nConclusion: %s", targetCheck.Name, targetCheck.Status, targetCheck.Conclusion), nil
	}

	for _, c := range checks {
		if strings.EqualFold(c.Name, checkName) || strings.Contains(strings.ToLower(c.Name), strings.ToLower(checkName)) {
			return fmt.Sprintf("Check: %s\nStatus: %s\nConclusion: %s\nDetails: %s", c.Name, targetCheck.Status, targetCheck.Conclusion, c.DetailsURL), nil
		}
	}

	return fmt.Sprintf("Check: %s\nStatus: %s\nConclusion: %s", targetCheck.Name, targetCheck.Status, targetCheck.Conclusion), nil
}

func (p *PRTools) searchCodebase(params map[string]any) (string, error) {
	pattern, _ := params["pattern"].(string)
	if pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}

	// Use gh api to search code in the repo
	// This requires the repo context, which we get from the PR
	args := []string{"api", "search/code", "-X", "GET", "-f", fmt.Sprintf("q=%s+repo:{owner}/{repo}", pattern), "--jq", ".items[:10] | .[] | \"\\(.path):\\(.text_matches[0].fragment // \"match\")\""}

	cmd := exec.Command("gh", args...)
	output, err := cmd.Output()
	if err != nil {
		// Fallback to local grep if available
		grepArgs := []string{"-rn", "--include=*.go", "--include=*.ts", "--include=*.js", "-m", "10", pattern, "."}
		if fp, ok := params["file_pattern"].(string); ok && fp != "" {
			grepArgs = []string{"-rn", "--include=" + fp, "-m", "10", pattern, "."}
		}
		cmd = exec.Command("grep", grepArgs...)
		output, err = cmd.Output()
		if err != nil {
			return fmt.Sprintf("No matches found for pattern: %s", pattern), nil
		}
	}

	result := strings.TrimSpace(string(output))
	if result == "" {
		return fmt.Sprintf("No matches found for pattern: %s", pattern), nil
	}

	lines := strings.Split(result, "\n")
	if len(lines) > 10 {
		lines = lines[:10]
		result = strings.Join(lines, "\n") + "\n... (truncated)"
	}

	return fmt.Sprintf("Search results for '%s':\n\n%s", pattern, result), nil
}

func (p *PRTools) getPRReviewComments() (string, error) {
	prNum := strconv.Itoa(p.prCtx.PR.Number)

	cmd := exec.Command("gh", "api", fmt.Sprintf("repos/{owner}/{repo}/pulls/%s/comments", prNum), "--jq", ".[] | \"\\(.path):\\(.line // .original_line) - @\\(.user.login): \\(.body[:200])\"")
	output, err := cmd.Output()
	if err != nil {
		return "No inline review comments found.", nil
	}

	result := strings.TrimSpace(string(output))
	if result == "" {
		return "No inline review comments found.", nil
	}

	return fmt.Sprintf("Inline review comments:\n\n%s", result), nil
}

func (p *PRTools) checkMergeConflicts() (string, error) {
	prNum := strconv.Itoa(p.prCtx.PR.Number)

	cmd := exec.Command("gh", "pr", "view", prNum, "--json", "mergeable,mergeStateStatus")
	output, err := cmd.Output()
	if err != nil {
		return "Unable to check merge status.", nil
	}

	var data struct {
		Mergeable        string `json:"mergeable"`
		MergeStateStatus string `json:"mergeStateStatus"`
	}
	if err := json.Unmarshal(output, &data); err != nil {
		return "Unable to parse merge status.", nil
	}

	switch data.Mergeable {
	case "MERGEABLE":
		return fmt.Sprintf("✓ No merge conflicts. Status: %s", data.MergeStateStatus), nil
	case "CONFLICTING":
		return "✗ PR has merge conflicts that need to be resolved.", nil
	case "UNKNOWN":
		return fmt.Sprintf("Merge status unknown (GitHub may still be checking). Status: %s", data.MergeStateStatus), nil
	default:
		return fmt.Sprintf("Merge status: %s (%s)", data.Mergeable, data.MergeStateStatus), nil
	}
}
