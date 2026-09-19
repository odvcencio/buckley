package tool

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/tool/builtin"
)

func TestGoFailureDiagnosticsReachModel(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("Go is required")
	}
	root := t.TempDir()
	for name, source := range map[string]string{
		"go.mod": "module example.com/model-diagnostics\n\ngo 1.26.0\n",
		"diagnostics_test.go": `package diagnostics
import ("strings"; "testing")
func TestBroken(t *testing.T) { t.Fatal("MODEL_ASSERTION: expected 13, got 12") }
func TestNoise(t *testing.T) { t.Log(strings.Repeat("harmless passing output\n", 300)); t.Log("--- FAIL: PRINTED_NOT_FAILED") }
`,
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
	}
	runner := &builtin.RunTestsTool{}
	runner.SetWorkDir(root)
	result, err := runner.Execute(map[string]any{"path": ".", "timeout_seconds": float64(60)})
	if err != nil || result == nil || result.Success || !result.ShouldAbridge {
		t.Fatalf("unexpected test result: %+v, error: %v", result, err)
	}
	if result.Data["failed"] != 1 || result.Data["passed"] != 1 {
		t.Fatalf("incorrect structured counts: %+v", result.Data)
	}
	for _, useToon := range []bool{false, true} {
		t.Run(strconv.FormatBool(useToon), func(t *testing.T) {
			SetResultEncoding(useToon)
			t.Cleanup(func() { SetResultEncoding(true) })
			encoded, err := ToModelOutput(result)
			if err != nil {
				t.Fatal(err)
			}
			if len(encoded) > DefaultModelOutputBytes || !strings.Contains(encoded, "MODEL_ASSERTION: expected 13, got 12") {
				t.Fatalf("model lost bounded assertion evidence: %s", encoded)
			}
			if !strings.Contains(encoded, "output_tail") || !strings.Contains(encoded, "test command exited with code 1") {
				t.Fatalf("model lost execution evidence: %s", encoded)
			}
		})
	}
}
