package builtin

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	// postEditDiagnosticsEnv disables the post-edit diagnostics loop when
	// set to "off". Config plumbing for this gate is out of scope; the env
	// var is the foundation until pkg/config grows a proper setting.
	postEditDiagnosticsEnv = "BUCKLEY_EDIT_DIAGNOSTICS"

	postEditDiagnosticsTimeout  = 10 * time.Second
	postEditDiagnosticsMaxBytes = 2 * 1024
)

// attachPostEditDiagnostics runs a fast `go build` probe against the
// package containing absPath after a successful edit_file/write_file
// write, and appends any compile errors to the Result as a bounded
// "diagnostics" field. It is a no-op for non-Go files, when no go.mod
// covers the file, when the go toolchain is unavailable, or when
// BUCKLEY_EDIT_DIAGNOSTICS=off.
func attachPostEditDiagnostics(result *Result, absPath string) {
	if result == nil || !result.Success {
		return
	}
	diagnostics := postEditDiagnostics(absPath)
	if diagnostics == "" {
		return
	}
	if result.Data == nil {
		result.Data = make(map[string]any)
	}
	result.Data["diagnostics"] = diagnostics
	if result.DisplayData == nil {
		result.DisplayData = make(map[string]any)
	}
	result.DisplayData["diagnostics"] = diagnostics
	result.ShouldAbridge = true
}

// postEditDiagnostics returns bounded `go build` failure output for the
// package containing absPath, or "" when the build is clean or the probe
// does not apply.
func postEditDiagnostics(absPath string) string {
	if strings.EqualFold(strings.TrimSpace(os.Getenv(postEditDiagnosticsEnv)), "off") {
		return ""
	}
	if filepath.Ext(absPath) != ".go" {
		return ""
	}
	packageDir := filepath.Dir(absPath)
	if findGoModDir(packageDir) == "" {
		return ""
	}
	if _, err := exec.LookPath("go"); err != nil {
		return ""
	}

	ctx, cancel := context.WithTimeout(context.Background(), postEditDiagnosticsTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "go", "build", ".")
	cmd.Dir = packageDir
	output, err := cmd.CombinedOutput()
	if err == nil {
		return ""
	}
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Sprintf("diagnostics probe timed out after %s", postEditDiagnosticsTimeout)
	}

	text := strings.TrimSpace(string(output))
	if text == "" {
		text = err.Error()
	}
	return boundDiagnostics(text)
}

// findGoModDir walks up from dir looking for the nearest go.mod, returning
// its directory or "" when none is found.
func findGoModDir(dir string) string {
	dir = filepath.Clean(dir)
	for {
		if info, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil && !info.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func boundDiagnostics(text string) string {
	if len(text) <= postEditDiagnosticsMaxBytes {
		return text
	}
	marker := fmt.Sprintf("\n... (truncated, %d bytes total)", len(text))
	keep := postEditDiagnosticsMaxBytes - len(marker)
	if keep < 0 {
		keep = 0
	}
	return text[:keep] + marker
}
