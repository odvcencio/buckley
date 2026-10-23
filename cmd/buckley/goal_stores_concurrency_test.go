package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

const goalStoresHelperEnv = "BUCKLEY_TEST_GOAL_STORES_HELPER"

// TestGoalStoresHelperProcess is the child body for
// TestOpenGoalStoresConcurrentProcesses. It does nothing in a normal run.
func TestGoalStoresHelperProcess(t *testing.T) {
	if os.Getenv(goalStoresHelperEnv) != "1" {
		t.Skip("helper process only")
	}
	_, cleanup, err := openGoalStores()
	if err != nil {
		t.Fatalf("openGoalStores: %v", err)
	}
	cleanup()
}

// TestOpenGoalStoresConcurrentProcesses starts several processes at once
// against one fresh ledger database, as parallel `buckley buckbot` runs do.
// Every process must open the stores.
func TestOpenGoalStoresConcurrentProcesses(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns subprocesses")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(t.TempDir(), "data")
	const procs = 32
	var wg sync.WaitGroup
	outs := make([]bytes.Buffer, procs)
	errs := make([]error, procs)
	start := make(chan struct{})
	for i := 0; i < procs; i++ {
		cmd := exec.Command(exe, "-test.run=^TestGoalStoresHelperProcess$", "-test.count=1")
		cmd.Env = append(os.Environ(), goalStoresHelperEnv+"=1", envBuckleyDataDir+"="+dataDir)
		cmd.Stdout, cmd.Stderr = &outs[i], &outs[i]
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs[i] = cmd.Run()
		}()
	}
	close(start)
	wg.Wait()
	for i := range errs {
		if errs[i] != nil {
			t.Errorf("process %d: %v\n%s", i, errs[i], outs[i].String())
		}
	}
}

// TestOpenGoalStoresWhileWriterHoldsLock checks that opening the ledger
// database (evidence store plus run ledger) does not wait for a writer that
// holds the write lock past the 5 s busy timeout.
func TestOpenGoalStoresWhileWriterHoldsLock(t *testing.T) {
	t.Setenv(envBuckleyDataDir, filepath.Join(t.TempDir(), "data"))
	first, cleanup, err := openGoalStores()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	holder, err := first.evidence.DB().Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	if _, err := holder.ExecContext(context.Background(), `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = holder.ExecContext(context.Background(), `ROLLBACK`) }()
	done := make(chan error, 1)
	go func() {
		_, c, err := openGoalStores()
		if err == nil {
			c()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("openGoalStores behind a writer: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("openGoalStores waited on the write lock although schemas are current")
	}
}
