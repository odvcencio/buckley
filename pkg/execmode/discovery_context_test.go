package execmode

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Cancel deterministically during work rather than racing a timer against the filesystem.
type discoveryCancelContext struct {
	context.Context
	cancel context.CancelFunc
	checks int
}

func (c *discoveryCancelContext) Err() error {
	c.checks++
	if c.checks == 8 {
		c.cancel()
	}
	return c.Context.Err()
}

func TestBrokerDiscovery_CanceledDuringWork(t *testing.T) {
	for _, tc := range []struct {
		name      string
		method    string
		params    string
		denseFile bool
	}{
		{"search_files", CapSearchText, `{"pattern":"absent"}`, false},
		{"search_dense_lines", CapSearchText, `{"pattern":"absent"}`, true},
		{"recursive_list", CapFilesList, `{"recursive":true}`, false},
		{"recursive_cursor", CapFilesList, `{"recursive":true,"cursor":20}`, false},
		{"flat_list", CapFilesList, `{}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			count, content := 40, "ordinary\n"
			if tc.denseFile {
				count, content = 1, strings.Repeat(content, 10000)
			}
			for i := 0; i < count; i++ {
				if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("%03d.txt", i)), []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			var records []AuditRecord
			broker, err := NewBroker(root, AuditSinkFunc(func(record AuditRecord) error {
				records = append(records, record)
				return nil
			}))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.params))
			request.Header.Set("Authorization", "Bearer "+broker.Token())
			request = request.WithContext(&discoveryCancelContext{Context: ctx, cancel: cancel})
			response := httptest.NewRecorder()
			capability := broker.searchText
			if tc.method == CapFilesList {
				capability = broker.filesList
			}
			broker.handleContext(tc.method, capability)(response, request)
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), context.Canceled.Error()) {
				t.Fatalf("canceled work returned status %d: %s", response.Code, response.Body.String())
			}
			if len(records) != 1 || records[0].Method != tc.method || records[0].Outcome != "error" || records[0].Detail != context.Canceled.Error() {
				t.Fatalf("cancellation audit: %+v", records)
			}
		})
	}
}

func TestBrokerDiscovery_ExpiredContext(t *testing.T) {
	broker, err := NewBroker(t.TempDir(), AuditSinkFunc(func(AuditRecord) error { return nil }))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		call func(context.Context, map[string]any) (any, error)
	}{
		{"search", broker.searchText},
		{"list", broker.filesList},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			defer cancel()
			result, err := tc.call(ctx, map[string]any{"pattern": "needle"})
			if !errors.Is(err, context.DeadlineExceeded) || result != nil {
				t.Fatalf("expired work: result=%v error=%v", result, err)
			}
		})
	}
}
