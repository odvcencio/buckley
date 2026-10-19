package headless

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/ipc/command"
	"m31labs.dev/buckley/pkg/replay"
	"m31labs.dev/buckley/pkg/runledger"
	"m31labs.dev/buckley/pkg/sessionexec"
	"m31labs.dev/buckley/pkg/storage"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

func TestDurableRunner_DirectoryMutationResumesAndReplays(t *testing.T) {
	work, evidencePath := t.TempDir(), t.TempDir()
	for name, content := range map[string]string{
		"go.mod":       "module directoryfixture\n\ngo 1.26\n",
		"calc.go":      "package fixture\nfunc Add(a,b int)int{return a-b}\n",
		"calc_test.go": "package fixture\nimport \"testing\"\nfunc TestAdd(t *testing.T){if Add(2,3)!=5{t.Fatal(\"wrong sum\")}}\n",
	} {
		if err := os.WriteFile(filepath.Join(work, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		n := requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		var function string
		switch n {
		case 1:
			function = `{"name":"write_file","arguments":"{\"path\":\"calc.go\",\"content\":\"package fixture\\nfunc Add(a,b int)int{return a+b}\\n\"}"}`
		case 2:
			function = `{"name":"run_tests","arguments":"{}"}`
		case 4:
			function = `{"name":"read_file","arguments":"{\"path\":\"calc.go\"}"}`
		case 3, 5:
			fmt.Fprint(w, `{"model":"gpt-4o","choices":[{"message":{"role":"assistant","content":"Observed result: a+b."},"finish_reason":"stop"}]}`)
			return
		default:
			t.Errorf("unexpected model replay: call %d", n)
			http.Error(w, "unexpected provider call", http.StatusBadRequest)
			return
		}
		fmt.Fprintf(w, `{"model":"gpt-4o","choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call_%d","type":"function","function":%s}]},"finish_reason":"tool_calls"}]}`, n, function)
	}))
	t.Cleanup(server.Close)
	newTools := func() *tool.Registry {
		registry := tool.NewEmptyRegistry()
		registry.Register(&builtin.WriteFileTool{})
		registry.Register(&builtin.RunTestsTool{})
		registry.Register(&builtin.ReadFileTool{})
		registry.SetWorkDir(work)
		t.Cleanup(func() { _ = registry.Close() })
		return registry
	}
	const sessionID = "directory-mutation"
	var saved RunnerConfig
	runner, store := newDurableHTTPRunnerConfigured(t, sessionID, server.URL, func(cfg *RunnerConfig, store *storage.Store) {
		cfg.Session.ProjectPath = work
		if err := store.UpdateSessionProjectPath(sessionID, work); err != nil {
			t.Fatal(err)
		}
		cfg.Tools = newTools()
		cfg.Config.Approval.Mode = "auto"
		var err error
		cfg.EvidenceStore, err = evidence.NewWithDB(store.DB(), evidencePath)
		if err != nil {
			t.Fatal(err)
		}
		saved = *cfg
	})
	input := command.SessionCommand{SessionID: sessionID, ID: "fix-add", Type: "input", Content: "Fix Add and verify it.", TaskIntent: "mutation", AcceptedBy: "alice"}
	if _, err := runner.AcceptCommand(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	waitForCommandState(t, store, sessionID, input.ID, sessionexec.StateSucceeded)
	if requests.Load() != 3 {
		t.Fatalf("mutation requests = %d, want 3", requests.Load())
	}
	if data, err := os.ReadFile(filepath.Join(work, "calc.go")); err != nil || !strings.Contains(string(data), "a+b") {
		t.Fatalf("mutation not applied: %q, %v", data, err)
	}
	if receipt, err := runner.AcceptCommand(context.Background(), input); err != nil || !receipt.Duplicate || requests.Load() != 3 {
		t.Fatalf("duplicate mutation reran: %+v, %v, requests=%d", receipt, err, requests.Load())
	}
	runner.Stop()
	var sequence int
	var name, path string
	if err := store.DB().QueryRow(`PRAGMA database_list`).Scan(&sequence, &name, &path); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := storage.New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	ledger, err := runledger.NewWithDB(reopened.DB())
	if err != nil {
		t.Fatal(err)
	}
	retained, err := evidence.NewWithDB(reopened.DB(), evidencePath)
	if err != nil {
		t.Fatal(err)
	}
	saved.Session, err = reopened.GetSession(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	saved.Store, saved.CommandJournal, saved.RunLedger, saved.EvidenceStore = reopened, reopened, ledger, retained
	saved.StepJournal = ledger
	saved.Tools, saved.LeaseOwner = newTools(), "restarted-directory-owner"
	resumed, err := NewRunner(saved)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(resumed.Stop)
	if receipt, err := resumed.AcceptCommand(context.Background(), input); err != nil || !receipt.Duplicate || requests.Load() != 3 {
		t.Fatalf("reopened mutation reran: %+v, %v, requests=%d", receipt, err, requests.Load())
	}
	if _, err := resumed.AcceptCommand(context.Background(), command.SessionCommand{
		SessionID: sessionID, ID: "read-result", Type: "input", TaskIntent: "read_only", Content: "Read the current Add implementation.", AcceptedBy: "alice",
	}); err != nil {
		t.Fatal(err)
	}
	waitForCommandState(t, reopened, sessionID, "read-result", sessionexec.StateSucceeded)
	resumed.Stop()
	report, err := replay.Verify(context.Background(), ledger, ledger, retained, sessionexec.RunIDForSession(sessionID))
	if err != nil || !report.Valid || requests.Load() != 5 {
		t.Fatalf("directory replay = %+v, %v, requests=%d", report, err, requests.Load())
	}
}
