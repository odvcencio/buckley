//go:build linux

package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	selfTestToken       = "buckley-launch-supervisor-v1"
	prSetChildSubreaper = 36
	cleanupGrace        = 2 * time.Second
	cleanupKillGrace    = 2 * time.Second
	cleanupFailureExit  = 125
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--self-test" {
		fmt.Println(selfTestToken)
		return
	}
	if len(os.Args) < 3 || os.Args[1] != "--" {
		os.Exit(64)
	}
	os.Exit(supervise(os.Args[2:]))
}

func supervise(argv []string) int {
	if len(argv) == 0 || argv[0] == "" {
		return 64
	}
	if _, _, errno := syscall.Syscall6(syscall.SYS_PRCTL, prSetChildSubreaper, 1, 0, 0, 0, 0); errno != 0 {
		return 70
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	if err := cmd.Start(); err != nil {
		return 126
	}

	signals := make(chan os.Signal, 8)
	signal.Notify(signals, syscall.SIGHUP, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGTERM)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	var waitErr error
	waiting := true
	for waiting {
		select {
		case sig := <-signals:
			if unixSignal, ok := sig.(syscall.Signal); ok {
				_ = syscall.Kill(-cmd.Process.Pid, unixSignal)
			}
		case waitErr = <-done:
			waiting = false
		}
	}
	signal.Stop(signals)
	if !cleanupDescendants(cmd.Process.Pid, cleanupGrace, cleanupKillGrace, descendantPIDs) {
		return cleanupFailureExit
	}
	return exitCode(waitErr)
}

func cleanupDescendants(groupLeader int, grace, killGrace time.Duration, descendants func(int) []int) bool {
	if descendants == nil || grace < 0 || killGrace <= 0 {
		return false
	}
	_ = syscall.Kill(-groupLeader, syscall.SIGTERM)
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		reapChildren()
		children := descendants(os.Getpid())
		if len(children) == 0 {
			return true
		}
		for _, pid := range children {
			_ = syscall.Kill(pid, syscall.SIGTERM)
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = syscall.Kill(-groupLeader, syscall.SIGKILL)
	for _, pid := range descendants(os.Getpid()) {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
	killDeadline := time.Now().Add(killGrace)
	for time.Now().Before(killDeadline) {
		reapChildren()
		if len(descendants(os.Getpid())) == 0 {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	reapChildren()
	return len(descendants(os.Getpid())) == 0
}

func reapChildren() {
	for {
		var status syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &status, syscall.WNOHANG, nil)
		if pid <= 0 || err != nil {
			return
		}
	}
}

func descendantPIDs(parent int) []int {
	seen := map[int]bool{parent: true}
	queue := []int{parent}
	var result []int
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, child := range directChildren(current) {
			if child <= 1 || seen[child] {
				continue
			}
			seen[child] = true
			result = append(result, child)
			queue = append(queue, child)
		}
	}
	return result
}

func directChildren(pid int) []int {
	path := filepath.Join("/proc", strconv.Itoa(pid), "task", strconv.Itoa(pid), "children")
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Split(bufio.ScanWords)
	var children []int
	for scanner.Scan() {
		child, err := strconv.Atoi(strings.TrimSpace(scanner.Text()))
		if err == nil {
			children = append(children, child)
		}
	}
	return children
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return 125
	}
	if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return 128 + int(status.Signal())
	}
	if code := exitErr.ExitCode(); code >= 0 && code <= 255 {
		return code
	}
	return 125
}
