package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/chatcheck"
)

const envBuckleyChatCheckModel = "BUCKLEY_CHAT_CHECK_MODEL"

func runDoctorCommand(args []string) error {
	subCmd := "check"
	if len(args) > 0 {
		subCmd = strings.TrimSpace(args[0])
	}

	switch subCmd {
	case "", "check", "config":
		return runConfigCommand([]string{"check"})
	case "chat":
		return runDoctorChatCommand(args[1:])
	default:
		return fmt.Errorf("unknown doctor command: %s (use check or chat)", subCmd)
	}
}

func runDoctorChatCommand(args []string) error {
	defaultModel := strings.TrimSpace(os.Getenv(envBuckleyChatCheckModel))
	if defaultModel == "" {
		defaultModel = chatcheck.DefaultModel
	}

	fs := flag.NewFlagSet("doctor chat", flag.ContinueOnError)
	modelID := fs.String("model", defaultModel, "model to use for the multi-turn chat check")
	timeout := fs.Duration("timeout", 45*time.Second, "per-turn timeout")
	jsonOutput := fs.Bool("json", false, "print machine-readable JSON report")
	outPath := fs.String("out", "", "write machine-readable JSON report to a file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected doctor chat argument: %s", fs.Arg(0))
	}

	_, mgr, store, err := initDependenciesFn()
	if err != nil {
		return err
	}
	defer store.Close()

	scenario := chatcheck.DefaultScenario(*modelID)
	scenario.Timeout = *timeout

	if *jsonOutput {
		fmt.Fprintf(os.Stderr, "Running chat health check with %s (%d turns)\n", scenario.Model, len(scenario.Turns))
	} else {
		fmt.Printf("Running chat health check with %s (%d turns)\n", scenario.Model, len(scenario.Turns))
	}
	result, runErr := (chatcheck.Runner{Client: mgr}).Run(context.Background(), scenario)
	if *outPath != "" {
		if err := writeChatCheckReport(*outPath, result); err != nil {
			return err
		}
	}
	if *jsonOutput {
		if err := printChatCheckJSON(os.Stdout, result); err != nil {
			return err
		}
	} else {
		printChatCheckResult(os.Stdout, result)
	}
	if runErr != nil {
		return withExitCode(runErr, 1)
	}
	if !*jsonOutput {
		fmt.Println("Chat health check passed")
	}
	return nil
}

func printChatCheckResult(w io.Writer, result *chatcheck.Result) {
	if result == nil {
		return
	}
	for _, turn := range result.Turns {
		status := "ok"
		if strings.TrimSpace(turn.Err) != "" {
			status = "fail"
		}
		fmt.Fprintf(w, "  [%s] turn %d: %s, %d chars", status, turn.Index, turn.Latency.Round(time.Millisecond), turn.CharLength)
		if turn.Model != "" {
			fmt.Fprintf(w, ", model=%s", turn.Model)
		}
		if turn.Finish != "" {
			fmt.Fprintf(w, ", finish=%s", turn.Finish)
		}
		if turn.ToolCalls > 0 {
			fmt.Fprintf(w, ", tool_calls=%d", turn.ToolCalls)
		}
		if turn.Reasoning {
			fmt.Fprint(w, ", reasoning=true")
		}
		if turn.Err != "" {
			fmt.Fprintf(w, ", error=%s", turn.Err)
		}
		fmt.Fprintln(w)
	}
}

func printChatCheckJSON(w io.Writer, result *chatcheck.Result) error {
	if result == nil {
		return nil
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(result)
}

func writeChatCheckReport(path string, result *chatcheck.Result) error {
	path = strings.TrimSpace(path)
	if path == "" || result == nil {
		return nil
	}
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create chat check report directory: %w", err)
		}
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal chat check report: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write chat check report: %w", err)
	}
	return nil
}
