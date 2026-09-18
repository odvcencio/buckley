package experiment

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/transparency"
)

func TestManifestCriterionRetainsTargetPresenceWithoutText(t *testing.T) {
	for _, tc := range []struct {
		hash     string
		nonempty bool
	}{
		{hashText(""), false},
		{strings.ToUpper(hashText("")), false},
		{hashText("ok"), true},
		{hashText("\n"), true},
		{"", false},
		{"not-a-hash", false},
		{"00", false},
	} {
		manifest := &RunInputManifest{Criteria: []RunManifestCriterion{{ID: 1, Name: "redacted", Type: CriterionContains, TargetHash: tc.hash, Weight: 1}}}
		criteria := manifest.criteriaDefinitions()
		if len(criteria) != 1 || criteria[0].Target != "" || criteria[0].targetKnownNonempty != tc.nonempty {
			t.Fatalf("redacted target presence for %q: %+v", tc.hash, criteria)
		}
		assessment := assessCriteria(criteria, []CriterionEvaluation{{CriterionID: 1, Passed: true}})
		if assessment.Verified != tc.nonempty {
			t.Errorf("redacted %q verification = %+v", tc.hash, assessment)
		}
	}
}

func TestExperimentSnapshotRoundTripCompareAndDigestSurvivesPrettyJSON(t *testing.T) {
	exp, runs, evals := snapshotFixture(t)
	snapshot := mustSnapshot(t, exp, runs, evals)
	var encoded bytes.Buffer
	if err := EncodeSnapshot(&encoded, snapshot); err != nil {
		t.Fatalf("EncodeSnapshot: %v", err)
	}
	for _, want := range []string{`"ExperimentID"`, `"InputManifest"`, `"ModelExecutions"`} {
		if !strings.Contains(encoded.String(), want) {
			t.Fatalf("snapshot JSON missing v1 Go-shaped row key %q:\n%s", want, encoded.String())
		}
	}

	var pretty bytes.Buffer
	if err := json.Indent(&pretty, encoded.Bytes(), "", "    "); err != nil {
		t.Fatalf("Indent: %v", err)
	}
	decoded, err := DecodeSnapshot(&pretty)
	if err != nil {
		t.Fatalf("DecodeSnapshot pretty JSON: %v", err)
	}
	report, err := decoded.Compare()
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if len(report.Variants) != 1 || !report.Variants[0].Verified {
		t.Fatalf("report variants = %+v, want one verified variant", report.Variants)
	}
	if report.Variants[0].ProvenanceStatus != "requested input manifest captured; actual backend revision, baseline, harness, and tool versions not captured" {
		t.Fatalf("provenance status = %q", report.Variants[0].ProvenanceStatus)
	}
	if decoded.Provenance != ExperimentSnapshotProvenance {
		t.Fatalf("snapshot provenance = %q", decoded.Provenance)
	}
}

func TestExperimentSnapshotDecodesFrozenV1Fixture(t *testing.T) {
	file, err := os.Open("testdata/snapshot-v1.json")
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer file.Close()
	snapshot, err := DecodeSnapshot(file)
	if err != nil {
		t.Fatalf("DecodeSnapshot fixture: %v", err)
	}
	report, err := snapshot.Compare()
	if err != nil {
		t.Fatalf("Compare fixture: %v", err)
	}
	if snapshot.Version != ExperimentSnapshotVersion || snapshot.Digest.Value != "5ffc8a8e94ecd28f86074c17e9b000ef33a3697d8b3c595f879d5d97e9df9a50" {
		t.Fatalf("fixture identity changed: version=%q digest=%+v", snapshot.Version, snapshot.Digest)
	}
	if len(snapshot.Runs) != 1 || snapshot.Runs[0].Metrics.Usage != nil || snapshot.Runs[0].Metrics.CostUnknown {
		t.Fatalf("fixture usage evidence = %+v/%v, want legacy absent evidence", snapshot.Runs[0].Metrics.Usage, snapshot.Runs[0].Metrics.CostUnknown)
	}
	if len(report.Variants) != 1 || !report.Variants[0].Verified || report.Variants[0].OutputPreview != "ok retained full output" {
		t.Fatalf("fixture report = %+v", report)
	}
}

func TestExperimentSnapshotRejectsMalformedInputs(t *testing.T) {
	exp, runs, evals := snapshotFixture(t)
	snapshot := mustSnapshot(t, exp, runs, evals)
	var encoded bytes.Buffer
	if err := EncodeSnapshot(&encoded, snapshot); err != nil {
		t.Fatalf("EncodeSnapshot: %v", err)
	}
	base := encoded.String()

	tests := []struct {
		name string
		json string
		want string
	}{
		{
			name: "unsupported version",
			json: strings.Replace(base, `"version": "buckley-experiment-snapshot-v1"`, `"version": "buckley-experiment-snapshot-v2"`, 1),
			want: "unsupported experiment snapshot version",
		},
		{
			name: "malformed digest",
			json: strings.Replace(base, snapshot.Digest.Value, "not-a-sha256", 1),
			want: "experiment snapshot digest is malformed",
		},
		{
			name: "duplicate key",
			json: strings.Replace(base, `"kind": "buckley.experiment.snapshot"`, `"kind": "buckley.experiment.snapshot", "kind": "duplicate"`, 1),
			want: "duplicate key",
		},
		{
			name: "trailing JSON",
			json: base + "{}",
			want: "trailing data",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DecodeSnapshot(strings.NewReader(tt.json))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("DecodeSnapshot error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestExperimentSnapshotEncodeDepthMatchesDecodeDepth(t *testing.T) {
	exp, runs, evals := snapshotFixture(t)
	exp.Variants[0].CustomConfig = map[string]any{
		"nested": nestedSnapshotValue(experimentSnapshotMaxDepth - 16),
	}
	snapshot := mustSnapshot(t, exp, runs, evals)
	var encoded bytes.Buffer
	if err := EncodeSnapshot(&encoded, snapshot); err != nil {
		t.Fatalf("EncodeSnapshot below depth cap: %v", err)
	}
	if _, err := DecodeSnapshot(bytes.NewReader(encoded.Bytes())); err != nil {
		t.Fatalf("DecodeSnapshot below depth cap: %v", err)
	}

	exp, runs, evals = snapshotFixture(t)
	exp.Variants[0].CustomConfig = map[string]any{
		"nested": nestedSnapshotValue(experimentSnapshotMaxDepth + 8),
	}
	snapshot = mustSnapshot(t, exp, runs, evals)
	encoded.Reset()
	err := EncodeSnapshot(&encoded, snapshot)
	if err == nil || !strings.Contains(err.Error(), "maximum depth") {
		t.Fatalf("EncodeSnapshot excessive depth error = %v, want depth error", err)
	}
	if encoded.Len() != 0 {
		t.Fatalf("EncodeSnapshot wrote %d bytes before depth rejection", encoded.Len())
	}
}

func TestDecodeSnapshotRejectsExcessiveDepth(t *testing.T) {
	exp, runs, evals := snapshotFixture(t)
	exp.Variants[0].CustomConfig = map[string]any{
		"nested": nestedSnapshotValue(experimentSnapshotMaxDepth + 8),
	}
	snapshot := mustSnapshot(t, exp, runs, evals)
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	_, err = DecodeSnapshot(bytes.NewReader(data))
	if err == nil || !strings.Contains(err.Error(), "maximum depth") {
		t.Fatalf("DecodeSnapshot error = %v, want depth error", err)
	}
}

func TestExperimentSnapshotMissingEvaluationStaysUnverifiedOffline(t *testing.T) {
	exp, runs, _ := snapshotFixture(t)
	snapshot := mustSnapshot(t, exp, runs, nil)
	report, err := snapshot.Compare()
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if len(report.Variants) != 1 {
		t.Fatalf("variants = %d, want 1", len(report.Variants))
	}
	got := report.Variants[0]
	if got.Verified || got.RankEligible || got.VerificationStatus != "unverified: missing automated evaluation" {
		t.Fatalf("variant = %+v, want retained missing eval to remain unverified", got)
	}
}

func TestExperimentSnapshotLegacyRunAndFullOutputPreserved(t *testing.T) {
	exp, _, _ := snapshotFixture(t)
	longOutput := strings.Repeat("full public output ", 80)
	run := Run{
		ID:           "run-legacy",
		ExperimentID: exp.ID,
		VariantID:    exp.Variants[0].ID,
		Status:       RunCompleted,
		Output:       longOutput,
	}
	evals := map[string][]CriterionEvaluation{"run-legacy": {{
		RunID:       "run-legacy",
		CriterionID: exp.Criteria[0].ID,
		Passed:      true,
		Score:       1,
		Details:     "ok retained",
		EvaluatedAt: time.Now(),
	}}}
	snapshot := mustSnapshot(t, exp, []Run{run}, evals)
	var encoded bytes.Buffer
	if err := EncodeSnapshot(&encoded, snapshot); err != nil {
		t.Fatalf("EncodeSnapshot: %v", err)
	}
	decoded, err := DecodeSnapshot(&encoded)
	if err != nil {
		t.Fatalf("DecodeSnapshot: %v", err)
	}
	if decoded.Runs[0].Output != longOutput {
		t.Fatalf("output length = %d, want full %d", len(decoded.Runs[0].Output), len(longOutput))
	}
	report, err := decoded.Compare()
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if !strings.Contains(report.Variants[0].ProvenanceStatus, "legacy run") {
		t.Fatalf("provenance = %q, want legacy disclosure", report.Variants[0].ProvenanceStatus)
	}
}

func TestExperimentSnapshotLegacyMissingEvaluationStaysUnverified(t *testing.T) {
	exp, _, _ := snapshotFixture(t)
	run := Run{
		ID:           "run-legacy-unverified",
		ExperimentID: exp.ID,
		VariantID:    exp.Variants[0].ID,
		Status:       RunCompleted,
		Output:       "ok",
	}
	snapshot := mustSnapshot(t, exp, []Run{run}, nil)
	report, err := snapshot.Compare()
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	got := report.Variants[0]
	if got.Verified || got.VerificationStatus != "unverified: missing automated evaluation" || !strings.Contains(got.ProvenanceStatus, "legacy run") {
		t.Fatalf("variant = %+v, want legacy unverified disclosure", got)
	}
}

func TestExperimentSnapshotDoesNotAliasInputsAndPreservesLargeIntegerConfig(t *testing.T) {
	exp, runs, evals := snapshotFixture(t)
	exp.Variants[0].CustomConfig = map[string]any{"seed": json.Number("9007199254740993")}
	manifest, err := buildRunInputManifest(exp, exp.Variants[0], exp.Task.Timeout)
	if err != nil {
		t.Fatalf("buildRunInputManifest: %v", err)
	}
	runs[0].InputManifest = manifest

	snapshot := mustSnapshot(t, exp, runs, evals)
	exp.Variants[0].Name = "mutated"
	runs[0].Output = "mutated"
	evals["run-snapshot"][0].Details = "mutated"

	if snapshot.Experiment.Variants[0].Name == "mutated" || snapshot.Runs[0].Output == "mutated" || snapshot.Evaluations["run-snapshot"][0].Details == "mutated" {
		t.Fatalf("snapshot aliases caller inputs: %+v", snapshot)
	}
	gotSeed := snapshot.Experiment.Variants[0].CustomConfig["seed"]
	if number, ok := gotSeed.(json.Number); !ok || number.String() != "9007199254740993" {
		t.Fatalf("custom seed = %#v, want json.Number large integer", gotSeed)
	}
	snapshot.Runs[0].Output = "mutated after seal"
	if _, err := snapshot.Compare(); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("mutated snapshot Compare error = %v, want digest mismatch", err)
	}
}

func TestExperimentSnapshotPreservesUsageEvidence(t *testing.T) {
	exp, runs, evals := snapshotFixture(t)
	reasoning := 2
	runs[0].Metrics = RunMetrics{
		PromptTokens:     10,
		CompletionTokens: 5,
		TotalCost:        0,
		Usage: &transparency.TokenUsage{
			Input:                10,
			Output:               5,
			ReportedTotal:        15,
			ReportedReasoning:    &reasoning,
			UsageEvidencePresent: true,
		},
	}
	snapshot := mustSnapshot(t, exp, runs, evals)
	*runs[0].Metrics.Usage.ReportedReasoning = 99

	var encoded bytes.Buffer
	if err := EncodeSnapshot(&encoded, snapshot); err != nil {
		t.Fatalf("EncodeSnapshot: %v", err)
	}
	decoded, err := DecodeSnapshot(&encoded)
	if err != nil {
		t.Fatalf("DecodeSnapshot: %v", err)
	}
	got := decoded.Runs[0].Metrics.Usage
	if got == nil || got.ReportedReasoning == nil || *got.ReportedReasoning != 2 || got.Input != 10 || got.Output != 5 {
		t.Fatalf("decoded usage = %+v, want retained rich evidence", got)
	}
	report, err := decoded.Compare()
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if evidence := report.Variants[0].CostEvidence; !evidence.Comparable || evidence.Status != CostEvidenceKnown {
		t.Fatalf("snapshot cost evidence = %+v, want known comparable zero", evidence)
	}
}

func TestExperimentSnapshotRejectsAmbiguousEvaluationReferences(t *testing.T) {
	exp, runs, _ := snapshotFixture(t)
	_, err := NewSnapshot(exp, runs, map[string][]CriterionEvaluation{
		"missing-run": {{RunID: "missing-run", CriterionID: 1, Passed: true, Score: 1}},
	})
	if err == nil || !strings.Contains(err.Error(), "missing run id") {
		t.Fatalf("NewSnapshot error = %v, want missing run id", err)
	}

	_, err = NewSnapshot(exp, runs, map[string][]CriterionEvaluation{
		"run-snapshot": {{RunID: "run-snapshot", CriterionID: 99, Passed: true, Score: 1}},
	})
	if err == nil || !strings.Contains(err.Error(), "missing criterion id 99") {
		t.Fatalf("NewSnapshot error = %v, want missing criterion id", err)
	}
}

func TestExperimentSnapshotCompareDoesNotExecuteRetainedCommandCriteria(t *testing.T) {
	exp, runs, evals := snapshotFixture(t)
	sentinel := filepath.Join(t.TempDir(), "offline-sentinel")
	exp.Criteria = append(exp.Criteria, SuccessCriterion{
		ID:     2,
		Name:   "command retained only",
		Type:   CriterionCommand,
		Target: "touch " + sentinel,
		Weight: 1,
	})
	manifest, err := buildRunInputManifest(exp, exp.Variants[0], exp.Task.Timeout)
	if err != nil {
		t.Fatalf("buildRunInputManifest: %v", err)
	}
	runs[0].InputManifest = manifest
	evals["run-snapshot"] = append(evals["run-snapshot"], CriterionEvaluation{
		RunID:       "run-snapshot",
		CriterionID: 2,
		Passed:      true,
		Score:       1,
		Details:     "retained command result",
		EvaluatedAt: time.Now(),
	})
	snapshot := mustSnapshot(t, exp, runs, evals)
	report, err := snapshot.Compare()
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if !report.Variants[0].Verified {
		t.Fatalf("variant = %+v, want retained command row to participate without execution", report.Variants[0])
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("offline compare executed retained command criterion; stat err=%v", err)
	}
}

func TestEncodeSnapshotRejectsOversizedOutputBeforeWriting(t *testing.T) {
	exp, runs, evals := snapshotFixture(t)
	runs[0].Output = strings.Repeat("x", ExperimentSnapshotMaxBytes)
	snapshot, err := NewSnapshot(exp, runs, evals)
	if err != nil {
		t.Fatalf("NewSnapshot: %v", err)
	}
	var out bytes.Buffer
	err = EncodeSnapshot(&out, snapshot)
	if err == nil || !strings.Contains(err.Error(), "snapshot exceeds") {
		t.Fatalf("EncodeSnapshot error = %v, want size rejection", err)
	}
	if out.Len() != 0 {
		t.Fatalf("EncodeSnapshot wrote %d bytes before rejecting oversized snapshot", out.Len())
	}
}

func snapshotFixture(t *testing.T) (*Experiment, []Run, map[string][]CriterionEvaluation) {
	t.Helper()
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	exp := &Experiment{
		ID:        "exp-snapshot",
		Name:      "snapshot fixture",
		CreatedAt: now,
		Task: Task{
			Prompt:  "produce ok",
			Timeout: time.Second,
		},
		Variants: []Variant{{
			ID:         "variant-snapshot",
			Name:       "snapshot-model",
			ModelID:    "provider/model",
			ProviderID: "provider",
		}},
		Criteria: []SuccessCriterion{{
			ID:     1,
			Name:   "contains ok",
			Type:   CriterionContains,
			Target: "ok",
			Weight: 1,
		}},
	}
	manifest, err := buildRunInputManifest(exp, exp.Variants[0], exp.Task.Timeout)
	if err != nil {
		t.Fatalf("buildRunInputManifest: %v", err)
	}
	done := now.Add(time.Second)
	runs := []Run{{
		ID:            "run-snapshot",
		ExperimentID:  exp.ID,
		VariantID:     exp.Variants[0].ID,
		SessionID:     "session-snapshot",
		Branch:        "experiment/snapshot",
		Status:        RunCompleted,
		Output:        "ok",
		StartedAt:     now,
		CompletedAt:   &done,
		InputManifest: manifest,
		ModelExecutions: []model.ExecutionIdentity{{
			RequestedModel: "provider/model",
			SelectedModel:  "provider/model",
			ProviderID:     "provider",
			ResponseModel:  "provider/model-2026-09-05",
			ResponseID:     "resp-snapshot",
		}},
	}}
	evals := map[string][]CriterionEvaluation{
		"run-snapshot": {{
			RunID:       "run-snapshot",
			CriterionID: 1,
			Passed:      true,
			Score:       1,
			Details:     "ok retained",
			EvaluatedAt: done,
		}},
	}
	return exp, runs, evals
}

func mustSnapshot(t *testing.T, exp *Experiment, runs []Run, evals map[string][]CriterionEvaluation) *ExperimentSnapshot {
	t.Helper()
	snapshot, err := NewSnapshot(exp, runs, evals)
	if err != nil {
		t.Fatalf("NewSnapshot: %v", err)
	}
	return snapshot
}

func nestedSnapshotValue(depth int) any {
	value := any("leaf")
	for i := 0; i < depth; i++ {
		value = []any{value}
	}
	return value
}
