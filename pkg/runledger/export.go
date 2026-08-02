package runledger

import (
	"context"
	"encoding/json"
	"time"

	"m31labs.dev/buckley/pkg/evidence"
)

// RunExport is the exportable, redacted document for one run's durable
// event log (section 13.3: exported run ledgers are one of the surfaces
// secret redaction MUST apply to).
type RunExport struct {
	Run    AgentRun `json:"run"`
	Events []Event  `json:"events"`
}

// ExportRun fetches runID and its events through store and returns a
// RunExport with every string payload value passed through
// evidence.Redact, so no credential-shaped content that made it into an
// event payload survives export. Evidence bodies themselves are governed by
// pkg/evidence's own sensitivity and export rules; ExportRun never reads
// them, only the run ledger's own event payloads.
func ExportRun(ctx context.Context, store Store, runID string) (RunExport, error) {
	run, err := store.GetRun(ctx, runID)
	if err != nil {
		return RunExport{}, err
	}
	events, err := store.ListEvents(ctx, EventQuery{RunID: runID})
	if err != nil {
		return RunExport{}, err
	}

	redacted := make([]Event, len(events))
	for i, ev := range events {
		redacted[i] = redactEventPayload(ev)
	}
	return RunExport{Run: run, Events: redacted}, nil
}

func redactEventPayload(ev Event) Event {
	out := ev
	out.Redaction = evidence.RedactionVersion
	if len(ev.Payload) == 0 {
		return out
	}
	normalized, err := json.Marshal(ev.Payload)
	if err != nil {
		out.Payload = map[string]any{"redaction_error": "payload is not exportable"}
		return out
	}
	var generic map[string]any
	if err := json.Unmarshal(normalized, &generic); err != nil {
		out.Payload = map[string]any{"redaction_error": "payload is not exportable"}
		return out
	}
	redacted, _ := redactValue(generic).(map[string]any)
	out.Payload = redacted
	out.Redaction = evidence.RedactionVersion
	return out
}

// redactValue walks a JSON-normalized value so every string, at any nesting
// depth, passes through evidence.Redact before export.
func redactValue(v any) any {
	switch t := v.(type) {
	case string:
		return string(evidence.Redact([]byte(t)))
	case map[string]any:
		for k, val := range t {
			t[k] = redactValue(val)
		}
		return t
	case []any:
		for i, val := range t {
			t[i] = redactValue(val)
		}
		return t
	default:
		return v
	}
}

// ContextReceiptSchemaVersion identifies the exportable context receipt
// document's JSON shape (Appendix A: "m31.context.receipt.v1").
const ContextReceiptSchemaVersion = "m31.context.receipt.v1"

// ContextReceiptItemDocument is one entry of ContextReceiptDocument.Items.
// Its field names and JSON tags are field-for-field compatible with
// Appendix A's canonical receipt item shape; note that Mode (JSON "mode")
// is stored as the projection_mode column, and Reasons (JSON "reasons") is
// stored as reasons_json, on ContextReceiptItem.
type ContextReceiptItemDocument struct {
	ItemID          string   `json:"item_id"`
	EntityID        string   `json:"entity_id,omitempty"`
	Path            string   `json:"path,omitempty"`
	Section         string   `json:"section"`
	Mode            string   `json:"mode"`
	StartLine       int      `json:"start_line,omitempty"`
	EndLine         int      `json:"end_line,omitempty"`
	ContentSHA256   string   `json:"content_sha256"`
	RawTokens       int      `json:"raw_tokens,omitempty"`
	ProjectedTokens int      `json:"projected_tokens,omitempty"`
	Score           int      `json:"score,omitempty"`
	Reasons         []Reason `json:"reasons,omitempty"`
	Required        bool     `json:"required"`
}

// ContextReceiptDocument is the canonical exportable JSON shape for a
// context receipt (Appendix A). It intentionally omits fields Appendix A
// shows that this storage layer does not persist: output_format,
// rendered_bytes, omission_summary, and the full expanded snapshot object
// (only SnapshotID is stored in context_receipts). Those are compiler-level
// fields that pkg/contextfabric — the package that actually runs context
// selection — will add when it builds on top of this layer in a later PR;
// this layer emits exactly what section 14.4's SQL schema durably stores.
type ContextReceiptDocument struct {
	SchemaVersion   string                       `json:"schema_version"`
	ID              string                       `json:"id"`
	SnapshotID      string                       `json:"snapshot_id"`
	RequestDigest   string                       `json:"request_digest"`
	PolicyVersion   string                       `json:"policy_version"`
	BudgetTokens    int                          `json:"budget_tokens"`
	EstimatedTokens int                          `json:"estimated_tokens"`
	CandidateTokens int                          `json:"candidate_tokens"`
	BundleSHA256    string                       `json:"bundle_sha256"`
	Items           []ContextReceiptItemDocument `json:"items"`
	CreatedAt       time.Time                    `json:"created_at"`
}

// BuildContextReceiptDocument converts a stored receipt and its items into
// the canonical exportable document shape (Appendix A).
func BuildContextReceiptDocument(receipt ContextReceipt, items []ContextReceiptItem) ContextReceiptDocument {
	doc := ContextReceiptDocument{
		SchemaVersion:   ContextReceiptSchemaVersion,
		ID:              receipt.ReceiptID,
		SnapshotID:      receipt.SnapshotID,
		RequestDigest:   receipt.RequestDigest,
		PolicyVersion:   receipt.PolicyVersion,
		BudgetTokens:    receipt.BudgetTokens,
		EstimatedTokens: receipt.EstimatedTokens,
		CandidateTokens: receipt.CandidateTokens,
		BundleSHA256:    receipt.BundleSHA256,
		CreatedAt:       receipt.CreatedAt,
	}
	for _, item := range items {
		doc.Items = append(doc.Items, ContextReceiptItemDocument{
			ItemID:          item.ItemID,
			EntityID:        item.EntityID,
			Path:            item.Path,
			Section:         item.Section,
			Mode:            item.Mode,
			StartLine:       item.StartLine,
			EndLine:         item.EndLine,
			ContentSHA256:   item.ContentSHA256,
			RawTokens:       item.RawTokens,
			ProjectedTokens: item.ProjectedTokens,
			Score:           item.Score,
			Reasons:         item.Reasons,
			Required:        item.Required,
		})
	}
	return doc
}
