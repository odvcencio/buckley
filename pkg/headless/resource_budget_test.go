package headless

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/ipc/command"
	"m31labs.dev/buckley/pkg/replay"
	"m31labs.dev/buckley/pkg/retention"
	"m31labs.dev/buckley/pkg/sessionexec"
	"m31labs.dev/buckley/pkg/storage"
)

func waitResourceRunnerState(t *testing.T, r *Runner, want RunnerState) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if r.State() == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("runner state=%s want=%s", r.State(), want)
}

func runnerHistoryDiskBytes(t *testing.T, r *Runner) int64 {
	t.Helper()
	var dbPath string
	if err := r.store.DB().QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&dbPath); err != nil {
		t.Fatal(err)
	}
	var disk int64
	seen := map[string]bool{}
	for _, root := range []string{filepath.Dir(dbPath), r.evidenceStore.(*evidence.SQLiteStore).Blobs().Root()} {
		if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || seen[path] {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			seen[path] = true
			disk += info.Size()
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	return disk
}

func TestDurableRunner_ResourceBudgetsBoundLongSessionPauseCleanupAndResume(t *testing.T) {
	var requests atomic.Int32
	var maxRequest atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		for old := maxRequest.Load(); int64(len(body)) > old; old = maxRequest.Load() {
			if maxRequest.CompareAndSwap(old, int64(len(body))) {
				break
			}
		}
		n := requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": fmt.Sprintf("soak-%d", n), "model": "gpt-4o",
			"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": strings.Repeat("history payload ", 512)}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 10, "completion_tokens": 10, "total_tokens": 20},
		})
	}))
	t.Cleanup(server.Close)
	const memoryLimit = 128 << 10
	const diskLimit = 64 << 20
	r, store := newDurableHTTPRunnerConfigured(t, "budget-soak", server.URL, func(cfg *RunnerConfig, s *storage.Store) {
		cfg.DurableTiming = &DurableTiming{ScanInterval: 20 * time.Millisecond}
		cfg.Config.Resources.SessionMemoryBytes = memoryLimit
		cfg.Config.Resources.StorageBytes = diskLimit
		if err := storage.ConfigureStorageBudget(context.Background(), s.DB(), diskLimit); err != nil {
			t.Fatal(err)
		}
	})
	ctx := context.Background()
	for i := range 120 {
		cid := fmt.Sprintf("turn-%03d", i)
		if _, err := r.AcceptCommand(ctx, command.SessionCommand{ID: cid, Type: "input", Content: cid, AcceptedBy: "alice"}); err != nil {
			t.Fatalf("accept %d: %v", i, err)
		}
		waitForCommandState(t, store, r.sessionID, cid, sessionexec.StateSucceeded)
		waitResourceRunnerState(t, r, StateIdle)
		if retained := r.conv.WorkingSetBytes(); retained > memoryLimit {
			t.Fatalf("turn %d retained %d bytes", i, retained)
		}
	}
	if maxRequest.Load() > 4*memoryLimit {
		t.Fatalf("context grew to %d request bytes", maxRequest.Load())
	}
	canonical, err := store.GetAllMessages(r.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(canonical) != 240 {
		t.Fatalf("canonical history=%d want=240", len(canonical))
	}
	if _, err := r.AcceptCommand(ctx, command.SessionCommand{ID: "pause", Type: "pause", AcceptedBy: "alice"}); err != nil {
		t.Fatal(err)
	}
	waitForCommandState(t, store, r.sessionID, "pause", sessionexec.StateSucceeded)
	cleaner, err := retention.New(store.DB(), r.evidenceStore.(*evidence.SQLiteStore))
	if err != nil {
		t.Fatal(err)
	}
	policy := retention.Policy{HistoryTTL: config.DefaultHistoryRetention,
		MaxCompletedSessions: config.DefaultMaxCompletedSessions, MaxCompletedRuns: config.DefaultMaxCompletedRuns,
		Evidence: evidence.DefaultRetentionPolicy()}
	if _, err := cleaner.Clean(ctx, policy, time.Now().Add(365*24*time.Hour), false); err != nil {
		t.Fatal(err)
	}
	report, err := replay.Verify(ctx, r.runLedger, r.stepJournal, r.evidenceStore, sessionexec.RunIDForSession(r.sessionID))
	if err != nil || !report.Valid || report.StepCount != 120 {
		t.Fatalf("paused replay: %+v err=%v", report, err)
	}
	disk := runnerHistoryDiskBytes(t, r)
	if disk > diskLimit {
		t.Fatalf("session disk grew to %d", disk)
	}
	r.Stop()
	sess, err := store.GetSession(r.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := NewRunner(RunnerConfig{Session: sess, ModelManager: r.modelManager, Tools: r.tools, Store: store, Config: r.config,
		CommandJournal: store, RunLedger: r.runLedger, EvidenceStore: r.evidenceStore, StepJournal: r.stepJournal, LeaseOwner: "resumed-budget-owner", DurableTiming: &r.durableTiming})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(resumed.Stop)
	if resumed.conv.WorkingSetBytes() > memoryLimit {
		t.Fatal("restart loaded full canonical history")
	}
	if _, err := resumed.AcceptCommand(ctx, command.SessionCommand{ID: "resume", Type: "resume", AcceptedBy: "alice"}); err != nil {
		t.Fatal(err)
	}
	waitForCommandState(t, store, r.sessionID, "resume", sessionexec.StateSucceeded)
	if _, err := resumed.AcceptCommand(ctx, command.SessionCommand{ID: "after-resume", Type: "input", Content: "continue", AcceptedBy: "alice"}); err != nil {
		t.Fatal(err)
	}
	waitForCommandState(t, store, r.sessionID, "after-resume", sessionexec.StateSucceeded)
	if requests.Load() != 121 {
		t.Fatalf("resume repeated provider calls: %d", requests.Load())
	}
	t.Logf("120-turn session: working=%d bytes, max request=%d bytes, disk=%d bytes; replay=%d steps; resumed once", r.conv.WorkingSetBytes(), maxRequest.Load(), disk, report.StepCount)
}

func TestDurableRunner_StorageBudgetRejectsAdmissionWithoutProviderCalls(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests.Add(1)
		http.Error(w, "unexpected provider call", 500)
	}))
	t.Cleanup(server.Close)
	r, store := newDurableHTTPRunnerConfigured(t, "quota-admission", server.URL, func(cfg *RunnerConfig, s *storage.Store) {
		cfg.DurableTiming = &DurableTiming{ScanInterval: 20 * time.Millisecond}
		if err := storage.ConfigureStorageBudget(context.Background(), s.DB(), 16<<20); err != nil {
			t.Fatal(err)
		}
		if _, err := s.DB().Exec(`CREATE TABLE budget_filler(payload BLOB)`); err != nil {
			t.Fatal(err)
		}
		if err := storage.RefreshStorageBudget(context.Background(), s.DB()); err != nil {
			t.Fatal(err)
		}
		if _, err := s.DB().Exec(`INSERT INTO budget_filler VALUES(zeroblob(10000))`); err != nil {
			t.Fatal(err)
		}
		// Consume accounted headroom without allocating a large diagnostic file.
		if _, err := s.DB().Exec(`UPDATE storage_budget SET used_bytes=limit_bytes-2048`); err != nil {
			t.Fatal(err)
		}
	})
	_, err := r.AcceptCommand(context.Background(), command.SessionCommand{ID: "rejected", Type: "input", Content: "must not execute", AcceptedBy: "alice"})
	if !storage.IsStorageBudgetError(err) {
		t.Fatalf("admission error=%v", err)
	}
	waitResourceRunnerState(t, r, StatePaused)
	if requests.Load() != 0 {
		t.Fatal("quota admitted a provider call")
	}
	if _, err := store.Get(context.Background(), r.sessionID, "rejected"); !errors.Is(err, sessionexec.ErrNotFound) {
		t.Fatalf("rejection created receipt: %v", err)
	}
}

func TestDurableRunner_StorageBudgetTerminalFailureResumesWithoutRepeatingEffects(t *testing.T) {
	var requests, tools atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_, _ = io.Copy(io.Discard, req.Body)
		w.Header().Set("Content-Type", "application/json")
		switch requests.Add(1) {
		case 1:
			_, _ = io.WriteString(w, `{"id":"quota-call","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"echo-quota","type":"function","function":{"name":"echo_tool","arguments":"{\"text\":\"exactly once\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`)
		case 2:
			_, _ = io.WriteString(w, `{"id":"quota-done","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"done once"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`)
		default:
			http.Error(w, "unexpected repeated effect", 500)
		}
	}))
	t.Cleanup(server.Close)
	r, store := newDurableHTTPRunnerConfigured(t, "quota-terminal", server.URL, func(cfg *RunnerConfig, s *storage.Store) {
		cfg.DurableTiming = &DurableTiming{ScanInterval: 20 * time.Millisecond}
		cfg.Tools.Register(countingEchoTool{calls: &tools})
		if err := storage.ConfigureStorageBudget(context.Background(), s.DB(), 16<<20); err != nil {
			t.Fatal(err)
		}
		if _, err := s.DB().Exec(`CREATE TRIGGER exhaust_on_terminal BEFORE INSERT ON messages WHEN NEW.role='assistant' BEGIN SELECT RAISE(ABORT,'storage byte budget exceeded'); END`); err != nil {
			t.Fatal(err)
		}
	})
	ctx := context.Background()
	if _, err := r.AcceptCommand(ctx, command.SessionCommand{ID: "work", Type: "input", Content: "echo exactly once", AcceptedBy: "alice"}); err != nil {
		t.Fatal(err)
	}
	waitResourceRunnerState(t, r, StatePaused)
	r.Stop()
	if requests.Load() != 2 || tools.Load() != 1 {
		t.Fatalf("original calls: provider=%d tool=%d", requests.Load(), tools.Load())
	}
	if _, err := store.DB().Exec(`DROP TRIGGER exhaust_on_terminal`); err != nil {
		t.Fatal(err)
	}
	if err := storage.ConfigureStorageBudget(ctx, store.DB(), 32<<20); err != nil {
		t.Fatal(err)
	}
	sess, err := store.GetSession(r.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := NewRunner(RunnerConfig{Session: sess, ModelManager: r.modelManager, Tools: r.tools, Store: store, Config: r.config,
		CommandJournal: store, RunLedger: r.runLedger, EvidenceStore: r.evidenceStore, StepJournal: r.stepJournal, LeaseOwner: "quota-restart", DurableTiming: &r.durableTiming})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(resumed.Stop)
	if _, err := resumed.AcceptCommand(ctx, command.SessionCommand{ID: "resume", Type: "resume", AcceptedBy: "alice"}); err != nil {
		t.Fatal(err)
	}
	waitForCommandState(t, store, r.sessionID, "resume", sessionexec.StateSucceeded)
	receipt := waitForCommandState(t, store, r.sessionID, "work", sessionexec.StateSucceeded)
	if receipt.Attempt < 2 || requests.Load() != 2 || tools.Load() != 1 {
		t.Fatalf("retry repeated effects: attempt=%d provider=%d tool=%d", receipt.Attempt, requests.Load(), tools.Load())
	}
	report, err := replay.Verify(ctx, r.runLedger, r.stepJournal, r.evidenceStore, sessionexec.RunIDForSession(r.sessionID))
	if err != nil || !report.Valid {
		t.Fatalf("post-recovery replay: %+v err=%v", report, err)
	}
	if got := durableTranscriptRoleSummary(t, store, r.sessionID); got != "user:1 assistant:2 tool:1" {
		t.Fatalf("transcript=%s", got)
	}
}

func TestRunner_ResourceBudgetBoundsLegacyQueuedBytes(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Resources.SessionMemoryBytes = 64 << 10
	r := &Runner{config: cfg, state: StatePaused, commandQueue: make(chan command.SessionCommand, 64)}
	for range 100 {
		err := r.handleLegacySessionCommand(command.SessionCommand{Type: "queue", Content: strings.Repeat("x", 2048)})
		if errors.Is(err, storage.ErrSessionMemoryBudget) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if r.queuedBytes > 32<<10 || len(r.commandQueue) >= 64 {
		t.Fatalf("unbounded queue: bytes=%d count=%d", r.queuedBytes, len(r.commandQueue))
	}
	if err := r.handleLegacySessionCommand(command.SessionCommand{Type: "queue", Content: strings.Repeat("x", 2048)}); !errors.Is(err, storage.ErrSessionMemoryBudget) {
		t.Fatalf("full queue=%v", err)
	}
}

func TestDurableRunner_StorageBudgetSaturationStopsGrowthAndResumes(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_, _ = io.Copy(io.Discard, req.Body)
		n := requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": fmt.Sprintf("fill-%d", n), "model": "gpt-4o",
			"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": strings.Repeat("bounded payload ", 4096)}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 10, "completion_tokens": 10, "total_tokens": 20},
		})
	}))
	t.Cleanup(server.Close)
	const diskLimit = 16 << 20
	r, store := newDurableHTTPRunnerConfigured(t, "quota-saturation", server.URL, func(cfg *RunnerConfig, s *storage.Store) {
		cfg.DurableTiming = &DurableTiming{ScanInterval: 20 * time.Millisecond}
		cfg.Config.Resources.SessionMemoryBytes = 512 << 10
		cfg.Config.Resources.StorageBytes = diskLimit
		if err := storage.ConfigureStorageBudget(context.Background(), s.DB(), diskLimit); err != nil {
			t.Fatal(err)
		}
	})
	ctx := context.Background()
	var turns int
	for ; turns < 100; turns++ {
		id := fmt.Sprintf("fill-%d", turns)
		_, err := r.AcceptCommand(ctx, command.SessionCommand{ID: id, Type: "input", Content: id, AcceptedBy: "alice"})
		if storage.IsStorageBudgetError(err) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		waitForCommandState(t, store, r.sessionID, id, sessionexec.StateSucceeded)
		waitResourceRunnerState(t, r, StateIdle)
	}
	if turns == 0 || turns == 100 {
		t.Fatalf("budget did not stop a growing session after successful work: turns=%d", turns)
	}
	waitResourceRunnerState(t, r, StatePaused)
	before := runnerHistoryDiskBytes(t, r)
	for i := range 20 {
		_, err := r.AcceptCommand(ctx, command.SessionCommand{ID: fmt.Sprintf("rejected-%d", i), Type: "input", Content: "wait for room", AcceptedBy: "alice"})
		if !storage.IsStorageBudgetError(err) {
			t.Fatalf("saturated admission=%v", err)
		}
	}
	after := runnerHistoryDiskBytes(t, r)
	if after > diskLimit || after > before || requests.Load() != int32(turns) {
		t.Fatalf("saturated session kept growing: before=%d after=%d provider=%d turns=%d", before, after, requests.Load(), turns)
	}
	report, err := replay.Verify(ctx, r.runLedger, r.stepJournal, r.evidenceStore, sessionexec.RunIDForSession(r.sessionID))
	if err != nil || !report.Valid || report.StepCount != turns {
		t.Fatalf("saturated replay=%+v err=%v", report, err)
	}
	if err := storage.ConfigureStorageBudget(ctx, store.DB(), 32<<20); err != nil {
		t.Fatal(err)
	}
	if _, err := r.AcceptCommand(ctx, command.SessionCommand{ID: "resume", Type: "resume", AcceptedBy: "alice"}); err != nil {
		t.Fatal(err)
	}
	waitForCommandState(t, store, r.sessionID, "resume", sessionexec.StateSucceeded)
	if _, err := r.AcceptCommand(ctx, command.SessionCommand{ID: "continue", Type: "input", Content: "continue", AcceptedBy: "alice"}); err != nil {
		t.Fatal(err)
	}
	waitForCommandState(t, store, r.sessionID, "continue", sessionexec.StateSucceeded)
	if requests.Load() != int32(turns+1) {
		t.Fatal("quota recovery repeated completed model calls")
	}
	t.Logf("saturation: %d turns, %d bytes including WAL and blobs; 20 rejected commands added no disk; raised budget resumed once", turns, after)
}
