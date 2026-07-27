package commands

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/oneshot"
	"m31labs.dev/buckley/pkg/prompts"
)

// ReviewBranchDef implements oneshot.RLMDefinition for branch code review.
//
// Unlike commit/PR which use single-tool invoke+retry, review runs a full
// RLM sub-agent with multi-turn, snapshot-bound inspection and verification
// tools. Verification can execute only constrained build/test/check commands
// inside an OS-enforced read-only-source sandbox.
// The agent produces free-form markdown which is parsed into structured data.
type ReviewBranchDef struct {
	ChangedFiles      []string
	ContextIncomplete bool
	ApprovalCritic    bool
}

func (ReviewBranchDef) Name() string { return "review" }

func (ReviewBranchDef) SystemPrompt() string {
	return prompts.ReviewBranchWithToolsPrompt(time.Now())
}

func (ReviewBranchDef) AllowedTools() []string {
	return reviewAllowedTools()
}

func (ReviewBranchDef) ParseResult(response string) (any, error) {
	return parseFinalReviewResult(response)
}

func (d ReviewBranchDef) ValidateResult(result any) error {
	review, ok := result.(*ReviewRLMResult)
	if !ok {
		return fmt.Errorf("unexpected branch review result type %T", result)
	}
	if err := validateFinalReviewResult(review); err != nil {
		return err
	}
	return ValidateParsedReview(review.Parsed, ReviewValidationOptions{
		ChangedFiles:      d.ChangedFiles,
		ContextIncomplete: d.ContextIncomplete,
	})
}

func (d ReviewBranchDef) ValidateRLMExecution(result any, execution *oneshot.RLMResult) error {
	return validateReviewExecutionEvidence(result, execution, d.ChangedFiles)
}

func (d ReviewBranchDef) RequiresApprovalCritic(result any) bool {
	return d.ApprovalCritic && reviewResultIsApproved(result)
}

func (d ReviewBranchDef) ApprovalCriticSystemPrompt() string {
	return prompts.ReviewApprovalCriticPrompt(d.SystemPrompt())
}

func (d ReviewBranchDef) BuildApprovalCriticPrompt(originalPrompt string, primaryResult any) (string, error) {
	return buildApprovalCriticPrompt(originalPrompt, primaryResult)
}

// ReviewProjectDef implements oneshot.RLMDefinition for project-wide review.
type ReviewProjectDef struct{}

func (ReviewProjectDef) Name() string { return "review-project" }

func (ReviewProjectDef) SystemPrompt() string {
	return prompts.ReviewProjectPrompt(time.Now())
}

func (ReviewProjectDef) AllowedTools() []string {
	return []string{"read_file", "find_files", "search_text"}
}

func (ReviewProjectDef) MaxRLMIterations() int { return 8 }

func (ReviewProjectDef) ParseResult(response string) (any, error) {
	return &ReviewRLMResult{
		Review: response,
		Parsed: ParseReview(response),
	}, nil
}

// ValidateResult keeps project mode explicitly advisory. Project review uses
// a broad architecture/recommendations format rather than the merge-gate
// schema, so it must never smuggle an approval verdict past the branch/PR
// evidence and critic requirements.
func (ReviewProjectDef) ValidateResult(result any) error {
	review, ok := result.(*ReviewRLMResult)
	if !ok {
		return fmt.Errorf("unexpected project review result type %T", result)
	}
	if review.Parsed != nil && review.Parsed.Approved {
		return fmt.Errorf("project review is advisory and cannot issue an approval verdict")
	}
	return nil
}

// ReviewPRDef implements oneshot.RLMDefinition for PR review.
type ReviewPRDef struct {
	ChangedFiles                []string
	ContextIncomplete           bool
	CIStatus                    string
	CIProvenance                string
	RequiresFeedbackDisposition bool
	RequiredFeedbackIDs         []string
	MaxIterations               int
	ApprovalCritic              bool
}

func (ReviewPRDef) Name() string { return "review-pr" }

func (d ReviewPRDef) MaxRLMIterations() int { return d.MaxIterations }

func (ReviewPRDef) SystemPrompt() string {
	return prompts.ReviewPRPrompt(time.Now())
}

func (ReviewPRDef) AllowedTools() []string {
	return reviewAllowedTools()
}

func reviewAllowedTools() []string {
	return []string{"read_file", "find_files", "search_text", "run_verification"}
}

func (ReviewPRDef) ParseResult(response string) (any, error) {
	return parseFinalReviewResult(response)
}

func (d ReviewPRDef) ValidateResult(result any) error {
	review, ok := result.(*ReviewRLMResult)
	if !ok {
		return fmt.Errorf("unexpected PR review result type %T", result)
	}
	if err := validateFinalReviewResult(review); err != nil {
		return err
	}
	return ValidateParsedReview(review.Parsed, ReviewValidationOptions{
		ChangedFiles:                d.ChangedFiles,
		ContextIncomplete:           d.ContextIncomplete,
		CIStatus:                    d.CIStatus,
		CIProvenance:                d.CIProvenance,
		RequiresFeedbackDisposition: d.RequiresFeedbackDisposition,
		RequiredFeedbackIDs:         d.RequiredFeedbackIDs,
		RequirePassingRemoteCI:      true,
	})
}

var finalReviewGradeHeadingRE = regexp.MustCompile(`^## Grade:\s*\[?[A-F]\]?\s*$`)

func parseFinalReviewResult(response string) (*ReviewRLMResult, error) {
	review, err := canonicalFinalReview(response)
	if err != nil {
		return nil, err
	}
	return &ReviewRLMResult{
		Review: review,
		Parsed: ParseReview(review),
	}, nil
}

func canonicalFinalReview(response string) (string, error) {
	normalized := strings.ReplaceAll(response, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	lines := strings.Split(strings.TrimSpace(normalized), "\n")

	start := -1
	fence := ""
	for i, line := range lines {
		if marker := reviewFenceMarker(line); marker != "" {
			if fence == "" {
				fence = marker
			} else if fence == marker {
				fence = ""
			}
			continue
		}
		if fence == "" && finalReviewGradeHeadingRE.MatchString(line) {
			start = i
			break
		}
	}
	if start < 0 {
		return "", fmt.Errorf(`final review is missing a schema heading such as "## Grade: A"`)
	}

	review := strings.TrimSpace(strings.Join(lines[start:], "\n"))
	if err := validateCanonicalFinalReview(review); err != nil {
		return "", err
	}
	return review, nil
}

func validateFinalReviewResult(review *ReviewRLMResult) error {
	if review == nil {
		return fmt.Errorf("final review result is missing")
	}
	if err := validateCanonicalFinalReview(review.Review); err != nil {
		return err
	}
	if review.Parsed == nil {
		return fmt.Errorf("parsed review is missing")
	}
	if review.Parsed.RawReview != review.Review {
		return fmt.Errorf("parsed review does not match the canonical final review")
	}
	return nil
}

func validateCanonicalFinalReview(review string) error {
	if review == "" || strings.TrimSpace(review) != review {
		return fmt.Errorf(`final review must start with one schema heading such as "## Grade: A"`)
	}

	lines := strings.Split(review, "\n")
	if len(lines) == 0 || !finalReviewGradeHeadingRE.MatchString(lines[0]) {
		return fmt.Errorf(`final review must start with one schema heading such as "## Grade: A"`)
	}

	headings := 0
	fence := ""
	for _, line := range lines {
		if marker := reviewFenceMarker(line); marker != "" {
			if fence == "" {
				fence = marker
			} else if fence == marker {
				fence = ""
			}
			continue
		}
		if fence == "" && finalReviewGradeHeadingRE.MatchString(line) {
			headings++
		}
	}
	if headings != 1 {
		return fmt.Errorf("final review must contain exactly one schema grade heading, got %d", headings)
	}
	return nil
}

func reviewFenceMarker(line string) string {
	trimmed := strings.TrimSpace(line)
	switch {
	case strings.HasPrefix(trimmed, "```"):
		return "```"
	case strings.HasPrefix(trimmed, "~~~"):
		return "~~~"
	default:
		return ""
	}
}

func (d ReviewPRDef) ValidateRLMExecution(result any, execution *oneshot.RLMResult) error {
	review, ok := result.(*ReviewRLMResult)
	if ok && review.Parsed != nil && review.Parsed.Approved &&
		parseRemoteCIState(d.CIStatus) == VerificationPass &&
		(d.CIProvenance == prCISourceHead || d.CIProvenance == prCISourceBase) {
		return nil
	}
	return validateReviewExecutionEvidence(result, execution, d.ChangedFiles)
}

func validateReviewExecutionEvidence(result any, execution *oneshot.RLMResult, changedFiles []string) error {
	review, ok := result.(*ReviewRLMResult)
	if !ok || review.Parsed == nil || !review.Parsed.Approved {
		return nil
	}
	if reviewChangedFilesDocumentationOnly(changedFiles) {
		// ValidateResult separately enforces normalized status, completeness, and
		// (for PRs) authoritative remote CI. The execution gate revalidates the
		// exact per-file ledger here so documentation-only approvals are grounded
		// in diff claims without manufacturing unrelated source commands.
		if err := validateCoverageLedger(review.Parsed.CoverageEntries, changedFiles); err != nil {
			return fmt.Errorf("documentation-only approval requires exact changed-file diff evidence: %w", err)
		}
		return nil
	}
	if execution == nil {
		return fmt.Errorf("approved review is missing execution evidence")
	}
	if strings.EqualFold(strings.TrimSpace(execution.ProviderID), "codex") {
		var trusted []reviewCommandEvidenceDetails
		for _, evidence := range execution.ExecutionEvidence {
			details, trustworthy := classifyReviewCommandEvidenceDetails(evidence)
			if trustworthy {
				trusted = append(trusted, details)
			}
		}
		if err := validateReviewEvidenceCoverage(changedFiles, trusted); err != nil {
			return fmt.Errorf("native Codex approval requires classifiable snapshot-root evidence: %w", err)
		}
		return nil
	}

	var trusted []reviewCommandEvidenceDetails
	for _, call := range execution.ToolCalls {
		if call.Name != "run_verification" || !call.Success {
			continue
		}
		kind, _ := call.Data["kind"].(string)
		language, _ := call.Data["language"].(string)
		path, _ := call.Data["path"].(string)
		pattern, _ := call.Data["pattern"].(string)
		stdout, _ := call.Data["stdout"].(string)
		status, _ := call.Data["status"].(string)
		exitCode, ok := reviewEvidenceExitCode(call.Data["exit_code"])
		if !ok || exitCode != 0 || status != "PASS" {
			continue
		}
		kind = strings.ToLower(strings.TrimSpace(kind))
		language = strings.ToLower(strings.TrimSpace(language))
		path = normalizeReviewEvidencePath(path)
		if (kind != reviewEvidenceBuild && kind != reviewEvidenceTest) || language == "" || path == "" {
			continue
		}
		if strings.TrimSpace(pattern) != "" &&
			(language != "go" || kind != reviewEvidenceTest || !goReviewOutputProvesTestExecution(stdout)) {
			continue
		}
		trusted = append(trusted, reviewCommandEvidenceDetails{
			Kind:     kind,
			Language: language,
			Targets: []reviewCoverageTarget{{
				Path:      path,
				Recursive: language != "go",
			}},
		})
	}
	if err := validateReviewEvidenceCoverage(changedFiles, trusted); err != nil {
		return fmt.Errorf("API-backed approval requires successful snapshot-bound run_verification evidence: %w", err)
	}
	return nil
}

func reviewEvidenceExitCode(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), typed == float64(int(typed))
	default:
		return 0, false
	}
}

func (d ReviewPRDef) RequiresApprovalCritic(result any) bool {
	return d.ApprovalCritic && reviewResultIsApproved(result)
}

func (d ReviewPRDef) ApprovalCriticSystemPrompt() string {
	return prompts.ReviewApprovalCriticPrompt(d.SystemPrompt())
}

func (d ReviewPRDef) BuildApprovalCriticPrompt(originalPrompt string, primaryResult any) (string, error) {
	return buildApprovalCriticPrompt(originalPrompt, primaryResult)
}

func reviewResultIsApproved(result any) bool {
	review, ok := result.(*ReviewRLMResult)
	return ok && review.Parsed != nil && review.Parsed.Approved
}

func buildApprovalCriticPrompt(originalPrompt string, primaryResult any) (string, error) {
	review, ok := primaryResult.(*ReviewRLMResult)
	if !ok || review.Parsed == nil {
		return "", fmt.Errorf("unexpected approval result type %T", primaryResult)
	}
	if !review.Parsed.Approved {
		return "", fmt.Errorf("approval critic requested for a non-approval result")
	}

	return `Perform an independent adversarial second-pass review using the original evidence below.

The prior review is included only so you can identify and verify its claims. Do not trust its verdict, coverage, or falsification conclusion. Re-read relevant source with tools, look for evidence it missed, and return a complete replacement review in the command's exact required format.

## Original Review Evidence

` + originalPrompt + `

## Prior Provisional Approval

` + review.Review + `

## Required Critic Outcome

Independently decide whether approval survives. Your complete machine-validated review becomes the final result.`, nil
}

// FixFindingDef implements oneshot.RLMDefinition for applying a fix to a finding.
type FixFindingDef struct{}

func (FixFindingDef) Name() string { return "fix-finding" }

func (FixFindingDef) SystemPrompt() string {
	return `You are a code fixer. Your job is to apply precise fixes to code based on review findings.

RULES:
1. Read the file first to understand context
2. Apply the MINIMUM change needed to fix the issue
3. Do NOT refactor unrelated code
4. Do NOT add features or improvements beyond the fix
5. Verify the fix compiles (run 'go build ./...' or equivalent)
6. Report exactly what you changed

TOOLS:
- read_file: Read file contents
- write_file: Write file (use sparingly - only for the fix)
- run_shell: Run commands (build, test, etc.)
- find_files: Find files
- search_text: Search code

OUTPUT:
After applying the fix, summarize:
1. What file(s) you changed
2. What the change was (brief)
3. Whether it compiles

Be concise. The user knows the context.`
}

func (FixFindingDef) AllowedTools() []string {
	return []string{"read_file", "find_files", "search_text", "run_shell", "write_file"}
}

func (FixFindingDef) ParseResult(response string) (any, error) {
	return &FixResult{
		Summary: response,
	}, nil
}

// ReviewRLMResult is the typed output from a review RLM execution.
type ReviewRLMResult struct {
	// Review is the full markdown review text.
	Review string

	// Parsed is the structured review data extracted from the markdown.
	Parsed *ParsedReview
}

// FixResult contains the result of fixing a finding.
type FixResult struct {
	// Summary describes what was changed.
	Summary string

	// FilesChanged lists files that were modified.
	FilesChanged []string
}
