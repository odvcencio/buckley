package execmode

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestBroker_CodeOutline(t *testing.T) {
	root := newWorkspace(t)
	outside := filepath.Join(t.TempDir(), "outside.go")
	if err := os.WriteFile(outside, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape.go")); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, body                               string
		status                                   int
		grant, failAudit, oversized, unavailable bool
	}{
		{name: "valid", body: `{"path":"sub/../greeting.txt","limit":1,"cursor":"opaque"}`, status: 200, grant: true},
		{name: "escape", body: `{"path":"../outside.go"}`, status: 400, grant: true},
		{name: "symlink", body: `{"path":"escape.go"}`, status: 400, grant: true},
		{name: "directory", body: `{"path":"sub"}`, status: 400, grant: true},
		{name: "fraction", body: `{"path":"greeting.txt","limit":1.5}`, status: 400, grant: true},
		{name: "negative", body: `{"path":"greeting.txt","limit":-1}`, status: 400, grant: true},
		{name: "limit", body: `{"path":"greeting.txt","limit":101}`, status: 400, grant: true},
		{name: "cursor", body: `{"path":"greeting.txt","cursor":17}`, status: 400, grant: true},
		{name: "denied", body: `{"path":"greeting.txt"}`, status: 403},
		{name: "unavailable", body: `{"path":"greeting.txt"}`, status: 400, grant: true, unavailable: true},
		{name: "oversized", body: `{"path":"greeting.txt"}`, status: 400, grant: true, oversized: true},
		{name: "audit", body: `{"path":"greeting.txt"}`, status: 500, grant: true, failAudit: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := &recordingSink{fail: tc.failAudit}
			grant := MinimalSet
			if tc.grant {
				grant = ReadOnlySet
			}
			b, err := NewBroker(root, sink, WithCapabilities(grant...))
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			b.outline = func(ctx context.Context, path string, limit int, cursor string) (map[string]any, error) {
				calls++
				if path != "greeting.txt" {
					t.Fatalf("provider path=%q", path)
				}
				if tc.name == "valid" && (limit != 1 || cursor != "opaque") {
					t.Fatalf("lost page params: %d %q", limit, cursor)
				}
				data := map[string]any{"format_version": 1, "coverage": map[string]any{"query_truncated": true}, "next_cursor": "next", "symbols": []any{}}
				if tc.oversized {
					data["large"] = strings.Repeat("x", 16<<10)
				}
				return data, nil
			}
			if tc.unavailable {
				b.outline = nil
			}
			req := httptest.NewRequest(http.MethodPost, "/v1/code/outline", strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer "+b.Token())
			w := httptest.NewRecorder()
			b.handleContext(CapCodeOutline, b.codeOutline)(w, req)
			if w.Code != tc.status {
				t.Fatalf("status=%d body=%s", w.Code, w.Body)
			}
			if tc.name == "valid" {
				var data map[string]any
				if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
					t.Fatal(err)
				}
				if data["coverage"].(map[string]any)["query_truncated"] != true || data["next_cursor"] != "next" {
					t.Fatal("lost warning or cursor")
				}
			}
			wantCalls := 0
			if tc.name == "valid" || tc.oversized || tc.failAudit {
				wantCalls = 1
			}
			if calls != wantCalls {
				t.Fatalf("provider calls=%d want=%d", calls, wantCalls)
			}
			if tc.failAudit {
				if strings.Contains(w.Body.String(), "next_cursor") {
					t.Fatal("unaudited evidence escaped")
				}
				return
			}
			records := sink.byMethod(CapCodeOutline)
			if len(records) != 1 {
				t.Fatalf("audit records=%d", len(records))
			}
			wantOutcome := "error"
			if tc.status == 200 {
				wantOutcome = "ok"
			}
			if tc.status == 403 {
				wantOutcome = "denied"
			}
			if records[0].Outcome != wantOutcome {
				t.Fatalf("audit=%+v", records)
			}
		})
	}
}

func TestBroker_OutlineCancellation(t *testing.T) {
	b, err := NewBroker(newWorkspace(t), &recordingSink{})
	if err != nil {
		t.Fatal(err)
	}
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	var calls atomic.Int64
	b.outline = func(ctx context.Context, _ string, _ int, _ string) (map[string]any, error) {
		calls.Add(1)
		close(entered)
		select {
		case <-release:
			return map[string]any{}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _, err := b.codeOutline(ctx, map[string]any{"path": "greeting.txt"}); done <- err }()
	<-entered
	queued, cancelQueued := context.WithCancel(context.Background())
	cancelQueued()
	if _, err := b.codeOutline(queued, map[string]any{"path": "greeting.txt"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("queued cancellation=%v", err)
	}
	if calls.Load() != 1 {
		t.Fatal("outline provider ran concurrently")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("active cancellation=%v", err)
	}
	close(release)
}
