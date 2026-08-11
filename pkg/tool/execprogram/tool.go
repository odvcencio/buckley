package execprogram

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

// ProgramTool exposes the capability-brokered code-mode runner as one model
// tool. Programs run in bubblewrap without a workspace mount; the audited caps
// client is their only view of the repository.
type ProgramTool struct {
	runner    *execmode.Runner
	evidence  evidence.Store
	runID     string
	sessionID string
}

// NewProgramTool constructs an audited, evidence-backed exec_program tool.
// It fails closed when any durability dependency or OS isolation is missing.
func NewProgramTool(workspaceRoot string, ledger runledger.Store, ev evidence.Store, runID, sessionID string, capabilities []string) (*ProgramTool, error) {
	if ledger == nil {
		return nil, fmt.Errorf("exec_program requires a run ledger")
	}
	if ev == nil {
		return nil, fmt.Errorf("exec_program requires an evidence store")
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil, fmt.Errorf("exec_program requires a run id")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("exec_program requires a session id")
	}

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
	if len(capabilities) == 0 {
		capabilities = execmode.ReadOnlySet
	}
	runner, err := execmode.NewRunner(workspaceRoot, sink, execmode.DefaultTimeout,
		execmode.WithCapabilitySet(capabilities...))
	if err != nil {
		return nil, err
	}
	if runner.Isolation() != execmode.IsolationBwrap {
		return nil, fmt.Errorf("exec_program requires OS isolation (install bubblewrap); it never runs unsandboxed")
	}
	return &ProgramTool{
		runner:    runner,
		evidence:  ev,
		runID:     runID,
		sessionID: sessionID,
	}, nil
}

func (t *ProgramTool) Name() string { return "exec_program" }

func (t *ProgramTool) Description() string {
	return "Execute a complete Go program (package main) in an OS sandbox against typed workspace capabilities. " +
		"Compose, filter, and aggregate in code, then print only the result — one program instead of many read/search tool calls. " +
		"Sandbox: no network, read-only system, no workspace mount (the audited caps client is the only window), writes confined to scratch, standard library only. " +
		"On a compile error or a caps error, fix the program and call this tool again in THIS turn; do not end the turn to retry. " +
		"Set language to \"fw\" for Ferrous Wheel. Pass reuse=<evidence-id> to re-run a previous program verbatim with no new source.\n\n" +
		execmode.CapsAPICard
}

func (t *ProgramTool) Parameters() builtin.ParameterSchema {
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
func (t *ProgramTool) Execute(params map[string]any) (*builtin.Result, error) {
	return t.ExecuteWithContext(context.Background(), params)
}

// ExecuteWithContext runs the program with the caller's deadline. Source and
// output evidence are mandatory so every acknowledged execution is replayable.
func (t *ProgramTool) ExecuteWithContext(ctx context.Context, params map[string]any) (*builtin.Result, error) {
	source, _ := params["source"].(string)
	language, _ := params["language"].(string)
	language = strings.ToLower(strings.TrimSpace(language))

	// Stabilized mode re-runs stored source without spending model tokens to
	// reconstruct a workflow that already worked.
	if reuse, _ := params["reuse"].(string); strings.TrimSpace(reuse) != "" {
		obj, err := t.evidence.Get(ctx, strings.TrimSpace(reuse))
		if err != nil {
			return &builtin.Result{Success: false, Error: fmt.Sprintf("exec_program: reuse %s: %v", reuse, err)}, nil
		}
		source = string(obj.InlineBody)
		if lang, ok := obj.Metadata["language"].(string); ok {
			language = strings.ToLower(strings.TrimSpace(lang))
		}
		if strings.TrimSpace(source) == "" {
			return &builtin.Result{Success: false, Error: fmt.Sprintf("exec_program: evidence %s holds no program source", reuse)}, nil
		}
	}
	if strings.TrimSpace(source) == "" {
		return &builtin.Result{Success: false, Error: "exec_program: source (or reuse) is required"}, nil
	}
	if language == "" {
		language = "go"
	}
	if language != "go" && language != "fw" {
		return &builtin.Result{Success: false, Error: fmt.Sprintf("exec_program: unsupported language %q", language)}, nil
	}

	mediaType := "text/x-go"
	if language == "fw" {
		mediaType = "text/x-ferrous-wheel"
	}
	program, err := t.evidence.Put(ctx, evidence.Object{
		Kind:       evidence.KindSource,
		MediaType:  mediaType,
		InlineBody: []byte(source),
		Metadata: map[string]any{
			evidence.MetaRunID:     t.runID,
			evidence.MetaSessionID: t.sessionID,
			"surface":              "exec_program",
			"language":             language,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("exec_program: store program evidence: %w", err)
	}

	started := time.Now()
	var result execmode.Result
	if language == "fw" {
		result, err = t.runner.RunFW(ctx, source)
	} else {
		result, err = t.runner.Run(ctx, source)
	}

	output, evidenceErr := t.evidence.Put(ctx, evidence.Object{
		Kind:       evidence.KindToolResult,
		MediaType:  "text/plain",
		InlineBody: []byte(fmt.Sprintf("exit=%d\n--- stdout ---\n%s\n--- stderr ---\n%s", result.ExitCode, result.Stdout, result.Stderr)),
		Metadata: map[string]any{
			evidence.MetaRunID:     t.runID,
			evidence.MetaSessionID: t.sessionID,
			"surface":              "exec_program",
			"program_evidence":     program.ID,
		},
	})
	if evidenceErr != nil {
		return nil, fmt.Errorf("exec_program: store output evidence: %w", evidenceErr)
	}
	if err != nil {
		return &builtin.Result{
			Success: false,
			Error:   err.Error(),
			Data: map[string]any{
				"stderr":          result.Stderr,
				"stdout":          result.Stdout,
				"output_evidence": output.ID,
			},
		}, nil
	}
	data := map[string]any{
		"stdout":           result.Stdout,
		"exit_code":        result.ExitCode,
		"duration_ms":      time.Since(started).Milliseconds(),
		"program_evidence": program.ID,
		"output_evidence":  output.ID,
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
