package execmode

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCompilerTempStorage_Helper(t *testing.T) {
	if os.Getenv("EXECMODE_COMPILER_STORAGE_FIXTURE") != "1" {
		t.Skip("subprocess fixture only")
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs("/tmp", &stat); err != nil {
		t.Fatal(err)
	}
	if stat.Blocks*uint64(stat.Bsize) != 1<<20 {
		t.Fatal("temporary capacity was not set")
	}
	block := make([]byte, 64<<10)
	for i := range block {
		block[i] = 'x'
	}
	var total int
	var writeErr error
	for _, name := range []string{"first", "second"} {
		f, err := os.Create(filepath.Join("/tmp", name))
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 12; i++ {
			n, err := f.Write(block)
			total += n
			if err != nil {
				writeErr = err
				break
			}
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if !errors.Is(writeErr, syscall.ENOSPC) || total != 1<<20 {
		t.Fatalf("written=%d err=%v", total, writeErr)
	}
	for _, name := range []string{"first", "second"} {
		if err := os.Remove(filepath.Join("/tmp", name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile("/tmp/recovered", []byte("ok"), 0600); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("source.txt")
	if err != nil || string(data) != "preserve" {
		t.Fatalf("source=%q err=%v", data, err)
	}
	if err := os.WriteFile("source.txt", []byte("changed"), 0600); !errors.Is(err, syscall.EROFS) {
		t.Fatalf("source write: %v", err)
	}
	fmt.Println("compiler_temp_quota_enforced recovered source_readonly")
}

func TestCompilerTempStorage_QuotaAndReadonlySource(t *testing.T) {
	if DetectIsolation() != IsolationBwrap {
		t.Skip("native sandbox unavailable")
	}
	scratch := t.TempDir()
	if err := os.WriteFile(filepath.Join(scratch, "source.txt"), []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	argv, err := sandboxBaseArgv()
	if err != nil {
		t.Fatal(err)
	}
	argv = append(argv, compilerSandboxFlags(scratch, 1<<20)...)
	argv = append(argv, "--ro-bind", os.Args[0], "/compiler-storage-test", "--", "/compiler-storage-test", "-test.run=^TestCompilerTempStorage_Helper$", "-test.timeout=10s")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + scratch, "EXECMODE_COMPILER_STORAGE_FIXTURE=1"}
	configureProcess(cmd)
	output, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "compiler_temp_quota_enforced recovered source_readonly") {
		t.Fatalf("probe: %v\n%s", err, output)
	}
	if data, err := os.ReadFile(filepath.Join(scratch, "source.txt")); err != nil || string(data) != "preserve" {
		t.Fatalf("host source=%q err=%v", data, err)
	}
}
