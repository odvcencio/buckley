package builtin

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunTestsTool_RealJestRequestedPath(t *testing.T) {
	for _, name := range []string{"node", "npm", "jest"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Skip(name + " not installed")
		}
	}
	root := t.TempDir()
	const appPackage = `{"name":"nested-app","private":true,"scripts":{"test":"jest --runInBand"},"jest":{"rootDir":".","testMatch":["**/*.test.js"]}}`
	for path, content := range map[string]string{
		"package.json":                             `{"name":"root-sentinel","private":true,"scripts":{"test":"jest --runInBand"},"jest":{"testMatch":["<rootDir>/root.test.js"]}}`,
		"root.test.js":                             `test('root_only', () => expect(2+2).toBe(4));`,
		"app [x]/package.json":                     appPackage,
		"app [x]/wanted [one]/selected.test.js":    `test('selected', () => expect(2+2).toBe(4));`,
		"app [x]/wanted o/other.test.js":           `test('regex_decoy', () => {throw Error('wrong directory')});`,
		"app [x]/wanted [one]-other/other.test.js": `test('prefix_decoy', () => {throw Error('wrong directory')});`,
		"app [x]/broken/failure.test.js":           `test('requested_failure', () => {throw Error('requested failure')});`,
		"app [x]/outside.test.js":                  `test('outside', () => expect(2+2).toBe(4));`,
	} {
		file := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	tool := &RunTestsTool{}
	tool.SetWorkDir(root)
	for _, tc := range []struct {
		path, pattern, marker string
		passed, failed        int
		success               bool
	}{
		{path: "app [x]", marker: "requested_failure", passed: 2, failed: 3},
		{path: "app [x]/wanted [one]", marker: "selected", passed: 1, success: true},
		{path: "app [x]/broken", marker: "requested_failure", failed: 1},
		{path: filepath.Join(root, "app [x]/wanted [one]"), marker: "selected", passed: 1, success: true},
		{path: "app [x]/wanted [one]/selected.test.js", marker: "selected", passed: 1, success: true},
		{path: "app [x]/wanted [one]", pattern: "no_such_test", marker: "selected"},
		{path: ".", marker: "root_only", passed: 1, success: true},
	} {
		t.Run(tc.path+"/"+tc.pattern, func(t *testing.T) {
			result, err := tool.Execute(map[string]any{"path": tc.path, "pattern": tc.pattern, "coverage": false, "timeout_seconds": float64(60)})
			if err != nil || result == nil || result.Success != tc.success || result.Data["passed"] != tc.passed || result.Data["failed"] != tc.failed {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			output, _ := result.Data["output"].(string)
			if tc.pattern == "" && (!strings.Contains(output, tc.marker) || (tc.path != "." && strings.Contains(output, "root_only"))) {
				t.Fatalf("wrong path output: %s", output)
			}
		})
	}
	t.Run("script broadens selection", func(t *testing.T) {
		content := strings.Replace(appPackage, "jest --runInBand", "jest --runInBand outside.test.js", 1)
		if err := os.WriteFile(filepath.Join(root, "app [x]/package.json"), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		result, err := tool.Execute(map[string]any{"path": "app [x]/wanted [one]", "coverage": false, "timeout_seconds": float64(60)})
		if err != nil || result == nil || result.Success || result.Data["exit_code"] != 0 || !strings.Contains(result.Error, "outside the requested path") {
			t.Fatalf("out-of-scope success accepted: result=%+v err=%v", result, err)
		}
	})
}

func TestDetectTestFrameworkLiteralDirectory(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files []string
		want  string
	}{
		{name: "Go priority", files: []string{"z_test.go", "a.test.js", "test_a.py"}, want: "go"},
		{name: "Jest priority", files: []string{"a.test.js", "test_a.py"}, want: "jest"},
		{name: "pytest fallback", files: []string{"test_a.py"}, want: "pytest"},
		{name: "manifest priority", files: []string{"package.json", "z_test.go"}, want: "jest"},
		{name: "empty", want: "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "literal [path]")
			if err := os.MkdirAll(filepath.Join(dir, "fake_test.go"), 0700); err != nil {
				t.Fatal(err)
			}
			for _, name := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, name), nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if got := (&RunTestsTool{}).detectTestFramework(dir); got != tc.want {
				t.Fatalf("framework=%s want=%s", got, tc.want)
			}
		})
	}
	for _, extension := range []string{".js", ".jsx", ".ts", ".tsx", ".cjs", ".mjs"} {
		t.Run(extension, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "selected"+extension)
			if err := os.WriteFile(path, nil, 0600); err != nil {
				t.Fatal(err)
			}
			if got := (&RunTestsTool{}).detectTestFramework(path); got != "jest" {
				t.Fatalf("framework=%s", got)
			}
		})
	}
}
