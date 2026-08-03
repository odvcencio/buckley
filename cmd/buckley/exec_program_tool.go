package main

import (
	"context"
	"fmt"
	"time"

	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/execmode"
	"m31labs.dev/buckley/pkg/runledger"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

// execProgramTool exposes execmode as one model tool (code-mode design):
// the model writes a complete Go (or Ferrous Wheel) program against the
// typed caps client; the broker below it is read-only, workspace-jailed,
// and audited to the run ledger; and the process itself runs in the
// bubblewrap sandbox (no network, read-only system, no workspace mount).
// Construction fails when bwrap is unavailable — this tool never runs
// unsandboxed (the #145 review finding is a constructor invariant now,
// not a description). Registration stays opt-in per launch
// (goal run --exec-program).
type execProgramTool struct {
	runner   *execmode.Runner
	evidence evidence.Store
	runID    string
}

func languageOrGo(language string) string {
	if language == "" {
		return "go"
	}
	return language
}

func newExecProgramTool(workspaceRoot string, ledger runledger.Store, ev evidence.Store, runID, sessionID string) (*execProgramTool, error) {
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
	if runner.Isolation() != execmode.IsolationBwrap {
		return nil, fmt.Errorf("exec_program requires OS isolation (install bubblewrap); it never runs unsandboxed")
	}
	return &execProgramTool{runner: runner, evidence: ev, runID: runID}, nil
}

func (t *execProgramTool) Name() string { return "exec_program" }

func (t *execProgramTool) Description() string {
	return "Execute a complete Go program (package main) in an OS sandbox against typed workspace capabilities. Import \"execprogram/caps\" for caps.ReadFile(path), caps.ListDir(dir), and caps.SearchText(pattern); compose, filter, and aggregate in code, then print only the result. The sandbox has no network, a read-only system, and no direct workspace mount — the audited caps client is the only window into the workspace, and writes are confined to the program's scratch directory. Standard library only (no module fetching). Set language to \"fw\" to write Ferrous Wheel instead of Go. Prefer one program over long chains of read/search tool calls."
}

func (t *execProgramTool) Parameters() builtin.ParameterSchema {
	return builtin.ParameterSchema{
		Type: "object",
		Properties: map[string]builtin.PropertySchema{
			"source": {
				Type:        "string",
				Description: "Complete source for a package main program",
			},
			"language": {
				Type:        "string",
				Description: "Source dialect",
				Enum:        []string{"go", "fw"},
			},
		},
		Required: []string{"source"},
	}
}

// Execute implements the registry's basic surface.
func (t *execProgramTool) Execute(params map[string]any) (*builtin.Result, error) {
	return t.ExecuteWithContext(context.Background(), params)
}

// ExecuteWithContext runs the program with the caller's deadline. The
// program source is stored as evidence before execution and the run
// output after it, so every program a model ever ran is reconstructable
// from the evidence store alone — full truth without any external
// observability system.
func (t *execProgramTool) ExecuteWithContext(ctx context.Context, params map[string]any) (*builtin.Result, error) {
	source, _ := params["source"].(string)
	language, _ := params["language"].(string)

	programEvidence := ""
	if t.evidence != nil {
		obj, err := t.evidence.Put(ctx, evidence.Object{
			Kind:       evidence.KindSource,
			MediaType:  "text/x-go",
			InlineBody: []byte(source),
			Metadata: map[string]any{
				evidence.MetaRunID: t.runID,
				"surface":          "exec_program",
				"language":         languageOrGo(language),
			},
		})
		if err != nil {
			return nil, fmt.Errorf("exec_program: store program evidence: %w", err)
		}
		programEvidence = obj.ID
	}

	started := time.Now()
	var result execmode.Result
	var err error
	if language == "fw" {
		result, err = t.runner.RunFW(ctx, source)
	} else {
		result, err = t.runner.Run(ctx, source)
	}
	if t.evidence != nil {
		_, _ = t.evidence.Put(ctx, evidence.Object{
			Kind:       evidence.KindToolResult,
			MediaType:  "text/plain",
			InlineBody: []byte(fmt.Sprintf("exit=%d\n--- stdout ---\n%s\n--- stderr ---\n%s", result.ExitCode, result.Stdout, result.Stderr)),
			Metadata: map[string]any{
				evidence.MetaRunID: t.runID,
				"surface":          "exec_program",
				"program_evidence": programEvidence,
			},
		})
	}
	if err != nil {
		return &builtin.Result{
			Success: false,
			Error:   err.Error(),
			Data:    map[string]any{"stderr": result.Stderr, "stdout": result.Stdout},
		}, nil
	}
	data := map[string]any{
		"stdout":           result.Stdout,
		"exit_code":        result.ExitCode,
		"duration_ms":      time.Since(started).Milliseconds(),
		"program_evidence": programEvidence,
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
