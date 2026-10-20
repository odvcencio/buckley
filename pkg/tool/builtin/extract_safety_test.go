package builtin

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const extractionSource = "package fixture\n\nvar Seen int\n\nfunc Apply() {\n\tSeen = 7\n}\n"

func TestExtractFunctionTool_RejectsInvalidSelectorsWithoutWrites(t *testing.T) {
	for _, tc := range []struct {
		name, key string
		value     any
	}{
		{"zero start", "start_line", 0}, {"negative start", "start_line", -1}, {"fractional start", "start_line", 1.5}, {"huge start", "start_line", 1e300}, {"nan", "start_line", math.NaN()}, {"infinity", "end_line", math.Inf(1)}, {"missing end", "end_line", nil}, {"huge json number", "start_line", json.Number("1e300")}, {"end outside file", "end_line", 999}, {"backwards end", "end_line", 1}, {"late insertion", "insert_before_line", 999}, {"overlapping insertion", "insert_before_line", 7}, {"fractional insertion", "insert_before_line", 1.5}, {"null insertion", "insert_before_line", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "source.go")
			if err := os.WriteFile(path, []byte(extractionSource), 0640); err != nil {
				t.Fatal(err)
			}
			tool := &ExtractFunctionTool{}
			tool.SetWorkDir(dir)
			params := map[string]any{"file": "source.go", "start_line": 6, "end_line": 6, "function_name": "Helper"}
			params[tc.key] = tc.value
			result, err := tool.Execute(params)
			if err != nil || result.Success || result.Error == "" {
				t.Fatalf("invalid selector accepted: %+v %v", result, err)
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != extractionSource {
				t.Fatal("invalid selector changed source")
			}
		})
	}
}

func TestExtractFunctionTool_Budgets(t *testing.T) {
	for _, tc := range []struct {
		name, source, fn string
		limit            int64
	}{
		{"input", extractionSource, "Helper", 16},
		{"output", extractionSource, "Helper", int64(len(extractionSource))},
		{"name", extractionSource, strings.Repeat("x", 1024), 64},
		{"python indentation", strings.Repeat(" ", 128) + "print(1)\n" + strings.Repeat("\n", 100), "helper", 512},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			name := "source.go"
			start, end := 6, 6
			if strings.HasPrefix(tc.name, "python") {
				name = "source.py"
				start, end = 1, 101
			}
			path := filepath.Join(dir, name)
			if err := os.WriteFile(path, []byte(tc.source), 0640); err != nil {
				t.Fatal(err)
			}
			tool := &ExtractFunctionTool{}
			tool.SetWorkDir(dir)
			tool.SetMaxFileSizeBytes(tc.limit)
			result, err := tool.Execute(map[string]any{"file": name, "start_line": start, "end_line": end, "function_name": tc.fn})
			if err != nil || result.Success || !strings.Contains(result.Error, "too large") {
				t.Fatalf("budget bypassed: %+v %v", result, err)
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != tc.source {
				t.Fatal("over-budget extraction changed source")
			}
		})
	}
}

func TestExtractFunctionTool_GoRejectsUnsafeSelections(t *testing.T) {
	for _, tc := range []struct {
		name, source, needle, fn string
		insert                   int
	}{
		{"outgoing declaration", "package fixture\nfunc Value()int {\n x := 1\n return x\n}\n", "x := 1", "Helper", 0},
		{"return", "package fixture\nfunc Value()int {\n return 1\n}\n", "return 1", "Helper", 0},
		{"defer", "package fixture\nfunc Value(){\n defer println(1)\n}\n", "defer println", "Helper", 0},
		{"branch", "package fixture\nfunc Value(){for {\n break\n}}\n", "break", "Helper", 0},
		{"recover", "package fixture\nfunc Value(){\n _ = recover()\n}\n", "_ = recover", "Helper", 0},
		{"partial statement", "package fixture\nfunc Value(){\n if true {\n println(1)\n }\n}\n", "if true", "Helper", 0},
		{"name collision", extractionSource, "Seen = 7", "Seen", 0},
		{"documentation insertion", "package fixture\nvar Seen int\n// Apply writes Seen.\nfunc Apply(){\n Seen = 7\n}\n", "Seen = 7", "Helper", 4},
		{"blank name", extractionSource, "Seen = 7", "_", 0},
		{"invalid name", extractionSource, "Seen = 7", "bad name", 0},
		{"top-level capture", "package fixture\nfunc Value()int {\n x := 1\n x += 2\n return x\n}\n", "x += 2", "Helper", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "source.go")
			if err := os.WriteFile(path, []byte(tc.source), 0640); err != nil {
				t.Fatal(err)
			}
			line := strings.Count(tc.source[:strings.Index(tc.source, tc.needle)], "\n") + 1
			params := map[string]any{"file": "source.go", "start_line": line, "end_line": line, "function_name": tc.fn}
			if tc.insert > 0 {
				params["insert_before_line"] = tc.insert
			}
			tool := &ExtractFunctionTool{}
			tool.SetWorkDir(dir)
			result, err := tool.Execute(params)
			if err != nil || result.Success || !strings.Contains(result.Error, "edit_file") {
				t.Fatalf("unsafe selection accepted: %+v %v", result, err)
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != tc.source {
				t.Fatal("rejected selection changed source")
			}
		})
	}
}

func TestExtractFunctionTool_GoCompiledBehavior(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module fixture\n\ngo 1.26.0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for i, tc := range []struct {
		name, source, needle, check string
		lines, insert               int
	}{
		{"capture", "package fixture\nfunc Value()int {\n x := 1\n x += 2\n return x\n}\n", "x += 2", "if Value()!=3 {t.Fatal(\"capture lost\")}", 1, 0},
		{"nested return", "package fixture\nfunc Value()int {\n x := 2\n result := 0\n callback := func()int{return x}\n result = callback()\n return result\n}\n", "callback :=", "if Value()!=2 {t.Fatal(\"callback changed\")}", 2, 0},
		{"self contained", "package fixture\nvar Seen int\nfunc Apply(){\n x := 1\n y := 2\n Seen = x+y\n}\n", "x := 1", "Apply();if Seen!=3 {t.Fatal(\"declarations changed\")}", 3, 0},
		{"top level", extractionSource, "Seen = 7", "Apply();if Seen!=7 {t.Fatal(\"top-level helper changed\")}", 1, 5},
		{"receiver capture", "package fixture\ntype Counter struct{ value int }\nfunc(c *Counter) Value()int {\n c.value += 2\n return c.value\n}\n", "c.value +=", "c:=&Counter{};if c.Value()!=2 {t.Fatal(\"receiver changed\")}", 1, 0},
		{"generic capture", "package fixture\nfunc Value[T ~int](x T) T {\n x += 2\n return x\n}\n", "x +=", "if Value(1)!=3 {t.Fatal(\"generic capture changed\")}", 1, 0},
		{"top level documentation", "package fixture\nvar Seen int\n// Apply writes Seen.\nfunc Apply(){\n Seen = 7\n}\n", "Seen = 7", "Apply();if Seen!=7 {t.Fatal(\"documented helper changed\")}", 1, 3},
		{"crlf", strings.ReplaceAll(extractionSource, "\n", "\r\n"), "Seen = 7", "Apply();if Seen!=7 {t.Fatal(\"CRLF changed\")}", 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(root, fmt.Sprintf("case%d", i))
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "source.go")
			if err := os.WriteFile(path, []byte(tc.source), 0640); err != nil {
				t.Fatal(err)
			}
			line := strings.Count(tc.source[:strings.Index(tc.source, tc.needle)], "\n") + 1
			params := map[string]any{"file": "source.go", "start_line": line, "end_line": line + tc.lines - 1, "function_name": "Helper"}
			if tc.insert > 0 {
				params["insert_before_line"] = tc.insert
			}
			tool := &ExtractFunctionTool{}
			tool.SetWorkDir(dir)
			result, err := tool.Execute(params)
			if err != nil || !result.Success {
				t.Fatalf("valid extraction failed: %+v %v", result, err)
			}
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0640 {
				t.Fatal("permissions changed")
			}
			check := "package fixture\nimport \"testing\"\nfunc TestBehavior(t *testing.T){" + tc.check + "}\n"
			if err := os.WriteFile(filepath.Join(dir, "verify_test.go"), []byte(check), 0600); err != nil {
				t.Fatal(err)
			}
		})
	}
	command := exec.Command("go", "test", "./...")
	command.Dir = root
	command.Env = append(os.Environ(), "GOWORK=off", "GOMAXPROCS=2")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generated Go changed behavior: %v\n%s", err, output)
	}
}

func TestExtractFunctionTool_ScriptBehavior(t *testing.T) {
	for _, tc := range []struct {
		name, binary, filename, source, needle string
		insert                                 int
	}{
		{"python local", "python3", "source.py", "seen = []\ndef run():\n    seen.append(7)\nrun()\nassert seen == [7]\n", "seen.append", 0},
		{"python top level", "python3", "source.py", "seen = []\ndef run():\n    seen.append(7)\nrun()\nassert seen == [7]\n", "seen.append", 2},
		{"javascript capture", "node", "source.js", "function value(){\n let score = 1;\n score += 2;\n return score;\n}\nif(value() !== 3) throw new Error('capture changed');\n", "score +=", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := exec.LookPath(tc.binary); err != nil {
				t.Skipf("%s unavailable", tc.binary)
			}
			dir := t.TempDir()
			path := filepath.Join(dir, tc.filename)
			if err := os.WriteFile(path, []byte(tc.source), 0640); err != nil {
				t.Fatal(err)
			}
			line := strings.Count(tc.source[:strings.Index(tc.source, tc.needle)], "\n") + 1
			params := map[string]any{"file": tc.filename, "start_line": line, "end_line": line, "function_name": "helper"}
			if tc.insert > 0 {
				params["insert_before_line"] = tc.insert
			}
			tool := &ExtractFunctionTool{}
			tool.SetWorkDir(dir)
			result, err := tool.Execute(params)
			if err != nil || !result.Success {
				t.Fatalf("extraction failed: %+v %v", result, err)
			}
			command := exec.Command(tc.binary, path)
			command.Dir = dir
			command.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("generated script changed behavior: %v\n%s", err, output)
			}
		})
	}
}
