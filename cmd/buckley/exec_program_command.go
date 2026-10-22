package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"m31labs.dev/buckley/pkg/execmode"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

func runExecProgramCommand(args []string) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	return runExecProgram(ctx, args, os.Stdout, os.Stderr)
}

func runExecProgram(ctx context.Context, args []string, stdout, stderr io.Writer) (retErr error) {
	fs := flag.NewFlagSet("exec-program", flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("file", "", "Go program file (bounded to 256 KiB)")
	reuse := fs.String("reuse", "", "Stored program_evidence ID to rerun against current workspace files")
	workdir := fs.String("workdir", ".", "Workspace root for read-only capabilities")
	format := fs.String("format", "text", "Output format: text or json (includes evidence IDs)")
	timeout := fs.Duration("timeout", execmode.DefaultTimeout, "Execution deadline, at most 2m")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "Usage: buckley exec-program (--file path.go | --reuse evidence-id) [flags]")
		fmt.Fprintln(stderr, "Runs an audited read-only sandboxed program without a model. Reuse performs fresh reads.")
		fmt.Fprintf(stderr, "Runtime scratch and /tmp share a %d MiB file-data limit.\n", execmode.DefaultRuntimeScratchBytes>>20)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || (*file == "") == (strings.TrimSpace(*reuse) == "") {
		return fmt.Errorf("provide exactly one of --file or --reuse, with no positional arguments")
	}
	if *format != "text" && *format != "json" {
		return fmt.Errorf("format must be text or json")
	}
	if *timeout <= 0 || *timeout > execmode.DefaultTimeout {
		return fmt.Errorf("timeout must be positive and at most %s", execmode.DefaultTimeout)
	}
	params := map[string]any{}
	if *file != "" {
		info, err := os.Stat(*file)
		if err != nil {
			return fmt.Errorf("stat program: %w", err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("program must be a regular file")
		}
		input, err := os.Open(*file)
		if err != nil {
			return fmt.Errorf("open program: %w", err)
		}
		body, err := io.ReadAll(io.LimitReader(input, execmode.MaxSourceBytes+1))
		closeErr := input.Close()
		if err := errors.Join(err, closeErr); err != nil {
			return fmt.Errorf("read program: %w", err)
		}
		if len(body) > execmode.MaxSourceBytes {
			return fmt.Errorf("program exceeds %d source bytes", execmode.MaxSourceBytes)
		}
		if strings.TrimSpace(string(body)) == "" {
			return fmt.Errorf("program source is empty")
		}
		params["source"] = string(body)
	} else {
		params["reuse"] = strings.TrimSpace(*reuse)
	}
	root, err := filepath.Abs(*workdir)
	if err != nil {
		return fmt.Errorf("resolve workspace: %w", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return fmt.Errorf("stat workspace: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("workspace must be a directory")
	}
	cfg, err := loadConfiguredConfig()
	if err != nil {
		return err
	}
	runtime, err := openCodeModeRuntime(root, cfg)
	if err != nil {
		return err
	}
	defer func() {
		if retErr != nil {
			retErr = errors.Join(retErr, runtime.Fail(retErr))
		} else {
			retErr = runtime.Close()
		}
	}()
	sessionID := fmt.Sprintf("program-%d", time.Now().UnixNano())
	program, err := runtime.Tool(sessionID)
	if err != nil {
		return err
	}
	executor, ok := program.(interface {
		ExecuteWithContext(context.Context, map[string]any) (*builtin.Result, error)
	})
	if !ok {
		return fmt.Errorf("program tool cannot enforce caller cancellation")
	}
	runID := runtime.runs[sessionID]
	runCtx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	result, execErr := executor.ExecuteWithContext(runCtx, params)
	if execErr == nil && !result.Success {
		execErr = errors.New(result.Error)
	}
	if execErr != nil {
		execErr = errors.Join(execErr, runtime.Fail(execErr))
	} else {
		execErr = runtime.Close()
	}
	if *format == "json" {
		output := map[string]any{"success": execErr == nil, "run_id": runID}
		if result != nil {
			output["data"] = result.Data
		}
		if execErr != nil {
			output["error"] = execErr.Error()
		}
		return errors.Join(execErr, json.NewEncoder(stdout).Encode(output))
	}
	if result != nil {
		if text, ok := result.Data["stdout"].(string); ok {
			_, err = io.WriteString(stdout, text)
		}
		if text, ok := result.Data["stderr"].(string); ok {
			_, writeErr := io.WriteString(stderr, text)
			err = errors.Join(err, writeErr)
		}
	}
	return errors.Join(execErr, err)
}
