package mcp

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestClient_CatalogRetentionProfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "methods")
	client, err := NewClient(context.Background(), fakeConfig(t, "catalog", "-tools=300", "-call-log="+path))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	tools, err := client.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	initial := len(tools)
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for range 30 {
		if _, err := client.ListTools(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pages := strings.Count(string(data), "tools/list\n")
	if initial > 20 || len(client.Tools()) > 20 || pages > 124 {
		t.Errorf("configured default tool budget not applied: initial=%d cached=%d pages=%d", initial, len(client.Tools()), pages)
	}
	t.Logf("refreshes=31 offered_tools=300 initial_cached=%d final_cached=%d list_pages=%d retained_heap_delta=%d client_alloc_bytes=%d", initial, len(client.Tools()), pages, int64(after.HeapAlloc)-int64(before.HeapAlloc), after.TotalAlloc-before.TotalAlloc)
}
