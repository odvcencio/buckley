package main

import (
	"context"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/runledger"
)

func TestRunGoalAuditExecProgram(t *testing.T) {
	t.Setenv(envBuckleyDataDir, t.TempDir())
	stores, cleanup, err := openGoalStores()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	ctx := context.Background()
	run, err := stores.ledger.StartRun(ctx, runledger.AgentRun{SessionID: "audit-exec", Backend: "code-mode"})
	if err != nil {
		t.Fatal(err)
	}
	start, err := stores.ledger.Append(ctx, runledger.Event{
		Type: "exec_program.started", RunID: run.RunID,
		EvidenceIDs: []string{"ev_source"}, Payload: map[string]any{"program_evidence": "ev_source", "source": "private-source-sentinel"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = stores.ledger.Append(ctx, runledger.Event{
		Type: "exec_program.finished", RunID: run.RunID,
		EvidenceIDs: []string{"ev_source", "ev_output"}, Payload: map[string]any{"execution_id": start.ID, "output_evidence": "ev_output", "success": true, "exit_code": 0, "stdout": "private-output-sentinel"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = stores.ledger.Append(ctx, runledger.Event{
		Type: "exec_program.finished", RunID: run.RunID,
		Payload: map[string]any{"execution_id": "bad\nINJECT", "output_evidence": "bad\nINJECT", "success": false, "exit_code": "bad\nINJECT"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var auditErr error
	output := captureStdout(t, func() { auditErr = runGoalAudit([]string{run.RunID}) })
	if auditErr != nil {
		t.Fatal(auditErr)
	}
	for _, want := range []string{"exec start", "exec finish", "id=" + start.ID, "program=ev_source", "output=ev_output", "success=true", "exit=0", "<invalid>"} {
		if !strings.Contains(output, want) {
			t.Fatalf("missing %q in %s", want, output)
		}
	}
	for _, forbidden := range []string{"private-source-sentinel", "private-output-sentinel", "INJECT", "No audited events"} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("unexpected %q in audit", forbidden)
		}
	}
}
