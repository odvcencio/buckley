package main

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	artifactv1 "m31labs.dev/buckley/pkg/artifact/v1"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

func coverageSource(t *testing.T, sink *builtin.ArtifactSubmission, path, content string, start, end int) string {
	t.Helper()
	page := map[string]any{"start_line": start, "end_line": end}
	visible := strings.TrimSuffix(strings.Join(strings.SplitAfter(content, "\n")[start-1:end], ""), "\n")
	ref, err := sink.CaptureReadSource(&builtin.Result{Success: true, ShouldAbridge: true, Data: map[string]any{"path": path, "content": content, "page": page}, DisplayData: map[string]any{"path": path, "content": visible, "page": page}})
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

func coverageRows(t *testing.T, a artifactv1.Artifact) [][]string {
	t.Helper()
	if len(a.Blocks) == 0 {
		t.Fatal("coverage table missing")
	}
	table := a.Blocks[len(a.Blocks)-1].Table
	if table == nil || !reflect.DeepEqual(table.Headers, []string{"required_source_text", "evidence_status", "source_ref", "start_line", "end_line"}) {
		t.Fatalf("invalid coverage table: %+v", table)
	}
	return table.Rows
}

func TestSourceTextRequirements_Instruction(t *testing.T) {
	if got := sourceTextRequirementInstruction(nil); got != "" {
		t.Fatalf("empty input: got %q", got)
	}
	literals := []string{" alpha ", "quote\"literal", "line\nbreak"}
	got := sourceTextRequirementInstruction(literals)
	if strings.Contains(got, "report each required literal verbatim") {
		t.Fatalf("stale verbatim-report directive present: %q", got)
	}
	encoded, _ := json.Marshal(literals)
	for _, want := range []string{string(encoded), `read_file`, `source_refs ["all"]`, "leave blocks and evidence_refs empty", "do not write a required_source_text table", "host-captured pages", "caller findings", "does not verify summary accuracy", "semantic properties"} {
		if !strings.Contains(got, want) {
			t.Fatalf("instruction missing %q: %q", want, got)
		}
	}
}

func TestSourceTextRequirements_ReportedAgainstReturnedCaptures(t *testing.T) {
	sink := &builtin.ArtifactSubmission{}
	ref := coverageSource(t, sink, "/never-reread/a", "ignored\nalpha\nbeta\nend\n", 2, 3)
	coverageSource(t, sink, "/never-reread/b", "Gamma\n", 1, 1)
	a := artifactv1.New(artifactv1.KindSubagentResult, artifactv1.StatusCompleted, "Result", "All items including Checksum and Gamma were covered.")
	if err := sink.SubmitWithSources(a, []string{ref}); err != nil {
		t.Fatal(err)
	}
	a, _ = sink.Artifact()
	before, _ := json.Marshal(a)
	got, err := applySourceTextRequirements(a, []string{"alpha", "alpha\nbeta\n", "Gamma", "Checksum", "ignored"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != artifactv1.StatusIncomplete || len(got.IncompleteReasons) == 0 || got.Summary != a.Summary || got.ArtifactID == a.ArtifactID {
		t.Fatalf("missing evidence not reflected honestly: %+v", got)
	}
	rows := coverageRows(t, got)
	want := [][]string{{"alpha", "observed", ref, "2", "2"}, {"alpha\nbeta\n", "observed", ref, "2", "3"}, {"Gamma", "not_observed", "", "", ""}, {"Checksum", "not_observed", "", "", ""}, {"ignored", "not_observed", "", "", ""}}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("coverage = %#v, want %#v", rows, want)
	}
	if got.Blocks[0].Table.Rows[0][4] != "alpha\nbeta\n" {
		t.Fatal("captured bytes changed")
	}
	got.Blocks[0].Table.Rows[0][4] = "changed detached result"
	after, _ := json.Marshal(a)
	if string(before) != string(after) {
		t.Fatal("coverage result aliases input artifact")
	}
}

func TestSourceTextRequirements_NoStatusUpgrade(t *testing.T) {
	for _, status := range []artifactv1.ArtifactStatus{artifactv1.StatusCompleted, artifactv1.StatusIncomplete, artifactv1.StatusFailed, artifactv1.StatusBlocked} {
		t.Run(string(status), func(t *testing.T) {
			sink := &builtin.ArtifactSubmission{}
			coverageSource(t, sink, "/source", "alpha\n", 1, 1)
			a := artifactv1.New(artifactv1.KindSubagentResult, status, "Result", "unverified summary")
			if status != artifactv1.StatusCompleted {
				a.IncompleteReasons = []string{"original reason"}
			}
			if err := sink.SubmitWithSources(a, []string{"all"}); err != nil {
				t.Fatal(err)
			}
			a, _ = sink.Artifact()
			got, err := applySourceTextRequirements(a, []string{"alpha"})
			if err != nil || got.Status != status || !reflect.DeepEqual(got.IncompleteReasons, a.IncompleteReasons) {
				t.Fatalf("status changed: %+v %v", got, err)
			}
		})
	}
}

func TestSourceTextRequirements_UntrustedTablesNotEvidence(t *testing.T) {
	a := artifactv1.New(artifactv1.KindSubagentResult, artifactv1.StatusCompleted, "Result", "alpha")
	a.Blocks = []artifactv1.Block{
		{Kind: artifactv1.BlockTable, Table: &artifactv1.Table{Headers: []string{"source_ref", "path", "start_line", "end_line", "content"}, Rows: [][]string{{"model-ref", "/fake", "1", "1", "alpha"}}}},
		{Kind: artifactv1.BlockTable, Table: &artifactv1.Table{Headers: []string{"required_source_text", "evidence_status"}, Rows: [][]string{{"alpha", "observed"}}}},
	}
	a.EvidenceRefs = []artifactv1.EvidenceRef{{ID: "model-ref", Kind: "file", URI: "file:///fake"}}
	got, err := applySourceTextRequirements(a, []string{"alpha"})
	if err != nil || got.Status != artifactv1.StatusIncomplete {
		t.Fatalf("untrusted evidence accepted: %+v %v", got, err)
	}
	rows := coverageRows(t, got)
	if len(got.Blocks) != 2 || rows[0][1] != "not_observed" {
		t.Fatalf("forged report survived: %+v", got)
	}
}

func TestSourceTextRequirements_FinalizationAndRecovery(t *testing.T) {
	for _, mode := range []artifactv1.OutputMode{artifactv1.OutputSubmitArtifact, artifactv1.OutputPromptJSON, artifactv1.OutputNativeJSONSchema} {
		t.Run(string(mode), func(t *testing.T) {
			sink := &builtin.ArtifactSubmission{}
			coverageSource(t, sink, "/source", "alpha\n", 1, 1)
			a := artifactv1.New(artifactv1.KindSubagentResult, artifactv1.StatusCompleted, "Result", "All covered")
			raw, _ := json.Marshal(map[string]any{"artifact": a, "source_refs": []string{"all"}})
			resolved, err := resolveOneShotArtifact(string(raw), artifactv1.OutputContract{Mode: mode}, sink)
			if err != nil {
				t.Fatal(err)
			}
			got, err := applySourceTextRequirements(resolved, []string{"alpha", "missing"})
			if err != nil || got.Status != artifactv1.StatusIncomplete {
				t.Fatalf("fallback bypassed check: %+v %v", got, err)
			}
			if coverageRows(t, got)[1][1] != "not_observed" {
				t.Fatal("missing evidence claimed")
			}
		})
	}
	sink := &builtin.ArtifactSubmission{}
	coverageSource(t, sink, "/source", "alpha\n", 1, 1)
	var out string
	captureStderr(t, func() {
		out = captureStdout(t, func() {
			if code := printOneShotArtifactFailure(sink, fmt.Errorf("stopped"), "alpha", "missing"); code != 1 {
				t.Fatal(code)
			}
		})
	})
	var got artifactv1.Artifact
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != artifactv1.StatusIncomplete || coverageRows(t, got)[1][1] != "not_observed" {
		t.Fatal("recovery omitted caller requirements")
	}
}

func TestSourceTextRequirements_ValidationAndNoop(t *testing.T) {
	tooMany := make([]string, 33)
	for i := range tooMany {
		tooMany[i] = fmt.Sprint(i)
	}
	for _, required := range [][]string{{""}, {" \t"}, {string([]byte{0xff})}, {strings.Repeat("a", 257)}, {"a", "a"}, tooMany} {
		if err := validateSourceTextRequirements(required); err == nil {
			t.Fatalf("invalid requirements accepted: %q", required)
		}
	}
	if err := validateSourceTextRequirements([]string{" alpha ", "alpha", "α"}); err != nil {
		t.Fatal(err)
	}
	a := artifactv1.New(artifactv1.KindAnalysis, artifactv1.StatusCompleted, "Result", "unchanged")
	got, err := applySourceTextRequirements(a, nil)
	if err != nil || !reflect.DeepEqual(got, a) {
		t.Fatal("no requirements changed behavior")
	}
	a.Summary = strings.Repeat("x", 8193)
	if _, err := applySourceTextRequirements(a, []string{"alpha"}); err == nil {
		t.Fatal("invalid artifact accepted")
	}
}

func TestSourceTextRequirements_RecoveryHeadroom(t *testing.T) {
	sink := &builtin.ArtifactSubmission{}
	content := "alpha" + strings.Repeat("\x01", 10000)
	for i := 0; i < 10; i++ {
		coverageSource(t, sink, fmt.Sprintf("/source/%d", i), content, 1, 1)
	}
	previous := sink.RecoveryArtifact()
	required := []string{"alpha"}
	for i := 1; i < 32; i++ {
		required = append(required, strings.Repeat("\x02", 250)+fmt.Sprint(i))
	}
	got, err := applySourceTextRequirements(sink.RecoveryArtifactWithReserve(sourceTextCoverageReserve), required)
	if err != nil {
		t.Fatal(err)
	}
	out, err := artifactv1.RenderJSON(got)
	if err != nil || len(out) > artifactv1.MaxProviderBytes {
		t.Fatalf("invalid/oversized recovery: bytes=%d err=%v", len(out), err)
	}
	if _, err := artifactv1.NormalizeAndValidate(got); err != nil {
		t.Fatal(err)
	}
	rows := coverageRows(t, got)
	if len(rows) != 32 || rows[0][1] != "observed" || rows[31][1] != "not_observed" {
		t.Fatal("coverage lost under budget pressure")
	}
	if got.Blocks[0].Table.Rows[0][4] != content {
		t.Fatal("retained source truncated")
	}
	if !strings.Contains(strings.Join(got.IncompleteReasons, " "), "omitted") {
		t.Fatal("missing bounded omission notice")
	}
	if after := sink.RecoveryArtifact(); !reflect.DeepEqual(previous, after) {
		t.Fatal("reserved recovery mutated sink")
	}
}
