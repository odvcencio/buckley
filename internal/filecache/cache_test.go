package filecache

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCache_BudgetsAndLegacyCleanup(t *testing.T) {
	dir := t.TempDir()
	cache := New(dir, "emb-", Limits{Bytes: 512, Entries: 3, EntryBytes: 256})
	for name, data := range map[string][]byte{
		"emb-old.json":      bytes.Repeat([]byte("x"), 200),
		"emb-large.json":    bytes.Repeat([]byte("x"), 300),
		".emb-tmp-orphan":   bytes.Repeat([]byte("x"), 100),
		"hunt-foreign.json": []byte("foreign"),
		"unrelated.json":    []byte("unrelated"),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(filepath.Join(dir, "emb-old.json"), time.Unix(1, 0), time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if err := cache.Set(fmt.Sprintf("new-%02d", i), bytes.Repeat([]byte("n"), 200)); err != nil {
			t.Fatal(err)
		}
		assertBudget(t, dir, "emb-", 512, 3)
	}
	if data, hit := cache.Get("new-09"); !hit || len(data) != 200 {
		t.Fatal("newest entry lost")
	}
	for _, name := range []string{"emb-old.json", "emb-large.json", ".emb-tmp-orphan"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("legacy/orphan not pruned: %s", name)
		}
	}
	for _, name := range []string{"hunt-foreign.json", "unrelated.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatal("foreign file deleted")
		}
	}
	if err := cache.Clear(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"hunt-foreign.json", "unrelated.json", ".emb-lock"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatal("foreign file or stable writer lock deleted")
		}
	}
}

func assertBudget(t *testing.T, dir, prefix string, maxBytes int64, maxEntries int) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var size int64
	count := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), prefix) && strings.HasSuffix(entry.Name(), ".json") {
			info, err := entry.Info()
			if err != nil {
				t.Fatal(err)
			}
			size += info.Size()
			count++
		}
	}
	if size > maxBytes || count > maxEntries {
		t.Fatalf("budget exceeded: entries=%d bytes=%d", count, size)
	}
}

func TestCache_ConstructorBoundsExistingFiles(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 8; i++ {
		path := filepath.Join(dir, fmt.Sprintf("emb-old-%d.json", i))
		if err := os.WriteFile(path, bytes.Repeat([]byte("x"), 100), 0600); err != nil {
			t.Fatal(err)
		}
		stamp := time.Unix(int64(i+1), 0)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	cache := New(dir, "emb-", Limits{Bytes: 250, Entries: 2, EntryBytes: 128})
	assertBudget(t, dir, "emb-", 250, 2)
	for _, key := range []string{"old-6", "old-7"} {
		if _, hit := cache.Get(key); !hit {
			t.Fatal("constructor did not retain newest writes")
		}
	}
}

func TestCache_EmptyDirectoryDisablesWrites(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	cache := New("", "emb-", Limits{Bytes: 512, Entries: 4, EntryBytes: 128})
	if err := cache.Set("key", []byte("optional")); err != nil {
		t.Fatal(err)
	}
	if _, hit := cache.Get("key"); hit {
		t.Fatal("disabled cache hit")
	}
	if err := cache.Clear(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatal("disabled cache wrote into current directory")
	}
}

func TestCache_InvalidKeysAndOversizedReads(t *testing.T) {
	dir := t.TempDir()
	cache := New(dir, "emb-", Limits{Bytes: 1024, Entries: 8, EntryBytes: 128})
	for _, key := range []string{"", "../escape", "a/b", "a\\b", strings.Repeat("a", 129)} {
		if err := cache.Set(key, []byte("x")); err == nil {
			t.Fatalf("invalid key accepted: %q", key)
		}
		if _, hit := cache.Get(key); hit {
			t.Fatal("invalid key hit")
		}
	}
	if err := cache.Set("good", []byte("valid")); err != nil {
		t.Fatal(err)
	}
	if err := cache.Set("good", bytes.Repeat([]byte("x"), 129)); !errors.Is(err, ErrEntryTooLarge) {
		t.Fatal(err)
	}
	if data, hit := cache.Get("good"); !hit || string(data) != "valid" {
		t.Fatal("oversized replacement changed valid cache")
	}
	path := filepath.Join(dir, "emb-large.json")
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), 129), 0600); err != nil {
		t.Fatal(err)
	}
	if _, hit := cache.Get("large"); hit {
		t.Fatal("oversized file accepted")
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "emb-link.json")); err == nil {
		if _, hit := cache.Get("link"); hit {
			t.Fatal("cache followed symlink")
		}
	}
}

func TestCache_MetadataSymlinksPreserveTarget(t *testing.T) {
	for _, name := range []string{".emb-lock", ".emb-generation"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			outside := filepath.Join(t.TempDir(), "outside")
			if err := os.WriteFile(outside, []byte("unchanged"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(dir, name)); err != nil {
				t.Skip(err)
			}
			cache := New(dir, "emb-", Limits{Bytes: 512, Entries: 4, EntryBytes: 128})
			if err := cache.Set("key", []byte("optional")); err == nil {
				t.Fatal("unsafe metadata target accepted")
			}
			if data, err := os.ReadFile(outside); err != nil || string(data) != "unchanged" {
				t.Fatal("cache metadata overwrote symlink target")
			}
		})
	}
}

func TestCache_AtomicReadAndFailedWriteCleanup(t *testing.T) {
	dir := t.TempDir()
	cache := New(dir, "emb-", Limits{Bytes: 4096, Entries: 8, EntryBytes: 2048})
	stop := make(chan struct{})
	readerDone := make(chan error, 1)
	t.Cleanup(func() {
		close(stop)
		if err := <-readerDone; err != nil {
			t.Error(err)
		}
	})
	go func() {
		for {
			select {
			case <-stop:
				readerDone <- nil
				return
			default:
			}
			if data, hit := cache.Get("shared"); hit && !json.Valid(data) {
				readerDone <- fmt.Errorf("partial JSON published")
				return
			}
		}
	}()
	for i := 0; i < 100; i++ {
		if err := cache.Set("shared", []byte(fmt.Sprintf(`{"number":%d,"padding":"%s"}`, i, strings.Repeat("x", i)))); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "emb-conflict.json"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := cache.Set("conflict", []byte("valid")); err == nil {
		t.Fatal("rename over a directory succeeded")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".emb-tmp-") {
			t.Fatal("failed write left temporary file")
		}
	}
}

func TestCache_ContentionAndConcurrentInstances(t *testing.T) {
	dir := t.TempDir()
	cache := New(dir, "emb-", Limits{Bytes: 2048, Entries: 8, EntryBytes: 256})
	lock, err := cache.lock()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Close() })
	other := New(dir, "emb-", cache.limits)
	if err := other.Set("skipped", []byte("optional")); err != nil {
		t.Fatal(err)
	}
	if _, hit := other.Get("skipped"); hit {
		t.Fatal("contended write bypassed process lock")
	}
	if err := other.Clear(); !errors.Is(err, ErrBusy) {
		t.Fatal("Clear hid contention")
	}
	lock.Close()
	var writers sync.WaitGroup
	for i := 0; i < 4; i++ {
		writers.Add(1)
		go func(id int) {
			defer writers.Done()
			instance := New(dir, "emb-", cache.limits)
			for n := 0; n < 40; n++ {
				if err := instance.Set(fmt.Sprintf("%d-%d", id, n), bytes.Repeat([]byte("x"), 128)); err != nil {
					t.Error(err)
				}
			}
		}(i)
	}
	writers.Wait()
	assertBudget(t, dir, "emb-", 2048, 8)
}

func TestCache_WriterProcess(t *testing.T) {
	dir := os.Getenv("BUCKLEY_TEST_FILECACHE_PROCESS")
	if dir == "" {
		t.Skip("subprocess fixture")
	}
	cache := New(dir, "emb-", Limits{Bytes: 2048, Entries: 12, EntryBytes: 256})
	if os.Getenv("BUCKLEY_TEST_FILECACHE_CRASH") == "1" {
		lock, err := cache.lock()
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Close()
		if err := cache.prepare(); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"emb-crashed.json", ".emb-tmp-crashed"} {
			if err := os.WriteFile(filepath.Join(dir, name), bytes.Repeat([]byte("x"), 200), 0600); err != nil {
				t.Fatal(err)
			}
		}
		os.Exit(77)
	}
	for i := 0; i < 120; i++ {
		if err := cache.Set(fmt.Sprintf("%d-%d", os.Getpid(), i), []byte(fmt.Sprintf(`{"value":"%0120d"}`, i))); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCache_PeerMutationAndProcessCrashInvalidateIndex(t *testing.T) {
	dir := t.TempDir()
	limits := Limits{Bytes: 400, Entries: 3, EntryBytes: 256}
	cache := New(dir, "emb-", limits)
	if err := cache.Set("seed", bytes.Repeat([]byte("s"), 200)); err != nil {
		t.Fatal(err)
	}
	peer := New(dir, "emb-", limits)
	if err := peer.Clear(); err != nil {
		t.Fatal(err)
	}
	if err := cache.Set("after-clear", bytes.Repeat([]byte("c"), 200)); err != nil {
		t.Fatal("peer clear left a stale index", err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestCache_WriterProcess$")
	cmd.Env = append(os.Environ(), "BUCKLEY_TEST_FILECACHE_PROCESS="+dir, "BUCKLEY_TEST_FILECACHE_CRASH=1")
	var exit *exec.ExitError
	if err := cmd.Run(); !errors.As(err, &exit) || exit.ExitCode() != 77 {
		t.Fatalf("crash fixture did not reach the mutation: %v", err)
	}
	if err := cache.Set("recovered", bytes.Repeat([]byte("r"), 200)); err != nil {
		t.Fatal(err)
	}
	assertBudget(t, dir, "emb-", 400, 3)
	if _, hit := cache.Get("recovered"); !hit {
		t.Fatal("fresh write failed after process death")
	}
	if _, err := os.Stat(filepath.Join(dir, ".emb-tmp-crashed")); !os.IsNotExist(err) {
		t.Fatal("orphan temporary file survived recovery")
	}
	if len(cache.entries) > limits.Entries || cache.bytes > limits.Bytes {
		t.Fatal("retained index exceeds budget")
	}
}

func TestCache_MultipleProcessesStayWithinBudget(t *testing.T) {
	dir := t.TempDir()
	var processes []*exec.Cmd
	for i := 0; i < 4; i++ {
		cmd := exec.Command(os.Args[0], "-test.run=^TestCache_WriterProcess$")
		cmd.Env = append(os.Environ(), "BUCKLEY_TEST_FILECACHE_PROCESS="+dir)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
		processes = append(processes, cmd)
	}
	for _, cmd := range processes {
		if err := cmd.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	assertBudget(t, dir, "emb-", 2048, 12)
	cache := New(dir, "emb-", Limits{Bytes: 2048, Entries: 12, EntryBytes: 256})
	if err := cache.Set("last", []byte(`{"final":true}`)); err != nil {
		t.Fatal(err)
	}
	if data, hit := cache.Get("last"); !hit || !json.Valid(data) {
		t.Fatal("fresh write or JSON integrity lost")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "emb-") && strings.HasSuffix(entry.Name(), ".json") {
			data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
			if err != nil || !json.Valid(data) {
				t.Fatal("cross-process write published partial JSON")
			}
		}
	}
}
