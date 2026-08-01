//go:build ignore

// Command hook_logger is a working example Buckley process plugin (ADR
// 0002) that demonstrates the plugin hook contract documented in
// pkg/tool/external/hook_process.go:
//
//   - Normal tool mode (no arguments): reads a single JSON object from
//     stdin, ignores it, and writes {"success":true} to stdout. This
//     keeps hook_logger usable as an ordinary tool.yaml-declared tool,
//     not just a hook subscriber.
//   - Hook mode ("hook" argument): a long-lived process. Reads JSONL
//     messages from stdin; "event" messages are appended to a log file
//     (BUCKLEY_HOOK_LOGGER_LOG, default ./hook_logger.log); "pre_tool"
//     messages naming the marker tool "hook_logger_marker" (override
//     with BUCKLEY_HOOK_LOGGER_VETO_TOOL) get a "deny" response; every
//     other pre_tool request gets "allow".
//
// This file carries a "go:build ignore" tag so `go build ./...` and
// `go vet ./...` skip it (it's meant to be `go run`, never imported); see
// hook_logger.sh.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

type hookMessage struct {
	Kind  string         `json:"kind"`
	ID    string         `json:"id,omitempty"`
	Event map[string]any `json:"event,omitempty"`
	Tool  string         `json:"tool,omitempty"`
	Args  map[string]any `json:"args,omitempty"`
}

type hookResponse struct {
	ID       string `json:"id"`
	Decision string `json:"decision"`
	Reason   string `json:"reason,omitempty"`
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "hook" {
		runHookMode()
		return
	}
	runToolMode()
}

func runToolMode() {
	_, _ = io.ReadAll(os.Stdin)
	fmt.Println(`{"success": true, "data": {"message": "hook_logger: no-op"}}`)
}

func runHookMode() {
	logPath := os.Getenv("BUCKLEY_HOOK_LOGGER_LOG")
	if logPath == "" {
		logPath = "hook_logger.log"
	}
	vetoTool := os.Getenv("BUCKLEY_HOOK_LOGGER_VETO_TOOL")
	if vetoTool == "" {
		vetoTool = "hook_logger_marker"
	}

	logFile, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		fmt.Fprintln(os.Stderr, "hook_logger: opening log file:", err)
		os.Exit(1)
	}
	defer logFile.Close()

	stdout := bufio.NewWriter(os.Stdout)
	defer stdout.Flush()

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var msg hookMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}
		switch msg.Kind {
		case "event":
			logFile.Write(line)
			logFile.Write([]byte("\n"))
		case "pre_tool":
			decision := "allow"
			reason := ""
			if msg.Tool == vetoTool {
				decision = "deny"
				reason = fmt.Sprintf("hook_logger: %s is denied by policy example", msg.Tool)
			}
			resp := hookResponse{ID: msg.ID, Decision: decision, Reason: reason}
			data, err := json.Marshal(resp)
			if err != nil {
				continue
			}
			stdout.Write(data)
			stdout.WriteByte('\n')
			stdout.Flush()
		}
	}
	// Clean shutdown: stdin closed (EOF). Exit 0.
}
