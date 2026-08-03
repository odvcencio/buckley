package main

import (
	"context"
	"fmt"
	"time"

	"m31labs.dev/buckley/pkg/execmode"
	"m31labs.dev/buckley/pkg/runledger"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

// execProgramTool exposes execmode as one model tool (code-mode design,
// slice 1): the model writes a complete Go program against the typed
// caps client; the broker below it is read-only, workspace-jailed, and
// audited to the run ledger. Registration is opt-in per launch
// (goal run --exec-program), never default.
//
// Honesty boundary (review finding on #145): only the CAPABILITY PATH is
// jailed and audited. The program process runs as the local user without
// OS sandboxing — the same risk class as run_shell, with a scrubbed
// environment and GOPROXY off as mitigations. OS-level isolation
// (namespaces/bwrap or the container sandbox) is slice 2; until then the
// tool's description says so plainly and the tool stays opt-in.
type execProgramTool struct {
	runner *execmode.Runner
}

func newExecProgramTool(workspaceRoot string, ledger runledger.Store, runID, sessionID string) (*execProgramTool, error) {
	sink := execmode.AuditSinkFunc(func(record execmode.AuditRecord) error {
		_, err := ledger.Append(context.Background(), runledger.Event{
			Type:      "capability.call",
			Timestamp: record.Timestamp,
			SessionID: sessionID,
			RunID:     runID,
			Payload: map[string]any{
				"method":  record.Method,
				"params":  record.Params,
				"outcome": record.Outcome,
				"detail":  record.Detail,
			},
		})
		return err
	})
	runner, err := execmode.NewRunner(workspaceRoot, sink, execmode.DefaultTimeout)
	if err != nil {
		return nil, err
	}
	return &execProgramTool{runner: runner}, nil
}

func (t *execProgramTool) Name() string { return "exec_program" }

func (t *execProgramTool) Description() string {
	return "Execute a complete Go program (package main) against typed workspace capabilities. Import \"execprogram/caps\" for caps.ReadFile(path), caps.ListDir(dir), and caps.SearchText(pattern); compose, filter, and aggregate in code, then print only the result. The caps client is workspace-jailed and every caps call is audited; environment variables are scrubbed and module fetching is off. The program process itself is NOT OS-sandboxed: like run_shell, it runs as the local user with filesystem and network access, so only use standard library and caps calls, and never touch paths outside the workspace. Prefer one program over long chains of read/search tool calls."
}

func (t *execProgramTool) Parameters() builtin.ParameterSchema {
	return builtin.ParameterSchema{
		Type: "object",
		Properties: map[string]builtin.PropertySchema{
			"source": {
				Type:        "string",
				Description: "Complete Go source for a package main program",
			},
		},
		Required: []string{"source"},
	}
}

// Execute implements the registry's basic surface.
func (t *execProgramTool) Execute(params map[string]any) (*builtin.Result, error) {
	return t.ExecuteWithContext(context.Background(), params)
}

// ExecuteWithContext runs the program with the caller's deadline.
func (t *execProgramTool) ExecuteWithContext(ctx context.Context, params map[string]any) (*builtin.Result, error) {
	source, _ := params["source"].(string)
	started := time.Now()
	result, err := t.runner.Run(ctx, source)
	if err != nil {
		return &builtin.Result{
			Success: false,
			Error:   err.Error(),
			Data:    map[string]any{"stderr": result.Stderr, "stdout": result.Stdout},
		}, nil
	}
	data := map[string]any{
		"stdout":      result.Stdout,
		"exit_code":   result.ExitCode,
		"duration_ms": time.Since(started).Milliseconds(),
	}
	if result.Stderr != "" {
		data["stderr"] = result.Stderr
	}
	if result.ExitCode != 0 {
		return &builtin.Result{
			Success: false,
			Error:   fmt.Sprintf("program exited %d", result.ExitCode),
			Data:    data,
		}, nil
	}
	return &builtin.Result{Success: true, Data: data}, nil
}
