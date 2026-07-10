package builtin

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/reviewsandbox"
)

// RunVerificationTool runs one focused build, test, or check against the
// immutable snapshot bound to this registry. It intentionally accepts no raw
// command or arbitrary argv.
type RunVerificationTool struct {
	snapshotRoot   string
	codexCommand   string
	verifier       reviewsandbox.Verifier
	maxOutputBytes int
}

// NewRunVerificationTool seals the tool to one canonical immutable snapshot.
// It is intentionally not registered as a general builtin and implements no
// SetWorkDir method. Review registries must opt in and register it explicitly.
func NewRunVerificationTool(snapshotRoot string, codexCommand ...string) (*RunVerificationTool, error) {
	root, err := filepath.Abs(strings.TrimSpace(snapshotRoot))
	if err != nil || strings.TrimSpace(snapshotRoot) == "" {
		return nil, fmt.Errorf("immutable review snapshot root is required")
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("resolve immutable review snapshot root: %w", err)
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("immutable review snapshot root is not a directory")
	}
	command := ""
	if len(codexCommand) > 0 {
		command = strings.TrimSpace(codexCommand[0])
	}
	return &RunVerificationTool{snapshotRoot: filepath.Clean(root), codexCommand: command}, nil
}

func (t *RunVerificationTool) SetMaxOutputBytes(max int) {
	if t == nil || max <= 0 {
		return
	}
	t.maxOutputBytes = max
}

func (t *RunVerificationTool) Name() string { return "run_verification" }

func (t *RunVerificationTool) Description() string {
	return "Run a focused build, test, or check in the immutable review snapshot. Source is OS-enforced read-only, temporary build output is private, and network access is disabled."
}

func (t *RunVerificationTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"kind": {
				Type:        "string",
				Description: "Verification operation",
				Enum:        []string{"build", "test", "check"},
			},
			"language": {
				Type:        "string",
				Description: "Language/toolchain, or auto-detect from the selected directory",
				Enum:        []string{"auto", "go", "rust", "python", "node"},
				Default:     "auto",
			},
			"path": {
				Type:        "string",
				Description: "Focused directory relative to the immutable snapshot root",
				Default:     ".",
			},
			"pattern": {
				Type:        "string",
				Description: "Optional test-name pattern; accepted only for kind=test",
			},
			"timeout_seconds": {
				Type:        "integer",
				Description: "Timeout from 1 to 900 seconds",
				Default:     300,
			},
		},
		Required: []string{"kind"},
	}
}

func (t *RunVerificationTool) Execute(params map[string]any) (*Result, error) {
	return t.ExecuteWithContext(context.Background(), params)
}

func (t *RunVerificationTool) ExecuteWithContext(ctx context.Context, params map[string]any) (*Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	kind, _ := params["kind"].(string)
	kind = strings.ToLower(strings.TrimSpace(kind))
	language, _ := params["language"].(string)
	language = strings.ToLower(strings.TrimSpace(language))
	if language == "" {
		language = string(reviewsandbox.LanguageAuto)
	}
	path, _ := params["path"].(string)
	pattern, _ := params["pattern"].(string)
	timeout := 300
	if raw, ok := params["timeout_seconds"]; ok {
		switch value := raw.(type) {
		case int:
			timeout = value
		case int32:
			timeout = int(value)
		case int64:
			timeout = int(value)
		case float64:
			if math.Trunc(value) != value {
				return unavailableVerificationResult(kind, language, "timeout_seconds must be an integer"), nil
			}
			timeout = int(value)
		default:
			return unavailableVerificationResult(kind, language, "timeout_seconds must be an integer"), nil
		}
	}
	if timeout < 1 || timeout > 900 {
		return unavailableVerificationResult(kind, language, "timeout_seconds must be between 1 and 900"), nil
	}
	if t == nil || strings.TrimSpace(t.snapshotRoot) == "" {
		return unavailableVerificationResult(kind, language, "run_verification requires an immutable snapshot-bound registry"), nil
	}

	verifier := t.verifier
	if verifier == nil {
		verifier = reviewsandbox.NewExecutorWithCodexCommand(t.codexCommand)
	}
	verification := verifier.Verify(ctx, reviewsandbox.Request{
		SnapshotRoot:   t.snapshotRoot,
		Kind:           reviewsandbox.Kind(kind),
		Language:       reviewsandbox.Language(language),
		Path:           path,
		Pattern:        pattern,
		Timeout:        time.Duration(timeout) * time.Second,
		MaxOutputBytes: t.maxOutputBytes,
	})

	data := map[string]any{
		"kind":        string(verification.Kind),
		"language":    string(verification.Language),
		"path":        verification.Path,
		"pattern":     verification.Pattern,
		"command":     verification.Command,
		"argv":        append([]string(nil), verification.Argv...),
		"exit_code":   verification.ExitCode,
		"status":      string(verification.Status),
		"stdout":      verification.Stdout,
		"stderr":      verification.Stderr,
		"duration_ms": verification.Duration.Milliseconds(),
		"truncated":   verification.Truncated,
	}
	result := &Result{
		Success: verification.Status == reviewsandbox.StatusPass && verification.ExitCode == 0,
		Data:    data,
	}
	if verification.Error != "" {
		result.Error = verification.Error
	}
	if len(verification.Stdout)+len(verification.Stderr) > 8_000 {
		result.ShouldAbridge = true
		result.DisplayData = map[string]any{
			"kind":      string(verification.Kind),
			"language":  string(verification.Language),
			"path":      verification.Path,
			"pattern":   verification.Pattern,
			"command":   verification.Command,
			"argv":      append([]string(nil), verification.Argv...),
			"exit_code": verification.ExitCode,
			"status":    string(verification.Status),
			"error":     verification.Error,
		}
	}
	return result, nil
}

func unavailableVerificationResult(kind, language, reason string) *Result {
	return &Result{
		Success: false,
		Error:   reason,
		Data: map[string]any{
			"kind":      kind,
			"language":  language,
			"path":      "",
			"pattern":   "",
			"command":   "",
			"argv":      []string{},
			"exit_code": -1,
			"status":    string(reviewsandbox.StatusUnavailable),
		},
	}
}
