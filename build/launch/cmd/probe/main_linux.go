//go:build linux

package main

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

func main() {
	if len(os.Args) != 2 {
		os.Exit(64)
	}
	identity, err := directoryIdentity(os.Args[1])
	if err != nil {
		os.Exit(65)
	}
	fmt.Println(identity)
}

func directoryIdentity(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return "", errors.New("probe target is not a directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", errors.New("probe target identity is unavailable")
	}
	return fmt.Sprintf("%d:%d", stat.Dev, stat.Ino), nil
}
