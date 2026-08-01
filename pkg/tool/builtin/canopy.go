package builtin

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
	canopyBinaryName      = "canopy"
	canopyInstallHint     = "canopy CLI not found in PATH; install with: go install m31labs.dev/canopy/cmd/canopy@latest"
	canopyExecTimeout     = 20 * time.Second
	canopyMaxOutputBytes  = 8 * 1024
	canopyDefaultDepth    = 2
	canopyDefaultMaxDepth = 10
)

// canopyRunner runs canopy CLI subcommands against the session workdir and
// bounds their output. Embedded by the code_callgraph, code_refs, and
// code_impact tools.
type canopyRunner struct{ workDirAware }

// run executes `canopy <args...>` with a timeout and returns stdout bounded
// to canopyMaxOutputBytes. A missing canopy binary yields a clear install
// hint instead of a raw "not found" error.
func (r *canopyRunner) run(ctx context.Context, args ...string) (output string, truncated bool, err error) {
	binPath, lookErr := exec.LookPath(canopyBinaryName)
	if lookErr != nil {
		return "", false, errors.New(canopyInstallHint)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, canopyExecTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, binPath, args...)
	if strings.TrimSpace(r.workDir) != "" {
		cmd.Dir = strings.TrimSpace(r.workDir)
	}
	cmd.Env = mergeEnv(cmd.Env, r.env)
	stdout := newLimitedBuffer(canopyMaxOutputBytes)
	stderr := newLimitedBuffer(canopyMaxOutputBytes)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	runErr := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return "", false, fmt.Errorf("canopy command timed out after %s", canopyExecTimeout)
	}
	if runErr != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg == "" {
			msg = runErr.Error()
		}
		return "", false, fmt.Errorf("canopy command failed: %s", msg)
	}

	return strings.TrimSpace(stdout.String()), stdout.Truncated(), nil
}

// scopedPath resolves an optional "path" parameter against the workdir,
// returning a workdir-relative path safe to pass as a canopy CLI argument.
func (r *canopyRunner) scopedPath(params map[string]any) (string, error) {
	raw := strings.TrimSpace(getStringParam(params, "path"))
	if raw == "" {
		return "", nil
	}
	if strings.TrimSpace(r.workDir) == "" {
		return raw, nil
	}
	_, rel, err := resolveRelPath(r.workDir, raw)
	if err != nil {
		return "", err
	}
	return rel, nil
}

// canopyResult wraps bounded canopy output in a Result, matching the
// git_diff convention: only duplicate into DisplayData when truncated.
func canopyResult(symbol, output string, truncated bool) *Result {
	data := map[string]any{
		"symbol": symbol,
		"output": output,
	}
	if truncated {
		data["truncated"] = true
	}
	result := &Result{Success: true, Data: data}
	if truncated {
		result.ShouldAbridge = true
		result.DisplayData = data
	}
	return result
}

// CodeCallgraphTool lists callees (or callers) of a symbol via canopy's
// call graph analysis.
type CodeCallgraphTool struct{ canopyRunner }

func (t *CodeCallgraphTool) Name() string { return "code_callgraph" }

func (t *CodeCallgraphTool) Description() string {
	return "Call graph for a symbol via canopy: callees, or callers with reverse."
}

func (t *CodeCallgraphTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"symbol":  {Type: "string", Description: "Function or method name"},
			"path":    {Type: "string", Description: "Path scope (default: workdir root)"},
			"depth":   {Type: "integer", Description: "Traversal depth (default 2)"},
			"reverse": {Type: "boolean", Description: "Walk callers instead of callees"},
		},
		Required: []string{"symbol"},
	}
}

func (t *CodeCallgraphTool) Execute(params map[string]any) (*Result, error) {
	return t.ExecuteWithContext(context.Background(), params)
}

func (t *CodeCallgraphTool) ExecuteWithContext(ctx context.Context, params map[string]any) (*Result, error) {
	symbol := strings.TrimSpace(getStringParam(params, "symbol"))
	if symbol == "" {
		return &Result{Success: false, Error: "symbol parameter must be a non-empty string"}, nil
	}

	pathArg, err := t.scopedPath(params)
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}

	args := []string{"graph", "calls", symbol}
	if pathArg != "" {
		args = append(args, pathArg)
	}
	depth := getIntParam(params, "depth", canopyDefaultDepth)
	if depth > 0 {
		args = append(args, "--depth", strconv.Itoa(depth))
	}
	if reverse, ok := params["reverse"].(bool); ok && reverse {
		args = append(args, "--reverse")
	}

	output, truncated, err := t.run(ctx, args...)
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}
	return canopyResult(symbol, output, truncated), nil
}

// CodeRefsTool finds indexed references to a symbol via canopy's structural
// search.
type CodeRefsTool struct{ canopyRunner }

func (t *CodeRefsTool) Name() string { return "code_refs" }

func (t *CodeRefsTool) Description() string {
	return "Find references to a symbol via canopy's structural index."
}

func (t *CodeRefsTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"symbol": {Type: "string", Description: "Symbol name to find references for"},
			"path":   {Type: "string", Description: "Path scope (default: workdir root)"},
		},
		Required: []string{"symbol"},
	}
}

func (t *CodeRefsTool) Execute(params map[string]any) (*Result, error) {
	return t.ExecuteWithContext(context.Background(), params)
}

func (t *CodeRefsTool) ExecuteWithContext(ctx context.Context, params map[string]any) (*Result, error) {
	symbol := strings.TrimSpace(getStringParam(params, "symbol"))
	if symbol == "" {
		return &Result{Success: false, Error: "symbol parameter must be a non-empty string"}, nil
	}

	pathArg, err := t.scopedPath(params)
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}

	args := []string{"search", "refs", symbol}
	if pathArg != "" {
		args = append(args, pathArg)
	}

	output, truncated, err := t.run(ctx, args...)
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}
	return canopyResult(symbol, output, truncated), nil
}

// CodeImpactTool computes the blast radius of a changed symbol via canopy's
// reverse call graph.
type CodeImpactTool struct{ canopyRunner }

func (t *CodeImpactTool) Name() string { return "code_impact" }

func (t *CodeImpactTool) Description() string {
	return "Blast radius of a changed symbol via canopy's reverse call graph."
}

func (t *CodeImpactTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"symbol":    {Type: "string", Description: "Changed symbol name"},
			"path":      {Type: "string", Description: "Path scope (default: workdir root)"},
			"max_depth": {Type: "integer", Description: "Max reverse-walk depth (default 10)"},
		},
		Required: []string{"symbol"},
	}
}

func (t *CodeImpactTool) Execute(params map[string]any) (*Result, error) {
	return t.ExecuteWithContext(context.Background(), params)
}

func (t *CodeImpactTool) ExecuteWithContext(ctx context.Context, params map[string]any) (*Result, error) {
	symbol := strings.TrimSpace(getStringParam(params, "symbol"))
	if symbol == "" {
		return &Result{Success: false, Error: "symbol parameter must be a non-empty string"}, nil
	}

	pathArg, err := t.scopedPath(params)
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}

	args := []string{"graph", "impact", symbol}
	if pathArg != "" {
		args = append(args, pathArg)
	}
	maxDepth := getIntParam(params, "max_depth", canopyDefaultMaxDepth)
	if maxDepth > 0 && maxDepth != canopyDefaultMaxDepth {
		args = append(args, "--max-depth", strconv.Itoa(maxDepth))
	}

	output, truncated, err := t.run(ctx, args...)
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}
	return canopyResult(symbol, output, truncated), nil
}
