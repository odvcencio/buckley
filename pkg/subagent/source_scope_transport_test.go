package subagent

import (
	"context"
	"encoding/base64"
	"fmt"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/agentcoord"
	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/runledger"
)

func transportTestScope() *agentcoord.SourceScope {
	return &agentcoord.SourceScope{Files: []agentcoord.SourceFile{{Path: "source.txt", StartLine: 1, EndLine: 3}}}
}

func TestSourceScopeChildTransportVersions(t *testing.T) {
	for _, scope := range []*agentcoord.SourceScope{nil, {}, transportTestScope()} {
		contract := ChildContractFromRequest(Request{SourceScope: scope})
		encoded, err := EncodeChildContract(contract)
		if err != nil {
			t.Fatal(err)
		}
		got, present, err := DecodeChildContract(encoded)
		if err != nil || !present || (got.SourceScope == nil) != (scope == nil) {
			t.Fatalf("scope roundtrip: %+v, %v", got, err)
		}
		if scope == nil && got.SchemaVersion != childContractVersion || scope != nil && got.SchemaVersion != childContractSourceScopeVersion {
			t.Fatalf("wrong compatibility version: %s", got.SchemaVersion)
		}
		if scope != nil && !reflect.DeepEqual(got.SourceScope, agentcoord.CloneSourceScope(scope)) {
			t.Fatalf("constraints changed: %+v", got.SourceScope)
		}
	}
	for _, data := range []string{
		`{"schema_version":"buckley.subagent-contract/v1","source_scope":{"files":[]}}`,
		`{"schema_version":"buckley.subagent-contract/v1","source_scope":null}`,
		`{"schema_version":"buckley.subagent-contract/v2"}`,
		`{"schema_version":"buckley.subagent-contract/v2","source_scope":null}`,
		`{"schema_version":"buckley.subagent-contract/v2","source_scope":{"files":[{"path":"../x"}]}}`,
		`{"schema_version":"buckley.subagent-contract/v2","source_scope":{"files":[{"path":"x","glob":"*"}]}}`,
		`{"schema_version":"buckley.subagent-contract/v2","source_scope":{"files":[{"path":"x","start_line":null,"end_line":null}]}}`,
	} {
		if _, present, err := DecodeChildContract(base64.RawURLEncoding.EncodeToString([]byte(data))); !present || err == nil {
			t.Fatalf("unsafe contract decoded: %s, %v", data, err)
		}
	}
	if _, err := EncodeChildContract(ChildContract{SchemaVersion: childContractVersion, SourceScope: transportTestScope()}); err == nil {
		t.Fatal("legacy child could silently drop scope")
	}
	scope := transportTestScope()
	contract := ChildContractFromRequest(Request{SourceScope: scope})
	scope.Files[0].EndLine = 999
	if contract.SourceScope.Files[0].EndLine != 3 {
		t.Fatal("request poisoned child contract")
	}
}

func TestSourceScopeManagerExternalCopies(t *testing.T) {
	requests := make(chan Request, 1)
	release := make(chan struct{})
	var observations atomic.Int32
	manager := NewManager(runnerFunc(func(ctx context.Context, request Request, started func(int)) (string, error) {
		started(42)
		requests <- request
		select {
		case <-release:
			return "done", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}), 1)
	t.Cleanup(func() { _ = manager.Close() })
	manager.SetLifecycleObserver(func(snapshot Snapshot) {
		observations.Add(1)
		snapshot.SourceScope.Files[0].EndLine = 999
	})
	manager.SetHeartbeatObserver(func(_ context.Context, snapshot Snapshot) error {
		snapshot.SourceScope.Files[0].EndLine = 888
		return nil
	}, time.Hour)
	scope := transportTestScope()
	spawned, err := manager.SpawnWithOptions(SpawnOptions{Task: "read", SourceScope: scope, AttemptID: "a", LeaseGeneration: 1})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var request Request
	select {
	case request = <-requests:
	case <-ctx.Done():
		t.Fatal("runner did not receive scope")
	}
	if request.SourceScope.Files[0].EndLine != 3 {
		t.Fatal("observer poisoned runner scope")
	}
	scope.Files[0].EndLine = 7
	spawned.SourceScope.Files[0].EndLine = 8
	request.SourceScope.Files[0].EndLine = 9
	listed := manager.List()
	listed[0].SourceScope.Files[0].EndLine = 10
	status, _ := manager.Status(spawned.ID)
	if status.SourceScope.Files[0].EndLine != 3 {
		t.Fatal("external snapshots poisoned stored scope")
	}
	status.SourceScope.Files[0].EndLine = 11
	close(release)
	finished, err := manager.Wait(ctx, spawned.ID)
	if err != nil || finished.SourceScope.Files[0].EndLine != 3 || observations.Load() < 2 {
		t.Fatalf("terminal scope: %+v, %v", finished, err)
	}
	finished.SourceScope.Files[0].EndLine = 12
	cancelled, err := manager.Cancel(spawned.ID)
	if err != nil || cancelled.SourceScope.Files[0].EndLine != 3 {
		t.Fatal("wait snapshot poisoned cancel snapshot")
	}
	cancelled.SourceScope.Files[0].EndLine = 13
	status, _ = manager.Status(spawned.ID)
	if status.SourceScope.Files[0].EndLine != 3 {
		t.Fatal("cancel snapshot poisoned stored scope")
	}
}

func TestSourceScopeAdmissionCopiesAndDigest(t *testing.T) {
	manager := NewManager(runnerFunc(func(context.Context, Request, func(int)) (string, error) {
		return "", fmt.Errorf("unexpected launch")
	}), 1)
	t.Cleanup(func() { _ = manager.Close() })
	if _, err := manager.SpawnWithOptions(SpawnOptions{Task: "read", SourceScope: &agentcoord.SourceScope{Files: []agentcoord.SourceFile{{Path: "../x"}}}}); err == nil {
		t.Fatal("invalid scope admitted")
	}
	spec := agentcoord.TaskSpec{RunID: "r", Task: "read", SourceScope: transportTestScope()}
	coordinator := NewCoordinator(manager)
	if err := coordinator.registerRun(spec); err != nil {
		t.Fatal(err)
	}
	digest := logicalTaskDigest(spec)
	spec.SourceScope.Files[0].EndLine = 999
	stored, _ := coordinator.taskSpec("r")
	if stored.SourceScope.Files[0].EndLine != 3 || digest != logicalTaskDigest(stored) {
		t.Fatal("caller poisoned coordinator task")
	}
	stored.SourceScope.Files[0].EndLine = 4
	if digest == logicalTaskDigest(stored) {
		t.Fatal("scope absent from durable identity")
	}
	stored, _ = coordinator.taskSpec("r")
	opts := spawnOptionsFromTask(stored)
	opts.SourceScope.Files[0].EndLine = 5
	fromSnapshot := taskSpecFromSnapshot(Snapshot{SourceScope: stored.SourceScope})
	fromSnapshot.SourceScope.Files[0].EndLine = 6
	if stored.SourceScope.Files[0].EndLine != 3 {
		t.Fatal("transport conversions alias scope")
	}
	coordinator.setTaskSpec(stored)
	stored.SourceScope = nil
	restored, _ := coordinator.taskSpec("r")
	if restored.SourceScope == nil {
		t.Fatal("external task erased stored scope")
	}
	restored.SourceScope = nil
	if digest == logicalTaskDigest(restored) {
		t.Fatal("an older reader ignoring source_scope could pass the durable input digest")
	}
}

func TestSourceScopeDurableRestore(t *testing.T) {
	store, err := evidence.New(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ledger, err := runledger.NewWithDB(store.DB())
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(runnerFunc(func(_ context.Context, r Request, started func(int)) (string, error) {
		if r.SourceScope == nil || r.SourceScope.Files[0].EndLine != 3 {
			return "", fmt.Errorf("runner lost source scope")
		}
		started(11)
		return "read result", nil
	}), 1)
	t.Cleanup(func() { _ = manager.Close() })
	coordinator := NewCoordinator(manager, WithRunLedger(ledger), WithEvidence(store))
	run, err := coordinator.Spawn(context.Background(), agentcoord.TaskSpec{RunID: "scoped", ID: "task", ParentSessionID: "session", Task: "read", SourceScope: transportTestScope()})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := coordinator.Wait(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	recovered := NewCoordinator(nil, WithRunLedger(ledger), WithEvidence(store))
	got, err := recovered.taskSpecForRun(ctx, run.ID)
	if err != nil || !reflect.DeepEqual(got.SourceScope, transportTestScope()) {
		t.Fatalf("durable contract lost scope: %+v, %v", got, err)
	}
	got.SourceScope.Files[0].EndLine = 999
	again, err := recovered.taskSpecForRun(ctx, run.ID)
	if err != nil || again.SourceScope.Files[0].EndLine != 3 {
		t.Fatalf("restored task poisoned coordinator: %+v, %v", again, err)
	}
}

func TestSourceScopeAdmissionPolicyCannotBroadenInput(t *testing.T) {
	coordinator := NewCoordinator(nil, WithAdmissionPolicy(AdmissionPolicyFunc(func(_ context.Context, input agentcoord.TaskSpec) (AdmissionDecision, error) {
		input.SourceScope.Files[0].StartLine = 0
		input.SourceScope.Files[0].EndLine = 0
		return AdmissionDecision{Allowed: true}, nil
	})))
	spec := agentcoord.TaskSpec{Task: "read", SourceScope: transportTestScope()}
	if err := coordinator.applyAdmission(context.Background(), &spec); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(spec.SourceScope, transportTestScope()) {
		t.Fatalf("admission callback broadened caller source constraints: %+v", spec.SourceScope)
	}
}
