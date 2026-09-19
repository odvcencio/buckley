package subagent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/agentcoord"
	artifactv1 "m31labs.dev/buckley/pkg/artifact/v1"
	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/runledger"
)

func TestCoordinatorDurableArtifactKeepsIncompleteStatus(t *testing.T) {
	child := artifactv1.New(artifactv1.KindSubagentResult, artifactv1.StatusIncomplete, "source handoff", "one condition found, another not observed")
	child.IncompleteReasons = []string{"second requested condition not observed"}
	encoded, err := json.Marshal(child)
	if err != nil {
		t.Fatal(err)
	}
	live, err := os.ReadFile(filepath.Join("testdata", "glm53_incomplete_source_handoff.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, output := range []string{string(encoded), "partial source evidence without the required schema", string(live)} {
		t.Run(output[:min(20, len(output))], func(t *testing.T) {
			store, err := evidence.New(filepath.Join(t.TempDir(), "ledger.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			ledger, err := runledger.NewWithDB(store.DB())
			if err != nil {
				t.Fatal(err)
			}
			manager := NewManager(runnerFunc(func(_ context.Context, _ Request, started func(int)) (string, error) {
				started(11)
				return output, nil
			}), 1)
			t.Cleanup(func() { _ = manager.Close() })
			c := NewCoordinator(manager, WithRunLedger(ledger), WithEvidence(store))
			run, err := c.Spawn(context.Background(), agentcoord.TaskSpec{
				RunID: "source-run", ID: "source-task", ParentSessionID: "source-session", ParentRunID: "parent-run",
				Agent: "extract", Task: "collect two source conditions", OutputSchema: artifactv1.SchemaVersion,
			})
			if err != nil {
				t.Fatal(err)
			}
			finished, err := c.Wait(context.Background(), run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if finished.State != agentcoord.RunCompleted {
				t.Fatalf("execution state changed: %s", finished.State)
			}
			found := false
			for _, id := range finished.Result.EvidenceRefs {
				object, err := store.Get(context.Background(), id)
				if err != nil {
					t.Fatal(err)
				}
				if object.MediaType != artifactv1.MediaType {
					continue
				}
				artifact, _, err := artifactv1.DecodeProviderOutput(context.Background(), object.InlineBody, artifactv1.OutputNativeJSONSchema, artifactv1.DecodeOptions{})
				if err != nil {
					t.Fatal(err)
				}
				if artifact.Status != artifactv1.StatusIncomplete || artifact.Metadata["subagent_run_id"] != run.ID || len(artifact.EvidenceRefs) == 0 {
					t.Fatalf("incomplete handoff or identity lost in storage: %+v", artifact)
				}
				var supplied artifactv1.Artifact
				if json.Unmarshal([]byte(output), &supplied) == nil {
					if artifact.Summary != supplied.Summary || !reflect.DeepEqual(artifact.IncompleteReasons, supplied.IncompleteReasons) {
						t.Fatal("typed partial evidence was altered")
					}
				} else if len(artifact.IncompleteReasons) == 0 {
					t.Fatal("malformed output lacks a schema-failure reason")
				}
				found = true
			}
			if !found {
				t.Fatal("no typed artifact retained")
			}
		})
	}
}

func TestCoordinatorTerminalArtifactPreservesCompletionEvidence(t *testing.T) {
	for _, tc := range []struct {
		name              string
		state             State
		childStatus       artifactv1.ArtifactStatus
		raw               string
		untyped           bool
		reasons           bool
		durabilityFailure bool
		want              artifactv1.ArtifactStatus
		wantSchemaError   bool
	}{
		{name: "completed typed", state: StateCompleted, childStatus: artifactv1.StatusCompleted, want: artifactv1.StatusCompleted},
		{name: "incomplete child", state: StateCompleted, childStatus: artifactv1.StatusIncomplete, reasons: true, want: artifactv1.StatusIncomplete},
		{name: "blocked child", state: StateCompleted, childStatus: artifactv1.StatusBlocked, want: artifactv1.StatusBlocked},
		{name: "failed child", state: StateCompleted, childStatus: artifactv1.StatusFailed, want: artifactv1.StatusFailed},
		{name: "draft child", state: StateCompleted, childStatus: artifactv1.StatusDraft, want: artifactv1.StatusDraft},
		{name: "unfinished reasons", state: StateCompleted, childStatus: artifactv1.StatusCompleted, reasons: true, want: artifactv1.StatusIncomplete},
		{name: "malformed required output", state: StateCompleted, raw: "partial source evidence, not an artifact", want: artifactv1.StatusIncomplete, wantSchemaError: true},
		{name: "empty required output", state: StateCompleted, want: artifactv1.StatusIncomplete, wantSchemaError: true},
		{name: "whitespace required output", state: StateCompleted, raw: " \n\t", want: artifactv1.StatusIncomplete, wantSchemaError: true},
		{name: "legacy text", state: StateCompleted, raw: "partial source evidence, not an artifact", untyped: true, want: artifactv1.StatusCompleted},
		{name: "failed execution", state: StateFailed, childStatus: artifactv1.StatusCompleted, want: artifactv1.StatusFailed},
		{name: "cancelled execution", state: StateCancelled, childStatus: artifactv1.StatusCompleted, want: artifactv1.StatusFailed},
		{name: "failed execution malformed output", state: StateFailed, raw: "partial source evidence", want: artifactv1.StatusFailed, wantSchemaError: true},
		{name: "durability failure", state: StateCompleted, childStatus: artifactv1.StatusCompleted, durabilityFailure: true, want: artifactv1.StatusFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output := tc.raw
			if tc.childStatus != "" {
				child := artifactv1.New(artifactv1.KindSubagentResult, tc.childStatus, "source handoff", "observed source evidence")
				if tc.reasons {
					child.IncompleteReasons = []string{"requested condition not observed"}
				}
				encoded, err := json.Marshal(child)
				if err != nil {
					t.Fatal(err)
				}
				output = string(encoded)
			}
			c := &Coordinator{}
			if tc.durabilityFailure {
				c.durabilityError = map[string]string{"child-run": "durable write failed"}
			}
			spec := agentcoord.AgentTaskSpec{Agent: "extract", Task: "collect source evidence", OutputSchema: artifactv1.SchemaVersion}
			if tc.untyped {
				spec.OutputSchema = ""
			}
			raw := artifactv1.EvidenceRef{ID: "raw-evidence", Kind: "tool_result"}
			got, err := c.terminalArtifact(spec, Snapshot{ID: "child-run", State: tc.state, Output: output}, raw)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != tc.want {
				t.Errorf("status = %q, want %q", got.Status, tc.want)
			}
			if got.ArtifactID == "" || got.Metadata["subagent_run_id"] != "child-run" {
				t.Fatalf("lost identity: %+v", got)
			}
			if len(got.EvidenceRefs) == 0 || got.EvidenceRefs[len(got.EvidenceRefs)-1].ID != raw.ID {
				t.Fatal("lost raw evidence reference")
			}
			if tc.reasons && (len(got.IncompleteReasons) != 1 || got.IncompleteReasons[0] != "requested condition not observed") {
				t.Fatal("lost incomplete reasons")
			}
			if tc.childStatus != "" && got.Summary != "observed source evidence" {
				t.Fatal("lost typed partial output")
			}
			foundSchemaError := false
			for _, d := range got.Diagnostics {
				if d.Code == "subagent.output_schema" {
					foundSchemaError = true
				}
			}
			if foundSchemaError != tc.wantSchemaError {
				t.Errorf("schema diagnostic=%v, want %v", foundSchemaError, tc.wantSchemaError)
			}
			if tc.wantSchemaError && len(got.IncompleteReasons) == 0 {
				t.Error("schema failure lacks incomplete reason")
			}
			if tc.raw != "" && strings.TrimSpace(tc.raw) != "" && tc.childStatus == "" && !strings.Contains(got.Summary, tc.raw) {
				t.Error("lost raw partial summary")
			}
		})
	}
}
