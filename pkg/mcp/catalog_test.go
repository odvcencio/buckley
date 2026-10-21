package mcp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/config"
)

func TestClient_CatalogLimitsAndCompleteness(t *testing.T) {
	for _, offered := range []int{7, 30} {
		cfg := fakeConfig(t, "catalog", "-tools="+strconv.Itoa(offered))
		cfg.MaxTools = 7
		client, err := NewClient(context.Background(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = client.Close() })
		tools, err := client.ListTools(context.Background())
		status := client.CatalogStatus()
		if err != nil || len(tools) != 7 || status.Tools != 7 || status.Pages != 2 || status.Bytes <= 0 || status.Truncated != (offered > 7) {
			t.Fatalf("bad catalog: %+v %v", status, err)
		}
		result, err := client.CallTool(context.Background(), "echo", nil)
		if err != nil || result.IsError {
			t.Fatalf("bounded catalog tool unavailable: %v", err)
		}
		t.Logf("offered=%d retained=%d pages=%d partial=%t tool_call_ok=true", offered, len(tools), status.Pages, status.Truncated)
	}
}

func TestClient_CatalogPaginationFailures(t *testing.T) {
	for _, mode := range []string{"cycle", "empty", "cursor", "duplicate"} {
		t.Run(mode, func(t *testing.T) {
			client, err := NewClient(context.Background(), fakeConfig(t, "catalog", "-tools=10", "-catalog-mode="+mode))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = client.Close() })
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, err = client.ListTools(ctx)
			want := ErrCatalogBudget
			if mode == "cycle" || mode == "duplicate" {
				want = ErrCatalogPagination
			}
			if !errors.Is(err, want) || len(client.Tools()) != 0 {
				t.Fatalf("pagination not rejected cleanly: %v", err)
			}
			t.Logf("mode=%s rejected=true error=%v", mode, err)
		})
	}
}

func TestClient_CatalogByteFailurePreservesSnapshot(t *testing.T) {
	cfg := fakeConfig(t, "catalog", "-tools=12", "-catalog-mode=bytes")
	cfg.MaxMessageBytes = 4096
	client, err := NewClient(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	first, err := client.ListTools(context.Background())
	if err != nil || len(first) != 12 {
		t.Fatalf("initial catalog: %v", err)
	}
	status := client.CatalogStatus()
	if _, err := client.ListTools(context.Background()); !errors.Is(err, ErrCatalogBudget) {
		t.Fatalf("aggregate byte limit ignored: %v", err)
	}
	if client.CatalogStatus() != status || len(client.Tools()) != 12 {
		t.Fatal("failed refresh changed working snapshot")
	}
	if _, err := client.CallTool(context.Background(), "echo", nil); err != nil {
		t.Fatalf("tool call after failed refresh: %v", err)
	}
	t.Log("catalog_byte_limit=4096 failed_refresh_preserved_tools=12 tool_call_ok=true")
}

func TestClient_CatalogDeadline(t *testing.T) {
	client, err := NewClient(context.Background(), fakeConfig(t, "catalog", "-tools=10", "-catalog-mode=slow"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := client.ListTools(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline cause lost: %v", err)
	}
	if len(client.Tools()) != 0 {
		t.Fatal("timed-out discovery published a snapshot")
	}
}

func TestManager_HealthDoesNotRefreshCatalog(t *testing.T) {
	for _, mode := range []string{"modern", "legacy", "legacy-no-ping"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "methods")
			args := []string{"-tools=300", "-call-log=" + path}
			if mode != "modern" {
				args = append(args, "-legacy")
			}
			if mode == "legacy-no-ping" {
				args = append(args, "-catalog-mode=no-ping")
			}
			manager := NewManager()
			cfg := fakeConfig(t, "catalog", args...)
			manager.AddServer(cfg)
			if err := manager.Connect(context.Background()); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = manager.Close() })
			client, _ := manager.GetClient(cfg.Name)
			snapshot := client.CatalogStatus()
			for range 10 {
				if !manager.HealthCheck(context.Background(), time.Second)[cfg.Name] {
					t.Fatal("health probe failed")
				}
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			pages := strings.Count(string(data), "tools/list\n")
			if strings.Contains(string(data), "buckley/catalog/") {
				t.Fatal("private SDK alias leaked onto the wire")
			}
			wantPages := 4
			if mode == "legacy-no-ping" {
				wantPages += 10
			}
			if pages != wantPages || client.CatalogStatus() != snapshot {
				t.Fatalf("health reloaded catalog: pages=%d status=%+v", pages, client.CatalogStatus())
			}
			statuses := manager.ServerStatus()
			if len(statuses) != 1 || !statuses[0].Catalog.Truncated {
				t.Fatal("partial catalog not visible in status")
			}
			t.Logf("mode=%s health_probes=10 list_pages=%d catalog_unchanged=true retained_tools=%d", mode, pages, len(client.Tools()))
		})
	}
}

func TestClient_InvalidToolBudget(t *testing.T) {
	for _, limit := range []int{-1, config.MaxMCPTools + 1} {
		cfg := fakeConfig(t, "normal")
		cfg.MaxTools = limit
		if client, err := NewClient(context.Background(), cfg); err == nil || client != nil {
			t.Fatalf("invalid limit accepted: %d", limit)
		}
	}
}
