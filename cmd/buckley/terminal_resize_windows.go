//go:build windows

package main

import "os"

func registerTerminalResize(sigCh chan<- os.Signal) {}

func unregisterTerminalResize(sigCh chan<- os.Signal) {}
