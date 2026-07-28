package builtin

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/reviewpolicy"
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
	timeoutLimit   time.Duration
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
	return &RunVerificationTool{
		snapshotRoot: filepath.Clean(root),
		codexCommand: command,
		verifier:     reviewsandbox.NewSessionExecutorWithCodexCommand(command),
	}, nil
}

// Close removes the private compiler caches for this review session.
func (t *RunVerificationTool) Close() error {
	if t == nil {
		return nil
	}
	if closer, ok := t.verifier.(interface{ Close() error }); ok {
		return closer.Close()
	}
	return nil
}

func (t *RunVerificationTool) SetMaxOutputBytes(max int) {
	if t == nil || max <= 0 {
		return
	}
	t.maxOutputBytes = max
}

// SetTimeoutLimit caps every verification command and changes its default.
func (t *RunVerificationTool) SetTimeoutLimit(limit time.Duration) {
	if t == nil || limit <= 0 {
		return
	}
	t.timeoutLimit = limit
}

func (t *RunVerificationTool) Name() string { return "run_verification" }

func (t *RunVerificationTool) Description() string {
	return "Run a focused build, test, or check in the immutable review snapshot. Repository AGENTS.md rules are enforced before process launch. Requests that require Docker, CI, or another unavailable execution surface return INCONCLUSIVE without running a host command. For Go approval evidence, use kind=test because it compiles the target and executes tests. Source is OS-enforced read-only, temporary build output is private, and network access is disabled."
}

func (t *RunVerificationTool) Parameters() ParameterSchema {
	defaultTimeout := 300
	maxTimeout := 900
	if t != nil && t.timeoutLimit > 0 {
		maxTimeout = max(1, int(t.timeoutLimit/time.Second))
		defaultTimeout = maxTimeout
	}
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"kind": {
				Type:        "string",
				Description: "Verification operation. For Go approval evidence, select test; build compiles but does not execute tests.",
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
				Description: "Optional test-name pattern, at most 4096 bytes; accepted only for kind=test",
			},
			"timeout_seconds": {
				Type:        "integer",
				Description: fmt.Sprintf("Timeout from 1 to %d seconds", maxTimeout),
				Default:     defaultTimeout,
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
	maxTimeout := 900
	timeout := 300
	if t != nil && t.timeoutLimit > 0 {
		maxTimeout = max(1, int(t.timeoutLimit/time.Second))
		timeout = maxTimeout
	}
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
	if timeout < 1 {
		return unavailableVerificationResult(kind, language, fmt.Sprintf("timeout_seconds must be between 1 and %d", maxTimeout)), nil
	}
	if timeout > maxTimeout {
		timeout = maxTimeout
	}
	if t == nil || strings.TrimSpace(t.snapshotRoot) == "" {
		return unavailableVerificationResult(kind, language, "run_verification requires an immutable snapshot-bound registry"), nil
	}
	constraints, policyErr := reviewpolicy.LoadApplicableVerificationConstraints(t.snapshotRoot, path)
	if policyErr != nil {
		return unavailableVerificationResult(kind, language, "repository verification policy is unavailable: "+policyErr.Error()), nil
	}
	if rejection := constraints.HostRejection(kind, language, path); rejection != "" {
		return unavailableVerificationResult(kind, language, rejection), nil
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
		"evidence":    verificationEvidenceClass(verification),
		"proves":      verificationProofKinds(verification),
		"stdout":      verification.Stdout,
		"stderr":      verification.Stderr,
		"error":       verification.Error,
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
			"evidence":  verificationEvidenceClass(verification),
			"proves":    verificationProofKinds(verification),
			"error":     verification.Error,
		}
	}
	return result, nil
}

func verificationProofKinds(verification reviewsandbox.Result) []string {
	if verification.Status != reviewsandbox.StatusPass || verification.ExitCode != 0 {
		return []string{}
	}
	if verification.Language == reviewsandbox.LanguageGo && verification.Kind == reviewsandbox.KindTest {
		return []string{"build", "test"}
	}
	switch verification.Kind {
	case reviewsandbox.KindBuild:
		return []string{"build"}
	case reviewsandbox.KindTest:
		return []string{"test"}
	case reviewsandbox.KindCheck:
		return []string{"check"}
	default:
		return []string{}
	}
}

func verificationEvidenceClass(verification reviewsandbox.Result) string {
	switch {
	case verification.Status == reviewsandbox.StatusPass && verification.ExitCode == 0:
		return "CONFIRMED_PASS"
	case verification.Status == reviewsandbox.StatusFail:
		return "CONFIRMED_FAIL"
	default:
		return "INCONCLUSIVE"
	}
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
			"evidence":  "INCONCLUSIVE",
			"proves":    []string{},
			"error":     reason,
		},
	}
}
