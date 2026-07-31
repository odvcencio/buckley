package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// BuildResult is a single `go build` wall-time and output-size measurement.
type BuildResult struct {
	Package     string `json:"package"`
	WallTimeMs  int64  `json:"wall_time_ms"`
	BinaryBytes int64  `json:"binary_bytes"`
	Skipped     bool   `json:"skipped"`
	Error       string `json:"error,omitempty"`
}

// measureBuild runs `go build -o <tmp binary> <pkg>` in dir, timing the
// wall-clock duration and recording the resulting binary size. It always
// builds into a temp file so the benchmark never leaves a binary behind in
// the measured repository.
func measureBuild(dir, pkg string) *BuildResult {
	result := &BuildResult{Package: pkg}

	tmpBinary, err := os.CreateTemp("", "ctxfabric-build-*")
	if err != nil {
		result.Error = err.Error()
		return result
	}
	tmpBinary.Close()
	os.Remove(tmpBinary.Name())
	defer os.Remove(tmpBinary.Name())

	cmd := exec.Command("go", "build", "-o", tmpBinary.Name(), pkg)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	start := time.Now()
	err = cmd.Run()
	elapsed := time.Since(start)

	if err != nil {
		result.Error = fmt.Sprintf("%v: %s", err, stderr.String())
		return result
	}
	result.WallTimeMs = elapsed.Milliseconds()

	st, statErr := os.Stat(tmpBinary.Name())
	if statErr != nil {
		result.Error = statErr.Error()
		return result
	}
	result.BinaryBytes = st.Size()
	return result
}
