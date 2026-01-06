package review

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/prompts"
	"github.com/odvcencio/buckley/pkg/transparency"
)

// PRInfo contains parsed PR metadata.
type PRInfo struct {
	Number    int
	Title     string
	Author    string
	State     string
	URL       string
	Body      string
	CIStatus  string
	Labels    []string
	BaseBranch string
	HeadBranch string
	Additions int
	Deletions int
	ChangedFiles int
}

// PRContext contains context for PR review.
type PRContext struct {
	PR       *PRInfo
	Diff     string
	Comments []PRComment
	Checks   []PRCheck
	Files    []string
}

// PRComment represents a PR comment.
type PRComment struct {
	Author string
	Body   string
	Path   string // For review comments
	Line   int
}

// PRCheck represents a CI check result.
type PRCheck struct {
	Name       string
	Status     string
	Conclusion string
}

// ReviewPR reviews a remote PR using gh CLI.
func (r *Runner) ReviewPR(ctx context.Context, prRef string) (*RunResult, error) {
	// Assemble PR context
	prCtx, audit, err := AssemblePRContext(prRef)
	if err != nil {
		return nil, fmt.Errorf("assemble PR context: %w", err)
	}

	// Build prompts
	systemPrompt := prompts.ReviewPRPrompt(time.Now())
	userPrompt := buildPRPrompt(prCtx)

	// Prefer RLM for full tool access
	if r.rlmRunner != nil {
		result, err := r.reviewPRWithRLM(ctx, prCtx, systemPrompt, userPrompt, audit)
		if err != nil {
			return nil, err
		}
		return result, nil
	}

	// Fallback to legacy invoker with custom PR tools
	return r.reviewPRWithLegacyTools(ctx, prCtx, systemPrompt, userPrompt, audit)
}

// reviewPRWithRLM runs a PR review using full RLM tool ecosystem.
func (r *Runner) reviewPRWithRLM(ctx context.Context, prCtx *PRContext, systemPrompt, userPrompt string, audit *transparency.ContextAudit) (*RunResult, error) {
	result := &RunResult{ContextAudit: audit, PRInfo: prCtx.PR}

	// Allow read, glob, grep, bash for verification
	// PR reviews focus on understanding business impact using standard tools
	allowedTools := []string{"read", "glob", "grep", "bash"}

	rlmResult, err := r.rlmRunner.Run(ctx, systemPrompt, userPrompt, allowedTools)
	if err != nil {
		result.Error = err
		return result, nil
	}

	result.Review = rlmResult.Response
	result.Trace = rlmResult.Trace
	return result, nil
}

// reviewPRWithLegacyTools runs a PR review with PR-specific tools (legacy fallback).
func (r *Runner) reviewPRWithLegacyTools(ctx context.Context, prCtx *PRContext, systemPrompt, userPrompt string, audit *transparency.ContextAudit) (*RunResult, error) {
	result := &RunResult{ContextAudit: audit}

	if r.invoker == nil {
		result.Error = fmt.Errorf("no invoker configured")
		return result, nil
	}

	// Create PR tools
	prTools := NewPRTools(prCtx)

	// Run with tools
	response, trace, err := r.invoker.InvokeWithTools(
		ctx,
		systemPrompt,
		userPrompt,
		prTools.Definitions(),
		prTools,
		10, // max iterations
	)
	result.Trace = trace
	result.PRInfo = prCtx.PR

	if err != nil {
		result.Error = err
		return result, nil
	}

	result.Review = response
	return result, nil
}

// AssemblePRContext gathers context for PR review using gh CLI.
func AssemblePRContext(prRef string) (*PRContext, *transparency.ContextAudit, error) {
	audit := transparency.NewContextAudit()
	prCtx := &PRContext{}

	// Parse PR reference (number or URL)
	prNumber, err := parsePRRef(prRef)
	if err != nil {
		return nil, nil, err
	}

	// Get PR details
	pr, err := getPRInfo(prNumber)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get PR info: %w", err)
	}
	prCtx.PR = pr
	audit.Add("PR metadata", estimateTokens(pr.Title+pr.Body))

	// Get PR diff
	diff, err := getPRDiff(prNumber)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get PR diff: %w", err)
	}
	prCtx.Diff = diff
	audit.Add("PR diff", estimateTokens(diff))

	// Get CI checks
	checks, err := getPRChecks(prNumber)
	if err == nil {
		prCtx.Checks = checks
		audit.Add("CI checks", len(checks)*10)
	}

	// Get PR comments
	comments, err := getPRComments(prNumber)
	if err == nil {
		prCtx.Comments = comments
		for _, c := range comments {
			audit.Add("comment", estimateTokens(c.Body))
		}
	}

	// Get changed files list
	files, err := getPRFiles(prNumber)
	if err == nil {
		prCtx.Files = files
		audit.Add("changed files", len(files)*5)
	}

	return prCtx, audit, nil
}

// parsePRRef extracts PR number from number or URL.
func parsePRRef(ref string) (int, error) {
	// Try direct number
	if n, err := strconv.Atoi(ref); err == nil {
		return n, nil
	}

	// Try URL: https://github.com/owner/repo/pull/123
	if strings.Contains(ref, "/pull/") {
		parts := strings.Split(ref, "/pull/")
		if len(parts) == 2 {
			numStr := strings.Split(parts[1], "/")[0]
			numStr = strings.Split(numStr, "?")[0]
			numStr = strings.Split(numStr, "#")[0]
			if n, err := strconv.Atoi(numStr); err == nil {
				return n, nil
			}
		}
	}

	return 0, fmt.Errorf("invalid PR reference: %s (use PR number or GitHub URL)", ref)
}

// getPRInfo fetches PR metadata using gh CLI.
func getPRInfo(prNumber int) (*PRInfo, error) {
	cmd := exec.Command("gh", "pr", "view", strconv.Itoa(prNumber), "--json",
		"number,title,author,state,url,body,labels,baseRefName,headRefName,additions,deletions,changedFiles")

	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("gh pr view failed: %w", err)
	}

	var data struct {
		Number       int    `json:"number"`
		Title        string `json:"title"`
		Author       struct {
			Login string `json:"login"`
		} `json:"author"`
		State        string `json:"state"`
		URL          string `json:"url"`
		Body         string `json:"body"`
		Labels       []struct {
			Name string `json:"name"`
		} `json:"labels"`
		BaseRefName  string `json:"baseRefName"`
		HeadRefName  string `json:"headRefName"`
		Additions    int    `json:"additions"`
		Deletions    int    `json:"deletions"`
		ChangedFiles int    `json:"changedFiles"`
	}

	if err := json.Unmarshal(output, &data); err != nil {
		return nil, fmt.Errorf("failed to parse PR data: %w", err)
	}

	pr := &PRInfo{
		Number:       data.Number,
		Title:        data.Title,
		Author:       data.Author.Login,
		State:        data.State,
		URL:          data.URL,
		Body:         data.Body,
		BaseBranch:   data.BaseRefName,
		HeadBranch:   data.HeadRefName,
		Additions:    data.Additions,
		Deletions:    data.Deletions,
		ChangedFiles: data.ChangedFiles,
	}

	for _, l := range data.Labels {
		pr.Labels = append(pr.Labels, l.Name)
	}

	// Get CI status
	pr.CIStatus = getCIStatus(prNumber)

	return pr, nil
}

// getCIStatus gets aggregated CI status.
func getCIStatus(prNumber int) string {
	cmd := exec.Command("gh", "pr", "checks", strconv.Itoa(prNumber), "--json", "state")
	output, err := cmd.Output()
	if err != nil {
		return "unknown"
	}

	var checks []struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(output, &checks); err != nil {
		return "unknown"
	}

	passing := 0
	failing := 0
	pending := 0

	for _, c := range checks {
		switch c.State {
		case "SUCCESS", "NEUTRAL", "SKIPPED":
			passing++
		case "FAILURE", "ERROR":
			failing++
		default:
			pending++
		}
	}

	if failing > 0 {
		return fmt.Sprintf("failing (%d/%d)", failing, len(checks))
	}
	if pending > 0 {
		return fmt.Sprintf("pending (%d/%d)", pending, len(checks))
	}
	if passing > 0 {
		return fmt.Sprintf("passing (%d/%d)", passing, len(checks))
	}
	return "no checks"
}

// getPRDiff fetches the PR diff.
func getPRDiff(prNumber int) (string, error) {
	cmd := exec.Command("gh", "pr", "diff", strconv.Itoa(prNumber))
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}

	// Truncate if too large
	diff := string(output)
	if len(diff) > 200000 {
		diff = diff[:200000] + "\n... (truncated)"
	}
	return diff, nil
}

// getPRChecks fetches CI check details.
func getPRChecks(prNumber int) ([]PRCheck, error) {
	cmd := exec.Command("gh", "pr", "checks", strconv.Itoa(prNumber), "--json", "name,state,conclusion")
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var data []struct {
		Name       string `json:"name"`
		State      string `json:"state"`
		Conclusion string `json:"conclusion"`
	}
	if err := json.Unmarshal(output, &data); err != nil {
		return nil, err
	}

	var checks []PRCheck
	for _, c := range data {
		checks = append(checks, PRCheck{
			Name:       c.Name,
			Status:     c.State,
			Conclusion: c.Conclusion,
		})
	}
	return checks, nil
}

// getPRComments fetches PR comments.
func getPRComments(prNumber int) ([]PRComment, error) {
	cmd := exec.Command("gh", "pr", "view", strconv.Itoa(prNumber), "--json", "comments")
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var data struct {
		Comments []struct {
			Author struct {
				Login string `json:"login"`
			} `json:"author"`
			Body string `json:"body"`
		} `json:"comments"`
	}
	if err := json.Unmarshal(output, &data); err != nil {
		return nil, err
	}

	var comments []PRComment
	for _, c := range data.Comments {
		comments = append(comments, PRComment{
			Author: c.Author.Login,
			Body:   c.Body,
		})
	}
	return comments, nil
}

// getPRFiles fetches the list of changed files.
func getPRFiles(prNumber int) ([]string, error) {
	cmd := exec.Command("gh", "pr", "view", strconv.Itoa(prNumber), "--json", "files")
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var data struct {
		Files []struct {
			Path string `json:"path"`
		} `json:"files"`
	}
	if err := json.Unmarshal(output, &data); err != nil {
		return nil, err
	}

	var files []string
	for _, f := range data.Files {
		files = append(files, f.Path)
	}
	return files, nil
}

// buildPRPrompt builds the user prompt for PR review.
func buildPRPrompt(ctx *PRContext) string {
	var sb strings.Builder

	sb.WriteString("## Pull Request\n\n")
	sb.WriteString(fmt.Sprintf("- **#%d**: %s\n", ctx.PR.Number, ctx.PR.Title))
	sb.WriteString(fmt.Sprintf("- **Author**: @%s\n", ctx.PR.Author))
	sb.WriteString(fmt.Sprintf("- **Branch**: %s → %s\n", ctx.PR.HeadBranch, ctx.PR.BaseBranch))
	sb.WriteString(fmt.Sprintf("- **Changes**: +%d/-%d in %d files\n", ctx.PR.Additions, ctx.PR.Deletions, ctx.PR.ChangedFiles))
	sb.WriteString(fmt.Sprintf("- **CI Status**: %s\n", ctx.PR.CIStatus))
	if len(ctx.PR.Labels) > 0 {
		sb.WriteString(fmt.Sprintf("- **Labels**: %s\n", strings.Join(ctx.PR.Labels, ", ")))
	}
	sb.WriteString("\n")

	if ctx.PR.Body != "" {
		sb.WriteString("## PR Description\n\n")
		sb.WriteString(ctx.PR.Body)
		sb.WriteString("\n\n")
	}

	// CI Checks
	if len(ctx.Checks) > 0 {
		sb.WriteString("## CI Checks\n\n")
		for _, c := range ctx.Checks {
			status := c.Conclusion
			if status == "" {
				status = c.Status
			}
			sb.WriteString(fmt.Sprintf("- %s: %s\n", c.Name, status))
		}
		sb.WriteString("\n")
	}

	// Changed files
	if len(ctx.Files) > 0 {
		sb.WriteString("## Changed Files\n\n")
		for _, f := range ctx.Files {
			sb.WriteString(fmt.Sprintf("- %s\n", f))
		}
		sb.WriteString("\n")
	}

	// Comments
	if len(ctx.Comments) > 0 {
		sb.WriteString("## Existing Comments\n\n")
		for _, c := range ctx.Comments {
			sb.WriteString(fmt.Sprintf("**@%s**:\n%s\n\n", c.Author, c.Body))
		}
	}

	sb.WriteString("## Diff\n\n")
	sb.WriteString("```diff\n")
	sb.WriteString(ctx.Diff)
	sb.WriteString("\n```\n")

	return sb.String()
}

// ghOutput runs a gh command and returns output.
func ghOutput(args ...string) (string, error) {
	cmd := exec.Command("gh", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s: %s", err, stderr.String())
	}
	return strings.TrimSpace(stdout.String()), nil
}
