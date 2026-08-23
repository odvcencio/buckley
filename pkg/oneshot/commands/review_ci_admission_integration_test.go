package commands

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/diffsignal"
	"m31labs.dev/buckley/pkg/reviewpolicy"
)

func TestCIAdmissionNonApprovalUsability_SingleCommentWithZeroRequiredContexts(t *testing.T) {
	ctx := stablePRRevalidationContext()
	ctx.Files = []string{"pkg/ratchet.go"}
	ctx.ContextStatus = []PRContextStatus{{Source: "CI admission", Status: "blocked"}}
	ctx.CIAdmission = testCIAdmissionReceipt(t, ctx, true, nil)
	if ctx.CIAdmission.Decision != reviewpolicy.CIAdmissionDeny ||
		ctx.CIAdmission.Reason != reviewpolicy.CIAdmissionReasonNoRequiredContexts {
		t.Fatalf("admission = %s/%s", ctx.CIAdmission.Decision, ctx.CIAdmission.Reason)
	}

	def := ReviewPRDef{
		ChangedFiles:           ctx.Files,
		ContextIncomplete:      ctx.HasIncompleteContext(),
		CIStatus:               ctx.PR.CIStatus,
		CIProvenance:           ctx.CIProvenance,
		CIAdmission:            ctx.CIAdmission,
		CIAdmissionExpectation: ctx.CIAdmissionExpectation(),
	}
	if !containsReviewTool(def.AllowedTools(), "run_verification") || len(def.AgentEvidenceRequests()) == 0 {
		t.Fatal("zero-required admission did not preserve focused verification")
	}

	review := completeReviewWithCoverage(
		"- **File**: `pkg/ratchet.go` — reviewed the exact changed file.\n" +
			"- **Feedback disposition**: `NONE_SUPPLIED` — no prior feedback was supplied.\n" +
			"- **Verification**: focused verification completed.",
	)
	review = strings.Replace(review, "## Grade: A", "## Grade: B", 1)
	review = strings.Replace(review, "**Recommendation**: APPROVE", "**Recommendation**: NEEDS DISCUSSION", 1)
	result, err := def.ParseResult(review)
	if err != nil {
		t.Fatalf("ParseResult: %v", err)
	}
	if err := def.ValidateResult(result); err != nil {
		t.Fatalf("nonapproval COMMENT validation: %v", err)
	}
	if result.(*ReviewAgentResult).Parsed.Approved {
		t.Fatal("NEEDS DISCUSSION review parsed as approval")
	}

	changed, err := revalidatePRContext(ctx, stablePRRevalidationRunner(prRevalidationOutputs{requiredChecks: `[]`}))
	if err != nil {
		t.Fatalf("stable deny revalidation: %v", err)
	}
	if changed != "" {
		t.Fatalf("stable deny changed evidence = %q", changed)
	}
	changed, err = revalidatePRContext(ctx, stablePRRevalidationRunner(prRevalidationOutputs{}))
	if err != nil {
		t.Fatalf("changed deny revalidation: %v", err)
	}
	if !strings.Contains(changed, "required CI contexts changed") {
		t.Fatalf("changed deny receipt was not invalidated: %q", changed)
	}
}

func TestCIAdmissionNonApprovalUsability_ShardedRequestChangesWithChangedTest(t *testing.T) {
	ctx := stablePRRevalidationContext()
	ctx.Files = []string{"pkg/a.go", "pkg/b_test.go"}
	ctx.ContextStatus = []PRContextStatus{{Source: "CI admission", Status: "unavailable"}}
	required := []reviewpolicy.CIRequiredContext{{Name: "required/unit", State: "SUCCESS"}}
	ctx.CIAdmission = testCIAdmissionReceipt(t, ctx, true, required)
	if ctx.CIAdmission.Decision != reviewpolicy.CIAdmissionUnavailable ||
		ctx.CIAdmission.Reason != reviewpolicy.CIAdmissionReasonTestReachabilityUnavailable {
		t.Fatalf("admission = %s/%s", ctx.CIAdmission.Decision, ctx.CIAdmission.Reason)
	}

	shards := []diffsignal.Shard{
		{Files: []string{"pkg/a.go"}},
		{Files: []string{"pkg/b_test.go"}},
	}
	results, err := RunPRShardsConcurrently(context.Background(), shards, 2, func(_ context.Context, shard diffsignal.Shard, index int) (*ParsedReview, error) {
		def := ReviewPRDef{
			ChangedFiles:           shard.Files,
			ContextIncomplete:      ctx.HasIncompleteContext(),
			CIStatus:               ctx.PR.CIStatus,
			CIProvenance:           ctx.CIProvenance,
			CIAdmission:            ctx.CIAdmission,
			CIAdmissionExpectation: ctx.CIAdmissionExpectation(),
		}
		if !containsReviewTool(def.AllowedTools(), "run_verification") || len(def.AgentEvidenceRequests()) == 0 {
			return nil, fmt.Errorf("shard %d lost focused verification", index+1)
		}
		findingID := fmt.Sprintf("FINDING-%03d", index+1)
		result, parseErr := def.ParseResult(requestChangesReviewForFile(shard.Files[0], findingID))
		if parseErr != nil {
			return nil, parseErr
		}
		if validateErr := def.ValidateResult(result); validateErr != nil {
			return nil, validateErr
		}
		return result.(*ReviewAgentResult).Parsed, nil
	})
	if err != nil {
		t.Fatalf("sharded nonapproval review: %v", err)
	}
	merged, _ := MergeShardedPRReview(results, nil, ctx.Files, DefaultSynthesisFanIn)
	if merged.Approved || len(merged.Findings) != 2 {
		t.Fatalf("merged request-changes result = approved:%t findings:%d", merged.Approved, len(merged.Findings))
	}

	changed, err := revalidatePRContext(ctx, stablePRRevalidationRunner(prRevalidationOutputs{
		requiredChecks: `[{"name":"required/unit","state":"SUCCESS"}]`,
	}))
	if err != nil {
		t.Fatalf("stable unavailable revalidation: %v", err)
	}
	if changed != "" {
		t.Fatalf("stable unavailable changed evidence = %q", changed)
	}
	changed, err = revalidatePRContext(ctx, stablePRRevalidationRunner(prRevalidationOutputs{
		requiredChecks: `[{"name":"required/replacement","state":"SUCCESS"}]`,
	}))
	if err != nil {
		t.Fatalf("changed unavailable revalidation: %v", err)
	}
	if !strings.Contains(changed, "required CI contexts changed") {
		t.Fatalf("changed unavailable receipt was not invalidated: %q", changed)
	}
}

func testCIAdmissionReceipt(
	t *testing.T,
	ctx *PRContext,
	requiredAvailable bool,
	required []reviewpolicy.CIRequiredContext,
) reviewpolicy.CIAdmissionReceipt {
	t.Helper()
	receipt, err := reviewpolicy.NewCIAdmissionReceipt(reviewpolicy.CIAdmissionInput{
		Expectation:               ctx.CIAdmissionExpectation(),
		RequiredContextsAvailable: requiredAvailable,
		RequiredContexts:          required,
	})
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}

func requestChangesReviewForFile(file, findingID string) string {
	review := completeReviewWithCoverage(
		fmt.Sprintf("- **File**: `%s` — reviewed the exact changed file.\n", file) +
			"- **Feedback disposition**: `NONE_SUPPLIED` — no prior feedback was supplied.\n" +
			"- **Verification**: focused verification reproduced the defect.",
	)
	review = strings.Replace(review, "## Grade: A", "## Grade: C", 1)
	review = strings.Replace(review, "**Conclusion**: DISPROVED", "**Conclusion**: PROVED", 1)
	review = strings.Replace(review, "## Findings\nNone.", fmt.Sprintf(`## Findings
### %s: [MAJOR] Demonstrated shard defect
- **File**: %s:1
- **Evidence**: Focused verification reproduces the changed behavior deterministically.
- **Business Impact**: The changed behavior can produce an incorrect result.
- **Fix**: Correct the implementation and retain the focused regression test.`, findingID, file), 1)
	review = strings.Replace(review, "**Recommendation**: APPROVE", "**Recommendation**: REQUEST CHANGES", 1)
	review = strings.Replace(review, "**Blockers**: None", "**Blockers**: "+findingID, 1)
	return review
}

func containsReviewTool(tools []string, wanted string) bool {
	for _, tool := range tools {
		if tool == wanted {
			return true
		}
	}
	return false
}
