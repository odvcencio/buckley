package builtin

import (
	"encoding/json"
	"testing"
)

func TestReadFileScopedRuntimeEndPastEOF(t *testing.T) {
	reader, path := scopedRuntimeReader(t, "OUTSIDE\nallowed\nlast", 2, 999)
	params := map[string]any{"path": "source.txt"}
	before, _ := json.Marshal(params)
	result, err := reader.Execute(params)
	if err != nil || !result.Success || !result.ShouldAbridge || result.DisplayData["content"] != "allowed\nlast" {
		t.Fatalf("scoped read past EOF: %+v, %v", result, err)
	}
	page := result.DisplayData["page"].(map[string]any)
	if page["start_line"] != 2 || page["end_line"] != 3 || page["has_more"] != false || page["next_start_line"] != nil {
		t.Fatalf("scope metadata clamped incorrectly: %#v", page)
	}
	after, _ := json.Marshal(params)
	if string(before) != string(after) || result.Data["content"] != "OUTSIDE\nallowed\nlast" {
		t.Fatal("caller parameters or host snapshot changed")
	}
	sink := &ArtifactSubmission{}
	if _, err := sink.CaptureReadSource(result); err != nil {
		t.Fatal(err)
	}
	rows := sink.RecoveryArtifact().Blocks[0].Table.Rows
	if len(rows) != 1 {
		t.Fatalf("expected exactly one captured row, got %d", len(rows))
	}
	row := rows[0]
	if row[1] != path || row[2] != "2" || row[3] != "3" || row[4] != "allowed\nlast" {
		t.Fatalf("captured source wrong: %#v", row)
	}
}
