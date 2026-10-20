package builtin

import (
	"crypto/sha256"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestMutationDigest_MatchesSHA256(t *testing.T) {
	for _, size := range []int{0, 1, 63, 64, 65, 4095, 4096, 4097, 180224} {
		const pattern = "\x00\xff界\r\n"
		content := strings.Repeat(pattern, size/len(pattern)+1)[:size]
		if got, want := mutationDigest(content), sha256.Sum256([]byte(content)); got != want {
			t.Fatalf("digest differs for %d bytes: %x != %x", len(content), got, want)
		}
	}
}

func TestReadMutationFile_ImmutableSnapshot(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "f")
	first := strings.Repeat("\x00\xff界\r\n", 1000)
	if err := os.WriteFile(path, []byte(first), 0600); err != nil {
		t.Fatal(err)
	}
	var source workDirAware
	source.SetMaxFileSizeBytes(math.MaxInt64)
	snapshot, err := source.readMutationFile(path)
	if err != nil || string(snapshot) != first {
		t.Fatalf("first snapshot differs: %v", err)
	}
	if err := os.WriteFile(path, []byte("new\n"), 0600); err != nil {
		t.Fatal(err)
	}
	next, err := source.readMutationFile(path)
	if err != nil || string(next) != "new\n" || string(snapshot) != first {
		t.Fatalf("snapshots changed across reads: %v", err)
	}
}

func TestReadMutationFile_ReportedSizeDoesNotBypassLimit(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("requires Linux procfs")
	}
	path := "/proc/version"
	info, err := os.Stat(path)
	if err != nil {
		t.Skipf("procfs unavailable: %v", err)
	}
	if info.Size() != 0 {
		t.Skip("requires a reported size of zero")
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var source workDirAware
	source.SetMaxFileSizeBytes(int64(len(want)))
	got, err := source.readMutationFile(path)
	if err != nil || string(got) != string(want) {
		t.Fatalf("exact-limit read differs: %v", err)
	}
	source.SetMaxFileSizeBytes(1)
	got, err = source.readMutationFile(path)
	if err == nil || !strings.Contains(err.Error(), "too large") || len(got) != 0 {
		t.Fatalf("reported size bypassed read limit: length=%d error=%v", len(got), err)
	}
}

var benchmarkMutationSnapshot string

func BenchmarkReadMutationFile_Snapshot(b *testing.B) {
	for _, size := range []int{0, 64, 8192, 180224} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			path := filepath.Join(b.TempDir(), "f")
			if err := os.WriteFile(path, []byte(strings.Repeat("x", size)), 0600); err != nil {
				b.Fatal(err)
			}
			var source workDirAware
			b.ReportAllocs()
			for b.Loop() {
				data, err := source.readMutationFile(path)
				if err != nil {
					b.Fatal(err)
				}
				benchmarkMutationSnapshot = string(data)
			}
		})
	}
}
