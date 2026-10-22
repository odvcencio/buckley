//go:build !windows

package execmode

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestBrokerFIFO_Helper(t *testing.T) {
	root := os.Getenv("EXECMODE_FIFO_ROOT")
	if root == "" {
		t.Skip("subprocess fixture only")
	}
	broker, err := NewBroker(root, &recordingSink{})
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("EXECMODE_FIFO_METHOD") == "read" {
		if _, err := broker.filesRead(map[string]any{"path": "alias.txt"}); err == nil {
			t.Fatal("FIFO read accepted")
		}
	} else if count := globSearchCount(t, broker, "needle", "*.txt"); count != 0 {
		t.Fatalf("FIFO search matches=%d", count)
	}
	fmt.Println("fifo refused without blocking")
}

func TestBroker_FIFORefusedWithoutBlocking(t *testing.T) {
	root := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(root, "target.pipe"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target.pipe", filepath.Join(root, "alias.txt")); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"read", "search"} {
		t.Run(method, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestBrokerFIFO_Helper$", "-test.timeout=10s")
			cmd.Env = append(os.Environ(), "EXECMODE_FIFO_ROOT="+root, "EXECMODE_FIFO_METHOD="+method)
			configureProcess(cmd)
			output, err := cmd.CombinedOutput()
			if err != nil || !strings.Contains(string(output), "fifo refused without blocking") {
				t.Fatalf("FIFO %s blocked or failed: %v\n%s", method, err, output)
			}
		})
	}
}

func TestBroker_ReadsStayConfinedDuringReplacement(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "workspace")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(parent, "outside.data")
	if err := os.WriteFile(outside, []byte("needle OUTSIDE_SENTINEL\n"), 0600); err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(root, "entry.txt")
	if err := os.WriteFile(entry, []byte("needle inside\n"), 0600); err != nil {
		t.Fatal(err)
	}
	broker, err := NewBroker(root, &recordingSink{})
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		replacement := filepath.Join(root, "replacement.tmp")
		for {
			select {
			case <-stop:
				done <- nil
				return
			default:
			}
			if err := os.Symlink(outside, replacement); err != nil {
				done <- err
				return
			}
			if err := os.Rename(replacement, entry); err != nil {
				done <- err
				return
			}
			if err := os.WriteFile(replacement, []byte("needle inside\n"), 0600); err != nil {
				done <- err
				return
			}
			if err := os.Rename(replacement, entry); err != nil {
				done <- err
				return
			}
		}
	}()
	t.Cleanup(func() {
		close(stop)
		if err := <-done; err != nil {
			t.Errorf("replace fixture: %v", err)
		}
	})
	for i := 0; i < 100; i++ {
		result, err := broker.filesRead(map[string]any{"path": "entry.txt"})
		if err == nil && result.(map[string]any)["content"] != "needle inside\n" {
			t.Fatal("direct read escaped workspace")
		}
		result, err = broker.searchText(context.Background(), map[string]any{"pattern": "needle", "glob": "entry.txt"})
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(result)
		if err != nil || strings.Contains(string(encoded), "OUTSIDE_SENTINEL") {
			t.Fatalf("search escaped workspace: %v", err)
		}
	}
}
