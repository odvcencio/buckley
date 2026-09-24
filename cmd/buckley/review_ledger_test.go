package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/oneshot"
	"m31labs.dev/buckley/pkg/oneshot/commands"
	"m31labs.dev/buckley/pkg/orchestrator"
	"m31labs.dev/buckley/pkg/storage/reviewledger"
)

type ledgerCommandObjects struct{ data map[string][]byte }

func (f *ledgerCommandObjects) Create(_ context.Context, key string, body []byte, _ string) error {
	if _, ok := f.data[key]; ok {
		return reviewledger.ErrExists
	}
	f.data[key] = body
	return nil
}
func (f *ledgerCommandObjects) Read(_ context.Context, key string) ([]byte, error) {
	body, ok := f.data[key]
	if !ok {
		return nil, reviewledger.ErrNotFound
	}
	return body, nil
}
func (f *ledgerCommandObjects) List(_ context.Context, prefix string) ([]string, error) {
	var keys []string
	for key := range f.data {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	return keys, nil
}

func TestReviewLedger_CommandsAndFailedReview(t *testing.T) {
	objects := &ledgerCommandObjects{data: map[string][]byte{}}
	ledger := reviewledger.New(objects, "bucket", "prefix", t.TempDir())
	record := orchestrator.ReviewRecord{SchemaVersion: 1, ReviewID: "failed-review", Repository: "owner/repo", Ref: "topic", Model: "model", StartedAt: time.Now().Add(-time.Second), Findings: json.RawMessage("[]")}
	exit := 3
	result := &reviewCommandResult{reviewText: "partial review", incomplete: true, toolEvidence: []oneshot.AgentToolCall{{Name: "run_verification", Data: map[string]any{"command": "go test ./pkg", "argv": []any{"go", "test", "./pkg with spaces"}, "exit_code": 2, "status": "FAIL", "stdout": "failed test output"}}}, commandEvidence: []model.CommandExecutionEvidence{{Command: "go vet ./pkg", ExitCode: &exit, AggregatedOutput: "native log", Status: "completed"}}}
	pr := &commands.PRInfo{Repository: "owner/repo", Number: 17, BaseSHA: strings.Repeat("b", 40), HeadSHA: strings.Repeat("a", 40), HeadBranch: "topic"}
	finishReviewLedger(ledger, record, result, pr, errors.New("provider stopped"))
	var output bytes.Buffer
	if err := executeReviewLedgerCommand(context.Background(), ledger, []string{"list", "--repo", "owner/repo", "--pr", "17"}, &output); err != nil {
		t.Fatal(err)
	}
	var records []orchestrator.ReviewRecord
	if err := json.Unmarshal(output.Bytes(), &records); err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Verdict != "INCOMPLETE" || records[0].Error != "provider stopped" || len(records[0].Verification) != 2 {
		t.Fatalf("records=%+v", records)
	}
	if got := records[0].Verification[0].Argv; len(got) != 3 || got[2] != "./pkg with spaces" {
		t.Fatalf("lost command arguments: %v", got)
	}
	if *records[0].Verification[0].ExitCode != 2 || *records[0].Verification[1].ExitCode != 3 {
		t.Fatal("lost exit codes")
	}
	output.Reset()
	if err := executeReviewLedgerCommand(context.Background(), ledger, []string{"show", "failed-review"}, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "native log") && len(records[0].Evidence) != 3 {
		t.Fatal("missing evidence")
	}
	if err := executeReviewLedgerCommand(context.Background(), ledger, []string{"show", "missing"}, &output); !errors.Is(err, reviewledger.ErrNotFound) {
		t.Fatal(err)
	}
}

func TestReviewLedger_Disabled(t *testing.T) {
	if configuredReviewLedger(&config.Config{}) != nil {
		t.Fatal("empty bucket enabled ledger")
	}
	finishReviewLedger(nil, orchestrator.ReviewRecord{}, nil, nil, errors.New("failed"))
}

func TestReviewLedger_CapturedSnapshotRevisions(t *testing.T) {
	objects := &ledgerCommandObjects{data: map[string][]byte{}}
	ledger := reviewledger.New(objects, "bucket", "", t.TempDir())
	root := t.TempDir()
	head, base := strings.Repeat("a", 40), strings.Repeat("b", 40)
	snapshot, err := model.NewReviewSnapshot(model.ReviewSnapshotHead, root, root, head, nil)
	if err != nil {
		t.Fatal(err)
	}
	record := orchestrator.ReviewRecord{SchemaVersion: 1, ReviewID: "captured", Repository: "owner/repo", Ref: "stale-ref", HeadSHA: strings.Repeat("c", 40), StartedAt: time.Now(), Findings: json.RawMessage("[]")}
	result := &reviewCommandResult{snapshot: snapshot, baseSHA: base, reviewRef: "captured-ref", reviewText: "review", parsed: &commands.ParsedReview{Verdict: "APPROVE"}}
	finishReviewLedger(ledger, record, result, nil, nil)
	stored, err := ledger.Show(context.Background(), "captured")
	if err != nil {
		t.Fatal(err)
	}
	if stored.HeadSHA != head || stored.BaseSHA != base || stored.Ref != "captured-ref" {
		t.Fatalf("record used a live revision: %+v", stored)
	}
}

func TestReviewRepositoryName_Credentials(t *testing.T) {
	for _, remote := range []string{"https://user:secret@github.com/owner/repo.git", "git@github.com:owner/repo.git", "https://github.com/owner/repo"} {
		if got := reviewRepositoryName(remote); got != "owner/repo" {
			t.Fatalf("got %s", got)
		}
	}
}
