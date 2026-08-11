package artifactv1

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/agentcoord"
	legacy "m31labs.dev/buckley/pkg/artifact"
	"m31labs.dev/buckley/pkg/goalloop"
)

func TestArtifactV1_RenderersConformAndRemainDeterministic(t *testing.T) {
	t.Parallel()
	artifact := fullArtifact()
	if err := artifact.ValidateStrict(); err != nil {
		t.Fatalf("ValidateStrict: %v diagnostics=%+v table=%+v", err, artifact.Validate(), artifact.Blocks[3].Table)
	}

	terminal, err := RenderTerminal(artifact)
	if err != nil {
		t.Fatalf("RenderTerminal: %v", err)
	}
	againTerminal, err := RenderTerminal(artifact)
	if err != nil {
		t.Fatalf("RenderTerminal second call: %v", err)
	}
	if terminal != againTerminal {
		t.Fatal("terminal renderer is not deterministic")
	}

	markdown, err := RenderMarkdownPP(artifact)
	if err != nil {
		t.Fatalf("RenderMarkdownPP: %v", err)
	}
	if err := ValidateMarkdownPP(markdown); err != nil {
		t.Fatalf("rendered Markdown++ is invalid: %v\n%s", err, markdown)
	}
	golden, err := os.ReadFile(filepath.Join("testdata", "full_artifact.md.golden"))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if string(golden) != markdown {
		t.Fatalf("Markdown golden mismatch\nwant:\n%s\ngot:\n%s", golden, markdown)
	}

	jsonBytes, err := RenderJSON(artifact)
	if err != nil {
		fatalf(t, "RenderJSON", err)
	}
	decoded, report, err := DecodeProviderOutput(context.Background(), jsonBytes, OutputNativeJSONSchema, DecodeOptions{})
	if err != nil {
		t.Fatalf("DecodeProviderOutput: %v", err)
	}
	if report.Repaired || decoded.ArtifactID != artifact.ArtifactID {
		t.Fatalf("decoded report = %+v artifact=%+v", report, decoded)
	}

	sarif, err := RenderSARIF(artifact)
	if err != nil {
		t.Fatalf("RenderSARIF: %v", err)
	}
	var sarifDocument map[string]any
	if err := json.Unmarshal(sarif, &sarifDocument); err != nil {
		t.Fatalf("decode SARIF: %v", err)
	}
	if sarifDocument["version"] != "2.1.0" {
		t.Fatalf("SARIF version = %#v", sarifDocument["version"])
	}

	fluffy, err := RenderFluffyUI(artifact)
	if err != nil {
		t.Fatalf("RenderFluffyUI: %v", err)
	}
	if fluffy.Markdown != markdown || len(fluffy.Blocks) != len(artifact.Blocks) {
		t.Fatalf("FluffyUI projection did not preserve artifact content: %+v", fluffy)
	}
	acp, err := RenderACP(artifact)
	if err != nil {
		t.Fatalf("RenderACP: %v", err)
	}
	if acp.Type != "buckley.artifact" || acp.Text != markdown || acp.Artifact.ArtifactID != artifact.ArtifactID {
		t.Fatalf("ACP projection = %+v", acp)
	}
}

func TestArtifactV1_ValidationRejectsMismatchedBlockPayload(t *testing.T) {
	t.Parallel()
	artifact := New(KindAnalysis, StatusCompleted, "Bad block", "The payload should fail validation")
	artifact.Blocks = []Block{{Kind: BlockCode, Text: "not code payload"}}
	if err := artifact.ValidateStrict(); err == nil {
		t.Fatal("ValidateStrict succeeded for a mismatched block payload")
	}
}

func TestArtifactV1_NormalizedDoesNotMutateInput(t *testing.T) {
	t.Parallel()
	source := Artifact{Blocks: []Block{{Kind: BlockHeading, Text: " A heading "}}}
	_ = source.Normalized()
	if source.Blocks[0].Text != " A heading " || source.Blocks[0].Level != 0 {
		t.Fatalf("Normalized mutated source: %+v", source.Blocks[0])
	}
}

func TestArtifactV1_NegotiatesNativeSchemaThenToolFallback(t *testing.T) {
	t.Parallel()
	native := NegotiatedOutput(ProviderCapabilities{NativeJSONSchema: true, ToolCalls: true})
	if native.Mode != OutputNativeJSONSchema || native.JSONSchema["$id"] == nil {
		t.Fatalf("native contract = %+v", native)
	}
	tool := NegotiatedOutput(ProviderCapabilities{ToolCalls: true})
	if tool.Mode != OutputSubmitArtifact || tool.SubmitArtifact == nil || tool.SubmitArtifact.Name != "submit_artifact" {
		t.Fatalf("tool fallback = %+v", tool)
	}
	prompt := NegotiatedOutput(ProviderCapabilities{})
	if prompt.Mode != OutputPromptJSON || !strings.Contains(prompt.Prompt, SchemaVersion) {
		t.Fatalf("prompt fallback = %+v", prompt)
	}
	if got := ArtifactPrompt("base", tool); !strings.Contains(got, "submit_artifact exactly once") || strings.Contains(got, "Return exactly one JSON") {
		t.Fatalf("submit artifact prompt = %q", got)
	}
	descriptor := NegotiatedOutputDescriptor(ProviderCapabilities{ToolCalls: true})
	if descriptor.Mode != OutputSubmitArtifact || descriptor.SubmitArtifact == nil || descriptor.SubmitArtifact.Parameters != nil || descriptor.JSONSchema != nil {
		t.Fatalf("lightweight descriptor = %+v", descriptor)
	}

	schemaBytes, err := JSONSchemaBytes()
	if err != nil {
		t.Fatalf("JSONSchemaBytes: %v", err)
	}
	var schema map[string]any
	if err := json.Unmarshal(schemaBytes, &schema); err != nil {
		t.Fatalf("decode schema: %v", err)
	}
	if schema["$id"] == nil || schema["$defs"] == nil {
		t.Fatalf("schema missing contract fields: %s", schemaBytes)
	}
}

func TestArtifactV1_ProviderOutputRepairsOnlyWithinBound(t *testing.T) {
	t.Parallel()
	localRaw := []byte("```json\n{\"title\": \" local output \", \"summary\": \" repair omissions \", \"blocks\": [{\"kind\": \"heading\", \"text\": \"Details\"}]}\n```")
	local, report, err := DecodeProviderOutput(context.Background(), localRaw, OutputPromptJSON, DecodeOptions{MaxRepairAttempts: 1})
	if err != nil {
		t.Fatalf("local repair: %v", err)
	}
	if !report.Repaired || report.Attempts != 1 || local.SchemaVersion != SchemaVersion || local.ArtifactID == "" {
		t.Fatalf("local repair report=%+v artifact=%+v", report, local)
	}

	valid, err := RenderJSON(fullArtifact())
	if err != nil {
		t.Fatalf("RenderJSON: %v", err)
	}
	repairer := &testRepairer{response: valid}
	invalid := []byte(`{"schema_version":"buckley.artifact/v1","artifact_id":"art_bad","kind":"review","status":"completed","title":"bad","summary":"bad","blocks":[{"kind":"code"}]}`)
	repaired, repairedReport, err := DecodeProviderOutput(context.Background(), invalid, OutputSubmitArtifact, DecodeOptions{MaxRepairAttempts: 1, Repairer: repairer})
	if err != nil {
		t.Fatalf("external repair: %v", err)
	}
	if repairer.calls != 1 || !repairedReport.Repaired || repaired.ArtifactID != fullArtifact().ArtifactID {
		t.Fatalf("external repair calls=%d report=%+v artifact=%+v", repairer.calls, repairedReport, repaired)
	}

	wrapped := append([]byte(`{"artifact":`), append(valid, '}')...)
	if _, err := DecodeSubmitArtifact(wrapped); err != nil {
		t.Fatalf("DecodeSubmitArtifact: %v", err)
	}
}

func TestArtifactV1_RejectsBreakingSchemaChanges(t *testing.T) {
	t.Parallel()
	previous := CurrentSchemaDescriptor()
	candidate := CurrentSchemaDescriptor()
	candidate.Fields = cloneFieldSpecs(candidate.Fields)
	candidate.Fields["title"] = FieldSpec{Type: "number", Required: true}
	candidate.Fields["new_required"] = FieldSpec{Type: "string", Required: true}
	err := CheckBackwardCompatibility(previous, candidate)
	if err == nil {
		t.Fatal("CheckBackwardCompatibility accepted breaking changes")
	}
	compatibility, ok := err.(*CompatibilityError)
	if !ok || len(compatibility.Reasons) < 2 {
		t.Fatalf("compatibility error = %#v", err)
	}
	if err := CheckBackwardCompatibility(previous, CurrentSchemaDescriptor()); err != nil {
		t.Fatalf("same descriptor is incompatible: %v", err)
	}
}

func TestArtifactV1_MigratesLegacyPlanReviewGoalAndSubagentResult(t *testing.T) {
	t.Parallel()
	plan, err := FromPlanning(&legacy.PlanningArtifact{
		Artifact: legacy.Artifact{Feature: "harness", Status: "completed"},
		Context:  legacy.ContextSection{UserGoal: "improve harness", ExistingPatterns: []string{"ports and adapters"}},
		Tasks:    []legacy.TaskBreakdown{{Description: "add contract", FilePath: "pkg/artifact/v1"}},
	})
	if err != nil || plan.Kind != KindPlan {
		t.Fatalf("FromPlanning = %+v, %v", plan, err)
	}

	review, err := FromReview(&legacy.ReviewArtifact{
		Artifact:    legacy.Artifact{Feature: "harness", Status: "changes_requested"},
		IssuesFound: []legacy.Issue{{Title: "test gap", Severity: "quality", Description: "missing test", Location: "pkg/x.go:12", Fix: "add test"}},
	})
	if err != nil || review.Kind != KindReview || len(review.Findings) != 1 {
		t.Fatalf("FromReview = %+v, %v", review, err)
	}

	goal, err := FromGoalReport(goalloop.Report{
		RunID:       "run_goal",
		Statement:   "ship harness",
		Status:      "partial",
		Completed:   []goalloop.ReportCompleted{{TaskID: "task-1", Text: "done", EvidenceID: "ev_done"}},
		Parked:      []goalloop.ReportParked{{TaskID: "task-2", Title: "verify", Reason: "needs benchmark", Needs: "run benchmark"}},
		NextActions: []goalloop.ReportAction{{TaskID: "task-2", Text: "run benchmark"}},
	})
	if err != nil || goal.Kind != KindGoal || goal.Status != StatusIncomplete {
		t.Fatalf("FromGoalReport = %+v, %v", goal, err)
	}

	subagent, err := FromSubagentRun(agentcoord.AgentRun{
		ID:      "run_child",
		State:   agentcoord.AgentRunCompleted,
		Adapter: "local-process",
		Task:    agentcoord.AgentTaskSpec{Agent: "reviewer", Model: "model", Tier: "frontier"},
		Result:  agentcoord.AgentResult{Summary: "reviewed", EvidenceRefs: []string{"ev_report"}},
	})
	if err != nil || subagent.Kind != KindSubagentResult || len(subagent.EvidenceRefs) != 1 {
		t.Fatalf("FromSubagentRun = %+v, %v", subagent, err)
	}
}

type testRepairer struct {
	calls    int
	response []byte
}

func (r *testRepairer) RepairArtifact(_ context.Context, _ []byte, _ []Diagnostic) ([]byte, error) {
	r.calls++
	return r.response, nil
}

func fullArtifact() Artifact {
	artifact := Artifact{
		SchemaVersion: SchemaVersion,
		Kind:          KindReview,
		Status:        StatusCompleted,
		Title:         "Harness review",
		Summary:       "The durable harness result is ready for verification.",
		Blocks: []Block{
			{Kind: BlockHeading, Text: "Summary", Level: 2},
			{Kind: BlockProse, Text: "A typed artifact keeps every client in agreement."},
			{Kind: BlockFacts, Facts: []Fact{{Label: "Mode", Value: "dynamic"}, {Label: "Workers", Value: "3"}}},
			{Kind: BlockTable, Table: &Table{Headers: []string{"Metric", "Value"}, Rows: [][]string{{"p95", "12 ms"}}}},
			{Kind: BlockCode, Code: &CodeBlock{Language: "go", Content: "fmt.Println(\"```\")"}},
			{Kind: BlockDiff, Diff: &DiffBlock{Path: "pkg/harness.go", Content: "- old\n+ new"}},
			{Kind: BlockChecklist, Checklist: []ChecklistItem{{Text: "Tests pass", State: "completed"}, {Text: "Benchmark", State: "pending", Detail: "run before release"}}},
			{Kind: BlockFinding, Finding: &Finding{ID: "finding-block", Severity: "low", Confidence: 0.9, Title: "Visible state", Summary: "Show zero-result searches clearly."}},
			{Kind: BlockOperationSummary, Operation: &OperationSummary{Operation: "verification", Status: "passed", DurationMS: 12, Detail: "unit tests passed", Metrics: []Fact{{Label: "allocs", Value: "0"}}, EvidenceRefs: []EvidenceRef{{ID: "ev_operation", Label: "test output"}}}},
			{Kind: BlockEvidenceLink, Evidence: &EvidenceLink{ID: "ev_block", Label: "operation transcript"}},
		},
		Findings: []Finding{{
			ID:             "finding-top",
			Severity:       "high",
			Confidence:     0.95,
			Title:          "Recoverable worker loss",
			Summary:        "A lost worker must become resumable instead of disappearing.",
			Location:       &Location{Path: "pkg/subagent/coordinator.go", StartLine: 42, EndLine: 45},
			Recommendation: "Persist the result before releasing claims.",
			EvidenceRefs:   []EvidenceRef{{ID: "ev_finding", Label: "run ledger"}},
		}},
		Diagnostics:       []Diagnostic{{Level: "warning", Code: "harness.replay", Message: "Replay is waiting for an evidence fetch.", Location: &Location{Path: "pkg/evidence/store.go", StartLine: 12}}},
		EvidenceRefs:      []EvidenceRef{{ID: "ev_summary", Label: "summary evidence", Kind: "test_output"}},
		NextActions:       []NextAction{{ID: "action-1", Description: "Run the performance suite.", Priority: "high", EvidenceRefs: []EvidenceRef{{ID: "ev_summary"}}}},
		IncompleteReasons: []string{"Cross-platform benchmark is pending."},
		Metadata:          map[string]string{"session": "sess-1", "surface": "tui"},
	}
	artifact.ArtifactID = ""
	return artifact.Normalized()
}

func cloneFieldSpecs(source map[string]FieldSpec) map[string]FieldSpec {
	cloned := make(map[string]FieldSpec, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func fatalf(t *testing.T, operation string, err error) {
	t.Helper()
	t.Fatalf("%s: %v", operation, err)
}
