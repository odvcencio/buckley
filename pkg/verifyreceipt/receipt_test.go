package verifyreceipt

import (
	"context"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/storage"
)

func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "init")
	return dir
}

func newStore(t *testing.T) *evidence.SQLiteStore {
	t.Helper()
	store, err := evidence.New(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestRecordAndLoadBindsReceiptToTree(t *testing.T) {
	repo, store, ctx := newRepo(t), newStore(t), context.Background()
	state, err := StateOf(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := Record(ctx, store, RecordOptions{Dir: repo, Argv: []string{"sh", "-c", "echo hello"}, Paths: []string{"**"}, Recorder: "record"})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Status != StatusPass || receipt.Tree != state.Tree || receipt.Commit != state.Commit || receipt.LogSHA256 == "" || receipt.Env.OS == "" {
		t.Fatalf("receipt = %+v", receipt)
	}
	loaded, err := Load(ctx, store, state.Tree)
	if err != nil || len(loaded.Receipts) != 1 || loaded.Receipts[0].ID != receipt.ID {
		t.Fatalf("Load = %+v, %v", loaded, err)
	}
	other, _ := Load(ctx, store, strings.Repeat("0", 40))
	if len(other.Receipts) != 0 {
		t.Fatalf("receipt for another tree was returned: %+v", other)
	}
}

func TestRecordFailingCommandStoresFailReceipt(t *testing.T) {
	repo, store, ctx := newRepo(t), newStore(t), context.Background()
	receipt, err := Record(ctx, store, RecordOptions{Dir: repo, Argv: []string{"sh", "-c", "echo boom; exit 3"}, Paths: []string{"**"}, Recorder: "record"})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Status != StatusFail || receipt.ExitCode != 3 {
		t.Fatalf("receipt = %+v, want FAIL exit 3", receipt)
	}
}

func TestRecordRefusesDirtyTree(t *testing.T) {
	repo, store, ctx := newRepo(t), newStore(t), context.Background()
	if err := os.WriteFile(filepath.Join(repo, "new.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Record(ctx, store, RecordOptions{Dir: repo, Argv: []string{"true"}, Paths: []string{"**"}})
	if err == nil || !strings.Contains(err.Error(), "not clean") {
		t.Fatalf("err = %v, want dirty-tree refusal", err)
	}
}

func TestRecordRefusesRunThatChangesTrackedFiles(t *testing.T) {
	repo, store, ctx := newRepo(t), newStore(t), context.Background()
	_, err := Record(ctx, store, RecordOptions{Dir: repo, Argv: []string{"sh", "-c", "echo two > a.txt"}, Paths: []string{"**"}})
	if err == nil || !strings.Contains(err.Error(), "changed HEAD or tracked files") {
		t.Fatalf("err = %v, want tracked-change refusal", err)
	}
}

func TestLoadRejectsTamperedLog(t *testing.T) {
	repo, store, ctx := newRepo(t), newStore(t), context.Background()
	receipt, err := Record(ctx, store, RecordOptions{Dir: repo, Argv: []string{"sh", "-c", "echo hello"}, Paths: []string{"**"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`UPDATE evidence_objects SET inline_body = ? WHERE evidence_id = ?`, []byte("forged\n"), receipt.LogEvidence); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(ctx, store, receipt.Tree)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Receipts) != 0 || len(loaded.Rejected) != 1 {
		t.Fatalf("loaded = %+v, want the tampered receipt rejected", loaded)
	}
}

func TestRecordRequiresCoveredPaths(t *testing.T) {
	repo, store := newRepo(t), newStore(t)
	if _, err := Record(context.Background(), store, RecordOptions{Dir: repo, Argv: []string{"true"}}); err == nil {
		t.Fatal("receipt without covered paths accepted")
	}
}

// The command has already run when the receipt is stored. A budget refusal at
// that point must be relieved and retried once, not lose the run.
func TestRecordRetriesStoreOnceAfterBudgetRelief(t *testing.T) {
	repo, store, ctx := newRepo(t), newStore(t), context.Background()
	if err := storage.ConfigureStorageBudget(ctx, store.DB(), 16<<20); err != nil {
		t.Fatal(err)
	}
	filler := make([]byte, 15<<20)
	_, _ = rand.New(rand.NewSource(1)).Read(filler)
	if _, err := store.Put(ctx, evidence.Object{Kind: evidence.KindToolResult, InlineBody: filler}); err != nil {
		t.Fatal(err)
	}
	opts := RecordOptions{Dir: repo, Argv: []string{"sh", "-c", "head -c 3000000 /dev/urandom | base64"}, Paths: []string{"**"}, Recorder: "record"}

	_, err := Record(ctx, store, opts)
	if !storage.IsStorageBudgetError(err) {
		t.Fatalf("without relief the store is full, got %v", err)
	}

	calls := 0
	opts.Relieve = func(context.Context) bool {
		calls++
		_, delErr := store.DB().Exec(`DELETE FROM evidence_objects WHERE kind = ?`, string(evidence.KindToolResult))
		return delErr == nil
	}
	receipt, err := Record(ctx, store, opts)
	if err != nil || receipt.ID == "" || calls != 1 {
		t.Fatalf("receipt=%q err=%v relief calls=%d", receipt.ID, err, calls)
	}
}
