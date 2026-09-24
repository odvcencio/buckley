package reviewledger

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/orchestrator"
)

type fakeObjects struct {
	mu      sync.Mutex
	data    map[string][]byte
	fail    bool
	creates []string
}

func (f *fakeObjects) Create(_ context.Context, key string, body []byte, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return errors.New("offline")
	}
	if _, ok := f.data[key]; ok {
		return ErrExists
	}
	f.data[key] = append([]byte(nil), body...)
	f.creates = append(f.creates, key)
	return nil
}
func (f *fakeObjects) Read(_ context.Context, key string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, ok := f.data[key]
	if !ok {
		return nil, ErrNotFound
	}
	return data, nil
}
func (f *fakeObjects) List(_ context.Context, prefix string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var keys []string
	for key := range f.data {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	return keys, nil
}
func fixtureRecord() orchestrator.ReviewRecord {
	now := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	return orchestrator.ReviewRecord{SchemaVersion: 1, ReviewID: "review-1", Repository: "owner/repo", PRNumber: 42, Ref: "topic", HeadSHA: strings.Repeat("a", 40), BaseSHA: strings.Repeat("b", 40), Model: "model", StartedAt: now, EndedAt: now.Add(time.Minute), Verdict: "APPROVE", Findings: json.RawMessage("[]"), Verification: []orchestrator.ReviewVerification{}, Evidence: []string{}}
}

func TestManifestKey_Schema(t *testing.T) {
	record := fixtureRecord()
	key, err := ManifestKey(record)
	if err != nil || key != "reviews/owner/repo/42/"+record.HeadSHA+"/review-1.json" {
		t.Fatalf("key=%s err=%v", key, err)
	}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"schema_version", "repository", "pr_number", "base_sha", "head_sha", "model", "started_at", "ended_at", "verdict", "findings", "verification", "evidence_blob_hashes"} {
		if _, ok := fields[name]; !ok {
			t.Errorf("missing %s", name)
		}
	}
	for _, test := range []struct {
		name   string
		change func(*orchestrator.ReviewRecord)
	}{
		{"version", func(r *orchestrator.ReviewRecord) { r.SchemaVersion = 2 }},
		{"repository", func(r *orchestrator.ReviewRecord) { r.Repository = "../repo" }},
		{"id", func(r *orchestrator.ReviewRecord) { r.ReviewID = "../review" }},
		{"time", func(r *orchestrator.ReviewRecord) { r.EndedAt = r.StartedAt.Add(-time.Second) }},
		{"hash", func(r *orchestrator.ReviewRecord) { r.Evidence = []string{"../../secret"} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := fixtureRecord()
			test.change(&r)
			if _, err := ManifestKey(r); err == nil {
				t.Fatal("accepted invalid manifest")
			}
		})
	}
	record.PRNumber = 0
	record.Ref = "feature/review"
	if key, err := ManifestKey(record); err != nil || !strings.Contains(key, "feature%2Freview/") {
		t.Fatalf("branch: %s %v", key, err)
	}
}

func TestLedger_RetryAndImmutableObjects(t *testing.T) {
	ctx := context.Background()
	objects := &fakeObjects{data: map[string][]byte{}, fail: true}
	queue := t.TempDir()
	ledger := New(objects, "bucket", "private/v1", queue)
	record := fixtureRecord()
	body := []byte("verification log")
	hash := Hash(body)
	record.Evidence = []string{hash}
	exit := 7
	record.Verification = []orchestrator.ReviewVerification{{Command: "go test ./...", ExitCode: &exit, LogHash: hash, Status: "FAIL"}}
	if err := ledger.Record(ctx, record, map[string][]byte{hash: body}); err == nil {
		t.Fatal("expected queued failure")
	}
	entries, _ := os.ReadDir(queue)
	if len(entries) != 1 {
		t.Fatalf("pending=%d", len(entries))
	}
	info, _ := entries[0].Info()
	if info.Mode().Perm() != 0600 {
		t.Fatal("queue is not private")
	}
	// A changed destination must not send an old private record to a new bucket.
	other := New(objects, "other", "private/v1", queue)
	objects.fail = false
	if err := other.Retry(ctx); err != nil {
		t.Fatal(err)
	}
	if len(objects.data) != 0 {
		t.Fatal("retargeted pending review")
	}
	if err := ledger.Retry(ctx); err != nil {
		t.Fatal(err)
	}
	entries, _ = os.ReadDir(queue)
	if len(entries) != 0 {
		t.Fatal("queue not drained")
	}
	if len(objects.creates) != 2 || !strings.Contains(objects.creates[0], "blobs/") || !strings.Contains(objects.creates[1], "reviews/") {
		t.Fatalf("upload order=%v", objects.creates)
	}
	if err := ledger.Record(ctx, record, map[string][]byte{hash: body}); err != nil {
		t.Fatal(err)
	}
	if len(objects.data) != 2 {
		t.Fatal("duplicate object")
	}
	record.Verdict = "REJECT"
	if err := ledger.Record(ctx, record, map[string][]byte{hash: body}); err == nil {
		t.Fatal("overwrote manifest")
	}
	got, err := ledger.Show(ctx, record.ReviewID)
	if err != nil || got.Verdict != "APPROVE" {
		t.Fatalf("show=%+v %v", got, err)
	}
	records, err := ledger.List(ctx, "owner/repo", 42)
	if err != nil || len(records) != 1 {
		t.Fatalf("list=%v %v", records, err)
	}
	records, err = ledger.List(ctx, "owner/repo", 43)
	if err != nil || len(records) != 0 {
		t.Fatalf("PR filter=%v %v", records, err)
	}
}

func TestLedger_ConcurrentRetry(t *testing.T) {
	objects := &fakeObjects{data: map[string][]byte{}, fail: true}
	queue := t.TempDir()
	ledger := New(objects, "bucket", "", queue)
	if err := ledger.Record(context.Background(), fixtureRecord(), nil); err == nil {
		t.Fatal("want offline")
	}
	objects.fail = false
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			if err := ledger.Retry(context.Background()); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if len(objects.data) != 1 {
		t.Fatalf("objects=%d", len(objects.data))
	}
}

func TestGCS_NoOverwriteAndPagination(t *testing.T) {
	var writes int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			if r.URL.Query().Get("ifGenerationMatch") != "0" || r.URL.Query().Get("uploadType") != "media" || r.URL.Query().Get("name") != "private/a b.json" {
				t.Errorf("unsafe upload %s", r.URL)
			}
			writes++
			if writes > 1 {
				w.WriteHeader(http.StatusPreconditionFailed)
			} else {
				w.WriteHeader(http.StatusOK)
			}
			return
		}
		if strings.Contains(r.URL.Path, "/o/") {
			_, _ = w.Write([]byte("body"))
			return
		}
		if r.URL.Query().Get("prefix") != "reviews/owner/repo/" {
			t.Errorf("prefix %s", r.URL)
		}
		if r.URL.Query().Get("pageToken") == "" {
			_, _ = w.Write([]byte(`{"items":[{"name":"one"}],"nextPageToken":"two"}`))
		} else {
			_, _ = w.Write([]byte(`{"items":[{"name":"two"}]}`))
		}
	}))
	defer server.Close()
	g := &GCS{client: server.Client(), endpoint: server.URL, bucket: "private-bucket"}
	ctx := context.Background()
	if err := g.Create(ctx, "private/a b.json", []byte("body"), "application/json"); err != nil {
		t.Fatal(err)
	}
	if err := g.Create(ctx, "private/a b.json", []byte("other"), "application/json"); !errors.Is(err, ErrExists) {
		t.Fatal(err)
	}
	keys, err := g.List(ctx, "reviews/owner/repo/")
	if err != nil || len(keys) != 2 {
		t.Fatalf("keys=%v %v", keys, err)
	}
	if body, err := g.Read(ctx, "private/a b.json"); err != nil || string(body) != "body" {
		t.Fatalf("read=%s %v", body, err)
	}
}
