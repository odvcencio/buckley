package builtin

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestFileMutations_LiteralPathsAndApproval(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX whitespace filenames")
	}
	for _, name := range []string{" leading.txt", "trailing.txt ", "\tname.txt\t", "\nname.txt\n", "\u00a0name.txt\u00a0", "   "} {
		t.Run(name, func(t *testing.T) {
			for _, tc := range []struct {
				name string
				tool interface {
					SetWorkDir(string)
					Preview(map[string]any) (*Result, error)
					Execute(map[string]any) (*Result, error)
				}
				params map[string]any
				want   string
			}{
				{"write", &WriteFileTool{}, map[string]any{"content": "new\ntail\n"}, "new\ntail\n"},
				{"edit", &EditFileTool{}, map[string]any{"old_string": "old", "new_string": "new"}, "new\ntail\n"},
				{"insert", &InsertTextTool{}, map[string]any{"line": 1, "text": "intro"}, "intro\nold\ntail\n"},
				{"delete", &DeleteLinesTool{}, map[string]any{"start_line": 1, "end_line": 1}, "tail\n"},
				{"search replace", &SearchReplaceTool{}, map[string]any{"search": "old", "replace": "new"}, "new\ntail\n"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					root := t.TempDir()
					path := filepath.Join(root, name)
					if err := os.WriteFile(path, []byte("old\ntail\n"), 0640); err != nil {
						t.Fatal(err)
					}
					trimmed := strings.TrimSpace(name)
					if trimmed != "" {
						if err := os.WriteFile(filepath.Join(root, trimmed), []byte("neighbor\n"), 0600); err != nil {
							t.Fatal(err)
						}
					}
					reader := &ReadFileTool{}
					reader.SetWorkDir(root)
					r, err := reader.Execute(map[string]any{"path": name})
					if err != nil || !r.Success || r.Data["content"] != "old\ntail\n" || r.Data["path"] != path {
						t.Fatalf("literal read: %+v %v", r, err)
					}
					tc.tool.SetWorkDir(root)
					tc.params["path"] = name
					preview, err := tc.tool.Preview(tc.params)
					if err != nil || !preview.Success || !preview.NeedsApproval || preview.DiffPreview.FilePath != path {
						t.Fatalf("literal approval: %+v %v", preview, err)
					}
					if err := preview.ValidateApproval(); err != nil {
						t.Fatal(err)
					}
					before, err := os.ReadFile(path)
					if err != nil || string(before) != "old\ntail\n" {
						t.Fatal("preview changed source")
					}
					r, err = tc.tool.Execute(tc.params)
					if err != nil || !r.Success {
						t.Fatalf("literal mutation: %+v %v", r, err)
					}
					got, err := os.ReadFile(path)
					if err != nil || string(got) != tc.want {
						t.Fatalf("literal bytes: %q %v want %q", got, err, tc.want)
					}
					mode, err := os.Stat(path)
					if err != nil || mode.Mode().Perm() != 0640 {
						t.Fatalf("changed literal mode: %v %v", mode, err)
					}
					if trimmed != "" {
						got, err := os.ReadFile(filepath.Join(root, trimmed))
						if err != nil || string(got) != "neighbor\n" {
							t.Fatalf("neighbor changed: %q %v", got, err)
						}
					}
					if err := preview.ValidateApproval(); err == nil {
						t.Fatal("changed source kept old approval valid")
					}
				})
			}
		})
	}
}

func TestReadFileTool_LiteralDeniedPaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX whitespace filenames and symlinks")
	}
	root := t.TempDir()
	work := filepath.Join(root, "work")
	denied := filepath.Join(root, "private ")
	for _, dir := range []string{work, denied, strings.TrimSpace(denied)} {
		if err := os.Mkdir(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	secret := filepath.Join(denied, "file.txt ")
	if err := os.WriteFile(secret, []byte("denied fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	public := filepath.Join(strings.TrimSpace(denied), "file.txt")
	if err := os.WriteFile(public, []byte("public fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(work, "link.txt ")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}
	reader := &ReadFileTool{}
	reader.SetWorkDir(work)
	for _, deny := range []string{denied, secret} {
		reader.SetOutsideWorkDirReads(true, []string{deny})
		for _, path := range []string{secret, link} {
			r, err := reader.Execute(map[string]any{"path": path})
			if err != nil || r.Success || !strings.Contains(r.Error, "denied") {
				t.Fatalf("denied=%q path=%q result=%+v error=%v", deny, path, r, err)
			}
		}
		r, err := reader.Execute(map[string]any{"path": public})
		if err != nil || !r.Success || r.Data["content"] != "public fixture\n" {
			t.Fatalf("distinct public path blocked: %+v %v", r, err)
		}
	}
}

func TestResolvePath_LiteralSymlinksAndMissingTargets(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX whitespace filenames and symlinks")
	}
	root := t.TempDir()
	work := filepath.Join(root, "work")
	outside := filepath.Join(root, "outside ")
	for _, dir := range []string{work, outside} {
		if err := os.Mkdir(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	secret := filepath.Join(outside, "file.txt ")
	if err := os.WriteFile(secret, []byte("outside\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, target string }{{"file ", secret}, {"dir ", outside}} {
		if err := os.Symlink(tc.target, filepath.Join(work, tc.name)); err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{tc.name, filepath.Join(tc.name, "new ", "missing.txt ")} {
			if _, err := resolvePath(work, path); err == nil {
				t.Fatalf("outside literal symlink allowed: %q", path)
			}
		}
	}
	inside := filepath.Join(work, "inside ")
	if err := os.Mkdir(inside, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(inside, filepath.Join(work, "alias ")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(work, "alias ", "new ", "missing.txt ")
	got, err := resolvePath(work, path)
	if err != nil || got != path {
		t.Fatalf("inside literal link rejected: %q %v", got, err)
	}
	if resolved := evalSymlinksFallbackForTarget(path); resolved != filepath.Join(inside, "new ", "missing.txt ") {
		t.Fatalf("missing target changed: %q", resolved)
	}
}
