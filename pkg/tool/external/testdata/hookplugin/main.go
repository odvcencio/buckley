// Command hookplugin is a small, behavior-controllable plugin used only by
// pkg/tool/external's tests to exercise the hook mode CLI/wire contract
// documented in ../../hook_process.go. It is built with `go build` (not
// `go run`) so tests have direct control over the child process for
// crash-simulation and clean-shutdown assertions, matching the
// pkg/mcp/testdata/fakeserver pattern. Not part of the Buckley binary:
// `go build ./...` and `go vet ./...` skip everything under a testdata
// directory by convention.
//
// Behavior is controlled entirely by environment variables (never CLI
// flags), matching the real hook mode contract, which only ever passes
// the fixed "hook" positional argument:
//
//	HOOKPLUGIN_MODE          response behavior for pre_tool requests:
//	                         "normal" (default; allow, or deny
//	                         HOOKPLUGIN_DENY_TOOL), "slow" (sleep
//	                         HOOKPLUGIN_SLOW_MS before responding),
//	                         "silent" (never respond), "malformed" (a
//	                         well-formed response with an unrecognized
//	                         decision value), "malformed_json" (a
//	                         non-JSON stdout line).
//	HOOKPLUGIN_DENY_TOOL     tool name to deny in "normal" mode (default
//	                         "marker_tool").
//	HOOKPLUGIN_SLOW_MS       sleep duration for "slow" mode.
//	HOOKPLUGIN_LOG_FILE      path to append every received "event"
//	                         message's raw JSON line to; empty disables
//	                         logging.
//	HOOKPLUGIN_CRASH         "" (default; never crash), "immediate"
//	                         (exit 1 before reading any input), or
//	                         "after" (exit 1 after reading
//	                         HOOKPLUGIN_CRASH_AFTER messages, without
//	                         responding to the triggering message).
//	HOOKPLUGIN_CRASH_AFTER   message count threshold for
//	                         HOOKPLUGIN_CRASH=after (default 1).
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
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
	Decision string `json:"decision,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

func main() {
	if len(os.Args) < 2 || os.Args[1] != "hook" {
		runToolMode()
		return
	}
	runHookMode()
}

// runToolMode answers a normal (non-hook) one-shot tool invocation, so
// the same binary can also be discovered/loaded as an ordinary
// tool.yaml-declared external tool if a test needs that.
func runToolMode() {
	_, _ = io.ReadAll(os.Stdin)
	fmt.Println(`{"success": true}`)
}

func runHookMode() {
	mode := envOr("HOOKPLUGIN_MODE", "normal")
	denyTool := envOr("HOOKPLUGIN_DENY_TOOL", "marker_tool")
	slowMs := envIntOr("HOOKPLUGIN_SLOW_MS", 0)
	crashWhen := envOr("HOOKPLUGIN_CRASH", "")
	crashAfter := envIntOr("HOOKPLUGIN_CRASH_AFTER", 1)
	logPath := os.Getenv("HOOKPLUGIN_LOG_FILE")

	var logFile *os.File
	if logPath != "" {
		f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			fmt.Fprintln(os.Stderr, "hookplugin: opening log file:", err)
			os.Exit(1)
		}
		defer f.Close()
		logFile = f
	}

	if crashWhen == "immediate" {
		fmt.Fprintln(os.Stderr, "hookplugin: crashing immediately (HOOKPLUGIN_CRASH=immediate)")
		os.Exit(1)
	}

	stdout := bufio.NewWriter(os.Stdout)
	defer stdout.Flush()

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	messageCount := 0
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		messageCount++

		// Crash before processing (and, in particular, before responding
		// to) the triggering message: this exercises a pending veto
		// request that never gets an answer because the process died,
		// distinct from a slow-but-eventual response.
		if crashWhen == "after" && messageCount >= crashAfter {
			fmt.Fprintln(os.Stderr, "hookplugin: crashing before handling message", messageCount)
			os.Exit(1)
		}

		var msg hookMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}

		switch msg.Kind {
		case "event":
			if logFile != nil {
				logFile.Write(line)
				logFile.Write([]byte("\n"))
			}
		case "pre_tool":
			handlePreTool(stdout, mode, denyTool, slowMs, msg)
		}
	}
	// Clean shutdown: stdin closed (EOF). Exit 0.
}

func handlePreTool(stdout *bufio.Writer, mode, denyTool string, slowMs int, msg hookMessage) {
	switch mode {
	case "slow":
		if slowMs > 0 {
			time.Sleep(time.Duration(slowMs) * time.Millisecond)
		}
	case "silent":
		// Never respond; used to exercise the veto middleware's own
		// timeout path.
		return
	case "malformed":
		stdout.WriteString(`{"id":"` + msg.ID + `","decision":"maybe"}` + "\n")
		stdout.Flush()
		return
	case "malformed_json":
		stdout.WriteString("not json at all\n")
		stdout.Flush()
		return
	}

	decision := "allow"
	reason := ""
	if msg.Tool == denyTool {
		decision = "deny"
		reason = "denied by hookplugin test double"
	}

	resp := hookResponse{ID: msg.ID, Decision: decision, Reason: reason}
	data, err := json.Marshal(resp)
	if err != nil {
		return
	}
	stdout.Write(data)
	stdout.WriteByte('\n')
	stdout.Flush()
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envIntOr(key string, def int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}
