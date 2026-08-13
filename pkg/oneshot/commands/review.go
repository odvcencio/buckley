package commands

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/oneshot"
	"m31labs.dev/buckley/pkg/prompts"
)

// ReviewBranchDef implements oneshot.AgentDefinition for branch code review.
//
// Unlike commit/PR which use single-tool invoke+retry, review runs a full
// tool agent with multi-turn, snapshot-bound inspection and verification
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

func (d ReviewBranchDef) AgentEvidenceRequests() []oneshot.AgentEvidenceRequest {
	return reviewVerificationEvidenceRequests(d.ChangedFiles)
}

func (ReviewBranchDef) ParseResult(response string) (any, error) {
	return parseFinalReviewResult(response)
}

func (d ReviewBranchDef) ValidateResult(result any) error {
	review, ok := result.(*ReviewAgentResult)
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

func (d ReviewBranchDef) ValidateAgentExecution(result any, execution *oneshot.AgentResult) error {
	return validateReviewExecutionEvidence(result, execution, d.ChangedFiles)
}

func (d ReviewBranchDef) RequiresApprovalCritic(result any) bool {
	return d.ApprovalCritic && reviewResultIsApproved(result)
}

func (d ReviewBranchDef) ApprovalCriticSystemPrompt() string {
	return prompts.ReviewApprovalCriticPrompt(d.SystemPrompt())
}

func (d ReviewBranchDef) BuildApprovalCriticPrompt(originalPrompt string, primaryResult any) (string, error) {
	return buildApprovalCriticPrompt(originalPrompt, primaryResult, false)
}

// ReviewProjectDef implements oneshot.AgentDefinition for project-wide review.
// Project reviews are deadline-bounded and governor-protected, but do not
// impose a fixed model-turn ceiling: the agent must be able to expand from
// the Canopy TOC into complete repository coverage.
type ReviewProjectDef struct{}

func (ReviewProjectDef) Name() string { return "review-project" }

func (ReviewProjectDef) SystemPrompt() string {
	return prompts.ReviewProjectPrompt(time.Now())
}

func (ReviewProjectDef) AllowedTools() []string {
	return reviewInspectionTools()
}

func (ReviewProjectDef) MaxAgentIterations() int { return 0 }

func (ReviewProjectDef) ParseResult(response string) (any, error) {
	return &ReviewAgentResult{
		Review: response,
		Parsed: ParseReview(response),
	}, nil
}

// ValidateResult keeps project mode explicitly advisory. Project review uses
// a broad architecture/recommendations format rather than the merge-gate
// schema, so it must never smuggle an approval verdict past the branch/PR
// evidence and critic requirements.
func (ReviewProjectDef) ValidateResult(result any) error {
	review, ok := result.(*ReviewAgentResult)
	if !ok {
		return fmt.Errorf("unexpected project review result type %T", result)
	}
	if review.Parsed != nil && review.Parsed.Approved {
		return fmt.Errorf("project review is advisory and cannot issue an approval verdict")
	}
	text := strings.TrimSpace(review.Review)
	if !strings.Contains(text, "## Evidence Collected") && !strings.Contains(text, "## Evidence Sampled") {
		return fmt.Errorf("project review must include evidence and coverage ledgers")
	}
	if !strings.Contains(text, "## Coverage") || !strings.Contains(text, "**Completeness**:") {
		return fmt.Errorf("project review must include a coverage ledger with completeness")
	}
	if !strings.Contains(text, "COMPLETE") && !strings.Contains(text, "PARTIAL") {
		return fmt.Errorf("project review coverage must declare COMPLETE or PARTIAL")
	}
	return nil
}

// ReviewPRDef implements oneshot.AgentDefinition for PR review.
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

func (d ReviewPRDef) MaxAgentIterations() int { return d.MaxIterations }

func (d ReviewPRDef) SystemPrompt() string {
	prompt := prompts.ReviewPRPrompt(time.Now())
	if d.authoritativeRemoteCIPasses() {
		prompt += `

REMOTE CI EXECUTION POLICY:
- Authoritative remote continuous integration already passed for this immutable revision.
- The run_verification tool is disabled. Use the named remote checks for Build and Tests evidence.
- Do not lower the grade or recommendation because run_verification is unavailable.
- Treat the named immutable-revision checks as complete execution evidence.`
	}
	return prompt
}

func (d ReviewPRDef) AllowedTools() []string {
	allowed := reviewAllowedTools()
	if !d.authoritativeRemoteCIPasses() {
		return allowed
	}
	return allowed[:len(allowed)-1]
}

func (d ReviewPRDef) AgentEvidenceRequests() []oneshot.AgentEvidenceRequest {
	if d.authoritativeRemoteCIPasses() {
		return nil
	}
	return reviewVerificationEvidenceRequests(d.ChangedFiles)
}

func (d ReviewPRDef) authoritativeRemoteCIPasses() bool {
	return parseRemoteCIState(d.CIStatus) == VerificationPass &&
		(d.CIProvenance == prCISourceHead || d.CIProvenance == prCISourceBase)
}

// AuthoritativeRemoteCIPasses reports whether immutable remote checks can
// replace duplicate critic verification.
func (d ReviewPRDef) AuthoritativeRemoteCIPasses() bool {
	return d.authoritativeRemoteCIPasses()
}

func reviewAllowedTools() []string {
	return append(reviewInspectionTools(), "run_verification")
}

func reviewInspectionTools() []string {
	return []string{"exec_program", "read_file", "find_files", "search_text"}
}

func (ReviewPRDef) ParseResult(response string) (any, error) {
	return parseFinalReviewResult(response)
}

func (d ReviewPRDef) ValidateResult(result any) error {
	review, ok := result.(*ReviewAgentResult)
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

var (
	finalReviewGradeHeadingRE    = regexp.MustCompile(`^## Grade:\s*\[?[A-F]\]?\s*$`)
	finalReviewSummaryHeadingRE  = regexp.MustCompile(`^##[ \t]+Summary[ \t]*$`)
	finalReviewLevelTwoHeadingRE = regexp.MustCompile(`^##(?:[ \t]+.*)?[ \t]*$`)
	finalReviewATXHeadingRE      = regexp.MustCompile(`^#{1,6}(?:[ \t]+.*)?[ \t]*$`)
)

func parseFinalReviewResult(response string) (*ReviewAgentResult, error) {
	review, err := canonicalFinalReview(response)
	if err != nil {
		return nil, err
	}
	return &ReviewAgentResult{
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
			switch fence {
			case "":
				fence = marker
			case marker:
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
	review = normalizeImplicitReviewSummary(review)
	if err := validateCanonicalFinalReview(review); err != nil {
		return "", err
	}
	return review, nil
}

// normalizeImplicitReviewSummary repairs one narrow provider formatting drift:
// substantive lead prose placed between the Grade heading and the next level-2
// section. It preserves every existing line and inserts only the missing
// heading. Other malformed envelopes still flow unchanged to validation.
func normalizeImplicitReviewSummary(review string) string {
	lines := strings.Split(review, "\n")
	if len(lines) < 3 || !finalReviewGradeHeadingRE.MatchString(lines[0]) || reviewHasSummaryHeading(lines) {
		return review
	}

	leadProse := -1
	fence := ""
	for i := 1; i < len(lines); i++ {
		line := lines[i]
		if marker := reviewFenceMarker(line); marker != "" {
			switch fence {
			case "":
				if leadProse < 0 {
					return review
				}
				fence = marker
			case marker:
				fence = ""
			}
			continue
		}
		if fence != "" {
			continue
		}

		if finalReviewLevelTwoHeadingRE.MatchString(line) {
			if leadProse < 0 {
				return review
			}
			normalized := make([]string, 0, len(lines)+1)
			normalized = append(normalized, lines[:leadProse]...)
			normalized = append(normalized, "## Summary")
			normalized = append(normalized, lines[leadProse:]...)
			return strings.Join(normalized, "\n")
		}

		if strings.TrimSpace(line) == "" {
			continue
		}
		if leadProse < 0 && finalReviewATXHeadingRE.MatchString(line) {
			return review
		}
		if leadProse < 0 {
			leadProse = i
		}
	}

	return review
}

func reviewHasSummaryHeading(lines []string) bool {
	fence := ""
	for _, line := range lines {
		if marker := reviewFenceMarker(line); marker != "" {
			switch fence {
			case "":
				fence = marker
			case marker:
				fence = ""
			}
			continue
		}
		if fence == "" && finalReviewSummaryHeadingRE.MatchString(line) {
			return true
		}
	}
	return false
}

func validateFinalReviewResult(review *ReviewAgentResult) error {
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
			switch fence {
			case "":
				fence = marker
			case marker:
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

func (d ReviewPRDef) ValidateAgentExecution(result any, execution *oneshot.AgentResult) error {
	review, ok := result.(*ReviewAgentResult)
	if ok && review.Parsed != nil && review.Parsed.Approved &&
		d.authoritativeRemoteCIPasses() {
		return nil
	}
	return validateReviewExecutionEvidence(result, execution, d.ChangedFiles)
}

func validateReviewExecutionEvidence(result any, execution *oneshot.AgentResult, changedFiles []string) error {
	review, ok := result.(*ReviewAgentResult)
	if !ok || review.Parsed == nil {
		return nil
	}
	if err := validateInconclusiveVerificationClaims(review.Parsed, execution); err != nil {
		return err
	}
	if err := validateReportedHostVerification(review.Parsed, execution); err != nil {
		return err
	}
	if !review.Parsed.Approved {
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
	var trusted []reviewCommandEvidenceDetails
	if strings.EqualFold(strings.TrimSpace(execution.ProviderID), "codex") {
		for _, evidence := range execution.ExecutionEvidence {
			details, trustworthy := classifyReviewCommandEvidenceDetails(evidence)
			if trustworthy {
				trusted = append(trusted, details)
			}
		}
	}

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
		if reviewNoTestScriptPolicyEvidence(call.Data) {
			trusted = append(trusted, reviewCommandEvidenceDetails{
				Kind:     reviewEvidenceTestPolicy,
				Language: "node",
				Targets: []reviewCoverageTarget{{
					Path:      path,
					Recursive: true,
				}},
			})
			continue
		}
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
		for _, provedKind := range reviewVerificationProofKinds(call.Data, kind) {
			trusted = append(trusted, reviewCommandEvidenceDetails{
				Kind:     provedKind,
				Language: language,
				Targets: []reviewCoverageTarget{{
					Path:      path,
					Recursive: language != "go",
				}},
			})
		}
	}
	if err := validateReviewEvidenceCoverage(changedFiles, trusted); err != nil {
		if strings.EqualFold(strings.TrimSpace(execution.ProviderID), "codex") {
			return fmt.Errorf("native Codex approval requires classifiable snapshot-root or harness-collected evidence: %w", err)
		}
		return fmt.Errorf("API-backed approval requires successful snapshot-bound run_verification evidence: %w; for Go, call kind=test because kind=build does not execute tests", err)
	}
	return nil
}

func reviewVerificationProofKinds(data map[string]any, legacyKind string) []string {
	if noTestScript, _ := data["no_test_script"].(bool); noTestScript {
		if reviewVerificationHasExactTestPolicyProof(data["proves"]) {
			return []string{reviewEvidenceTestPolicy}
		}
		return nil
	}
	// A successful Go test command with no test files proves compilation, not
	// execution. Keep this fail-closed even if stale or malformed evidence also
	// claims a test proof.
	if noTestFiles, _ := data["no_test_files"].(bool); noTestFiles {
		return []string{reviewEvidenceBuild}
	}
	raw, present := data["proves"]
	if !present {
		// Compatibility for persisted evidence created before run_verification
		// emitted explicit proof kinds.
		return []string{legacyKind}
	}
	var values []string
	switch proofs := raw.(type) {
	case []string:
		values = proofs
	case []any:
		for _, proof := range proofs {
			if value, ok := proof.(string); ok {
				values = append(values, value)
			}
		}
	default:
		return nil
	}
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if (value == reviewEvidenceBuild || value == reviewEvidenceTest) && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}

func reviewVerificationHasExactTestPolicyProof(raw any) bool {
	switch proofs := raw.(type) {
	case []string:
		return len(proofs) == 1 && strings.EqualFold(strings.TrimSpace(proofs[0]), reviewEvidenceTestPolicy)
	case []any:
		if len(proofs) != 1 {
			return false
		}
		proof, ok := proofs[0].(string)
		return ok && strings.EqualFold(strings.TrimSpace(proof), reviewEvidenceTestPolicy)
	default:
		return false
	}
}

func reviewNoTestScriptPolicyEvidence(data map[string]any) bool {
	if data == nil {
		return false
	}
	noTestScript, _ := data["no_test_script"].(bool)
	kind, _ := data["kind"].(string)
	language, _ := data["language"].(string)
	path, _ := data["path"].(string)
	pattern, _ := data["pattern"].(string)
	status, _ := data["status"].(string)
	evidence, _ := data["evidence"].(string)
	command, commandPresent := data["command"].(string)
	exitCode, exitPresent := reviewEvidenceExitCode(data["exit_code"])
	argv, argvPresent := reviewVerificationArgv(data["argv"])
	proofs := reviewVerificationProofKinds(data, reviewEvidenceTest)
	return noTestScript &&
		strings.EqualFold(strings.TrimSpace(kind), reviewEvidenceTest) &&
		strings.EqualFold(strings.TrimSpace(language), "node") &&
		normalizeReviewEvidencePath(path) != "" &&
		strings.TrimSpace(pattern) == "" &&
		strings.EqualFold(strings.TrimSpace(status), string(VerificationNotApplicable)) &&
		strings.EqualFold(strings.TrimSpace(evidence), "NO_TEST_GATE") &&
		commandPresent && strings.TrimSpace(command) == "" &&
		exitPresent && exitCode == -1 && argvPresent && len(argv) == 0 &&
		len(proofs) == 1 && proofs[0] == reviewEvidenceTestPolicy
}

func reviewVerificationArgv(raw any) ([]string, bool) {
	switch values := raw.(type) {
	case []string:
		return append([]string(nil), values...), true
	case []any:
		result := make([]string, 0, len(values))
		for _, value := range values {
			text, ok := value.(string)
			if !ok {
				return nil, false
			}
			result = append(result, text)
		}
		return result, true
	default:
		return nil, false
	}
}

func validateInconclusiveVerificationClaims(parsed *ParsedReview, execution *oneshot.AgentResult) error {
	if parsed == nil || execution == nil {
		return nil
	}
	hasTimedOutVerification := false
	inconclusiveKinds := make(map[string]bool)
	confirmedFailureKinds := make(map[string]bool)
	for _, call := range execution.ToolCalls {
		if call.Name != "run_verification" {
			continue
		}
		text := strings.ToLower(call.Result)
		for _, key := range []string{"error", "evidence", "status"} {
			if value, ok := call.Data[key].(string); ok {
				text += " " + strings.ToLower(value)
			}
		}
		kind, _ := call.Data["kind"].(string)
		kind = strings.ToLower(strings.TrimSpace(kind))
		evidence, _ := call.Data["evidence"].(string)
		status, _ := call.Data["status"].(string)
		if strings.EqualFold(strings.TrimSpace(evidence), "CONFIRMED_FAIL") ||
			strings.EqualFold(strings.TrimSpace(status), "FAIL") {
			confirmedFailureKinds[kind] = true
		}
		if reviewTextMentionsInconclusiveExecution(text) {
			hasTimedOutVerification = true
			inconclusiveKinds[kind] = true
		}
	}
	if !hasTimedOutVerification {
		return nil
	}

	if parsed.BuildVerification == VerificationFail &&
		(inconclusiveKinds[reviewEvidenceBuild] || inconclusiveKinds[""]) &&
		!confirmedFailureKinds[reviewEvidenceBuild] && !confirmedFailureKinds[""] {
		return fmt.Errorf("inconclusive build verification cannot be reported as FAIL")
	}
	if parsed.TestVerification == VerificationFail &&
		(inconclusiveKinds[reviewEvidenceTest] || inconclusiveKinds[""]) &&
		!confirmedFailureKinds[reviewEvidenceTest] && !confirmedFailureKinds[""] {
		return fmt.Errorf("inconclusive test verification cannot be reported as FAIL")
	}
	if parsed.FalsificationConclusion == FalsificationProved &&
		reviewClaimMentionsInconclusiveExecution(parsed.Falsification) {
		return fmt.Errorf("verification timeout is inconclusive and cannot prove the falsification hypothesis")
	}
	for _, finding := range parsed.Findings {
		claim := strings.Join([]string{finding.Title, finding.Evidence, finding.Impact, finding.Fix}, " ")
		if reviewClaimMentionsInconclusiveExecution(claim) {
			return fmt.Errorf("finding %s treats inconclusive verification as a product defect", finding.ID)
		}
	}
	return nil
}

func reviewTextMentionsInconclusiveExecution(text string) bool {
	text = strings.ToLower(text)
	for _, marker := range []string{
		"timed out",
		"timeout",
		"deadline exceeded",
		"context deadline",
		"cancelled",
		"canceled",
		"inconclusive",
		"unavailable",
		"not started",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func reviewClaimMentionsInconclusiveExecution(text string) bool {
	text = strings.ToLower(text)
	for _, marker := range []string{
		"timed out",
		"timeout",
		"deadline exceeded",
		"context deadline",
		"verification unavailable",
		"test unavailable",
		"command unavailable",
		"inconclusive verification",
		"verification was not started",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
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
	return buildApprovalCriticPrompt(originalPrompt, primaryResult, d.authoritativeRemoteCIPasses())
}

func reviewResultIsApproved(result any) bool {
	review, ok := result.(*ReviewAgentResult)
	return ok && review.Parsed != nil && review.Parsed.Approved
}

func buildApprovalCriticPrompt(originalPrompt string, primaryResult any, directEvidencePass bool) (string, error) {
	review, ok := primaryResult.(*ReviewAgentResult)
	if !ok || review.Parsed == nil {
		return "", fmt.Errorf("unexpected approval result type %T", primaryResult)
	}
	if !review.Parsed.Approved {
		return "", fmt.Errorf("approval critic requested for a non-approval result")
	}

	evidenceInstructions := `Re-read relevant source with tools. Look for evidence the prior review missed.`
	outcomeInstructions := `Independently decide whether approval survives. Your complete machine-validated review becomes the final result.`
	if directEvidencePass {
		evidenceInstructions = `Use one direct evidence pass. Tools are unavailable in this critic phase. The original prompt contains the exact snapshot and remote continuous integration evidence.`
		outcomeInstructions = `Independently decide whether approval survives. Do not request more evidence or another pass. Your complete machine-validated review becomes the final result.`
	}

	return `Perform an independent adversarial second-pass review using the original evidence below.

` + evidenceInstructions + `

The prior review is included only so you can challenge its claims. Do not trust its verdict, coverage, or falsification conclusion. Return a complete replacement review in the command's exact required format.

## Original Review Evidence

` + originalPrompt + `

## Prior Provisional Approval

` + review.Review + `

## Required Critic Outcome

` + outcomeInstructions, nil
}

// FixFindingDef implements oneshot.AgentDefinition for applying a fix to a finding.
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

// ReviewAgentResult is the typed output from a review agent execution.
type ReviewAgentResult struct {
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
