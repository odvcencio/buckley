package artifactv1

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

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

func TestArtifactV1_SubmissionExampleAppliesBeforeToolsDisabledFallback(t *testing.T) {
	t.Parallel()
	contract := NegotiatedOutput(ProviderCapabilities{ToolCalls: true})
	for _, base := range []string{"", "Gather requested source only."} {
		prompt := ArtifactPrompt(base, contract)
		start := strings.Index(prompt, `{"artifact":`)
		fallback := strings.Index(prompt, "If tools are disabled")
		if start < 0 || fallback < 0 || start >= fallback {
			t.Fatalf("minimal tool arguments must precede conditional JSON fallback: %s", prompt)
		}
		if strings.Count(prompt, `{"artifact":`) != 1 {
			t.Fatalf("tool and JSON fallback must share one example: %s", prompt)
		}
		var envelope struct {
			Artifact   json.RawMessage `json:"artifact"`
			SourceRefs []string        `json:"source_refs"`
		}
		if err := json.NewDecoder(strings.NewReader(prompt[start:])).Decode(&envelope); err != nil {
			t.Fatal(err)
		}
		if len(envelope.SourceRefs) != 1 || envelope.SourceRefs[0] != "all" {
			t.Fatalf("example changed capture selector: %v", envelope.SourceRefs)
		}
		raw, err := json.Marshal(map[string]json.RawMessage{"artifact": envelope.Artifact})
		if err != nil {
			t.Fatal(err)
		}
		artifact, err := DecodeSubmitArtifact(raw)
		if err != nil || artifact.Status != StatusIncomplete || len(artifact.IncompleteReasons) == 0 {
			t.Fatalf("shared example must validate without implying completion: %+v %v", artifact, err)
		}
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
	for _, required := range []string{"If tools are disabled", "same submission arguments", "source_refs", "incomplete_reasons"} {
		if got := ArtifactPrompt("base", tool); !strings.Contains(got, required) {
			t.Fatalf("submit artifact fallback missing %q: %s", required, got)
		}
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
