package builtin

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	artifactv1 "m31labs.dev/buckley/pkg/artifact/v1"
)

func recoveryJSON(t *testing.T, a artifactv1.Artifact) []byte {
	t.Helper()
	body, err := artifactv1.RenderJSON(a)
	if err != nil || len(body) > artifactv1.MaxProviderBytes {
		t.Fatalf("invalid or oversized recovery: bytes=%d err=%v", len(body), err)
	}
	if _, _, err := artifactv1.DecodeProviderOutput(context.Background(), body, artifactv1.OutputPromptJSON, artifactv1.DecodeOptions{}); err != nil {
		t.Fatalf("recovery is not a decodable artifact: %v", err)
	}
	return body
}

func TestRecoveryArtifactPendingSnapshots(t *testing.T) {
	for _, sink := range []*ArtifactSubmission{nil, {}} {
		got := sink.RecoveryArtifact()
		recoveryJSON(t, got)
		if got.Status != artifactv1.StatusIncomplete || len(got.Blocks) != 0 || len(got.EvidenceRefs) != 0 || len(got.IncompleteReasons) == 0 {
			t.Fatalf("empty recovery invented evidence or success: %+v", got)
		}
	}
	sink := &ArtifactSubmission{}
	want := map[string]string{}
	for i, content := range []string{"\tvalue\r\n", "last", "\n", ""} {
		end := 1
		if content == "" {
			end = 0
		}
		ref, err := sink.CaptureReadSource(sourceReadResult(fmt.Sprintf("/nonexistent/recovery-%d", i), content, 1, end))
		if err != nil {
			t.Fatal(err)
		}
		want[ref] = content
	}
	beforeBytes := sink.sourceBytes
	got := sink.RecoveryArtifact()
	first := recoveryJSON(t, got)
	if !bytes.Equal(first, recoveryJSON(t, sink.RecoveryArtifact())) {
		t.Fatal("recovery changed without new evidence")
	}
	if got.Status != artifactv1.StatusIncomplete || len(got.Blocks) != 1 || got.Blocks[0].Table == nil || len(got.Blocks[0].Table.Rows) != len(want) {
		t.Fatalf("lost pending captures: %+v", got)
	}
	for _, row := range got.Blocks[0].Table.Rows {
		content, ok := want[row[0]]
		if !ok || row[4] != content {
			t.Fatalf("snapshot changed: %#v", row)
		}
	}
	if _, ok := sink.Artifact(); ok || len(sink.sources) != len(want) || sink.sourceBytes != beforeBytes {
		t.Fatal("recovery mutated or finalized sink")
	}
	got.Blocks[0].Table.Rows[0][4] = "mutated detached result"
	if !bytes.Equal(first, recoveryJSON(t, sink.RecoveryArtifact())) {
		t.Fatal("result aliases sink")
	}
	var refs []string
	for ref := range want {
		refs = append(refs, ref)
	}
	if err := sink.SubmitWithSources(artifactv1.New(artifactv1.KindAnalysis, artifactv1.StatusCompleted, "Later", "Valid final response"), refs); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryArtifactBoundsExpandedJSON(t *testing.T) {
	sink := &ArtifactSubmission{}
	content := strings.Repeat("\x01", 10000)
	for i := 0; i < 10; i++ {
		if _, err := sink.CaptureReadSource(sourceReadResult(fmt.Sprintf("/recovery-%d", i), content, 1, 1)); err != nil {
			t.Fatal(err)
		}
	}
	got := sink.RecoveryArtifact()
	recoveryJSON(t, got)
	if len(got.Blocks) != 1 || got.Blocks[0].Table == nil {
		t.Fatal("all evidence discarded")
	}
	rows := got.Blocks[0].Table.Rows
	if len(rows) == 0 || len(rows) >= 10 || !strings.Contains(strings.Join(got.IncompleteReasons, " "), "omitted") {
		t.Fatalf("missing explicit bounded omission: %+v", got.IncompleteReasons)
	}
	for _, row := range rows {
		if row[4] != content {
			t.Fatal("bounded recovery truncated literal bytes")
		}
	}
	if len(sink.sources) != 10 {
		t.Fatal("recovery discarded stored captures")
	}
}

func TestRecoveryArtifactSubmittedStatusAndBounds(t *testing.T) {
	for _, status := range []artifactv1.ArtifactStatus{artifactv1.StatusCompleted, artifactv1.StatusIncomplete, artifactv1.StatusFailed, artifactv1.StatusBlocked} {
		t.Run(string(status), func(t *testing.T) {
			sink := &ArtifactSubmission{}
			a := artifactv1.New(artifactv1.KindAnalysis, status, "Final", "Accepted summary")
			if status != artifactv1.StatusCompleted {
				a.IncompleteReasons = []string{"original reason"}
			}
			if err := sink.Submit(a); err != nil {
				t.Fatal(err)
			}
			before, _ := sink.Artifact()
			beforeJSON := recoveryJSON(t, before)
			got := sink.RecoveryArtifact()
			recoveryJSON(t, got)
			want := status
			if want == artifactv1.StatusCompleted {
				want = artifactv1.StatusIncomplete
			}
			if got.Status != want || got.Summary != a.Summary || len(got.IncompleteReasons) <= len(before.IncompleteReasons) {
				t.Fatalf("lost status or accepted result: %+v", got)
			}
			after, _ := sink.Artifact()
			if !bytes.Equal(beforeJSON, recoveryJSON(t, after)) {
				t.Fatal("recovery mutated submitted result")
			}
		})
	}
	sink := &ArtifactSubmission{}
	a := artifactv1.New(artifactv1.KindAnalysis, artifactv1.StatusCompleted, "Large", "Accepted but too large for recovery")
	for i := 0; i < 8; i++ {
		a.Blocks = append(a.Blocks, artifactv1.Block{Kind: artifactv1.BlockProse, Text: strings.Repeat("x", 60000)})
	}
	if err := sink.Submit(a); err != nil {
		t.Fatal(err)
	}
	got := sink.RecoveryArtifact()
	recoveryJSON(t, got)
	if got.Status != artifactv1.StatusIncomplete || len(got.Blocks) != 0 || !strings.Contains(strings.Join(got.IncompleteReasons, " "), "omitted") {
		t.Fatal("oversized artifact silently lost or promoted")
	}
	stored, _ := sink.Artifact()
	if len(stored.Blocks) != 8 {
		t.Fatal("recovery changed accepted artifact")
	}
}
