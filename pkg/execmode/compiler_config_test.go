package execmode

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type compilerConfigSink struct {
	mu       sync.Mutex
	tmp      string
	mode     string
	counters []string
	err      error
	calls    int
}

func (s *compilerConfigSink) Record(AuditRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	entries, err := os.ReadDir(s.tmp)
	if err != nil {
		s.err = err
		return nil
	}
	var scratch string
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "buckley-exec-") {
			scratch = filepath.Join(s.tmp, entry.Name())
		}
	}
	if scratch == "" {
		s.err = fmt.Errorf("compiler scratch missing during execution")
		return nil
	}
	cmd := exec.Command("go", "telemetry")
	cmd.Dir = scratch
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + scratch, "GOPROXY=off"}
	output, err := cmd.CombinedOutput()
	if err != nil {
		s.err = fmt.Errorf("query private Go mode: %w: %s", err, output)
		return nil
	}
	s.mode = strings.TrimSpace(string(output))
	dir := filepath.Join(scratch, ".config", "go", "telemetry")
	s.err = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && entry.Name() != "mode" {
			s.counters = append(s.counters, path)
		}
		return nil
	})
	return nil
}

func TestRunner_CompilerDoesNotCollectDisposableCounters(t *testing.T) {
	if DetectIsolation() != IsolationBwrap {
		t.Skip("native sandbox unavailable")
	}
	dir, err := os.MkdirTemp("", "counter-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("TMPDIR", dir)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	sink := &compilerConfigSink{tmp: dir}
	runner, err := NewRunner(newWorkspace(t), sink, DefaultTimeout)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(context.Background(), `package main
import("fmt";"execprogram/caps")
func main(){s,_,err:=caps.ReadFile("greeting.txt");if err!=nil{panic(err)};fmt.Print(s)}`)
	if err != nil || result.ExitCode != 0 || result.Stdout != "hello from the workspace\n" {
		t.Fatalf("Run: %+v err=%v", result, err)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if sink.err != nil || sink.mode != "off" || len(sink.counters) != 0 || sink.calls != 1 {
		t.Fatalf("mode=%q counters=%v calls=%d err=%v", sink.mode, sink.counters, sink.calls, sink.err)
	}
}
