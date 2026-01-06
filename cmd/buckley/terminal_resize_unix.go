//go:build !windows

package main

import (
	"os"
	"os/signal"
	"syscall"
)

func registerTerminalResize(sigCh chan<- os.Signal) {
	if sigCh == nil {
		return
	}
	signal.Notify(sigCh, syscall.SIGWINCH)
}

func unregisterTerminalResize(sigCh chan<- os.Signal) {
	if sigCh == nil {
		return
	}
	signal.Stop(sigCh)
}
