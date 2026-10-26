package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/pprof"
	"time"

	"github.com/odvcencio/gotreesitter/grammars"
)

// This file exists only on the temporary diagnostic branch. The installed
// production executable has no profiling trigger or background collector.
func init() {
	dir := os.Getenv("BUCKLEY_TUI_HEAP_PROBE")
	if os.Getenv("BUCKLEY_TEST_RUN_MAIN") != "1" || dir == "" {
		return
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return
	}
	if err := os.WriteFile(filepath.Join(dir, "ready"), []byte(commit), 0600); err != nil {
		return
	}
	go func() {
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		labels := regexp.MustCompile(`^[a-z][a-z0-9-]{0,47}$`)
		for range ticker.C {
			request, err := os.ReadFile(filepath.Join(dir, "request.json"))
			if err != nil {
				continue
			}
			var command struct {
				Label string `json:"label"`
			}
			if json.Unmarshal(request, &command) != nil || !labels.MatchString(command.Label) {
				continue
			}
			if os.Remove(filepath.Join(dir, "request.json")) != nil {
				continue
			}
			captureTUIHeap(dir, command.Label)
		}
	}()
}

func captureTUIHeap(dir, label string) {
	started := time.Now()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	runtime.GC()
	runtime.ReadMemStats(&after)
	loaded, limit := grammars.EmbeddedLanguageCacheStats()
	profile, err := os.Create(filepath.Join(dir, label+".pprof"))
	if err == nil {
		err = pprof.WriteHeapProfile(profile)
		if closeErr := profile.Close(); err == nil {
			err = closeErr
		}
	}
	errorText := ""
	if err != nil {
		errorText = err.Error()
	}
	report := map[string]any{
		"label": label, "source_commit": commit, "time_utc": started.UTC(),
		"before_gc": before, "after_gc": after, "forced_gc": true,
		"grammar_cache_loaded": loaded, "grammar_cache_limit": limit,
		"goroutines": runtime.NumGoroutine(), "mem_profile_rate": runtime.MemProfileRate,
		"duration_seconds": time.Since(started).Seconds(), "error": errorText,
	}
	data, marshalErr := json.MarshalIndent(report, "", "  ")
	if marshalErr != nil {
		return
	}
	temporary := filepath.Join(dir, label+".json.tmp")
	if os.WriteFile(temporary, append(data, '\n'), 0600) == nil {
		_ = os.Rename(temporary, filepath.Join(dir, label+".json"))
	}
}
