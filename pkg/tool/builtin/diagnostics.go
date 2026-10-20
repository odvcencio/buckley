package builtin

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
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
	diagnostics := postEditDiagnostics(context.Background(), absPath)
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
func postEditDiagnostics(parent context.Context, absPath string) string {
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

	ctx, cancel := context.WithTimeout(parent, postEditDiagnosticsTimeout)
	defer cancel()
	scratch, err := os.MkdirTemp(os.Getenv("GOTMPDIR"), "buckley-diagnostics-")
	if err != nil {
		text := fmt.Sprintf("diagnostics probe unavailable: create scratch directory: %v", err)
		return boundDiagnostics(text, int64(len(text)))
	}
	defer os.RemoveAll(scratch)
	scratch, err = filepath.Abs(scratch)
	if err != nil {
		text := fmt.Sprintf("diagnostics probe unavailable: resolve scratch directory: %v", err)
		return boundDiagnostics(text, int64(len(text)))
	}

	cmd := exec.CommandContext(ctx, "go", "build", "-o", os.DevNull, ".")
	cmd.Dir = packageDir
	cmd.Env = mergeEnv(nil, map[string]string{"GOTMPDIR": scratch, "TMPDIR": scratch, "TMP": scratch, "TEMP": scratch})
	configureCommandCancellation(cmd)
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Cancel()
		}
	}()
	output := newLimitedBuffer(postEditDiagnosticsMaxBytes)
	cmd.Stdout, cmd.Stderr = output, output
	err = cmd.Run()
	if err == nil {
		return ""
	}
	if ctx.Err() != nil {
		return "diagnostics probe canceled: " + ctx.Err().Error()
	}

	text := strings.TrimSpace(output.String())
	total := output.TotalBytes()
	if text == "" {
		text = err.Error()
		total = max(total, int64(len(text)))
	}
	return boundDiagnostics(text, total)
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

func boundDiagnostics(text string, total int64) string {
	text = strings.ToValidUTF8(text, "�")
	if total <= postEditDiagnosticsMaxBytes && len(text) <= postEditDiagnosticsMaxBytes {
		return text
	}
	marker := fmt.Sprintf("\n... (truncated, %d bytes total)", total)
	keep := min(len(text), postEditDiagnosticsMaxBytes-len(marker))
	for keep > 0 && !utf8.ValidString(text[:keep]) {
		keep--
	}
	return text[:keep] + marker
}
