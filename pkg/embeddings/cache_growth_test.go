package embeddings_test

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/embeddings"
	"m31labs.dev/buckley/pkg/envdetect"
	"m31labs.dev/buckley/pkg/hunt"
)

func TestCaches_DiskGrowthProfile(t *testing.T) {
	dir := t.TempDir()
	emb := embeddings.NewCache(dir)
	env := envdetect.NewCache(dir)
	hunts := hunt.NewCache(dir)
	vector := make([]float64, 1024)
	for i := range vector {
		vector[i] = 0.12345678901234567
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	for i := 0; i < 2200; i++ {
		if err := emb.Set(fmt.Sprintf("profile-%04d", i), vector); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 300; i++ {
		if err := env.Set(fmt.Sprintf("profile-%04d", i), &envdetect.EnvironmentProfile{CacheKey: strings.Repeat("e", 16<<10)}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 400; i++ {
		if err := hunts.Set(fmt.Sprintf("profile-%04d", i), []hunt.ImprovementSuggestion{{Rationale: strings.Repeat("h", 12<<10)}}); err != nil {
			t.Fatal(err)
		}
	}
	counts := map[string]int{}
	sizes := map[string]int64{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		for _, prefix := range []string{"emb-", "envdetect-", "hunt-"} {
			if strings.HasPrefix(entry.Name(), prefix) && strings.HasSuffix(entry.Name(), ".json") {
				info, err := entry.Info()
				if err != nil {
					t.Fatal(err)
				}
				counts[prefix]++
				sizes[prefix] += info.Size()
			}
		}
	}
	_, embHit := emb.Get("profile-2199")
	_, envHit := env.Get("profile-0299")
	_, huntHit := hunts.Get("profile-0399")
	runtime.ReadMemStats(&after)
	t.Logf("embedding_entries=%d embedding_bytes=%d environment_entries=%d environment_bytes=%d hunt_entries=%d hunt_bytes=%d writes=2900 elapsed_ns=%d newest_hits=%t", counts["emb-"], sizes["emb-"], counts["envdetect-"], sizes["envdetect-"], counts["hunt-"], sizes["hunt-"], time.Since(start).Nanoseconds(), embHit && envHit && huntHit)
	t.Logf("allocated_bytes=%d", after.TotalAlloc-before.TotalAlloc)
	if counts["emb-"] > 2048 || sizes["emb-"] > 32<<20 || counts["envdetect-"] > 128 || sizes["envdetect-"] > 4<<20 || counts["hunt-"] > 256 || sizes["hunt-"] > 8<<20 || !embHit || !envHit || !huntHit {
		t.Error("cache growth unbounded or newest entry lost")
	}
}

func TestCaches_OversizedReadAllocationProfile(t *testing.T) {
	dir := t.TempDir()
	cache := embeddings.NewCache(dir)
	path := filepath.Join(dir, "emb-large.json")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = file.Truncate(8 << 20); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, hit := cache.Get("large")
	runtime.ReadMemStats(&after)
	allocated := after.TotalAlloc - before.TotalAlloc
	t.Logf("oversized_cache_file_bytes=%d allocated_bytes=%d cache_miss=%t", 8<<20, allocated, !hit)
	if hit || allocated > 512<<10 {
		t.Error("oversized cache read was not bounded")
	}
}

func TestCaches_ClearPreservesOtherNamespaces(t *testing.T) {
	dir := t.TempDir()
	emb := embeddings.NewCache(dir)
	env := envdetect.NewCache(dir)
	hunts := hunt.NewCache(dir)
	if err := emb.Set("one", []float64{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	if err := env.Set("one", &envdetect.EnvironmentProfile{CacheKey: "environment"}); err != nil {
		t.Fatal(err)
	}
	if err := hunts.Set("one", []hunt.ImprovementSuggestion{{ID: "suggestion"}}); err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(dir, "unrelated.json")
	if err := os.WriteFile(unrelated, []byte(`{"keep":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := emb.Clear(); err != nil {
		t.Fatal(err)
	}
	_, gone := emb.Get("one")
	_, envHit := env.Get("one")
	_, huntHit := hunts.Get("one")
	raw, err := os.ReadFile(unrelated)
	t.Logf("embedding_removed=%t environment_preserved=%t hunt_preserved=%t unrelated_preserved=%t", !gone, envHit, huntHit, err == nil)
	if gone || !envHit || !huntHit || err != nil || string(raw) != `{"keep":true}` {
		t.Error("embedding clear deleted another namespace")
	}
}
