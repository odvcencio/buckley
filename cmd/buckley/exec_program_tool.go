package main

import (
	"context"
	"fmt"
	"strings"
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
	return "Execute a complete Go program (package main) in an OS sandbox against typed workspace capabilities. " +
		"Compose, filter, and aggregate in code, then print only the result — one program instead of many read/search tool calls. " +
		"Sandbox: no network, read-only system, no workspace mount (the audited caps client is the only window), writes confined to scratch, standard library only. " +
		"On a compile error or a caps error, fix the program and call this tool again in THIS turn; do not end the turn to retry. " +
		"Set language to \"fw\" for Ferrous Wheel. Pass reuse=<evidence-id> to re-run a previous program verbatim with no new source.\n\n" +
		execmode.CapsAPICard
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
			"reuse": {
				Type:        "string",
				Description: "Evidence ID of a previously executed program to re-run verbatim; omit source when set",
			},
		},
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

	// Stabilized mode: re-running a stored program costs zero model
	// tokens. The source comes from the evidence store, so a workflow
	// that already worked never gets re-reasoned.
	if reuse, _ := params["reuse"].(string); strings.TrimSpace(reuse) != "" {
		if t.evidence == nil {
			return nil, fmt.Errorf("exec_program: reuse requires an evidence store")
		}
		obj, err := t.evidence.Get(ctx, strings.TrimSpace(reuse))
		if err != nil {
			return &builtin.Result{Success: false, Error: fmt.Sprintf("exec_program: reuse %s: %v", reuse, err)}, nil
		}
		source = string(obj.InlineBody)
		if lang, ok := obj.Metadata["language"].(string); ok {
			language = lang
		}
		if strings.TrimSpace(source) == "" {
			return &builtin.Result{Success: false, Error: fmt.Sprintf("exec_program: evidence %s holds no program source", reuse)}, nil
		}
	}
	if strings.TrimSpace(source) == "" {
		return &builtin.Result{Success: false, Error: "exec_program: source (or reuse) is required"}, nil
	}

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
