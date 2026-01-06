package ipc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/ipc/command"
	"github.com/odvcencio/buckley/pkg/orchestrator"
	"github.com/odvcencio/buckley/pkg/storage"
)

func TestHandleMetricsUnauthorized(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := storage.New(filepath.Join(tmpDir, "buckley.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	server := NewServer(Config{ProjectRoot: tmpDir, RequireToken: true}, store, nil, command.NewGateway(), orchestrator.NewFilePlanStore(filepath.Join(tmpDir, "plans")), &config.Config{}, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	// No authorization header - with RequireToken=true, should return 401
	rr := httptest.NewRecorder()
	server.handleMetrics(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected status %d, got %d", http.StatusUnauthorized, rr.Code)
	}
}

func TestHandleMetricsForbiddenForLowScope(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := storage.New(filepath.Join(tmpDir, "buckley.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	server := NewServer(Config{ProjectRoot: tmpDir}, store, nil, command.NewGateway(), orchestrator.NewFilePlanStore(filepath.Join(tmpDir, "plans")), &config.Config{}, nil, nil)

	// Create a request with a principal that has too low scope
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	// Use a scope lower than viewer - e.g., empty or unknown scope
	ctx := context.WithValue(req.Context(), principalContextKey, &requestPrincipal{
		Name:  "test",
		Scope: "unknown", // This should be lower than viewer
	})
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	server.handleMetrics(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected status %d, got %d: %s", http.StatusForbidden, rr.Code, rr.Body.String())
	}
}

func TestHandleMetricsSuccess(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := storage.New(filepath.Join(tmpDir, "buckley.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	server := NewServer(Config{ProjectRoot: tmpDir}, store, nil, command.NewGateway(), orchestrator.NewFilePlanStore(filepath.Join(tmpDir, "plans")), &config.Config{}, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	ctx := context.WithValue(req.Context(), principalContextKey, &requestPrincipal{
		Name:  "test",
		Scope: storage.TokenScopeViewer,
	})
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	server.handleMetrics(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}

	// The response should contain prometheus metrics
	body := rr.Body.String()
	if body == "" {
		t.Error("expected non-empty metrics response")
	}
}

func TestRefreshTicketGaugeNilStore(t *testing.T) {
	server := &Server{store: nil}
	// Should not panic with nil store
	server.refreshTicketGauge()
}

func TestRefreshAuthSessionGaugeNilStore(t *testing.T) {
	server := &Server{store: nil}
	// Should not panic with nil store
	server.refreshAuthSessionGauge()
}

func TestRefreshTicketGaugeWithStore(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := storage.New(filepath.Join(tmpDir, "buckley.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	server := &Server{store: store}
	// Should not panic and should update the metric
	server.refreshTicketGauge()
}

func TestRefreshAuthSessionGaugeWithStore(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := storage.New(filepath.Join(tmpDir, "buckley.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	server := &Server{store: store}
	// Should not panic and should update the metric
	server.refreshAuthSessionGauge()
}
