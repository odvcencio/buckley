package main

import (
	"fmt"
	"strings"
	"testing"

	artifactv1 "m31labs.dev/buckley/pkg/artifact/v1"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

func TestResolveOneShotArtifactRejectsUnsubmittedCaptureClaims(t *testing.T) {
	for _, mode := range []artifactv1.OutputMode{artifactv1.OutputSubmitArtifact, artifactv1.OutputPromptJSON, artifactv1.OutputNativeJSONSchema} {
		for _, withSink := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/sink=%t", mode, withSink), func(t *testing.T) {
				var sink *builtin.ArtifactSubmission
				if withSink {
					sink = &builtin.ArtifactSubmission{}
				}
				artifact := artifactv1.New(artifactv1.KindAnalysis, artifactv1.StatusCompleted, "Claim", "not a host capture")
				artifact.EvidenceRefs = []artifactv1.EvidenceRef{{ID: "fake", Kind: "captured_source", URI: "file:///never-read"}}
				body, err := artifactv1.RenderJSON(artifact)
				if err != nil {
					t.Fatal(err)
				}
				_, err = resolveOneShotArtifact(string(body), artifactv1.OutputContract{Mode: mode}, sink)
				if err == nil || !strings.Contains(err.Error(), "source_refs") {
					t.Fatalf("unsubmitted capture accepted or unclear error: %v", err)
				}
				if sink != nil {
					if _, ok := sink.Artifact(); ok {
						t.Fatal("rejected fallback finalized sink")
					}
				}
			})
		}
	}
}

func TestResolveOneShotArtifactFinalizesOrdinaryFallback(t *testing.T) {
	sink := &builtin.ArtifactSubmission{}
	artifact := artifactv1.New(artifactv1.KindAnalysis, artifactv1.StatusIncomplete, "Partial", "ordinary model summary")
	artifact.IncompleteReasons = []string{"missing source"}
	artifact.EvidenceRefs = []artifactv1.EvidenceRef{{ID: "ordinary", Kind: "file", URI: "file:///unverified"}}
	body, err := artifactv1.RenderJSON(artifact)
	if err != nil {
		t.Fatal(err)
	}
	got, err := resolveOneShotArtifact(string(body), artifactv1.OutputContract{Mode: artifactv1.OutputSubmitArtifact}, sink)
	if err != nil || got.ArtifactID != artifact.ArtifactID || got.Status != artifactv1.StatusIncomplete || len(got.IncompleteReasons) != 1 {
		t.Fatalf("ordinary fallback changed: %+v, %v", got, err)
	}
	stored, ok := sink.Artifact()
	if !ok || stored.ArtifactID != got.ArtifactID {
		t.Fatal("fallback bypassed submission finalization")
	}
	got, err = resolveOneShotArtifact("invalid replacement", artifactv1.OutputContract{Mode: artifactv1.OutputSubmitArtifact}, sink)
	if err != nil || got.ArtifactID != stored.ArtifactID {
		t.Fatalf("finalized fallback was replaced: %+v, %v", got, err)
	}
}

func TestResolveOneShotArtifactPreservesHostCapture(t *testing.T) {
	sink := &builtin.ArtifactSubmission{}
	ref, err := sink.CaptureReadSource(&builtin.Result{Success: true, Data: map[string]any{
		"path": "/source", "content": "\tactual\n", "page": map[string]any{"start_line": 1, "end_line": 1},
	}})
	if err != nil {
		t.Fatal(err)
	}
	a := artifactv1.New(artifactv1.KindAnalysis, artifactv1.StatusCompleted, "Captured", "model summary")
	if err := sink.SubmitWithSources(a, []string{ref}); err != nil {
		t.Fatal(err)
	}
	got, err := resolveOneShotArtifact("ignored model prose", artifactv1.OutputContract{Mode: artifactv1.OutputSubmitArtifact}, sink)
	if err != nil || len(got.Blocks) != 1 || got.Blocks[0].Table.Rows[0][4] != "\tactual\n" || got.EvidenceRefs[0].ID != ref {
		t.Fatalf("host capture lost: %+v, %v", got, err)
	}
}
