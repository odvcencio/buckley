package commands

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path"
	"sort"
	"strconv"
	"strings"

	"m31labs.dev/buckley/pkg/diffsignal"
	"m31labs.dev/buckley/pkg/transparency"
)

// PRInfo contains parsed PR metadata.
type PRInfo struct {
	Number         int
	Title          string
	Author         string
	State          string
	URL            string
	Host           string
	Repository     string
	Body           string
	CIStatus       string
	ReviewDecision string
	Labels         []string
	BaseBranch     string
	BaseSHA        string
	HeadBranch     string
	HeadSHA        string
	Additions      int
	Deletions      int
	ChangedFiles   int
}

// PRContext contains context for PR review.
type PRContext struct {
	PR                      *PRInfo
	Diff                    string
	Comments                []PRComment
	Reviews                 []PRReview
	InlineComments          []PRComment
	ResolvedThreadsFiltered int
	Checks                  []PRCheck
	CIProvenance            string
	CIRevision              string
	Files                   []string
	AgentsMD                string
	CheckoutSHA             string
	ContextStatus           []PRContextStatus
	target                  prReference
}

const (
	prCISourceHead = "pull request head"
	prCISourceBase = "immutable base"
)

// PRComment represents a PR comment.
type PRComment struct {
	ID              string
	ThreadID        string
	Author          string
	Body            string
	Path            string
	Line            int
	StartLine       int
	OriginalLine    int
	Resolved        bool
	ResolutionKnown bool
	Outdated        bool
}

// PRReview represents a submitted pull request review.
type PRReview struct {
	ID          string
	Author      string
	Body        string
	State       string
	SubmittedAt string
}

// PRContextStatus records whether a review context source is complete.
type PRContextStatus struct {
	Source    string
	Status    string
	Detail    string
	Truncated bool
}

// PRCheck represents a CI check result.
type PRCheck struct {
	Name       string
	Status     string
	Conclusion string
}

type prCommandRunner func(name string, args ...string) ([]byte, error)

type prContextDependencies struct {
	run prCommandRunner
}

// PRContextOptions controls automated-review context limits. Interactive
// reviews use the larger default; callers with a strict cost envelope can
// reduce only the diff while retaining metadata, feedback, and CI evidence.
type PRContextOptions struct {
	MaxDiffBytes int
}

// DefaultPRContextOptions returns the full interactive review context budget.
func DefaultPRContextOptions() PRContextOptions {
	return PRContextOptions{MaxDiffBytes: diffsignal.ReviewDiffBudget}
}

type prDiff struct {
	Text          string
	Truncated     bool
	OriginalBytes int
}

type prReference struct {
	Number     int
	Host       string
	Repository string
}

type inlineCommentsResult struct {
	Comments                []PRComment
	ResolvedThreadsFiltered int
	Truncated               bool
	Fallback                bool
	FallbackReason          string
}

// AssemblePRContext gathers context for PR review using gh CLI.
func AssemblePRContext(prRef string) (*PRContext, *transparency.ContextAudit, error) {
	return AssemblePRContextWithOptions(prRef, DefaultPRContextOptions())
}

// AssemblePRContextWithOptions gathers PR context with explicit size limits.
func AssemblePRContextWithOptions(prRef string, opts PRContextOptions) (*PRContext, *transparency.ContextAudit, error) {
	return assemblePRContextWithOptions(prRef, prContextDependencies{
		run: defaultPRCommandRunner,
	}, opts)
}

func assemblePRContext(prRef string, deps prContextDependencies) (*PRContext, *transparency.ContextAudit, error) {
	return assemblePRContextWithOptions(prRef, deps, DefaultPRContextOptions())
}

func assemblePRContextWithOptions(prRef string, deps prContextDependencies, opts PRContextOptions) (*PRContext, *transparency.ContextAudit, error) {
	audit := transparency.NewContextAudit()
	prCtx := &PRContext{}
	if deps.run == nil {
		deps.run = defaultPRCommandRunner
	}
	if opts.MaxDiffBytes <= 0 {
		opts.MaxDiffBytes = diffsignal.ReviewDiffBudget
	}

	target, err := parsePRRef(prRef)
	if err != nil {
		return nil, nil, err
	}
	prCtx.target = target

	pr, err := getPRInfo(deps.run, target)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get PR info: %w", err)
	}
	prCtx.PR = pr
	target.Number = pr.Number
	target.Host = firstPRString(target.Host, pr.Host)
	target.Repository = firstPRString(target.Repository, pr.Repository)
	prCtx.target = target
	metadata := pr.Title + pr.Body + pr.Host + pr.Repository + pr.HeadSHA + pr.BaseSHA + pr.ReviewDecision
	audit.Add("PR metadata", reviewEstimateTokens(metadata))
	prCtx.addStatus("PR metadata", "complete", "immutable repository and base/head revisions captured", false)

	diff, err := getPRDiffWithBudget(deps.run, target, opts.MaxDiffBytes)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get PR diff: %w", err)
	}
	prCtx.Diff = diff.Text
	if diff.Truncated {
		audit.AddTruncated("PR diff", reviewEstimateTokens(diff.Text), estimatePRBytesTokens(diff.OriginalBytes))
		prCtx.addStatus("PR diff", "truncated", fmt.Sprintf("%d original bytes; review coverage is partial", diff.OriginalBytes), true)
	} else {
		audit.Add("PR diff", reviewEstimateTokens(diff.Text))
		prCtx.addStatus("PR diff", "complete", fmt.Sprintf("%d bytes", diff.OriginalBytes), false)
	}

	headChecks, headChecksErr := getPRChecks(deps.run, target)

	comments, err := getPRComments(deps.run, target)
	if err == nil {
		prCtx.Comments = comments
		audit.Add("top-level comments", reviewEstimateTokens(prCommentBodies(comments)))
		prCtx.addStatus("Top-level comments", "complete", fmt.Sprintf("%d comments", len(comments)), false)
	} else {
		recordPRContextFailure(prCtx, audit, "Top-level comments", err)
	}

	reviews, err := getPRReviews(deps.run, target)
	if err == nil {
		prCtx.Reviews = reviews
		audit.Add("submitted reviews", reviewEstimateTokens(prReviewBodies(reviews)))
		prCtx.addStatus("Submitted reviews", "complete", fmt.Sprintf("%d reviews", len(reviews)), false)
	} else {
		recordPRContextFailure(prCtx, audit, "Submitted reviews", err)
	}

	inline, err := getPRInlineComments(deps.run, pr)
	if err == nil {
		prCtx.InlineComments = inline.Comments
		prCtx.ResolvedThreadsFiltered = inline.ResolvedThreadsFiltered
		name := "inline review comments"
		status := "complete"
		detail := fmt.Sprintf("%s unresolved; %s filtered",
			formatPRCount(len(inline.Comments), "comment"),
			formatPRCount(inline.ResolvedThreadsFiltered, "resolved thread"))
		if inline.Fallback {
			name += " (REST fallback)"
			status = "fallback"
			detail += "; resolution state unavailable; GraphQL failed: " + compactPRContextErrorText(inline.FallbackReason)
		}
		if inline.Truncated {
			status = "truncated"
			detail += "; additional inline review context was not fetched"
			audit.AddTruncated(name, reviewEstimateTokens(prCommentBodies(inline.Comments)), reviewEstimateTokens(prCommentBodies(inline.Comments))+1)
		} else {
			audit.Add(name, reviewEstimateTokens(prCommentBodies(inline.Comments)))
		}
		prCtx.addStatus("Inline review threads", status, detail, inline.Truncated)
	} else {
		recordPRContextFailure(prCtx, audit, "Inline review threads", err)
	}

	files, filesErr := getPRFiles(deps.run, pr)
	filesAuthoritative := filesErr == nil && len(files) == pr.ChangedFiles
	if filesErr == nil {
		prCtx.Files = files
		if len(files) != pr.ChangedFiles {
			audit.Add("changed files (cardinality mismatch)", len(files)*5)
			prCtx.addStatus("Changed files", "incomplete",
				fmt.Sprintf("metadata reports %d files; paginated API returned %d; review coverage is not authoritative", pr.ChangedFiles, len(files)), false)
		} else {
			audit.Add("changed files", len(files)*5)
			prCtx.addStatus("Changed files", "complete", fmt.Sprintf("%d files", len(files)), false)
		}
	} else {
		recordPRContextFailure(prCtx, audit, "Changed files", filesErr)
	}

	if headChecksErr != nil {
		prCtx.PR.CIStatus = "unknown"
		recordPRContextFailure(prCtx, audit, "CI checks", headChecksErr)
	} else {
		selection, selectionErr := selectPRCIEvidence(deps.run, pr, files, filesAuthoritative, headChecks)
		if selectionErr != nil {
			prCtx.PR.CIStatus = "unknown"
			recordPRContextFailure(prCtx, audit, "CI checks (immutable base)", selectionErr)
		} else {
			prCtx.Checks = selection.Checks
			prCtx.CIProvenance = selection.Source
			prCtx.CIRevision = selection.Revision
			prCtx.PR.CIStatus = summarizePRChecks(selection.Checks)
			name, detail := describePRCISelection(selection)
			audit.Add(name, len(selection.Checks)*10)
			prCtx.addStatus("CI checks", "complete", detail, false)
		}
	}

	assemblePRAgentsContext(prCtx, audit, deps)
	revalidateAssembledPRMetadata(prCtx, audit, deps.run)
	audit.Add("context completeness", reviewEstimateTokens(formatPRContextStatus(prCtx.ContextStatus)))

	return prCtx, audit, nil
}

// BuildPRPrompt builds the user prompt for PR review.
func BuildPRPrompt(ctx *PRContext) string {
	var sb strings.Builder
	feedbackIDs := ctx.feedbackIDs()

	sb.WriteString("## Pull Request\n\n")
	sb.WriteString(fmt.Sprintf("- **#%d**: %s\n", ctx.PR.Number, ctx.PR.Title))
	sb.WriteString(fmt.Sprintf("- **Author**: @%s\n", ctx.PR.Author))
	if ctx.PR.Repository != "" {
		sb.WriteString(fmt.Sprintf("- **Repository**: %s\n", qualifiedPRRepository(ctx.PR.Host, ctx.PR.Repository)))
	}
	sb.WriteString(fmt.Sprintf("- **Head**: %s @ %s\n", ctx.PR.HeadBranch, displayPRRevision(ctx.PR.HeadSHA)))
	sb.WriteString(fmt.Sprintf("- **Base**: %s @ %s\n", ctx.PR.BaseBranch, displayPRRevision(ctx.PR.BaseSHA)))
	sb.WriteString(fmt.Sprintf("- **Changes**: +%d/-%d in %d files\n", ctx.PR.Additions, ctx.PR.Deletions, ctx.PR.ChangedFiles))
	sb.WriteString(fmt.Sprintf("- **CI Status**: %s\n", ctx.PR.CIStatus))
	if ctx.CIProvenance != "" {
		sb.WriteString(fmt.Sprintf("- **CI Evidence**: %s @ %s\n", ctx.CIProvenance, displayPRRevision(ctx.CIRevision)))
	}
	sb.WriteString(fmt.Sprintf("- **Review Decision**: %s\n", displayPRValue(ctx.PR.ReviewDecision)))
	if ctx.CheckoutSHA != "" {
		sb.WriteString(fmt.Sprintf("- **Local Verification Checkout**: %s\n", ctx.CheckoutSHA))
	}
	if len(ctx.PR.Labels) > 0 {
		sb.WriteString(fmt.Sprintf("- **Labels**: %s\n", strings.Join(ctx.PR.Labels, ", ")))
	}
	sb.WriteString("\n")

	if len(ctx.ContextStatus) > 0 {
		sb.WriteString("## Context Completeness\n\n")
		sb.WriteString("Missing or truncated sources are review limitations, not evidence that the PR is clean.\n\n")
		for _, status := range ctx.ContextStatus {
			sb.WriteString(fmt.Sprintf("- **%s**: %s", status.Source, status.Status))
			if status.Detail != "" {
				sb.WriteString(" — ")
				sb.WriteString(status.Detail)
			}
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}

	if ctx.PR.Body != "" {
		sb.WriteString("## PR Description\n\n")
		sb.WriteString(ctx.PR.Body)
		sb.WriteString("\n\n")
	}

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

	if len(ctx.Files) > 0 {
		sb.WriteString("## Changed Files\n\n")
		for _, f := range ctx.Files {
			sb.WriteString(fmt.Sprintf("- %s\n", f))
		}
		sb.WriteString("\n")
	}

	if len(ctx.Comments) > 0 {
		sb.WriteString("## Top-Level PR Comments\n\n")
		sb.WriteString("Each supplied item has a stable Feedback ID that must be dispositioned explicitly.\n\n")
		for i, c := range ctx.Comments {
			sb.WriteString(fmt.Sprintf("**@%s** — Feedback ID: `%s`:\n%s\n\n", c.Author, feedbackIDs.Comments[i], c.Body))
		}
	}

	if len(ctx.Reviews) > 0 {
		sb.WriteString("## Submitted Reviews\n\n")
		sb.WriteString("Each supplied item has a stable Feedback ID that must be dispositioned explicitly.\n\n")
		for i, review := range ctx.Reviews {
			sb.WriteString(fmt.Sprintf("**@%s** — %s", review.Author, displayPRValue(review.State)))
			if review.SubmittedAt != "" {
				sb.WriteString(fmt.Sprintf(" (%s)", review.SubmittedAt))
			}
			sb.WriteString(fmt.Sprintf(" — Feedback ID: `%s`:\n", feedbackIDs.Reviews[i]))
			if strings.TrimSpace(review.Body) == "" {
				sb.WriteString("*(no review body)*")
			} else {
				sb.WriteString(review.Body)
			}
			sb.WriteString("\n\n")
		}
	}

	if len(ctx.InlineComments) > 0 {
		sb.WriteString("## Inline Review Threads\n\n")
		sb.WriteString("Each supplied item has a stable Feedback ID that identifies both its thread and comment and must be dispositioned explicitly.\n\n")
		for i, comment := range ctx.InlineComments {
			sb.WriteString(fmt.Sprintf("**@%s**", comment.Author))
			if location := formatPRCommentLocation(comment); location != "" {
				sb.WriteString(" on ")
				sb.WriteString(location)
			}
			if comment.ResolutionKnown {
				if comment.Resolved {
					sb.WriteString(" [resolved; historical]")
				} else {
					sb.WriteString(" [unresolved]")
				}
			} else {
				sb.WriteString(" [resolution unknown; fallback context]")
			}
			if comment.Outdated {
				sb.WriteString(" [outdated location]")
			}
			sb.WriteString(fmt.Sprintf(" — Feedback ID: `%s`:\n", feedbackIDs.InlineComments[i]))
			sb.WriteString(comment.Body)
			sb.WriteString("\n\n")
		}
	}

	if ctx.AgentsMD != "" {
		sb.WriteString("## Project Guidelines (applicable AGENTS.md chain)\n\n")
		sb.WriteString(ctx.AgentsMD)
		sb.WriteString("\n\n")
	}

	sb.WriteString("## Diff\n\n")
	sb.WriteString("```diff\n")
	sb.WriteString(ctx.Diff)
	sb.WriteString("\n```\n")

	return sb.String()
}

func defaultPRCommandRunner(name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	output, err := cmd.CombinedOutput()
	return normalizePRCommandResult(name, args, output, err)
}

func normalizePRCommandResult(name string, args []string, output []byte, err error) ([]byte, error) {
	if err != nil {
		// `gh pr checks` deliberately exits 8 while checks are pending, even
		// when it emitted the complete JSON requested by --json. Preserve that
		// state-bearing payload so review-pr can report a blocked review instead
		// of aborting before analysis.
		var status interface{ ExitCode() int }
		if isJSONPRChecksCommand(name, args) && errors.As(err, &status) {
			switch {
			case status.ExitCode() == 8 && json.Valid(output):
				return output, nil
			case status.ExitCode() == 1 && isNoPRChecksReported(output):
				// `gh pr checks --json` emits prose rather than [] when a branch
				// has no check runs. Preserve that stable empty state as JSON so
				// initial capture and revalidation compare empty against empty.
				return []byte("[]"), nil
			}
		}
		detail := strings.TrimSpace(string(output))
		if detail == "" {
			return nil, err
		}
		return nil, fmt.Errorf("%s: %w", detail, err)
	}
	return output, nil
}

func isNoPRChecksReported(output []byte) bool {
	const (
		prefix = "no checks reported on the '"
		suffix = "' branch"
	)
	detail := strings.TrimSpace(string(output))
	if !strings.HasPrefix(detail, prefix) || !strings.HasSuffix(detail, suffix) {
		return false
	}
	branch := strings.TrimSuffix(strings.TrimPrefix(detail, prefix), suffix)
	return branch != "" && !strings.ContainsAny(branch, "\r\n")
}

func isJSONPRChecksCommand(name string, args []string) bool {
	if name != "gh" || len(args) < 3 || args[0] != "pr" || args[1] != "checks" {
		return false
	}
	for _, arg := range args[2:] {
		if arg == "--json" || strings.HasPrefix(arg, "--json=") {
			return true
		}
	}
	return false
}

func parsePRRef(ref string) (prReference, error) {
	if n, err := strconv.Atoi(ref); err == nil {
		return prReference{Number: n}, nil
	}

	parsed, err := url.Parse(ref)
	if err == nil && parsed.Hostname() != "" {
		parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
		if len(parts) >= 4 && parts[0] != "" && parts[1] != "" && parts[2] == "pull" {
			if n, numberErr := strconv.Atoi(parts[3]); numberErr == nil {
				return prReference{
					Number:     n,
					Host:       strings.ToLower(parsed.Host),
					Repository: parts[0] + "/" + parts[1],
				}, nil
			}
		}
	}

	return prReference{}, fmt.Errorf("invalid PR reference: %s (use PR number or GitHub URL)", ref)
}

func getPRInfo(run prCommandRunner, target prReference) (*PRInfo, error) {
	args := withPRTarget([]string{"pr", "view", strconv.Itoa(target.Number), "--json",
		"number,title,author,state,url,body,labels,baseRefName,baseRefOid,headRefName,headRefOid,additions,deletions,changedFiles,reviewDecision"}, target)
	output, err := run("gh", args...)
	if err != nil {
		return nil, fmt.Errorf("gh pr view failed: %w", err)
	}

	var data struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		Author struct {
			Login string `json:"login"`
		} `json:"author"`
		State  string `json:"state"`
		URL    string `json:"url"`
		Body   string `json:"body"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
		BaseRefName    string `json:"baseRefName"`
		BaseRefOID     string `json:"baseRefOid"`
		HeadRefName    string `json:"headRefName"`
		HeadRefOID     string `json:"headRefOid"`
		ReviewDecision string `json:"reviewDecision"`
		Additions      int    `json:"additions"`
		Deletions      int    `json:"deletions"`
		ChangedFiles   int    `json:"changedFiles"`
	}

	if err := json.Unmarshal(output, &data); err != nil {
		return nil, fmt.Errorf("failed to parse PR data: %w", err)
	}

	host := target.Host
	repository := target.Repository
	if host == "" || repository == "" {
		if source, sourceErr := parsePRRef(data.URL); sourceErr == nil {
			if host == "" {
				host = source.Host
			}
			if repository == "" {
				repository = source.Repository
			}
		}
	}
	pr := &PRInfo{
		Number:         data.Number,
		Title:          data.Title,
		Author:         data.Author.Login,
		State:          data.State,
		URL:            data.URL,
		Host:           host,
		Repository:     repository,
		Body:           data.Body,
		ReviewDecision: data.ReviewDecision,
		BaseBranch:     data.BaseRefName,
		BaseSHA:        data.BaseRefOID,
		HeadBranch:     data.HeadRefName,
		HeadSHA:        data.HeadRefOID,
		Additions:      data.Additions,
		Deletions:      data.Deletions,
		ChangedFiles:   data.ChangedFiles,
	}

	for _, l := range data.Labels {
		pr.Labels = append(pr.Labels, l.Name)
	}

	pr.CIStatus = "unknown"

	return pr, nil
}

type prMetadataSnapshot struct {
	Number         int
	Host           string
	Repository     string
	BaseBranch     string
	BaseSHA        string
	HeadBranch     string
	HeadSHA        string
	ReviewDecision string
}

type prEvidenceSnapshot struct {
	Metadata                prMetadataSnapshot
	CIStatus                string
	CIProvenance            string
	CIRevision              string
	ChecksFingerprint       string
	CommentIDsFingerprint   string
	CommentsFingerprint     string
	ReviewIDsFingerprint    string
	ReviewsFingerprint      string
	InlineIDsFingerprint    string
	InlineFingerprint       string
	ResolvedThreadsFiltered int
}

// RevalidatePRContext verifies that the PR identity, revisions, CI, review
// decision, and prior feedback captured before a review still match GitHub.
// Commands should call this after long-running model and critic passes so a
// concurrent push or evidence change cannot make a stale verdict look current.
func RevalidatePRContext(ctx *PRContext) error {
	changed, err := revalidatePRContext(ctx, defaultPRCommandRunner)
	if err != nil {
		return err
	}
	if changed != "" {
		return fmt.Errorf("PR evidence changed while review was in flight: %s", changed)
	}
	return nil
}

func revalidateAssembledPRMetadata(ctx *PRContext, audit *transparency.ContextAudit, run prCommandRunner) {
	changed, err := revalidatePRContext(ctx, run)
	if err != nil {
		recordPRContextFailure(ctx, audit, "PR evidence revalidation", err)
		return
	}
	if changed != "" {
		ctx.addStatus("PR evidence revalidation", "changed",
			"PR state moved while evidence was fetched; the assembled diff, comments, checks, and file list are not a coherent snapshot: "+changed, false)
		audit.Add("PR evidence changed during assembly", reviewEstimateTokens(changed))
		return
	}
	ctx.addStatus("PR evidence revalidation", "complete", "repository, revisions, CI, review decision, and feedback remained stable during evidence assembly", false)
	audit.Add("PR evidence revalidation", 0)
}

func revalidatePRContext(ctx *PRContext, run prCommandRunner) (string, error) {
	if ctx == nil || ctx.PR == nil {
		return "", fmt.Errorf("captured PR metadata is missing")
	}
	if run == nil {
		return "", fmt.Errorf("PR metadata runner is missing")
	}

	target := ctx.target
	if target.Number == 0 {
		target = prReference{
			Number:     ctx.PR.Number,
			Host:       ctx.PR.Host,
			Repository: ctx.PR.Repository,
		}
	}
	captured := snapshotPRContextEvidence(ctx)
	currentMetadata, err := getPRMetadataSnapshot(run, target)
	if err != nil {
		return "", fmt.Errorf("re-fetch PR metadata: %w", err)
	}
	if changed := describePRMetadataChanges(captured.Metadata, currentMetadata); changed != "" {
		return changed, nil
	}

	current, err := fetchPRReviewEvidence(run, target, currentMetadata, captured.CIProvenance)
	if err != nil {
		return "", fmt.Errorf("re-fetch PR review evidence: %w", err)
	}
	return describePREvidenceChanges(captured, current), nil
}

func getPRMetadataSnapshot(run prCommandRunner, target prReference) (prMetadataSnapshot, error) {
	args := withPRTarget([]string{"pr", "view", strconv.Itoa(target.Number), "--json",
		"number,url,baseRefName,baseRefOid,headRefName,headRefOid,reviewDecision"}, target)
	output, err := run("gh", args...)
	if err != nil {
		return prMetadataSnapshot{}, fmt.Errorf("gh pr view failed: %w", err)
	}
	var data struct {
		Number         int    `json:"number"`
		URL            string `json:"url"`
		BaseRefName    string `json:"baseRefName"`
		BaseRefOID     string `json:"baseRefOid"`
		HeadRefName    string `json:"headRefName"`
		HeadRefOID     string `json:"headRefOid"`
		ReviewDecision string `json:"reviewDecision"`
	}
	if err := json.Unmarshal(output, &data); err != nil {
		return prMetadataSnapshot{}, fmt.Errorf("failed to parse PR metadata: %w", err)
	}

	host := target.Host
	repository := target.Repository
	if source, sourceErr := parsePRRef(data.URL); sourceErr == nil {
		if host == "" {
			host = source.Host
		}
		if repository == "" {
			repository = source.Repository
		}
	}
	return prMetadataSnapshot{
		Number:         data.Number,
		Host:           host,
		Repository:     repository,
		BaseBranch:     data.BaseRefName,
		BaseSHA:        data.BaseRefOID,
		HeadBranch:     data.HeadRefName,
		HeadSHA:        data.HeadRefOID,
		ReviewDecision: data.ReviewDecision,
	}, nil
}

func describePRMetadataChanges(captured, current prMetadataSnapshot) string {
	var changes []string
	appendChange := func(name, before, after string) {
		if before != after {
			changes = append(changes, fmt.Sprintf("%s %q -> %q", name, before, after))
		}
	}
	if captured.Number != current.Number {
		changes = append(changes, fmt.Sprintf("number %d -> %d", captured.Number, current.Number))
	}
	appendChange("host", captured.Host, current.Host)
	appendChange("repository", captured.Repository, current.Repository)
	appendChange("base branch", captured.BaseBranch, current.BaseBranch)
	appendChange("base revision", captured.BaseSHA, current.BaseSHA)
	appendChange("head branch", captured.HeadBranch, current.HeadBranch)
	appendChange("head revision", captured.HeadSHA, current.HeadSHA)
	appendChange("review decision", captured.ReviewDecision, current.ReviewDecision)
	return strings.Join(changes, "; ")
}

func snapshotPRContextEvidence(ctx *PRContext) prEvidenceSnapshot {
	metadata := prMetadataSnapshot{}
	if ctx.PR != nil {
		metadata = prMetadataSnapshot{
			Number:         ctx.PR.Number,
			Host:           ctx.PR.Host,
			Repository:     ctx.PR.Repository,
			BaseBranch:     ctx.PR.BaseBranch,
			BaseSHA:        ctx.PR.BaseSHA,
			HeadBranch:     ctx.PR.HeadBranch,
			HeadSHA:        ctx.PR.HeadSHA,
			ReviewDecision: ctx.PR.ReviewDecision,
		}
	}
	return newPREvidenceSnapshot(metadata, ctx.Checks, ctx.CIProvenance, ctx.CIRevision, ctx.Comments, ctx.Reviews, ctx.InlineComments, ctx.ResolvedThreadsFiltered)
}

func fetchPRReviewEvidence(run prCommandRunner, target prReference, metadata prMetadataSnapshot, capturedCISource string) (prEvidenceSnapshot, error) {
	checks, ciSource, ciRevision, err := refetchPRCIEvidence(run, target, metadata, capturedCISource)
	if err != nil {
		return prEvidenceSnapshot{}, fmt.Errorf("CI checks: %w", err)
	}
	feedbackTarget := target
	feedbackTarget.Number = metadata.Number
	feedbackTarget.Host = firstPRString(feedbackTarget.Host, metadata.Host)
	feedbackTarget.Repository = firstPRString(feedbackTarget.Repository, metadata.Repository)
	comments, err := getPRComments(run, feedbackTarget)
	if err != nil {
		return prEvidenceSnapshot{}, fmt.Errorf("top-level comments: %w", err)
	}
	reviews, err := getPRReviews(run, feedbackTarget)
	if err != nil {
		return prEvidenceSnapshot{}, fmt.Errorf("submitted reviews: %w", err)
	}
	inline, err := getPRInlineComments(run, &PRInfo{
		Number:     metadata.Number,
		Host:       metadata.Host,
		Repository: metadata.Repository,
	})
	if err != nil {
		return prEvidenceSnapshot{}, fmt.Errorf("inline review threads: %w", err)
	}
	if inline.Fallback {
		return prEvidenceSnapshot{}, fmt.Errorf("inline review threads: resolution state unavailable after GraphQL failure: %s", compactPRContextErrorText(inline.FallbackReason))
	}
	if inline.Truncated {
		return prEvidenceSnapshot{}, fmt.Errorf("inline review threads: paginated evidence was truncated")
	}

	metadataAfter, err := getPRMetadataSnapshot(run, target)
	if err != nil {
		return prEvidenceSnapshot{}, fmt.Errorf("final PR metadata: %w", err)
	}
	if changed := describePRMetadataChanges(metadata, metadataAfter); changed != "" {
		return prEvidenceSnapshot{}, fmt.Errorf("PR metadata changed during evidence re-fetch: %s", changed)
	}
	return newPREvidenceSnapshot(metadataAfter, checks, ciSource, ciRevision, comments, reviews, inline.Comments, inline.ResolvedThreadsFiltered), nil
}

func newPREvidenceSnapshot(
	metadata prMetadataSnapshot,
	checks []PRCheck,
	ciProvenance string,
	ciRevision string,
	comments []PRComment,
	reviews []PRReview,
	inlineComments []PRComment,
	resolvedThreadsFiltered int,
) prEvidenceSnapshot {
	feedback := (&PRContext{
		Comments:       comments,
		Reviews:        reviews,
		InlineComments: inlineComments,
	}).feedbackIDs()
	return prEvidenceSnapshot{
		Metadata:                metadata,
		CIStatus:                summarizePRChecks(checks),
		CIProvenance:            ciProvenance,
		CIRevision:              ciRevision,
		ChecksFingerprint:       fingerprintPRChecks(checks),
		CommentIDsFingerprint:   fingerprintPRStrings(feedback.Comments),
		CommentsFingerprint:     fingerprintPRComments(comments),
		ReviewIDsFingerprint:    fingerprintPRStrings(feedback.Reviews),
		ReviewsFingerprint:      fingerprintPRReviews(reviews),
		InlineIDsFingerprint:    fingerprintPRStrings(feedback.InlineComments),
		InlineFingerprint:       fingerprintPRComments(inlineComments),
		ResolvedThreadsFiltered: resolvedThreadsFiltered,
	}
}

func describePREvidenceChanges(captured, current prEvidenceSnapshot) string {
	var changes []string
	if changed := describePRMetadataChanges(captured.Metadata, current.Metadata); changed != "" {
		changes = append(changes, changed)
	}
	if captured.CIStatus != current.CIStatus {
		changes = append(changes, fmt.Sprintf("CI status %q -> %q", captured.CIStatus, current.CIStatus))
	}
	if captured.CIProvenance != current.CIProvenance {
		changes = append(changes, fmt.Sprintf("CI provenance %q -> %q", captured.CIProvenance, current.CIProvenance))
	}
	if captured.CIRevision != current.CIRevision {
		changes = append(changes, fmt.Sprintf("CI revision %q -> %q", captured.CIRevision, current.CIRevision))
	}
	if captured.ChecksFingerprint != current.ChecksFingerprint {
		changes = append(changes, "CI check outcomes changed")
	}
	if captured.CommentIDsFingerprint != current.CommentIDsFingerprint {
		changes = append(changes, "top-level comment IDs changed")
	}
	if captured.CommentsFingerprint != current.CommentsFingerprint {
		changes = append(changes, "top-level comment content changed")
	}
	if captured.ReviewIDsFingerprint != current.ReviewIDsFingerprint {
		changes = append(changes, "submitted review IDs changed")
	}
	if captured.ReviewsFingerprint != current.ReviewsFingerprint {
		changes = append(changes, "submitted review content or state changed")
	}
	if captured.InlineIDsFingerprint != current.InlineIDsFingerprint {
		changes = append(changes, "unresolved inline feedback IDs changed")
	}
	if captured.InlineFingerprint != current.InlineFingerprint || captured.ResolvedThreadsFiltered != current.ResolvedThreadsFiltered {
		changes = append(changes, "inline feedback content or resolution changed")
	}
	return strings.Join(changes, "; ")
}

func fingerprintPRChecks(checks []PRCheck) string {
	records := make([]string, 0, len(checks))
	for _, check := range checks {
		records = append(records, check.Name+"\x00"+check.Status+"\x00"+check.Conclusion)
	}
	return fingerprintPRStrings(records)
}

func fingerprintPRComments(comments []PRComment) string {
	records := make([]string, 0, len(comments))
	for _, comment := range comments {
		encoded, _ := json.Marshal(comment)
		records = append(records, string(encoded))
	}
	return fingerprintPRStrings(records)
}

func fingerprintPRReviews(reviews []PRReview) string {
	records := make([]string, 0, len(reviews))
	for _, review := range reviews {
		encoded, _ := json.Marshal(review)
		records = append(records, string(encoded))
	}
	return fingerprintPRStrings(records)
}

func fingerprintPRStrings(values []string) string {
	ordered := append([]string(nil), values...)
	sort.Strings(ordered)
	hash := sha256.New()
	for _, value := range ordered {
		_, _ = fmt.Fprintf(hash, "%d:", len(value))
		_, _ = io.WriteString(hash, value)
	}
	return fmt.Sprintf("%x", hash.Sum(nil))
}

func getCIStatus(run prCommandRunner, target prReference) string {
	checks, err := getPRChecks(run, target)
	if err != nil {
		return "unknown"
	}
	return summarizePRChecks(checks)
}

func summarizePRChecks(checks []PRCheck) string {
	succeeded := 0
	nonBlocking := 0
	failing := 0
	pending := 0

	for _, c := range checks {
		state := strings.ToUpper(strings.TrimSpace(firstPRString(c.Conclusion, c.Status)))
		switch state {
		case "SUCCESS", "PASS":
			succeeded++
			nonBlocking++
		case "NEUTRAL", "SKIPPED", "SKIPPING":
			nonBlocking++
		case "FAILURE", "FAIL", "ERROR", "CANCELLED", "CANCELED", "TIMED_OUT", "ACTION_REQUIRED", "STARTUP_FAILURE", "STALE":
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
	if succeeded > 0 {
		return fmt.Sprintf("passing (%d/%d)", nonBlocking, len(checks))
	}
	return "no checks"
}

type prCISelection struct {
	Checks   []PRCheck
	Source   string
	Revision string
}

func selectPRCIEvidence(run prCommandRunner, pr *PRInfo, files []string, filesAuthoritative bool, headChecks []PRCheck) (prCISelection, error) {
	selection := prCISelection{
		Checks:   headChecks,
		Source:   prCISourceHead,
		Revision: pr.HeadSHA,
	}
	if len(headChecks) > 0 || !filesAuthoritative || !reviewChangedFilesDocumentationOnly(files) {
		return selection, nil
	}

	baseChecks, err := getCommitCIEvidence(run, pr.Host, pr.Repository, pr.BaseSHA)
	if err != nil {
		return prCISelection{}, err
	}
	return prCISelection{
		Checks:   baseChecks,
		Source:   prCISourceBase,
		Revision: pr.BaseSHA,
	}, nil
}

func refetchPRCIEvidence(run prCommandRunner, target prReference, metadata prMetadataSnapshot, capturedCISource string) ([]PRCheck, string, string, error) {
	headChecks, err := getPRChecks(run, target)
	if err != nil {
		return nil, "", "", err
	}
	if len(headChecks) > 0 || capturedCISource != prCISourceBase {
		return headChecks, prCISourceHead, metadata.HeadSHA, nil
	}

	baseChecks, err := getCommitCIEvidence(run, metadata.Host, metadata.Repository, metadata.BaseSHA)
	if err != nil {
		return nil, "", "", err
	}
	return baseChecks, prCISourceBase, metadata.BaseSHA, nil
}

func describePRCISelection(selection prCISelection) (string, string) {
	if selection.Source == prCISourceBase {
		return "CI checks (immutable base)", fmt.Sprintf(
			"%d check runs inherited from immutable base %s for a documentation-only diff after the PR head reported zero checks",
			len(selection.Checks), displayPRRevision(selection.Revision))
	}
	return "CI checks", fmt.Sprintf("%d checks from pull request head %s", len(selection.Checks), displayPRRevision(selection.Revision))
}

func getCommitCIEvidence(run prCommandRunner, host, repository, revision string) ([]PRCheck, error) {
	suiteCount, err := getCommitCheckSuiteCount(run, host, repository, revision)
	if err != nil {
		return nil, fmt.Errorf("check suites: %w", err)
	}
	if suiteCount > 1000 {
		return nil, fmt.Errorf("check suites: base check-runs may be incomplete because GitHub limits the endpoint to the 1000 most recent check suites (found %d)", suiteCount)
	}
	checkRuns, err := getCommitCheckRuns(run, host, repository, revision)
	if err != nil {
		return nil, fmt.Errorf("check runs: %w", err)
	}
	statuses, err := getCommitStatuses(run, host, repository, revision)
	if err != nil {
		return nil, fmt.Errorf("commit statuses: %w", err)
	}
	return append(checkRuns, statuses...), nil
}

func getCommitCheckSuiteCount(run prCommandRunner, host, repository, revision string) (int, error) {
	if strings.TrimSpace(host) == "" {
		return 0, fmt.Errorf("explicit GitHub host is required for base check suites")
	}
	owner, repo, err := splitPRRepository(repository)
	if err != nil {
		return 0, fmt.Errorf("resolve repository for base check suites: %w", err)
	}
	revision = strings.TrimSpace(revision)
	if revision == "" {
		return 0, fmt.Errorf("immutable base revision is required for base check suites")
	}

	endpoint := fmt.Sprintf("repos/%s/%s/commits/%s/check-suites?per_page=1", owner, repo, url.PathEscape(revision))
	args := withPRAPIHostname([]string{"api", endpoint}, host)
	output, err := run("gh", args...)
	if err != nil {
		return 0, err
	}
	var response struct {
		TotalCount int `json:"total_count"`
	}
	if err := json.Unmarshal(output, &response); err != nil {
		return 0, err
	}
	return response.TotalCount, nil
}

func getCommitCheckRuns(run prCommandRunner, host, repository, revision string) ([]PRCheck, error) {
	if strings.TrimSpace(host) == "" {
		return nil, fmt.Errorf("explicit GitHub host is required for base check-runs")
	}
	owner, repo, err := splitPRRepository(repository)
	if err != nil {
		return nil, fmt.Errorf("resolve repository for base check-runs: %w", err)
	}
	revision = strings.TrimSpace(revision)
	if revision == "" {
		return nil, fmt.Errorf("immutable base revision is required for base check-runs")
	}

	type checkRun struct {
		ID         int64  `json:"id"`
		Name       string `json:"name"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
	}
	type checkRunsPage struct {
		TotalCount int        `json:"total_count"`
		CheckRuns  []checkRun `json:"check_runs"`
	}

	// Ask GitHub for the latest check run per check name explicitly. That keeps
	// an obsolete failed attempt from poisoning a successful rerun on the same
	// immutable commit while preserving distinct current checks across apps and
	// workflows.
	endpoint := fmt.Sprintf("repos/%s/%s/commits/%s/check-runs?filter=latest&per_page=100", owner, repo, url.PathEscape(revision))
	args := withPRAPIHostname([]string{"api", "--paginate", "--slurp", endpoint}, host)
	output, err := run("gh", args...)
	if err != nil {
		return nil, err
	}

	var pages []checkRunsPage
	if err := json.Unmarshal(output, &pages); err != nil {
		var page checkRunsPage
		if flatErr := json.Unmarshal(output, &page); flatErr != nil {
			return nil, err
		}
		pages = []checkRunsPage{page}
	}
	if len(pages) == 0 {
		return nil, fmt.Errorf("base check-runs response contained no pages")
	}

	total := pages[0].TotalCount
	checks := make([]PRCheck, 0, total)
	seenIDs := make(map[int64]struct{}, total)
	for pageIndex, page := range pages {
		if page.TotalCount != total {
			return nil, fmt.Errorf("base check-runs total_count changed across pages: page 1 reported %d, page %d reported %d", total, pageIndex+1, page.TotalCount)
		}
		for _, check := range page.CheckRuns {
			if check.ID != 0 {
				if _, exists := seenIDs[check.ID]; exists {
					return nil, fmt.Errorf("base check-runs pagination returned duplicate check-run id %d", check.ID)
				}
				seenIDs[check.ID] = struct{}{}
			}
			checks = append(checks, PRCheck{Name: check.Name, Status: check.Status, Conclusion: check.Conclusion})
		}
	}
	if len(checks) != total {
		return nil, fmt.Errorf("base check-runs cardinality mismatch: API reported %d but pagination returned %d", total, len(checks))
	}
	return checks, nil
}

func getCommitStatuses(run prCommandRunner, host, repository, revision string) ([]PRCheck, error) {
	if strings.TrimSpace(host) == "" {
		return nil, fmt.Errorf("explicit GitHub host is required for base commit statuses")
	}
	owner, repo, err := splitPRRepository(repository)
	if err != nil {
		return nil, fmt.Errorf("resolve repository for base commit statuses: %w", err)
	}
	revision = strings.TrimSpace(revision)
	if revision == "" {
		return nil, fmt.Errorf("immutable base revision is required for base commit statuses")
	}

	type commitStatus struct {
		ID        int64  `json:"id"`
		Context   string `json:"context"`
		State     string `json:"state"`
		CreatedAt string `json:"created_at"`
	}
	type combinedStatusPage struct {
		TotalCount int            `json:"total_count"`
		Statuses   []commitStatus `json:"statuses"`
	}

	// The combined-status endpoint returns only the latest status for each
	// context, so an obsolete failed attempt cannot override a successful rerun.
	endpoint := fmt.Sprintf("repos/%s/%s/commits/%s/status?per_page=100", owner, repo, url.PathEscape(revision))
	args := withPRAPIHostname([]string{"api", "--paginate", "--slurp", endpoint}, host)
	output, err := run("gh", args...)
	if err != nil {
		return nil, err
	}

	var pages []combinedStatusPage
	if err := json.Unmarshal(output, &pages); err != nil {
		var page combinedStatusPage
		if flatErr := json.Unmarshal(output, &page); flatErr != nil {
			return nil, err
		}
		pages = []combinedStatusPage{page}
	}
	if len(pages) == 0 {
		return nil, fmt.Errorf("base commit-status response contained no pages")
	}

	total := pages[0].TotalCount
	latestByContext := make(map[string]commitStatus, total)
	seenIDs := make(map[int64]struct{}, total)
	statusRecords := 0
	for pageIndex, page := range pages {
		if page.TotalCount != total {
			return nil, fmt.Errorf("base commit-status total_count changed across pages: page 1 reported %d, page %d reported %d", total, pageIndex+1, page.TotalCount)
		}
		for _, status := range page.Statuses {
			statusRecords++
			if status.ID != 0 {
				if _, exists := seenIDs[status.ID]; exists {
					return nil, fmt.Errorf("base commit-status pagination returned duplicate status id %d", status.ID)
				}
				seenIDs[status.ID] = struct{}{}
			}
			current, exists := latestByContext[status.Context]
			if !exists || status.CreatedAt > current.CreatedAt ||
				(status.CreatedAt == current.CreatedAt && status.ID > current.ID) {
				latestByContext[status.Context] = status
			}
		}
	}
	if statusRecords != total {
		return nil, fmt.Errorf("base commit-status cardinality mismatch: API reported %d but pagination returned %d", total, statusRecords)
	}

	contexts := make([]string, 0, len(latestByContext))
	for context := range latestByContext {
		contexts = append(contexts, context)
	}
	sort.Strings(contexts)
	checks := make([]PRCheck, 0, len(contexts))
	for _, context := range contexts {
		status := latestByContext[context]
		checks = append(checks, PRCheck{Name: "status: " + context, Status: status.State})
	}
	return checks, nil
}

func getPRDiff(run prCommandRunner, target prReference) (prDiff, error) {
	return getPRDiffWithBudget(run, target, diffsignal.ReviewDiffBudget)
}

func getPRDiffWithBudget(run prCommandRunner, target prReference, maxBytes int) (prDiff, error) {
	args := withPRTarget([]string{"pr", "diff", strconv.Itoa(target.Number)}, target)
	output, err := run("gh", args...)
	if err != nil {
		return prDiff{}, err
	}

	// Reserve space for the truncation marker so output stays within budget.
	const truncMarker = "\n... (truncated)"
	if maxBytes <= len(truncMarker) {
		maxBytes = diffsignal.ReviewDiffBudget
	}
	budget := maxBytes - len(truncMarker)
	res := diffsignal.Prioritize(string(output), budget)
	diff := res.Context
	if res.Truncated {
		diff += truncMarker
	}
	return prDiff{
		Text:          diff,
		Truncated:     res.Truncated,
		OriginalBytes: len(output),
	}, nil
}

func getPRChecks(run prCommandRunner, target prReference) ([]PRCheck, error) {
	args := withPRTarget([]string{"pr", "checks", strconv.Itoa(target.Number), "--json", "name,state"}, target)
	output, err := run("gh", args...)
	if err != nil {
		return nil, err
	}

	var data []struct {
		Name  string `json:"name"`
		State string `json:"state"`
	}
	if err := json.Unmarshal(output, &data); err != nil {
		return nil, err
	}

	var checks []PRCheck
	for _, c := range data {
		checks = append(checks, PRCheck{
			Name:   c.Name,
			Status: c.State,
		})
	}
	return checks, nil
}

func getPRComments(run prCommandRunner, target prReference) ([]PRComment, error) {
	owner, repo, err := splitPRRepository(target.Repository)
	if err != nil {
		return nil, fmt.Errorf("resolve repository for paginated top-level comments: %w", err)
	}
	endpoint := fmt.Sprintf("repos/%s/%s/issues/%d/comments?per_page=100", owner, repo, target.Number)
	args := withPRAPIHostname([]string{"api", "--paginate", "--slurp", endpoint}, target.Host)
	output, err := run("gh", args...)
	if err != nil {
		return nil, err
	}

	type restComment struct {
		ID   json.RawMessage `json:"id"`
		User struct {
			Login string `json:"login"`
		} `json:"user"`
		Body string `json:"body"`
	}
	var pages [][]restComment
	if err := json.Unmarshal(output, &pages); err != nil {
		var flat []restComment
		if flatErr := json.Unmarshal(output, &flat); flatErr != nil {
			return nil, err
		}
		pages = [][]restComment{flat}
	}

	var comments []PRComment
	for _, page := range pages {
		for _, c := range page {
			id := prRESTID(c.ID)
			comments = append(comments, PRComment{
				ID:     stablePRFeedbackSourceID(id, "top-level-comment", c.User.Login, c.Body),
				Author: c.User.Login,
				Body:   c.Body,
			})
		}
	}
	return comments, nil
}

func getPRReviews(run prCommandRunner, target prReference) ([]PRReview, error) {
	owner, repo, err := splitPRRepository(target.Repository)
	if err != nil {
		return nil, fmt.Errorf("resolve repository for paginated submitted reviews: %w", err)
	}
	endpoint := fmt.Sprintf("repos/%s/%s/pulls/%d/reviews?per_page=100", owner, repo, target.Number)
	args := withPRAPIHostname([]string{"api", "--paginate", "--slurp", endpoint}, target.Host)
	output, err := run("gh", args...)
	if err != nil {
		return nil, err
	}

	type restReview struct {
		ID   json.RawMessage `json:"id"`
		User struct {
			Login string `json:"login"`
		} `json:"user"`
		Body        string `json:"body"`
		State       string `json:"state"`
		SubmittedAt string `json:"submitted_at"`
	}
	var pages [][]restReview
	if err := json.Unmarshal(output, &pages); err != nil {
		var flat []restReview
		if flatErr := json.Unmarshal(output, &flat); flatErr != nil {
			return nil, err
		}
		pages = [][]restReview{flat}
	}

	var reviews []PRReview
	for _, page := range pages {
		for _, review := range page {
			id := prRESTID(review.ID)
			reviews = append(reviews, PRReview{
				ID:          stablePRFeedbackSourceID(id, "submitted-review", review.User.Login, review.State, review.SubmittedAt, review.Body),
				Author:      review.User.Login,
				Body:        review.Body,
				State:       review.State,
				SubmittedAt: review.SubmittedAt,
			})
		}
	}
	return reviews, nil
}

func prRESTID(raw json.RawMessage) string {
	value := strings.TrimSpace(string(raw))
	if unquoted, err := strconv.Unquote(value); err == nil {
		return unquoted
	}
	return value
}

const prReviewThreadsQuery = `query($owner: String!, $name: String!, $number: Int!, $after: String) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      reviewThreads(first: 100, after: $after) {
        pageInfo { hasNextPage endCursor }
        nodes {
		  id
          isResolved
          isOutdated
          path
          line
          startLine
          originalLine
          comments(first: 100) {
            pageInfo { hasNextPage }
            nodes {
			  id
              author { login }
              body
              path
              line
              startLine
              originalLine
            }
          }
        }
      }
    }
  }
}`

func getPRInlineComments(run prCommandRunner, pr *PRInfo) (inlineCommentsResult, error) {
	owner, repo, err := splitPRRepository(pr.Repository)
	if err != nil {
		return inlineCommentsResult{}, err
	}

	result, graphErr := getPRInlineCommentsGraphQL(run, pr.Host, owner, repo, pr.Number)
	if graphErr == nil {
		return result, nil
	}

	result, restErr := getPRInlineCommentsREST(run, pr.Host, owner, repo, pr.Number)
	if restErr != nil {
		return inlineCommentsResult{}, fmt.Errorf("GraphQL review threads failed: %v; REST inline comments failed: %w", graphErr, restErr)
	}
	result.Fallback = true
	result.FallbackReason = compactPRContextErrorText(graphErr.Error())
	return result, nil
}

func getPRInlineCommentsGraphQL(run prCommandRunner, host, owner, repo string, prNumber int) (inlineCommentsResult, error) {
	type graphComment struct {
		ID     string `json:"id"`
		Author struct {
			Login string `json:"login"`
		} `json:"author"`
		Body         string `json:"body"`
		Path         string `json:"path"`
		Line         int    `json:"line"`
		StartLine    int    `json:"startLine"`
		OriginalLine int    `json:"originalLine"`
	}
	type graphThread struct {
		ID           string `json:"id"`
		Resolved     bool   `json:"isResolved"`
		Outdated     bool   `json:"isOutdated"`
		Path         string `json:"path"`
		Line         int    `json:"line"`
		StartLine    int    `json:"startLine"`
		OriginalLine int    `json:"originalLine"`
		Comments     struct {
			PageInfo struct {
				HasNextPage bool `json:"hasNextPage"`
			} `json:"pageInfo"`
			Nodes []graphComment `json:"nodes"`
		} `json:"comments"`
	}
	type graphResponse struct {
		Data struct {
			Repository *struct {
				PullRequest *struct {
					ReviewThreads struct {
						PageInfo struct {
							HasNextPage bool   `json:"hasNextPage"`
							EndCursor   string `json:"endCursor"`
						} `json:"pageInfo"`
						Nodes []graphThread `json:"nodes"`
					} `json:"reviewThreads"`
				} `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}

	var result inlineCommentsResult
	after := ""
	for page := 0; ; page++ {
		args := []string{
			"api", "graphql",
			"-F", "owner=" + owner,
			"-F", "name=" + repo,
			"-F", "number=" + strconv.Itoa(prNumber),
			"-f", "query=" + prReviewThreadsQuery,
		}
		if after != "" {
			args = append(args, "-F", "after="+after)
		}
		args = withPRAPIHostname(args, host)
		output, err := run("gh", args...)
		if err != nil {
			return inlineCommentsResult{}, err
		}

		var response graphResponse
		if err := json.Unmarshal(output, &response); err != nil {
			return inlineCommentsResult{}, err
		}
		if len(response.Errors) > 0 {
			return inlineCommentsResult{}, fmt.Errorf("%s", response.Errors[0].Message)
		}
		if response.Data.Repository == nil || response.Data.Repository.PullRequest == nil {
			return inlineCommentsResult{}, fmt.Errorf("pull request not found in GraphQL response")
		}

		threads := response.Data.Repository.PullRequest.ReviewThreads
		for _, thread := range threads.Nodes {
			if thread.Resolved {
				result.ResolvedThreadsFiltered++
				continue
			}
			if thread.Comments.PageInfo.HasNextPage {
				result.Truncated = true
			}
			var threadEvidence strings.Builder
			for _, comment := range thread.Comments.Nodes {
				threadEvidence.WriteString(comment.ID)
				threadEvidence.WriteByte('\n')
				threadEvidence.WriteString(comment.Author.Login)
				threadEvidence.WriteByte('\n')
				threadEvidence.WriteString(comment.Body)
				threadEvidence.WriteByte('\n')
			}
			threadID := stablePRFeedbackSourceID(thread.ID, "inline-thread",
				thread.Path, strconv.Itoa(thread.Line), strconv.Itoa(thread.StartLine), strconv.Itoa(thread.OriginalLine),
				strconv.FormatBool(thread.Outdated), threadEvidence.String())
			for _, comment := range thread.Comments.Nodes {
				commentID := stablePRFeedbackSourceID(comment.ID, "inline-comment", threadID,
					comment.Author.Login, comment.Body, comment.Path, strconv.Itoa(comment.Line),
					strconv.Itoa(comment.StartLine), strconv.Itoa(comment.OriginalLine))
				result.Comments = append(result.Comments, PRComment{
					ID:              commentID,
					ThreadID:        threadID,
					Author:          comment.Author.Login,
					Body:            comment.Body,
					Path:            firstPRString(comment.Path, thread.Path),
					Line:            firstPRInt(comment.Line, thread.Line, comment.OriginalLine, thread.OriginalLine),
					StartLine:       firstPRInt(comment.StartLine, thread.StartLine),
					OriginalLine:    firstPRInt(comment.OriginalLine, thread.OriginalLine),
					Resolved:        false,
					ResolutionKnown: true,
					Outdated:        thread.Outdated,
				})
			}
		}

		if !threads.PageInfo.HasNextPage {
			break
		}
		if threads.PageInfo.EndCursor == "" || page >= 99 {
			result.Truncated = true
			break
		}
		after = threads.PageInfo.EndCursor
	}
	return result, nil
}

func getPRInlineCommentsREST(run prCommandRunner, host, owner, repo string, prNumber int) (inlineCommentsResult, error) {
	type restComment struct {
		ID   int64 `json:"id"`
		User struct {
			Login string `json:"login"`
		} `json:"user"`
		Body         string `json:"body"`
		Path         string `json:"path"`
		Line         int    `json:"line"`
		StartLine    int    `json:"start_line"`
		OriginalLine int    `json:"original_line"`
	}

	endpoint := fmt.Sprintf("repos/%s/%s/pulls/%d/comments?per_page=100", owner, repo, prNumber)
	args := withPRAPIHostname([]string{"api", "--paginate", "--slurp", endpoint}, host)
	output, err := run("gh", args...)
	if err != nil {
		return inlineCommentsResult{}, err
	}

	var pages [][]restComment
	if err := json.Unmarshal(output, &pages); err != nil {
		var comments []restComment
		if flatErr := json.Unmarshal(output, &comments); flatErr != nil {
			return inlineCommentsResult{}, err
		}
		pages = [][]restComment{comments}
	}

	var result inlineCommentsResult
	for _, page := range pages {
		for _, comment := range page {
			line := firstPRInt(comment.Line, comment.OriginalLine)
			rawID := ""
			if comment.ID != 0 {
				rawID = strconv.FormatInt(comment.ID, 10)
			}
			commentID := stablePRFeedbackSourceID(rawID, "inline-comment-rest",
				comment.User.Login, comment.Body, comment.Path, strconv.Itoa(comment.Line),
				strconv.Itoa(comment.StartLine), strconv.Itoa(comment.OriginalLine))
			threadID := stablePRFeedbackSourceID("", "inline-thread-rest", commentID)
			result.Comments = append(result.Comments, PRComment{
				ID:              commentID,
				ThreadID:        threadID,
				Author:          comment.User.Login,
				Body:            comment.Body,
				Path:            comment.Path,
				Line:            line,
				StartLine:       comment.StartLine,
				OriginalLine:    comment.OriginalLine,
				ResolutionKnown: false,
				Outdated:        comment.Line == 0 && comment.OriginalLine > 0,
			})
		}
	}
	return result, nil
}

func getPRFiles(run prCommandRunner, pr *PRInfo) ([]string, error) {
	owner, repo, err := splitPRRepository(pr.Repository)
	if err != nil {
		return nil, err
	}

	type restFile struct {
		Filename string `json:"filename"`
	}

	endpoint := fmt.Sprintf("repos/%s/%s/pulls/%d/files?per_page=100", owner, repo, pr.Number)
	args := withPRAPIHostname([]string{"api", "--paginate", "--slurp", endpoint}, pr.Host)
	output, err := run("gh", args...)
	if err != nil {
		return nil, err
	}

	var pages [][]restFile
	if err := json.Unmarshal(output, &pages); err != nil {
		var files []restFile
		if flatErr := json.Unmarshal(output, &files); flatErr != nil {
			return nil, err
		}
		pages = [][]restFile{files}
	}
	var files []string
	for _, page := range pages {
		for _, file := range page {
			files = append(files, file.Filename)
		}
	}
	return files, nil
}

func withPRTarget(args []string, target prReference) []string {
	repository := qualifiedPRRepository(target.Host, target.Repository)
	if repository == "" {
		return args
	}
	return append(args, "--repo", repository)
}

func withPRAPIHostname(args []string, host string) []string {
	if host == "" {
		return args
	}
	return append(args, "--hostname", host)
}

func qualifiedPRRepository(host, repository string) string {
	if repository == "" {
		return ""
	}
	if host == "" {
		return repository
	}
	return host + "/" + repository
}

func assemblePRAgentsContext(ctx *PRContext, audit *transparency.ContextAudit, deps prContextDependencies) {
	rootOutput, err := deps.run("git", "--no-pager", "rev-parse", "--show-toplevel")
	if err != nil {
		recordPRContextFailure(ctx, audit, "Root AGENTS.md", fmt.Errorf("repository root: %w", err))
		return
	}
	root := strings.TrimSpace(string(rootOutput))
	if root == "" {
		recordPRContextFailure(ctx, audit, "Root AGENTS.md", fmt.Errorf("repository root was empty"))
		return
	}
	if headOutput, headErr := deps.run("git", "--no-pager", "-C", root, "rev-parse", "HEAD"); headErr != nil {
		recordPRContextFailure(ctx, audit, "Local verification checkout", headErr)
	} else {
		ctx.CheckoutSHA = strings.TrimSpace(string(headOutput))
		if ctx.PR != nil && ctx.PR.HeadSHA != "" && ctx.CheckoutSHA != ctx.PR.HeadSHA {
			ctx.addStatus("Local verification checkout", "mismatch",
				fmt.Sprintf("local HEAD %s does not match PR head %s; local read/search/shell evidence is not authoritative", ctx.CheckoutSHA, ctx.PR.HeadSHA), false)
			audit.Add("local checkout mismatch", reviewEstimateTokens(ctx.CheckoutSHA+ctx.PR.HeadSHA))
		} else {
			ctx.addStatus("Local verification checkout", "complete", "local tools are pinned to the PR head", false)
			audit.Add("local checkout", reviewEstimateTokens(ctx.CheckoutSHA))
		}
	}
	if statusOutput, statusErr := deps.run("git", "--no-pager", "-C", root, "status", "--porcelain"); statusErr != nil {
		recordPRContextFailure(ctx, audit, "Local verification worktree", statusErr)
	} else if dirty := strings.TrimSpace(string(statusOutput)); dirty != "" {
		ctx.addStatus("Local verification worktree", "dirty",
			"uncommitted files can contaminate local read/search/shell evidence", false)
		audit.Add("dirty local verification worktree", reviewEstimateTokens(dirty))
	} else {
		ctx.addStatus("Local verification worktree", "complete", "no uncommitted files", false)
		audit.Add("clean local verification worktree", 0)
	}

	const agentsLimit = 10_000
	if ctx.PR == nil || strings.TrimSpace(ctx.PR.HeadSHA) == "" {
		recordPRContextFailure(ctx, audit, "Root AGENTS.md", fmt.Errorf("immutable PR head was unavailable"))
		return
	}
	content, truncated, err := readPRHeadFile(deps.run, root, ctx.PR.HeadSHA, "AGENTS.md", agentsLimit)
	switch {
	case err != nil && os.IsNotExist(err):
		ctx.addStatus("Root AGENTS.md", "missing", "no root project instructions found", false)
		audit.Add("AGENTS.md (missing)", 0)
	case err != nil:
		recordPRContextFailure(ctx, audit, "Root AGENTS.md", err)
	case truncated:
		ctx.AgentsMD = content
		audit.AddTruncated("AGENTS.md", reviewEstimateTokens(content), reviewEstimateTokens(content)+1)
		ctx.addStatus("Root AGENTS.md", "truncated", fmt.Sprintf("limited to %d bytes", agentsLimit), true)
	default:
		ctx.AgentsMD = content
		audit.Add("AGENTS.md", reviewEstimateTokens(content))
		ctx.addStatus("Root AGENTS.md", "complete", fmt.Sprintf("%d bytes", len(content)), false)
	}
	appendNestedPRAgentsContext(ctx, audit, deps.run, root, agentsLimit)
}

func appendNestedPRAgentsContext(ctx *PRContext, audit *transparency.ContextAudit, run prCommandRunner, root string, perFileLimit int) {
	candidates := nestedPRAgentsCandidates(ctx.Files)
	if len(candidates) == 0 {
		ctx.addStatus("Nested AGENTS.md", "missing", "no nested instruction scope applies to changed files", false)
		return
	}
	const aggregateLimit = 40_000
	remaining := aggregateLimit
	applicable := 0
	for _, candidate := range candidates {
		if remaining <= 0 {
			ctx.addStatus("Nested AGENTS.md", "truncated", fmt.Sprintf("applicable guidance exceeded %d bytes", aggregateLimit), true)
			audit.AddTruncated("nested AGENTS.md", 0, 1)
			return
		}
		limit := perFileLimit
		if limit > remaining {
			limit = remaining
		}
		content, truncated, err := readPRHeadFile(run, root, ctx.PR.HeadSHA, candidate, limit)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			recordPRContextFailure(ctx, audit, "Nested AGENTS.md ("+candidate+")", err)
			continue
		}
		applicable++
		remaining -= len(content)
		if ctx.AgentsMD != "" {
			ctx.AgentsMD += "\n\n"
		}
		ctx.AgentsMD += "### " + candidate + "\n\n" + content
		if truncated {
			ctx.addStatus("Nested AGENTS.md ("+candidate+")", "truncated", fmt.Sprintf("limited to %d bytes", limit), true)
			audit.AddTruncated(candidate, reviewEstimateTokens(content), reviewEstimateTokens(content)+1)
		}
	}
	if applicable == 0 {
		ctx.addStatus("Nested AGENTS.md", "missing", "no tracked nested instruction files apply to changed files", false)
		return
	}
	ctx.addStatus("Nested AGENTS.md", "complete", fmt.Sprintf("%d applicable instruction files", applicable), false)
	audit.Add("nested AGENTS.md", reviewEstimateTokens(ctx.AgentsMD))
}

func nestedPRAgentsCandidates(files []string) []string {
	seen := make(map[string]struct{})
	for _, file := range files {
		file = path.Clean(strings.TrimSpace(strings.ReplaceAll(file, "\\", "/")))
		if file == "." || file == ".." || strings.HasPrefix(file, "../") || strings.HasPrefix(file, "/") {
			continue
		}
		for dir := path.Dir(file); dir != "." && dir != "/"; dir = path.Dir(dir) {
			seen[path.Join(dir, "AGENTS.md")] = struct{}{}
		}
	}
	candidates := make([]string, 0, len(seen))
	for candidate := range seen {
		candidates = append(candidates, candidate)
	}
	sort.Slice(candidates, func(i, j int) bool {
		leftDepth := strings.Count(candidates[i], "/")
		rightDepth := strings.Count(candidates[j], "/")
		if leftDepth != rightDepth {
			return leftDepth < rightDepth
		}
		return candidates[i] < candidates[j]
	})
	return candidates
}

func readPRHeadFile(run prCommandRunner, root, headSHA, path string, maxBytes int) (string, bool, error) {
	tree, err := run("git", "--no-pager", "-C", root, "ls-tree", headSHA, "--", path)
	if err != nil {
		return "", false, err
	}
	fields := strings.Fields(string(tree))
	if len(fields) == 0 {
		return "", false, os.ErrNotExist
	}
	if fields[0] == "120000" {
		return "", false, fmt.Errorf("refusing to follow tracked symlink %s at PR head %s", path, headSHA)
	}
	content, err := run("git", "--no-pager", "-C", root, "show", headSHA+":"+path)
	if err != nil {
		return "", false, err
	}
	if maxBytes >= 0 && len(content) > maxBytes {
		return string(content[:maxBytes]), true, nil
	}
	return string(content), false, nil
}

func (ctx *PRContext) addStatus(source, status, detail string, truncated bool) {
	ctx.ContextStatus = append(ctx.ContextStatus, PRContextStatus{
		Source:    source,
		Status:    status,
		Detail:    detail,
		Truncated: truncated,
	})
}

// HasIncompleteContext reports whether a source needed for an authoritative
// clean verdict was truncated, unavailable, running in fallback mode, or tied
// to a checkout other than the immutable PR head.
func (ctx *PRContext) HasIncompleteContext() bool {
	if ctx == nil {
		return true
	}
	for _, status := range ctx.ContextStatus {
		switch status.Status {
		case "complete", "missing":
			continue
		default:
			return true
		}
	}
	return false
}

// HasReviewFeedback reports whether the PR context contains feedback whose
// disposition must appear in the review coverage ledger.
func (ctx *PRContext) HasReviewFeedback() bool {
	return ctx != nil && (len(ctx.Comments) > 0 || len(ctx.Reviews) > 0 || len(ctx.InlineComments) > 0)
}

// RequiredFeedbackIDs returns the stable identifiers for every supplied piece
// of prior review feedback, in the same order that BuildPRPrompt renders them.
// Callers can require an explicit disposition for each identifier instead of
// accepting one uncheckable sentence about all prior feedback.
func (ctx *PRContext) RequiredFeedbackIDs() []string {
	ids := ctx.feedbackIDs()
	result := make([]string, 0, len(ids.Comments)+len(ids.Reviews)+len(ids.InlineComments))
	result = append(result, ids.Comments...)
	result = append(result, ids.Reviews...)
	result = append(result, ids.InlineComments...)
	return result
}

type prFeedbackIDSet struct {
	Comments       []string
	Reviews        []string
	InlineComments []string
}

func (ctx *PRContext) feedbackIDs() prFeedbackIDSet {
	var result prFeedbackIDSet
	if ctx == nil {
		return result
	}

	used := make(map[string]int, len(ctx.Comments)+len(ctx.Reviews)+len(ctx.InlineComments))
	unique := func(id string) string {
		used[id]++
		if used[id] == 1 {
			return id
		}
		return fmt.Sprintf("%s#%d", id, used[id])
	}

	result.Comments = make([]string, 0, len(ctx.Comments))
	for _, comment := range ctx.Comments {
		sourceID := stablePRFeedbackSourceID(comment.ID, "top-level-comment",
			comment.Author, comment.Body, comment.Path, strconv.Itoa(comment.Line), strconv.Itoa(comment.OriginalLine))
		result.Comments = append(result.Comments, unique("top-level-comment:"+sourceID))
	}

	result.Reviews = make([]string, 0, len(ctx.Reviews))
	for _, review := range ctx.Reviews {
		sourceID := stablePRFeedbackSourceID(review.ID, "submitted-review",
			review.Author, review.State, review.SubmittedAt, review.Body)
		result.Reviews = append(result.Reviews, unique("submitted-review:"+sourceID))
	}

	result.InlineComments = make([]string, 0, len(ctx.InlineComments))
	for _, comment := range ctx.InlineComments {
		threadID := stablePRFeedbackSourceID(comment.ThreadID, "inline-thread",
			comment.Path, strconv.Itoa(comment.Line), strconv.Itoa(comment.StartLine), strconv.Itoa(comment.OriginalLine), comment.Body)
		commentID := stablePRFeedbackSourceID(comment.ID, "inline-comment", threadID,
			comment.Author, comment.Body, comment.Path, strconv.Itoa(comment.Line), strconv.Itoa(comment.OriginalLine))
		result.InlineComments = append(result.InlineComments,
			unique("inline-thread:"+threadID+"/comment:"+commentID))
	}
	return result
}

func stablePRFeedbackSourceID(rawID, kind string, parts ...string) string {
	if id := strings.TrimSpace(rawID); id != "" {
		return id
	}
	hash := sha256.New()
	_, _ = io.WriteString(hash, kind)
	for _, part := range parts {
		_, _ = fmt.Fprintf(hash, "\x00%d:", len(part))
		_, _ = io.WriteString(hash, part)
	}
	return fmt.Sprintf("synthetic-%x", hash.Sum(nil)[:10])
}

func recordPRContextFailure(ctx *PRContext, audit *transparency.ContextAudit, source string, err error) {
	detail := compactPRContextErrorText(err.Error())
	ctx.addStatus(source, "fetch failed", detail, false)
	audit.Add(source+" (fetch failed)", 0)
}

func compactPRContextErrorText(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	const maxErrorBytes = 240
	if len(text) > maxErrorBytes {
		return text[:maxErrorBytes] + "..."
	}
	return text
}

func formatPRContextStatus(statuses []PRContextStatus) string {
	var sb strings.Builder
	for _, status := range statuses {
		sb.WriteString(status.Source)
		sb.WriteString(": ")
		sb.WriteString(status.Status)
		if status.Detail != "" {
			sb.WriteString(" ")
			sb.WriteString(status.Detail)
		}
		sb.WriteByte('\n')
	}
	return sb.String()
}

func prCommentBodies(comments []PRComment) string {
	var sb strings.Builder
	for _, comment := range comments {
		sb.WriteString(comment.Author)
		sb.WriteByte('\n')
		sb.WriteString(comment.Body)
		sb.WriteByte('\n')
		sb.WriteString(comment.Path)
		sb.WriteByte('\n')
	}
	return sb.String()
}

func prReviewBodies(reviews []PRReview) string {
	var sb strings.Builder
	for _, review := range reviews {
		sb.WriteString(review.Author)
		sb.WriteByte('\n')
		sb.WriteString(review.State)
		sb.WriteByte('\n')
		sb.WriteString(review.Body)
		sb.WriteByte('\n')
	}
	return sb.String()
}

func splitPRRepository(repository string) (string, string, error) {
	parts := strings.Split(repository, "/")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", "", fmt.Errorf("cannot determine repository owner/name from %q", repository)
	}
	return parts[0], parts[1], nil
}

func displayPRRevision(revision string) string {
	if strings.TrimSpace(revision) == "" {
		return "(unavailable)"
	}
	return revision
}

func displayPRValue(value string) string {
	if strings.TrimSpace(value) == "" {
		return "(none)"
	}
	return value
}

func formatPRCommentLocation(comment PRComment) string {
	path := strings.ReplaceAll(comment.Path, "`", "'")
	line := firstPRInt(comment.Line, comment.OriginalLine)
	if path == "" {
		if line == 0 {
			return ""
		}
		return fmt.Sprintf("line %d", line)
	}
	if line == 0 {
		return "`" + path + "`"
	}
	if comment.StartLine > 0 && comment.StartLine != line {
		return fmt.Sprintf("`%s:%d-%d`", path, comment.StartLine, line)
	}
	return fmt.Sprintf("`%s:%d`", path, line)
}

func firstPRString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func firstPRInt(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func estimatePRBytesTokens(bytes int) int {
	if bytes <= 0 {
		return 0
	}
	return (bytes + 3) / 4
}

func formatPRCount(count int, singular string) string {
	if count == 1 {
		return fmt.Sprintf("1 %s", singular)
	}
	return fmt.Sprintf("%d %ss", count, singular)
}
