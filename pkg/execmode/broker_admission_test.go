package execmode

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBroker_RequestBudgetIncludesRejectedAttempts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
	}{
		{"valid", http.StatusOK},
		{"bad_token", http.StatusUnauthorized},
		{"expired", http.StatusUnauthorized},
		{"ungranted", http.StatusForbidden},
		{"malformed", http.StatusBadRequest},
		{"wrong_method", http.StatusMethodNotAllowed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := &recordingSink{}
			broker, err := NewBroker(t.TempDir(), sink, WithCapabilityCallLimit(2))
			if err != nil {
				t.Fatal(err)
			}
			if tc.name == "expired" {
				broker.expires = time.Now().Add(-time.Second)
			}
			if tc.name == "ungranted" {
				delete(broker.granted, CapFilesRead)
			}
			var executed atomic.Int32
			server := httptest.NewServer(broker.handle(CapFilesRead, func(map[string]any) (any, error) {
				executed.Add(1)
				return map[string]any{"ok": true}, nil
			}))
			t.Cleanup(server.Close)
			client := server.Client()
			client.Timeout = 3 * time.Second
			t.Cleanup(client.CloseIdleConnections)
			for i := 0; i < 20; i++ {
				body, method, token := `{}`, http.MethodPost, broker.Token()
				if tc.name == "bad_token" {
					token = "invalid"
				}
				if tc.name == "malformed" {
					body = `{`
				}
				if tc.name == "wrong_method" {
					method = http.MethodGet
				}
				req, err := http.NewRequest(method, server.URL, strings.NewReader(body))
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Authorization", "Bearer "+token)
				response, err := client.Do(req)
				if i > 2 {
					if err == nil {
						response.Body.Close()
						t.Fatalf("request %d received an unaudited response", i)
					}
					continue
				}
				if err != nil {
					t.Fatal(err)
				}
				_, readErr := io.Copy(io.Discard, response.Body)
				response.Body.Close()
				want := tc.status
				if i == 2 {
					want = http.StatusTooManyRequests
				}
				if readErr != nil || response.StatusCode != want {
					t.Fatalf("request=%d status=%d want=%d read=%v", i, response.StatusCode, want, readErr)
				}
			}
			records := sink.byMethod(CapFilesRead)
			if len(records) != 3 || records[2].Outcome != "denied" || !strings.Contains(records[2].Detail, "budget exhausted") {
				t.Fatalf("audit records=%+v", records)
			}
			wantExecuted := int32(0)
			if tc.name == "valid" {
				wantExecuted = 2
			}
			if executed.Load() != wantExecuted {
				t.Fatalf("executed=%d want=%d", executed.Load(), wantExecuted)
			}
		})
	}
}

func TestBroker_ConcurrentRejectionsHaveOneTerminalAudit(t *testing.T) {
	sink := &recordingSink{}
	broker, err := NewBroker(t.TempDir(), sink, WithCapabilityCallLimit(8))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(broker.handle(CapFilesRead, func(map[string]any) (any, error) {
		t.Error("unauthorized capability executed")
		return nil, nil
	}))
	t.Cleanup(server.Close)
	client := server.Client()
	client.Timeout = 3 * time.Second
	t.Cleanup(client.CloseIdleConnections)
	var group sync.WaitGroup
	for i := 0; i < 64; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			response, err := client.Post(server.URL, "application/json", strings.NewReader(`{}`))
			if err == nil {
				_, _ = io.Copy(io.Discard, response.Body)
				response.Body.Close()
			}
		}()
	}
	group.Wait()
	records := sink.byMethod(CapFilesRead)
	terminal := 0
	for _, record := range records {
		if strings.Contains(record.Detail, "budget exhausted") {
			terminal++
		}
	}
	if len(records) != 9 || terminal != 1 {
		t.Fatalf("records=%d terminal=%d", len(records), terminal)
	}
}

func TestBroker_DenialAuditFailureFailsClosed(t *testing.T) {
	var attempts atomic.Int32
	sink := AuditSinkFunc(func(AuditRecord) error {
		attempts.Add(1)
		return errors.New("audit unavailable")
	})
	broker, err := NewBroker(t.TempDir(), sink, WithCapabilityCallLimit(1))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(broker.handle(CapFilesRead, func(map[string]any) (any, error) {
		t.Error("unauthorized capability executed")
		return nil, nil
	}))
	t.Cleanup(server.Close)
	client := server.Client()
	t.Cleanup(client.CloseIdleConnections)
	for i := 0; i < 2; i++ {
		response, err := client.Post(server.URL, "application/json", strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || response.StatusCode != http.StatusInternalServerError || !strings.Contains(string(body), "audit unavailable") {
			t.Fatalf("status=%d body=%q err=%v", response.StatusCode, body, err)
		}
	}
	if attempts.Load() != 2 {
		t.Fatalf("audit attempts=%d", attempts.Load())
	}
}
