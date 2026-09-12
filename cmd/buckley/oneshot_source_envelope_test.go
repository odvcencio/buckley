package main

import (
	"encoding/json"
	"strings"
	"testing"

	artifactv1 "m31labs.dev/buckley/pkg/artifact/v1"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

func TestSubmissionFallbackPromptHasValidMinimalEnvelope(t *testing.T) {
	start := strings.Index(artifactv1.SubmissionFallbackPrompt, "{")
	if start < 0 {
		t.Fatal("missing minimal output shape")
	}
	var raw json.RawMessage
	if err := json.NewDecoder(strings.NewReader(artifactv1.SubmissionFallbackPrompt[start:])).Decode(&raw); err != nil {
		t.Fatal(err)
	}
	a, err := resolveOneShotArtifact(string(raw), artifactv1.OutputContract{Mode: artifactv1.OutputSubmitArtifact}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.Status != artifactv1.StatusIncomplete || len(a.Blocks) != 0 || len(a.IncompleteReasons) == 0 {
		t.Fatalf("fallback shape invents evidence or completion: %+v", a)
	}
}

func TestResolveOneShotSourceEnvelope(t *testing.T) {
	for _, mode := range []artifactv1.OutputMode{artifactv1.OutputSubmitArtifact, artifactv1.OutputPromptJSON, artifactv1.OutputNativeJSONSchema} {
		t.Run(string(mode), func(t *testing.T) {
			sink := &builtin.ArtifactSubmission{}
			ref, err := sink.CaptureReadSource(&builtin.Result{Success: true, Data: map[string]any{"path": "/not-reread", "content": "\texact\r\n", "page": map[string]any{"start_line": 1, "end_line": 1}}})
			if err != nil {
				t.Fatal(err)
			}
			a := artifactv1.New(artifactv1.KindAnalysis, artifactv1.StatusIncomplete, "Source handoff", "Observed one source before the tool budget ended")
			a.IncompleteReasons = []string{"work budget exhausted; coverage unverified"}
			body, err := json.Marshal(map[string]any{"artifact": a, "source_refs": []string{ref}})
			if err != nil {
				t.Fatal(err)
			}
			got, err := resolveOneShotArtifact(string(body), artifactv1.OutputContract{Mode: mode}, sink)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != a.Status || got.Summary != a.Summary || len(got.IncompleteReasons) != 1 || len(got.Blocks) != 1 || got.Blocks[0].Table.Rows[0][4] != "\texact\r\n" || got.EvidenceRefs[0].ID != ref || got.EvidenceRefs[0].Kind != "captured_source" {
				t.Fatalf("handoff lost summary/status/snapshot: %+v", got)
			}
			stored, ok := sink.Artifact()
			if !ok || stored.ArtifactID != got.ArtifactID {
				t.Fatal("envelope bypassed finalization")
			}
			got.Blocks[0].Table.Rows[0][4] = "mutated"
			again, err := resolveOneShotArtifact("invalid replacement", artifactv1.OutputContract{Mode: mode}, sink)
			if err != nil || again.Blocks[0].Table.Rows[0][4] != "\texact\r\n" {
				t.Fatal("final output aliases sink or replaced prior submission")
			}
		})
	}
}

func TestResolveOneShotSourceEnvelopeRejectsInvalid(t *testing.T) {
	for _, kind := range []string{"unknown", "duplicate", "wrong type", "null", "missing artifact", "extra field", "model blocks", "forged capture", "invalid status", "oversized", "trailing JSON", "fenced"} {
		t.Run(kind, func(t *testing.T) {
			sink := &builtin.ArtifactSubmission{}
			ref, err := sink.CaptureReadSource(&builtin.Result{Success: true, Data: map[string]any{"path": "/source", "content": "x\n", "page": map[string]any{"start_line": 1, "end_line": 1}}})
			if err != nil {
				t.Fatal(err)
			}
			a := artifactv1.New(artifactv1.KindAnalysis, artifactv1.StatusCompleted, "Source", "model summary")
			args := map[string]any{"artifact": a, "source_refs": []string{ref}}
			switch kind {
			case "unknown":
				args["source_refs"] = []string{"src_unknown"}
			case "duplicate":
				args["source_refs"] = []string{ref, ref}
			case "wrong type":
				args["source_refs"] = ref
			case "null":
				args["source_refs"] = nil
			case "missing artifact":
				delete(args, "artifact")
			case "extra field":
				args["ignored"] = true
			case "invalid status":
				a.Status = "complete"
				args["artifact"] = a
			case "model blocks":
				a.Blocks = []artifactv1.Block{{Kind: artifactv1.BlockProse, Text: "model-written excerpt"}}
				args["artifact"] = a
			case "forged capture":
				a.EvidenceRefs = []artifactv1.EvidenceRef{{ID: ref, Kind: "captured_source", URI: "file:///source"}}
				args["artifact"] = a
			}
			body, err := json.Marshal(args)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "oversized" {
				body = append(body, []byte(strings.Repeat(" ", artifactv1.MaxProviderBytes))...)
			}
			if kind == "fenced" {
				body = []byte("```json\n" + string(body) + "\n```")
			}
			if kind == "trailing JSON" {
				body = append(body, []byte(" {}")...)
			}
			if _, err := resolveOneShotArtifact(string(body), artifactv1.OutputContract{Mode: artifactv1.OutputSubmitArtifact}, sink); err == nil {
				t.Fatal("invalid source envelope accepted")
			}
			if _, ok := sink.Artifact(); ok {
				t.Fatal("rejected envelope finalized sink")
			}
			if got := sink.RecoveryArtifact(); len(got.Blocks) != 1 || got.Blocks[0].Table.Rows[0][4] != "x\n" {
				t.Fatal("rejected envelope destroyed recovery evidence")
			}
		})
	}
	a := artifactv1.New(artifactv1.KindAnalysis, artifactv1.StatusCompleted, "Unknown", "No captures in this sink")
	body, _ := json.Marshal(map[string]any{"artifact": a, "source_refs": []string{"src_unknown"}})
	if _, err := resolveOneShotArtifact(string(body), artifactv1.OutputContract{Mode: artifactv1.OutputSubmitArtifact}, nil); err == nil {
		t.Fatal("nil sink accepted uncaptured reference")
	}
}
