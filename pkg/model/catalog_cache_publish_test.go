package model

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func catalogCacheFixture(generation, count int) map[string]ModelInfo {
	catalog := make(map[string]ModelInfo, count)
	for i := range count {
		id := fmt.Sprintf("fixture/model-%04d", i)
		info := ModelInfo{ID: id, Name: fmt.Sprintf("generation-%d", generation),
			Description:   strings.Repeat("Catalog metadata café 日本語. ", 8),
			ContextLength: 100000 + generation, MaxCompletionTokens: 4096,
			Pricing: ModelPricing{Prompt: .125, Completion: .5}, PricingKnown: true,
			SupportedParameters: []string{"tools", "tool_choice", "temperature", "reasoning"}}
		info.markSupportedParametersComplete()
		catalog[id] = info
	}
	return catalog
}

func TestSaveCatalogCache_OpenReaderKeepsSnapshot(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "catalog.json")
	if err := SaveCatalogCache(path, catalogCacheFixture(1, 3)); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	writeErr := SaveCatalogCache(path, catalogCacheFixture(2, 3))
	if writeErr != nil && runtime.GOOS != "windows" {
		t.Fatal(writeErr)
	}
	snapshot, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(snapshot, before) {
		t.Fatal("refresh changed the already-open reader's catalog snapshot")
	}
	loaded, err := LoadCatalogCache(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "generation-2"
	if writeErr != nil {
		want = "generation-1"
	}
	if loaded["fixture/model-0000"].Name != want {
		t.Fatalf("loaded catalog = %+v, want %s", loaded, want)
	}
	leftovers, err := filepath.Glob(filepath.Join(dir, ".model-catalog-*.tmp"))
	if err != nil || len(leftovers) != 0 {
		t.Fatalf("staging files left after publication: %v, err=%v", leftovers, err)
	}
}

func TestSaveCatalogCache_CompactAndDeterministic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.json")
	catalog := catalogCacheFixture(1, 4)
	if err := SaveCatalogCache(path, catalog); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(first, []byte("\n")) {
		t.Fatal("catalog cache contains pretty-print whitespace")
	}
	if err := SaveCatalogCache(path, catalog); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("identical catalog did not produce deterministic bytes")
	}
	loaded, err := LoadCatalogCache(path)
	if err != nil {
		t.Fatal(err)
	}
	for id, info := range loaded {
		if info.Description != catalog[id].Description || info.Pricing.Prompt != .125 || info.Pricing.Completion != .5 || !info.PricingKnown || info.resolveParameterCapability("unsupported") != CapabilityNotAdvertised {
			t.Fatalf("catalog metadata changed: %+v", info)
		}
	}
}

func TestSaveCatalogCache_PreservesPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits")
	}
	for _, mode := range []os.FileMode{0o600, 0o640, 0o444} {
		t.Run(fmt.Sprintf("%o", mode), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "catalog.json")
			if err := SaveCatalogCache(path, catalogCacheFixture(1, 1)); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, mode); err != nil {
				t.Fatal(err)
			}
			err := SaveCatalogCache(path, catalogCacheFixture(2, 1))
			if mode&0o222 == 0 {
				if !errors.Is(err, os.ErrPermission) {
					t.Fatalf("read-only update error = %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != mode {
				t.Fatalf("mode = %o, want %o", info.Mode().Perm(), mode)
			}
		})
	}
}

func TestSaveCatalogCache_FollowsSymlinkChain(t *testing.T) {
	for _, exists := range []bool{false, true} {
		t.Run(fmt.Sprintf("existing_%t", exists), func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, "target.json")
			if exists {
				if err := SaveCatalogCache(target, catalogCacheFixture(1, 1)); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink("target.json", filepath.Join(dir, "second")); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			link := filepath.Join(dir, "catalog.json")
			if err := os.Symlink("second", link); err != nil {
				t.Fatal(err)
			}
			if err := SaveCatalogCache(link, catalogCacheFixture(2, 1)); err != nil {
				t.Fatal(err)
			}
			for name, want := range map[string]string{"catalog.json": "second", "second": "target.json"} {
				got, err := os.Readlink(filepath.Join(dir, name))
				if err != nil || got != want {
					t.Fatalf("link %s changed: %q, %v", name, got, err)
				}
			}
			loaded, err := LoadCatalogCache(target)
			if err != nil || loaded["fixture/model-0000"].Name != "generation-2" {
				t.Fatalf("target catalog = %+v, err=%v", loaded, err)
			}
		})
	}
}

func TestSaveCatalogCache_EncodingFailurePreservesCache(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "catalog.json")
	catalog := catalogCacheFixture(1, 1)
	if err := SaveCatalogCache(path, catalog); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info := catalog["fixture/model-0000"]
	info.Pricing.Prompt = math.NaN()
	catalog[info.ID] = info
	if err := SaveCatalogCache(path, catalog); err == nil {
		t.Fatal("invalid price was encoded")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("failed encode changed cache: %v", err)
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 1 {
		t.Fatalf("failed encode left scratch files: %v, %v", files, err)
	}
}

func TestSaveCatalogCache_LinkUsesPhysicalParent(t *testing.T) {
	dir := t.TempDir()
	physical := filepath.Join(dir, "shared", "inner")
	if err := os.MkdirAll(physical, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("shared/inner", filepath.Join(dir, "alias")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink("../target.json", filepath.Join(physical, "catalog.json")); err != nil {
		t.Fatal(err)
	}
	if err := SaveCatalogCache(filepath.Join(dir, "alias", "catalog.json"), catalogCacheFixture(1, 1)); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadCatalogCache(filepath.Join(dir, "shared", "target.json"))
	if err != nil || len(loaded) != 1 {
		t.Fatalf("physical target: %d models, %v", len(loaded), err)
	}
	if _, err := os.Stat(filepath.Join(dir, "target.json")); !os.IsNotExist(err) {
		t.Fatalf("logical parent target was changed: %v", err)
	}
}

func TestSaveCatalogCache_SymlinkCycleIsBounded(t *testing.T) {
	dir := t.TempDir()
	if err := os.Symlink("second", filepath.Join(dir, "first")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink("first", filepath.Join(dir, "second")); err != nil {
		t.Fatal(err)
	}
	if err := SaveCatalogCache(filepath.Join(dir, "first"), catalogCacheFixture(1, 1)); err == nil {
		t.Fatal("cyclic links accepted")
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 2 {
		t.Fatalf("cyclic link update changed files: %v, %v", files, err)
	}
}

func TestSaveCatalogCache_ConcurrentReadersSeeCompleteGenerations(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "catalog.json")
	if err := SaveCatalogCache(path, catalogCacheFixture(0, 100)); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	var reads atomic.Int64
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				loaded, err := LoadCatalogCache(path)
				if err != nil {
					t.Error(err)
					return
				}
				if len(loaded) != 100 {
					t.Errorf("partial catalog: %d models", len(loaded))
					return
				}
				generation := loaded["fixture/model-0000"].ContextLength
				for _, info := range loaded {
					if info.ContextLength != generation || info.Name != fmt.Sprintf("generation-%d", generation-100000) {
						t.Errorf("mixed generation: %+v", info)
						return
					}
				}
				reads.Add(1)
			}
		}()
	}
	defer wg.Wait()
	defer close(stop)
	for generation := 1; generation <= 32; generation++ {
		if err := SaveCatalogCache(path, catalogCacheFixture(generation, 100)); err != nil && runtime.GOOS != "windows" {
			t.Fatal(err)
		}
	}
	if reads.Load() == 0 {
		t.Fatal("no concurrent cache reads completed")
	}
}

func BenchmarkCatalogCacheIO(b *testing.B) {
	catalog := catalogCacheFixture(1, 1000)
	for _, operation := range []string{"save", "load"} {
		b.Run(operation, func(b *testing.B) {
			path := filepath.Join(b.TempDir(), "catalog.json")
			if err := SaveCatalogCache(path, catalog); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			for b.Loop() {
				if operation == "save" {
					if err := SaveCatalogCache(path, catalog); err != nil {
						b.Fatal(err)
					}
				} else {
					loaded, err := LoadCatalogCache(path)
					if err != nil || len(loaded) != 1000 {
						b.Fatalf("load: %d models, %v", len(loaded), err)
					}
				}
			}
			info, err := os.Stat(path)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportMetric(float64(info.Size()), "B/cache")
		})
	}
}
