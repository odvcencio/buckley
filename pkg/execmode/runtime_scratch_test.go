package execmode

import (
	"context"
	"encoding/json"
	"testing"
)

func TestRunner_RuntimeScratchSharesQuotaAndRecovers(t *testing.T) {
	if DetectIsolation() != IsolationBwrap {
		t.Skip("bounded runtime sandbox unavailable")
	}
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	sink := &recordingSink{}
	runner, err := NewRunner(newWorkspace(t), sink, DefaultTimeout)
	if err != nil {
		t.Fatal(err)
	}
	const source = `package main
import("encoding/json";"errors";"os";"os/exec";"path/filepath";"strconv";"strings";"syscall";"execprogram/caps")
func main() {
	block := make([]byte, 64<<10)
	paths := []string{filepath.Join(os.Getenv("HOME"), "home-data"), "/tmp/tmp-data"}
	total := 0; quota := false
	for _, path := range paths {
		f, err := os.Create(path); if err != nil { panic(err) }
		written := 0
		for written < 40<<20 {
			n, err := f.Write(block); written += n; total += n
			if err != nil { quota = quota || errors.Is(err, syscall.ENOSPC); break }
		}
		if err := f.Close(); err != nil { panic(err) }
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(os.Getenv("HOME"), &stat); err != nil { panic(err) }
	for _, path := range paths { if err := os.Remove(path); err != nil { panic(err) } }
	recovered := os.WriteFile("recovered", []byte("ok"), 0600) == nil
	rootDenied := os.WriteFile("/root-escape-probe", block[:1], 0600) != nil
	devDenied := os.WriteFile("/dev/shm/escape-probe", block[:1], 0600) != nil
	_, nsErr := exec.Command("bwrap", "--unshare-user", "--ro-bind", "/", "/", "true").Output()
	_, nsDenied := nsErr.(*exec.ExitError)
	main, err := os.ReadFile("main.go")
	sourceOK := err == nil && strings.Contains(string(main), "package main") && os.WriteFile("main.go", append(main, '\n'), 0644) == nil
	leaked := false
	for fd := 3; fd < 32; fd++ { target, _ := os.Readlink("/proc/self/fd/"+strconv.Itoa(fd)); if strings.Contains(target, "buckley-exec-") { leaked = true } }
	input, _, err := caps.ReadFile("greeting.txt"); if err != nil { panic(err) }
	json.NewEncoder(os.Stdout).Encode(map[string]any{"written":total, "quota":quota, "capacity":stat.Blocks*uint64(stat.Bsize), "recovered":recovered, "root_denied":rootDenied, "dev_denied":devDenied, "namespace_denied":nsDenied, "source_ok":sourceOK, "fd_leak":leaked, "input":input})
}`
	result, err := runner.Run(context.Background(), source)
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("Run: %+v err=%v", result, err)
	}
	var got struct {
		Written         int
		Quota           bool
		Capacity        int64
		Recovered       bool
		RootDenied      bool `json:"root_denied"`
		DevDenied       bool `json:"dev_denied"`
		NamespaceDenied bool `json:"namespace_denied"`
		SourceOK        bool `json:"source_ok"`
		FDLeak          bool `json:"fd_leak"`
		Input           string
	}
	if err := json.Unmarshal([]byte(result.Stdout), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Quota || got.Capacity != DefaultRuntimeScratchBytes || got.Written <= 40<<20 || int64(got.Written) >= DefaultRuntimeScratchBytes {
		t.Fatalf("shared quota not enforced: %+v", got)
	}
	if !got.Recovered || !got.RootDenied || !got.DevDenied || !got.NamespaceDenied || !got.SourceOK || got.FDLeak || got.Input != "hello from the workspace\n" {
		t.Fatalf("runtime isolation/recovery failed: %+v", got)
	}
	if calls := sink.byMethod(CapFilesRead); len(calls) != 1 {
		t.Fatalf("audited %d reads, want 1", len(calls))
	}
}
