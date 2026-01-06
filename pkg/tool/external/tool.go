package external

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/odvcencio/buckley/pkg/tool/builtin"
)

// ExternalTool wraps an external executable as a Tool
type ExternalTool struct {
	manifest    *ToolManifest
	executable  string
	workDir     string
	env         map[string]string
	maxExecTime time.Duration
}

// NewTool creates a new external tool from a manifest and executable path
func NewTool(manifest *ToolManifest, executablePath string) *ExternalTool {
	return &ExternalTool{
		manifest:   manifest,
		executable: executablePath,
	}
}

// SetWorkDir configures the working directory for the external tool process.
func (et *ExternalTool) SetWorkDir(dir string) {
	if et == nil {
		return
	}
	et.workDir = dir
}

func (et *ExternalTool) SetEnv(env map[string]string) {
	if et == nil {
		return
	}
	et.env = sanitizeEnvMap(env)
}

func (et *ExternalTool) SetMaxExecTimeSeconds(seconds int32) {
	if et == nil {
		return
	}
	if seconds <= 0 {
		et.maxExecTime = 0
		return
	}
	et.maxExecTime = time.Duration(seconds) * time.Second
}

// Name returns the tool name
func (et *ExternalTool) Name() string {
	return et.manifest.Name
}

// Description returns the tool description
func (et *ExternalTool) Description() string {
	return et.manifest.Description
}

// Parameters returns the parameter schema
func (et *ExternalTool) Parameters() builtin.ParameterSchema {
	// Convert manifest parameters to ParameterSchema
	schema := builtin.ParameterSchema{
		Type:       "object",
		Properties: make(map[string]builtin.PropertySchema),
		Required:   []string{},
	}

	// Parse parameters from manifest
	if params, ok := et.manifest.Parameters["properties"].(map[string]any); ok {
		for name, prop := range params {
			if propMap, ok := prop.(map[string]any); ok {
				propSchema := builtin.PropertySchema{}
				if propType, ok := propMap["type"].(string); ok {
					propSchema.Type = propType
				}
				if desc, ok := propMap["description"].(string); ok {
					propSchema.Description = desc
				}
				if def, ok := propMap["default"]; ok {
					propSchema.Default = def
				}
				schema.Properties[name] = propSchema
			}
		}
	}

	// Extract required fields
	if required, ok := et.manifest.Parameters["required"].([]any); ok {
		for _, req := range required {
			if reqStr, ok := req.(string); ok {
				schema.Required = append(schema.Required, reqStr)
			}
		}
	}

	return schema
}

// Execute runs the external tool with the given parameters
func (et *ExternalTool) Execute(params map[string]any) (*builtin.Result, error) {
	// 1. Serialize params to JSON
	input, err := json.Marshal(params)
	if err != nil {
		return &builtin.Result{
			Success: false,
			Error:   fmt.Sprintf("failed to serialize parameters: %v", err),
		}, nil
	}

	timeout := time.Duration(et.manifest.TimeoutMs) * time.Millisecond
	if et.maxExecTime > 0 && et.maxExecTime < timeout {
		timeout = et.maxExecTime
	}

	// 2. Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// 3. Spawn process
	cmd := exec.CommandContext(ctx, et.executable)
	if et.workDir != "" {
		cmd.Dir = et.workDir
	}
	cmd.Env = mergeEnv(cmd.Env, et.env)
	cmd.Stdin = bytes.NewReader(input)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// 4. Execute and capture output
	err = cmd.Run()
	if err != nil {
		// Check if it was a timeout
		if ctx.Err() == context.DeadlineExceeded {
			return &builtin.Result{
				Success: false,
				Error:   fmt.Sprintf("tool execution timed out after %s", timeout),
			}, nil
		}

		// Include stderr in error
		errorMsg := fmt.Sprintf("tool execution failed: %v", err)
		if stderr.Len() > 0 {
			errorMsg += fmt.Sprintf("\nstderr: %s", stderr.String())
		}

		return &builtin.Result{
			Success: false,
			Error:   errorMsg,
		}, nil
	}

	// 5. Parse result JSON from stdout
	var result builtin.Result
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		// If we can't parse the JSON, return the raw output
		return &builtin.Result{
			Success: false,
			Error:   fmt.Sprintf("failed to parse tool output as JSON: %v\nOutput: %s", err, stdout.String()),
		}, nil
	}

	return &result, nil
}

func sanitizeEnvMap(env map[string]string) map[string]string {
	if len(env) == 0 {
		return nil
	}
	out := make(map[string]string, len(env))
	for k, v := range env {
		key := strings.TrimSpace(k)
		if !isValidEnvKey(key) {
			continue
		}
		out[key] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func isValidEnvKey(key string) bool {
	key = strings.TrimSpace(key)
	if key == "" {
		return false
	}
	for i, r := range key {
		if i == 0 {
			if !(r == '_' || unicode.IsLetter(r)) {
				return false
			}
			continue
		}
		if !(r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)) {
			return false
		}
	}
	return true
}

func mergeEnv(base []string, overrides map[string]string) []string {
	overrides = sanitizeEnvMap(overrides)
	if len(overrides) == 0 {
		return base
	}
	if base == nil {
		base = os.Environ()
	}

	keys := make([]string, 0, len(overrides))
	for k := range overrides {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		base = append(base, fmt.Sprintf("%s=%s", k, overrides[k]))
	}
	return base
}
