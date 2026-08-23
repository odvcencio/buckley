//go:build linux

package main

import (
	"fmt"
	"os"
	"syscall"
	"testing"
)

func TestDirectoryIdentity_ExactDeviceAndInode(t *testing.T) {
	root := t.TempDir()
	info, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	stat := info.Sys().(*syscall.Stat_t)
	want := fmt.Sprintf("%d:%d", stat.Dev, stat.Ino)
	if got, err := directoryIdentity(root); err != nil || got != want {
		t.Fatalf("directoryIdentity = %q, %v; want %q", got, err, want)
	}
	if got, err := directoryIdentity(root + "/missing"); err == nil || got != "" {
		t.Fatalf("missing identity = %q, %v", got, err)
	}
}
