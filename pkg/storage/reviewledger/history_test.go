package reviewledger

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/runledger"
)

func TestBackfill_HistoricalReviewAndReferences(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	filename := filepath.Join(root, "ledger.db")
	runs, err := runledger.New(filename)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { runs.Close() })
	ev, err := evidence.New(filename)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ev.Close() })
	run, err := runs.StartRun(ctx, runledger.AgentRun{SessionID: "session", AgentID: "buckbot", Backend: "review-code-mode", ModelID: "model"})
	if err != nil {
		t.Fatal(err)
	}
	object, err := ev.Put(ctx, evidence.Object{Kind: evidence.KindToolResult, InlineBody: []byte("saved verification"), Metadata: map[string]any{"run_id": run.RunID}})
	if err != nil {
		t.Fatal(err)
	}
	if err := runs.EndRun(ctx, run.RunID, "completed", time.Now().UTC(), map[string]any{"surface": "review"}); err != nil {
		t.Fatal(err)
	}
	referenced, err := ReferencedEvidence(ctx, filename, map[string][]byte{"call": []byte(object.ID)})
	if err != nil || string(referenced[object.ContentSHA256]) != "saved verification" {
		t.Fatalf("reference: %v %v", referenced, err)
	}
	objects := &fakeObjects{data: map[string][]byte{}}
	ledger := New(objects, "bucket", "", t.TempDir())
	for range 2 {
		counts, err := ledger.Backfill(ctx, filepath.Join(root, "evidence"), filename, "")
		if err != nil || counts.Records != 1 {
			t.Fatalf("backfill %+v %v", counts, err)
		}
	}
	record, err := ledger.Show(ctx, "legacy-"+run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Verdict != "UNKNOWN" || record.Repository != "local/legacy-review" || record.HeadSHA != "" || len(record.Evidence) != 2 {
		t.Fatalf("invented historical data: %+v", record)
	}
}
