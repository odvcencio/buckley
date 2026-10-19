package subagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/agentcoord"
	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/runledger"
)

func withCompletedRunCacheLimits(entries int, bytes int64) CoordinatorOption {
	return func(c *Coordinator) {
		c.completedCacheEntries = entries
		c.completedCacheLimit = bytes
	}
}

func TestCoordinator_CompletedCachePreservesColdResultsAndReplay(t *testing.T) {
	for _, test := range []struct {
		name    string
		entries int
		bytes   int64
	}{
		{name: "entry limit", entries: 2, bytes: 1024 * 1024},
		{name: "byte limit", entries: 32, bytes: 128 * 1024},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			dbPath := filepath.Join(t.TempDir(), "reports.db")
			store, err := evidence.New(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			ledger, err := runledger.NewWithDB(store.DB())
			if err != nil {
				t.Fatal(err)
			}
			activeStarted := make(chan struct{})
			var calls atomic.Int32
			manager := NewManager(runnerFunc(func(ctx context.Context, req Request, _ func(int)) (string, error) {
				calls.Add(1)
				if req.Agent == "active" {
					close(activeStarted)
					<-ctx.Done()
					return "", ctx.Err()
				}
				return req.ID + strings.Repeat("r", 64*1024-len(req.ID)), nil
			}), 2)
			t.Cleanup(func() { _ = manager.Close() })
			coordinator := NewCoordinator(manager, WithRunLedger(ledger), WithEvidence(store), withCompletedRunCacheLimits(test.entries, test.bytes))
			active, err := coordinator.Spawn(ctx, agentcoord.TaskSpec{RunID: "active", ParentSessionID: "reports", Agent: "active", Task: "keep working"})
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-activeStarted:
			case <-ctx.Done():
				t.Fatal("active worker did not start")
			}
			fullTask := strings.TrimSpace(strings.Repeat("full task contract must survive cache eviction\n", 300))
			var firstSpec agentcoord.TaskSpec
			for index := 0; index < 12; index++ {
				spec := agentcoord.TaskSpec{RunID: fmt.Sprintf("report-%02d", index), ParentSessionID: "reports", Task: fullTask, Metadata: map[string]string{"index": fmt.Sprint(index)}}
				if index == 0 {
					firstSpec = spec
				}
				run, err := coordinator.Spawn(ctx, spec)
				if err != nil {
					t.Fatal(err)
				}
				if finished, err := coordinator.Wait(ctx, run.ID); err != nil || finished.State != agentcoord.RunCompleted {
					t.Fatalf("completion = %+v, %v", finished, err)
				}
				if index == 0 {
					if _, err := coordinator.Claim(ctx, agentcoord.ClaimRequest{RunID: run.ID, Resources: []string{"pkg/cold-report"}}); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, cached := manager.Status(firstSpec.RunID); cached {
				t.Fatal("old report was not evicted")
			}
			if snapshot, ok := manager.Status(active.ID); !ok || snapshot.State != StateRunning {
				t.Fatalf("cache evicted active work: %+v, %v", snapshot, ok)
			}
			for repeat := 0; repeat < 2; repeat++ {
				for index := 0; index < 12; index++ {
					id := fmt.Sprintf("report-%02d", index)
					run, err := coordinator.Wait(ctx, id)
					if err != nil || run.State != agentcoord.RunCompleted || run.Task.Task != fullTask || run.Task.Metadata["index"] != fmt.Sprint(index) || run.AttemptID == "" || run.LeaseGeneration < 1 || len(run.Result.EvidenceRefs) < 2 {
						t.Fatalf("cold result %s: state=%s task_bytes=%d refs=%d err=%v", id, run.State, len(run.Task.Task), len(run.Result.EvidenceRefs), err)
					}
				}
				if runs, err := coordinator.List(ctx, agentcoord.RunFilter{ParentSessionID: "reports"}); err != nil || len(runs) != 13 {
					t.Fatalf("cold list count = %d, %v", len(runs), err)
				}
				if run, err := coordinator.Spawn(ctx, firstSpec); err != nil || run.State != agentcoord.RunCompleted || run.Task.Task != fullTask {
					t.Fatalf("idempotent cold spawn: state=%s task_bytes=%d err=%v", run.State, len(run.Task.Task), err)
				}
			}
			coordinator.mu.RLock()
			cachedBytes, cachedEntries, taskEntries := coordinator.completedCacheBytes, len(coordinator.completedCache), len(coordinator.runs)
			coordinator.mu.RUnlock()
			if cachedBytes > test.bytes || cachedEntries > test.entries || taskEntries > cachedEntries+1 {
				t.Fatalf("cold reads refilled the cache: bytes=%d reports=%d tasks=%d", cachedBytes, cachedEntries, taskEntries)
			}
			if calls.Load() != 13 {
				t.Fatalf("history reads reran work: %d calls", calls.Load())
			}
			if run, err := coordinator.Status(ctx, firstSpec.RunID); err != nil || len(run.Claims) != 1 || run.Claims[0] != "pkg/cold-report" {
				t.Fatalf("cache eviction changed an active claim: claims=%v err=%v", run.Claims, err)
			}
			if run, err := coordinator.Cancel(ctx, firstSpec.RunID, "already finished"); err != nil || run.State != agentcoord.RunCompleted {
				t.Fatalf("cold cancel = %+v, %v", run, err)
			}
			if _, err := coordinator.Claim(ctx, agentcoord.ClaimRequest{RunID: firstSpec.RunID, Resources: []string{"pkg/cold-report"}}); err != nil {
				t.Fatal(err)
			}
			if err := coordinator.Release(ctx, agentcoord.ClaimRequest{RunID: firstSpec.RunID}, "synthesis complete"); err != nil {
				t.Fatal(err)
			}
			if err := manager.Close(); err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := evidence.New(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = reopened.Close() })
			reopenedLedger, err := runledger.NewWithDB(reopened.DB())
			if err != nil {
				t.Fatal(err)
			}
			recovered := NewCoordinator(nil, WithRunLedger(reopenedLedger), WithEvidence(reopened))
			run, err := recovered.Wait(ctx, firstSpec.RunID)
			if err != nil || run.Task.Task != fullTask || run.State != agentcoord.RunCompleted || run.AttemptID == "" || run.LeaseGeneration < 1 {
				t.Fatalf("replay: state=%s task_bytes=%d err=%v", run.State, len(run.Task.Task), err)
			}
			found := false
			for _, id := range run.Result.EvidenceRefs {
				var pins int
				if err := reopened.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM evidence_pins WHERE evidence_id = ? AND reason = ?", id, "run:"+run.ID).Scan(&pins); err != nil || pins != 1 {
					t.Fatalf("replay evidence pin %s: count=%d err=%v", id, pins, err)
				}
				object, err := reopened.Get(ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				if object.MediaType == "application/json" {
					var report struct {
						Output string `json:"output"`
					}
					if err := json.Unmarshal(object.InlineBody, &report); err != nil {
						t.Fatal(err)
					}
					found = len(report.Output) == 64*1024 && strings.HasPrefix(report.Output, firstSpec.RunID)
				}
			}
			if !found {
				t.Fatal("full report did not survive eviction and restart")
			}
		})
	}
}

func TestCoordinator_CompletedCacheConcurrentWaits(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	store, err := evidence.New(filepath.Join(t.TempDir(), "reports.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ledger, err := runledger.NewWithDB(store.DB())
	if err != nil {
		t.Fatal(err)
	}
	finish := make(chan struct{})
	manager := NewManager(runnerFunc(func(ctx context.Context, _ Request, _ func(int)) (string, error) {
		select {
		case <-finish:
			return strings.Repeat("report", 1024), nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}), 2)
	t.Cleanup(func() { _ = manager.Close() })
	coordinator := NewCoordinator(manager, WithRunLedger(ledger), WithEvidence(store), withCompletedRunCacheLimits(1, 1024))
	for index := 0; index < 16; index++ {
		run, err := coordinator.Spawn(ctx, agentcoord.TaskSpec{RunID: fmt.Sprintf("wait-%02d", index), ParentSessionID: "concurrent", Task: "finish during concurrent wait"})
		if err != nil {
			t.Fatal(err)
		}
		ready := make(chan struct{}, 8)
		var waiters sync.WaitGroup
		for waiter := 0; waiter < 8; waiter++ {
			waiters.Add(1)
			go func() {
				defer waiters.Done()
				ready <- struct{}{}
				finished, err := coordinator.Wait(ctx, run.ID)
				if err != nil || finished.ID != run.ID || finished.State != agentcoord.RunCompleted || finished.Result.Summary == "" {
					t.Errorf("concurrent wait lost result: id=%s state=%s summary_bytes=%d err=%v", finished.ID, finished.State, len(finished.Result.Summary), err)
				}
			}()
		}
		for waiter := 0; waiter < 8; waiter++ {
			<-ready
		}
		select {
		case finish <- struct{}{}:
		case <-ctx.Done():
			t.Fatal("worker did not finish")
		}
		waiters.Wait()
	}
}

func TestCoordinator_CompletedCacheLeavesLocalReportsAvailable(t *testing.T) {
	payload := strings.Repeat("local report", 1024)
	manager := NewManager(runnerFunc(func(context.Context, Request, func(int)) (string, error) { return payload, nil }), 1)
	t.Cleanup(func() { _ = manager.Close() })
	coordinator := NewCoordinator(manager, withCompletedRunCacheLimits(1, 1))
	for index := 0; index < 3; index++ {
		run, err := coordinator.Spawn(context.Background(), agentcoord.TaskSpec{RunID: fmt.Sprintf("local-%d", index), Task: "retain without an archive"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := coordinator.Wait(context.Background(), run.ID); err != nil {
			t.Fatal(err)
		}
	}
	if reports := manager.List(); len(reports) != 3 {
		t.Fatalf("local reports discarded: %d", len(reports))
	}
	if oldest, ok := manager.Status("local-0"); !ok || oldest.Output != payload {
		t.Fatal("full local report was discarded")
	}
}

type failingReportEvidence struct{ evidence.Store }

func (s failingReportEvidence) Put(ctx context.Context, object evidence.Object) (evidence.Object, error) {
	if object.Kind == evidence.KindSubagentReport {
		return evidence.Object{}, errors.New("report write unavailable")
	}
	return s.Store.Put(ctx, object)
}

type failingReportFinalizer struct{ *runledger.SQLiteStore }

func (s failingReportFinalizer) FinalizeRunAttempt(context.Context, runledger.AttemptFinalization) error {
	return errors.New("terminal commit unavailable")
}

func TestCoordinator_CompletedCacheRetainsUnpersistedReports(t *testing.T) {
	for _, mode := range []string{"report write", "terminal commit"} {
		t.Run(mode, func(t *testing.T) {
			store, err := evidence.New(filepath.Join(t.TempDir(), "reports.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			ledger, err := runledger.NewWithDB(store.DB())
			if err != nil {
				t.Fatal(err)
			}
			var reportStore evidence.Store = store
			var reportLedger runledger.Store = ledger
			if mode == "report write" {
				reportStore = failingReportEvidence{store}
			} else {
				reportLedger = failingReportFinalizer{ledger}
			}
			var calls atomic.Int32
			manager := NewManager(runnerFunc(func(context.Context, Request, func(int)) (string, error) {
				calls.Add(1)
				return strings.Repeat("unpersisted output", 1024), nil
			}), 1)
			t.Cleanup(func() { _ = manager.Close() })
			coordinator := NewCoordinator(manager, WithRunLedger(reportLedger), WithEvidence(reportStore), withCompletedRunCacheLimits(1, 1024))
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			for index := 0; index < 6; index++ {
				run, err := coordinator.Spawn(ctx, agentcoord.TaskSpec{RunID: fmt.Sprintf("failed-%d", index), ParentSessionID: "unpersisted", Task: "keep unsaved evidence"})
				if index >= 2 {
					if err == nil || !strings.Contains(err.Error(), "report cache is full") {
						t.Fatalf("capacity error = %v", err)
					}
					continue
				}
				if err != nil {
					t.Fatal(err)
				}
				finished, err := coordinator.Wait(ctx, run.ID)
				if err != nil || finished.State != agentcoord.RunFailed {
					t.Fatalf("failed report = %+v, %v", finished, err)
				}
			}
			if calls.Load() != 2 {
				t.Fatalf("cache overflow launched %d workers", calls.Load())
			}
			if snapshot, ok := manager.Status("failed-0"); !ok || len(snapshot.Output) == 0 {
				t.Fatalf("unsaved report was discarded: %+v, %v", snapshot, ok)
			}
			coordinator.mu.RLock()
			entries, tasks, refs := len(coordinator.completedCache), len(coordinator.runs), len(coordinator.evidenceByRun)
			coordinator.mu.RUnlock()
			if entries != 0 || tasks > 2 || refs > 2 {
				t.Fatalf("unpersisted cache grew past its capacity: reports=%d tasks=%d refs=%d", entries, tasks, refs)
			}
		})
	}
}
