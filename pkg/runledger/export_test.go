package runledger

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/evidence"
)

func TestExportRun_RedactsPayloadSecrets(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	run, err := store.StartRun(ctx, AgentRun{SessionID: "sess-1"})
	if err != nil {
		t.Fatalf("StartRun() error = %v", err)
	}
	if _, err := store.Append(ctx, Event{
		RunID: run.RunID, Type: EventToolCompleted,
		Payload: map[string]any{
			"summary": "aws key AKIAIOSFODNN7EXAMPLE leaked in output",
			"count":   3,
		},
	}); err != nil {
		t.Fatalf("Append() error = %v", err)
	}

	export, err := ExportRun(ctx, store, run.RunID)
	if err != nil {
		t.Fatalf("ExportRun() error = %v", err)
	}
	if len(export.Events) != 1 {
		t.Fatalf("got %d events, want 1", len(export.Events))
	}

	raw, err := json.Marshal(export)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if strings.Contains(string(raw), "AKIAIOSFODNN7EXAMPLE") {
		t.Fatalf("exported ledger contains raw secret content: %s", raw)
	}
	if export.Events[0].Payload["count"] != float64(3) {
		// count survives untouched since it is not a string field.
		t.Fatalf("non-string payload field was altered: %+v", export.Events[0].Payload)
	}
}

// TestGoldenContextReceipt_MatchesAppendixAShape verifies that a receipt
// built from the values in Appendix A of the M31 Context Fabric spec
// ("Canonical context receipt example"), stored and read back through
// SQLiteStore, produces a ContextReceiptDocument whose JSON keys and values
// match Appendix A field-for-field for every field this storage layer
// persists. Appendix A also shows output_format, rendered_bytes, and
// omission_summary, plus an expanded snapshot object; those are
// pkg/contextfabric-level fields this layer does not store (see the doc
// comment on ContextReceiptDocument), so they are intentionally absent
// here rather than compared.
func TestGoldenContextReceipt_MatchesAppendixAShape(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	createdAt, err := time.Parse(time.RFC3339, "2026-07-31T00:00:00Z")
	if err != nil {
		t.Fatalf("time.Parse() error = %v", err)
	}

	receipt := ContextReceipt{
		ReceiptID:       "ctx_2FJ5H6M7GOLDENTEST00000001",
		SnapshotID:      "worktree:a237df4d:9b31...:cfg7...",
		RequestDigest:   "c76d...",
		PolicyVersion:   "context-selection-v1",
		BudgetTokens:    8000,
		EstimatedTokens: 7842,
		CandidateTokens: 92310,
		BundleSHA256:    "ab32...",
		CreatedAt:       createdAt,
	}
	items := []ContextReceiptItem{
		{
			ItemID:          "itm_...",
			EntityID:        "pkg/agentloop/governor.go|function_definition|Observe|68",
			Path:            "pkg/agentloop/governor.go",
			Section:         "focus",
			Mode:            "body",
			StartLine:       68,
			EndLine:         137,
			ContentSHA256:   "5f10...",
			RawTokens:       1420,
			ProjectedTokens: 1420,
			Score:           11400,
			Reasons: []Reason{
				{Kind: "required_selector", Weight: 10000},
				{Kind: "focus_entity", Weight: 1400},
			},
			Required: true,
		},
	}

	if _, err := store.CreateContextReceipt(ctx, receipt, items); err != nil {
		t.Fatalf("CreateContextReceipt() error = %v", err)
	}

	gotReceipt, gotItems, err := store.GetContextReceipt(ctx, receipt.ReceiptID)
	if err != nil {
		t.Fatalf("GetContextReceipt() error = %v", err)
	}

	doc := BuildContextReceiptDocument(gotReceipt, gotItems)
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	wantTop := map[string]any{
		"schema_version":   "m31.context.receipt.v1",
		"id":               "ctx_2FJ5H6M7GOLDENTEST00000001",
		"snapshot_id":      "worktree:a237df4d:9b31...:cfg7...",
		"request_digest":   "c76d...",
		"policy_version":   "context-selection-v1",
		"budget_tokens":    float64(8000),
		"estimated_tokens": float64(7842),
		"candidate_tokens": float64(92310),
		"bundle_sha256":    "ab32...",
		"created_at":       "2026-07-31T00:00:00Z",
	}
	for key, want := range wantTop {
		if got := decoded[key]; got != want {
			t.Fatalf("document[%q] = %v, want %v", key, got, want)
		}
	}

	itemsRaw, ok := decoded["items"].([]any)
	if !ok || len(itemsRaw) != 1 {
		t.Fatalf("document[items] = %v, want a one-element array", decoded["items"])
	}
	gotItem, ok := itemsRaw[0].(map[string]any)
	if !ok {
		t.Fatalf("document[items][0] is not an object: %v", itemsRaw[0])
	}

	wantItem := map[string]any{
		"item_id":          "itm_...",
		"entity_id":        "pkg/agentloop/governor.go|function_definition|Observe|68",
		"path":             "pkg/agentloop/governor.go",
		"section":          "focus",
		"mode":             "body",
		"start_line":       float64(68),
		"end_line":         float64(137),
		"content_sha256":   "5f10...",
		"raw_tokens":       float64(1420),
		"projected_tokens": float64(1420),
		"score":            float64(11400),
		"required":         true,
	}
	for key, want := range wantItem {
		if got := gotItem[key]; got != want {
			t.Fatalf("document[items][0][%q] = %v, want %v", key, got, want)
		}
	}

	reasonsRaw, ok := gotItem["reasons"].([]any)
	if !ok || len(reasonsRaw) != 2 {
		t.Fatalf("document[items][0][reasons] = %v, want a two-element array", gotItem["reasons"])
	}
	first, ok := reasonsRaw[0].(map[string]any)
	if !ok || first["kind"] != "required_selector" || first["weight"] != float64(10000) {
		t.Fatalf("reasons[0] = %v, want {kind: required_selector, weight: 10000}", reasonsRaw[0])
	}
	second, ok := reasonsRaw[1].(map[string]any)
	if !ok || second["kind"] != "focus_entity" || second["weight"] != float64(1400) {
		t.Fatalf("reasons[1] = %v, want {kind: focus_entity, weight: 1400}", reasonsRaw[1])
	}

	// This layer must not fabricate the compiler-level fields it does not
	// persist; Appendix A has them, this document intentionally does not.
	for _, absent := range []string{"output_format", "rendered_bytes", "omission_summary", "snapshot"} {
		if _, present := decoded[absent]; present {
			t.Fatalf("document unexpectedly contains %q, which pkg/contextfabric is responsible for", absent)
		}
	}
}

func TestExportRunRedactsNestedPayloadSecrets(t *testing.T) {
	payload := map[string]any{
		"result": map[string]any{
			"stderr": "failure: AKIAIOSFODNN7EXAMPLE was rejected",
			"attempts": []any{
				map[string]any{"token": "sk-proj-abcdef1234567890abcdef1234567890"},
			},
		},
	}
	ev := redactEventPayload(Event{Payload: payload})
	raw, err := json.Marshal(ev.Payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, secret := range []string{"AKIAIOSFODNN7EXAMPLE", "sk-proj-abcdef1234567890abcdef1234567890"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("nested secret %q survived export redaction: %s", secret, raw)
		}
	}
}

func TestExportedEventsCarryEvidenceRedactionVersion(t *testing.T) {
	ev := redactEventPayload(Event{Redaction: DefaultRedactionVersion, Payload: map[string]any{"k": "v"}})
	if ev.Redaction != evidence.RedactionVersion {
		t.Fatalf("exported event claims ruleset %q, want %q", ev.Redaction, evidence.RedactionVersion)
	}
}
