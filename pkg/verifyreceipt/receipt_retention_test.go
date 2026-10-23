package verifyreceipt

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/evidence"
)

func TestRecord_ExpiredReceiptAndLogAreCollectable(t *testing.T) {
	repo, store := newRepo(t), newStore(t)
	receipt, err := Record(t.Context(), store, RecordOptions{Dir: repo, Argv: []string{"sh", "-c", "printf 'bounded receipt log'"}, Paths: []string{"**"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`UPDATE evidence_objects SET created_at=?`, "2000-01-01T00:00:00.000000000Z"); err != nil {
		t.Fatal(err)
	}
	removed, err := store.Sweep(t.Context(), evidence.DefaultRetentionPolicy(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 2 {
		t.Fatalf("expired receipt/log retained indefinitely: removed=%v receipt=%s log=%s", removed, receipt.ID, receipt.LogEvidence)
	}
}

func TestRecord_RetentionPreservesProtectedReceiptsAcrossReopen(t *testing.T) {
	for _, protection := range []string{"user:keep", "run:paused", "legacy step reference"} {
		t.Run(protection, func(t *testing.T) {
			repo := newRepo(t)
			path := filepath.Join(t.TempDir(), "ledger.db")
			store, err := evidence.New(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			marker := filepath.Join(t.TempDir(), "calls")
			receipt, err := Record(t.Context(), store, RecordOptions{Dir: repo, Argv: []string{"sh", "-c", `printf protected; printf called >> "$1"`, "record", marker}, Paths: []string{"**"}})
			if err != nil {
				t.Fatal(err)
			}
			if protection == "legacy step reference" {
				if _, err := store.DB().Exec(`CREATE TABLE execution_steps(output_evidence_id TEXT); INSERT INTO execution_steps VALUES(?)`, receipt.ID); err != nil {
					t.Fatal(err)
				}
			} else if err := store.Pin(t.Context(), receipt.ID, protection); err != nil {
				t.Fatal(err)
			}
			if _, err := store.DB().Exec(`UPDATE evidence_objects SET created_at=?`, "2000-01-01T00:00:00.000000000Z"); err != nil {
				t.Fatal(err)
			}
			removed, err := store.Sweep(t.Context(), evidence.DefaultRetentionPolicy(), time.Now())
			if err != nil || len(removed) != 0 {
				t.Fatalf("protected evidence removed=%v err=%v", removed, err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := evidence.New(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = reopened.Close() })
			loaded, err := Load(t.Context(), reopened, receipt.Tree)
			if err != nil || len(loaded.Receipts) != 1 || len(loaded.Rejected) != 0 {
				t.Fatalf("protected receipt unavailable after reopen: %+v err=%v", loaded, err)
			}
			body, err := os.ReadFile(marker)
			if err != nil || string(body) != "called" {
				t.Fatalf("load repeated verification: %q err=%v", body, err)
			}
		})
	}
}

func TestRecord_RetentionKeepsSharedLogForFreshReceipt(t *testing.T) {
	repo, store := newRepo(t), newStore(t)
	opts := RecordOptions{Dir: repo, Argv: []string{"sh", "-c", "printf shared"}, Paths: []string{"**"}}
	old, err := Record(t.Context(), store, opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`UPDATE evidence_objects SET created_at=?`, "2000-01-01T00:00:00.000000000Z"); err != nil {
		t.Fatal(err)
	}
	fresh, err := Record(t.Context(), store, opts)
	if err != nil {
		t.Fatal(err)
	}
	if old.LogEvidence != fresh.LogEvidence || old.ID == fresh.ID {
		t.Fatal("fixture did not share a log across distinct receipts")
	}
	removed, err := store.Sweep(t.Context(), evidence.DefaultRetentionPolicy(), time.Now())
	if err != nil || len(removed) != 1 || removed[0] != old.ID {
		t.Fatalf("removed=%v err=%v", removed, err)
	}
	loaded, err := Load(t.Context(), store, fresh.Tree)
	if err != nil || len(loaded.Receipts) != 1 || loaded.Receipts[0].ID != fresh.ID {
		t.Fatalf("fresh shared log lost: %+v err=%v", loaded, err)
	}
}

func TestRecord_PinFailureDoesNotAcknowledgeReceipt(t *testing.T) {
	repo, store := newRepo(t), newStore(t)
	if _, err := store.DB().Exec(`CREATE TRIGGER reject_log_pin BEFORE INSERT ON evidence_pins WHEN NEW.reason LIKE 'verification-receipt:%' BEGIN SELECT RAISE(ABORT,'pin rejected'); END`); err != nil {
		t.Fatal(err)
	}
	receipt, err := Record(t.Context(), store, RecordOptions{Dir: repo, Argv: []string{"true"}, Paths: []string{"**"}})
	if err == nil || receipt.ID != "" {
		t.Fatalf("unretained receipt acknowledged: %+v err=%v", receipt, err)
	}
	var pins int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM evidence_pins`).Scan(&pins); err != nil || pins != 0 {
		t.Fatalf("atomic pin rollback: pins=%d err=%v", pins, err)
	}
}
