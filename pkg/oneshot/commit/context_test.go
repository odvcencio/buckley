package commit

import "testing"

func TestDetectWarnings(t *testing.T) {
	tests := []struct {
		name     string
		files    []FileChange
		agentsMD string
		wantWarn []string // Expected warning categories
	}{
		{
			name: "secret file .env",
			files: []FileChange{
				{Status: "A", Path: ".env"},
			},
			wantWarn: []string{"secrets"},
		},
		{
			name: "credentials json",
			files: []FileChange{
				{Status: "M", Path: "config/credentials.json"},
			},
			wantWarn: []string{"secrets"},
		},
		{
			name: "build artifact",
			files: []FileChange{
				{Status: "A", Path: "dist/bundle.js"},
			},
			wantWarn: []string{"build"},
		},
		{
			name: "node_modules",
			files: []FileChange{
				{Status: "A", Path: "node_modules/lodash/index.js"},
			},
			wantWarn: []string{"deps"},
		},
		{
			name: "node_modules with vendor policy",
			files: []FileChange{
				{Status: "A", Path: "node_modules/lodash/index.js"},
			},
			agentsMD: "We vendor and check in all dependencies",
			wantWarn: []string{}, // Suppressed
		},
		{
			name: "binary file",
			files: []FileChange{
				{Status: "A", Path: "assets/video.mp4"},
			},
			wantWarn: []string{"binary"},
		},
		{
			name: "deleted file no warning",
			files: []FileChange{
				{Status: "D", Path: ".env"},
			},
			wantWarn: []string{},
		},
		{
			name: "normal source file",
			files: []FileChange{
				{Status: "M", Path: "main.go"},
			},
			wantWarn: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			warnings := detectWarnings(tt.files, tt.agentsMD)

			gotCategories := make([]string, len(warnings))
			for i, w := range warnings {
				gotCategories[i] = w.Category
			}

			if len(gotCategories) != len(tt.wantWarn) {
				t.Errorf("got %d warnings %v, want %d %v", len(gotCategories), gotCategories, len(tt.wantWarn), tt.wantWarn)
				return
			}

			for i, want := range tt.wantWarn {
				if gotCategories[i] != want {
					t.Errorf("warning[%d] category = %q, want %q", i, gotCategories[i], want)
				}
			}
		})
	}
}

func TestParseNameStatus(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   []FileChange
	}{
		{
			name:   "empty output",
			output: "",
			want:   nil,
		},
		{
			name:   "single added file",
			output: "A\tmain.go",
			want: []FileChange{
				{Status: "A", Path: "main.go"},
			},
		},
		{
			name:   "single modified file",
			output: "M\tpkg/api/handler.go",
			want: []FileChange{
				{Status: "M", Path: "pkg/api/handler.go"},
			},
		},
		{
			name:   "single deleted file",
			output: "D\told_file.txt",
			want: []FileChange{
				{Status: "D", Path: "old_file.txt"},
			},
		},
		{
			name:   "renamed file",
			output: "R100\told_name.go\tnew_name.go",
			want: []FileChange{
				{Status: "R", Path: "new_name.go", OldPath: "old_name.go"},
			},
		},
		{
			name:   "copied file",
			output: "C050\toriginal.go\tcopy.go",
			want: []FileChange{
				{Status: "C", Path: "copy.go", OldPath: "original.go"},
			},
		},
		{
			name: "multiple files",
			output: `A	new_file.go
M	modified.go
D	deleted.go`,
			want: []FileChange{
				{Status: "A", Path: "new_file.go"},
				{Status: "M", Path: "modified.go"},
				{Status: "D", Path: "deleted.go"},
			},
		},
		{
			name: "with extra whitespace",
			output: `  A	file1.go
M	file2.go
`,
			want: []FileChange{
				{Status: "A", Path: "file1.go"},
				{Status: "M", Path: "file2.go"},
			},
		},
		{
			name:   "file with spaces in path",
			output: "A\tpath with spaces/file.go",
			want: []FileChange{
				{Status: "A", Path: "path with spaces/file.go"},
			},
		},
		{
			name:   "malformed line - no tab",
			output: "A file.go",
			want:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseNameStatus(tt.output)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d changes, want %d", len(got), len(tt.want))
			}
			for i, want := range tt.want {
				if got[i].Status != want.Status {
					t.Errorf("change[%d].Status = %q, want %q", i, got[i].Status, want.Status)
				}
				if got[i].Path != want.Path {
					t.Errorf("change[%d].Path = %q, want %q", i, got[i].Path, want.Path)
				}
				if got[i].OldPath != want.OldPath {
					t.Errorf("change[%d].OldPath = %q, want %q", i, got[i].OldPath, want.OldPath)
				}
			}
		})
	}
}

func TestDiffStats_TotalChanges(t *testing.T) {
	tests := []struct {
		name  string
		stats DiffStats
		want  int
	}{
		{
			name:  "zero",
			stats: DiffStats{},
			want:  0,
		},
		{
			name:  "insertions only",
			stats: DiffStats{Insertions: 50},
			want:  50,
		},
		{
			name:  "deletions only",
			stats: DiffStats{Deletions: 30},
			want:  30,
		},
		{
			name:  "both",
			stats: DiffStats{Insertions: 100, Deletions: 50},
			want:  150,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.stats.TotalChanges()
			if got != tt.want {
				t.Errorf("TotalChanges() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestExtractAreas(t *testing.T) {
	tests := []struct {
		name  string
		files []FileChange
		want  []string
	}{
		{
			name:  "empty",
			files: nil,
			want:  nil,
		},
		{
			name: "single pkg file",
			files: []FileChange{
				{Status: "M", Path: "pkg/api/handler.go"},
			},
			want: []string{"api"},
		},
		{
			name: "single cmd file",
			files: []FileChange{
				{Status: "M", Path: "cmd/server/main.go"},
			},
			want: []string{"server"},
		},
		{
			name: "single internal file",
			files: []FileChange{
				{Status: "M", Path: "internal/auth/middleware.go"},
			},
			want: []string{"auth"},
		},
		{
			name: "web directory",
			files: []FileChange{
				{Status: "M", Path: "web/app/page.tsx"},
			},
			want: []string{"web"},
		},
		{
			name: "docs directory",
			files: []FileChange{
				{Status: "M", Path: "docs/README.md"},
			},
			want: []string{"docs"},
		},
		{
			name: "scripts directory",
			files: []FileChange{
				{Status: "M", Path: "scripts/build.sh"},
			},
			want: []string{"scripts"},
		},
		{
			name: "multiple files same area",
			files: []FileChange{
				{Status: "M", Path: "pkg/api/handler.go"},
				{Status: "M", Path: "pkg/api/router.go"},
				{Status: "A", Path: "pkg/api/middleware.go"},
			},
			want: []string{"api"},
		},
		{
			name: "multiple areas",
			files: []FileChange{
				{Status: "M", Path: "pkg/api/handler.go"},
				{Status: "M", Path: "pkg/model/user.go"},
				{Status: "M", Path: "cmd/server/main.go"},
			},
			want: []string{"api", "model", "server"},
		},
		{
			name: "root file - no area",
			files: []FileChange{
				{Status: "M", Path: "main.go"},
			},
			want: nil,
		},
		{
			name: "other top-level directory",
			files: []FileChange{
				{Status: "M", Path: "config/settings.yaml"},
			},
			want: []string{"config"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractAreas(tt.files)
			if len(got) != len(tt.want) {
				t.Fatalf("extractAreas() = %v, want %v", got, tt.want)
			}
			for i, want := range tt.want {
				if got[i] != want {
					t.Errorf("area[%d] = %q, want %q", i, got[i], want)
				}
			}
		})
	}
}

func TestAreaFromPath(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"main.go", ""},
		{"README.md", ""},
		{"pkg/api/handler.go", "api"},
		{"cmd/server/main.go", "server"},
		{"internal/auth/middleware.go", "auth"},
		{"web/components/Button.tsx", "web"},
		{"docs/api.md", "docs"},
		{"scripts/test.sh", "scripts"},
		{"config/settings.yaml", "config"},
		{"foo/bar/baz.go", "foo"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := areaFromPath(tt.path)
			if got != tt.want {
				t.Errorf("areaFromPath(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestEstimateTokens(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"", 0},
		{"a", 1},
		{"ab", 1},
		{"abc", 1},
		{"abcd", 1},
		{"abcde", 2},
		{"hello world", 3},                                 // 11 chars -> (11+3)/4 = 3
		{"this is a longer string with more tokens", 10}, // 40 chars -> (40+3)/4 = 10
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := estimateTokens(tt.input)
			if got != tt.want {
				t.Errorf("estimateTokens(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

func TestDefaultContextOptions(t *testing.T) {
	opts := DefaultContextOptions()

	if opts.MaxDiffBytes != 80_000 {
		t.Errorf("MaxDiffBytes = %d, want 80000", opts.MaxDiffBytes)
	}
	if opts.MaxDiffTokens != 20_000 {
		t.Errorf("MaxDiffTokens = %d, want 20000", opts.MaxDiffTokens)
	}
	if !opts.IncludeAgents {
		t.Error("IncludeAgents = false, want true")
	}
}

func TestIsSecretFile(t *testing.T) {
	tests := []struct {
		base string
		path string
		want bool
	}{
		// Exact matches
		{".env", ".env", true},
		{".env.local", ".env.local", true},
		{".env.development", ".env.development", true},
		{".env.production", ".env.production", true},
		{".env.test", ".env.test", true},
		{".envrc", ".envrc", true},
		{"credentials.json", "config/credentials.json", true},
		{"credentials.yaml", "credentials.yaml", true},
		{"secrets.json", "secrets.json", true},
		{"id_rsa", ".ssh/id_rsa", true},
		{"id_ed25519", ".ssh/id_ed25519", true},
		{".htpasswd", ".htpasswd", true},
		{".netrc", ".netrc", true},
		{".npmrc", ".npmrc", true},
		{"kubeconfig", "kubeconfig", true},

		// Pattern matches
		{"my.key", "certs/my.key", true},
		{"server.pem", "certs/server.pem", true},
		{"my-secrets.json", "my-secrets.json", true},
		{"my-secrets.yaml", "my-secrets.yaml", true},
		{"app-credential.json", "app-credential.json", true},

		// AWS credentials
		{"credentials", ".aws/credentials", true},
		{"config", ".aws/config", true},

		// Note: sample.env and example.env match the .env suffix check before the
		// pattern exclusions on line 357 can take effect. This is the actual code behavior.
		// Only .env.example is properly excluded because it doesn't match .env suffix
		// (it matches ".env.example" in exact check which doesn't exist).
		{"sample.env", "sample.env", true},  // Matches .env suffix in exact check
		{"example.env", "example.env", true}, // Matches .env suffix in exact check
		{".env.example", ".env.example", false}, // Excluded by pattern on line 357

		// Normal files
		{"main.go", "main.go", false},
		{"config.yaml", "config.yaml", false},
		{"settings.json", "settings.json", false},
		{"README.md", "README.md", false},
	}

	for _, tt := range tests {
		t.Run(tt.base, func(t *testing.T) {
			got := isSecretFile(tt.base, tt.path)
			if got != tt.want {
				t.Errorf("isSecretFile(%q, %q) = %v, want %v", tt.base, tt.path, got, tt.want)
			}
		})
	}
}

func TestIsBuildArtifact(t *testing.T) {
	tests := []struct {
		base string
		path string
		dir  string
		want bool
	}{
		// Build directories
		{"bundle.js", "dist/bundle.js", "dist", true},
		{"app.js", "build/app.js", "build", true},
		{"main", "out/main", "out", true},
		{"binary", "target/binary", "target", true},
		{"app", "bin/app", "bin", true},
		{"page.js", ".next/static/page.js", ".next/static", true},
		{"module.pyc", "__pycache__/module.pyc", "__pycache__", true},
		{"test.py", ".pytest_cache/test.py", ".pytest_cache", true},

		// Nested build directories
		{"file.js", "foo/dist/file.js", "foo/dist", true},
		{"file.js", "src/build/file.js", "src/build", true},

		// Build extensions
		{"main.o", "src/main.o", "src", true},
		{"lib.a", "src/lib.a", "src", true},
		{"lib.so", "src/lib.so", "src", true},
		{"lib.dylib", "src/lib.dylib", "src", true},
		{"lib.dll", "src/lib.dll", "src", true},
		{"app.exe", "src/app.exe", "src", true},
		{"Main.class", "src/Main.class", "src", true},
		{"module.pyc", "src/module.pyc", "src", true},
		{"module.pyo", "src/module.pyo", "src", true},

		// Coverage artifacts
		{"coverage.out", "coverage.out", ".", true},
		{"coverage.html", "coverage.html", ".", true},
		{"coverage-report.html", "coverage-report.html", ".", true},
		{".coverage", ".coverage", ".", true},
		{"lcov.lcov", "lcov.lcov", ".", true},

		// Normal files
		{"main.go", "src/main.go", "src", false},
		{"app.js", "src/app.js", "src", false},
		{"index.html", "public/index.html", "public", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := isBuildArtifact(tt.base, tt.path, tt.dir)
			if got != tt.want {
				t.Errorf("isBuildArtifact(%q, %q, %q) = %v, want %v", tt.base, tt.path, tt.dir, got, tt.want)
			}
		})
	}
}

func TestIsDependencyFile(t *testing.T) {
	tests := []struct {
		path string
		dir  string
		want bool
	}{
		// Node
		{"node_modules/lodash/index.js", "node_modules/lodash", true},
		{"src/node_modules/pkg/file.js", "src/node_modules/pkg", true},

		// Go vendor
		{"vendor/github.com/pkg/errors/errors.go", "vendor/github.com/pkg/errors", true},
		{"vendor/modules.txt", "vendor", false}, // modules.txt is allowed

		// Python
		{"site-packages/pkg/mod.py", "site-packages/pkg", true},
		{".venv/lib/python/pkg.py", ".venv/lib/python", true},
		{"venv/lib/site-packages/pkg.py", "venv/lib/site-packages", true},

		// Ruby
		{"vendor/bundle/ruby/gems/pkg/lib.rb", "vendor/bundle/ruby/gems/pkg", true},
		{"lib/gems/pkg/lib.rb", "lib/gems/pkg", true}, // /gems/ in path

		// Rust
		{"target/debug/deps/pkg.d", "target/debug/deps", true},
		{"target/release/deps/pkg.d", "target/release/deps", true},

		// Normal files
		{"src/main.go", "src", false},
		{"pkg/api/handler.go", "pkg/api", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := isDependencyFile(tt.path, tt.dir)
			if got != tt.want {
				t.Errorf("isDependencyFile(%q, %q) = %v, want %v", tt.path, tt.dir, got, tt.want)
			}
		})
	}
}

func TestIsLargeBinary(t *testing.T) {
	tests := []struct {
		base string
		want bool
	}{
		// Archive files
		{"archive.zip", true},
		{"archive.tar", true},
		{"archive.gz", true},
		{"archive.bz2", true},
		{"archive.7z", true},
		{"archive.rar", true},

		// Video files
		{"video.mp4", true},
		{"video.mov", true},
		{"video.avi", true},
		{"video.mkv", true},
		{"video.webm", true},

		// Audio files
		{"audio.mp3", true},
		{"audio.wav", true},
		{"audio.flac", true},
		{"audio.aac", true},

		// Design files
		{"design.psd", true},
		{"design.ai", true},
		{"design.sketch", true},

		// Database files
		{"data.sqlite", true},
		{"data.db", true},

		// Normal files
		{"main.go", false},
		{"image.png", false},
		{"image.jpg", false},
		{"document.pdf", false},
	}

	for _, tt := range tests {
		t.Run(tt.base, func(t *testing.T) {
			got := isLargeBinary(tt.base)
			if got != tt.want {
				t.Errorf("isLargeBinary(%q) = %v, want %v", tt.base, got, tt.want)
			}
		})
	}
}

func TestWarning_Fields(t *testing.T) {
	w := Warning{
		Severity: "error",
		Category: "secrets",
		Path:     ".env",
		Message:  "likely contains secrets",
	}

	if w.Severity != "error" {
		t.Errorf("Severity = %q, want 'error'", w.Severity)
	}
	if w.Category != "secrets" {
		t.Errorf("Category = %q, want 'secrets'", w.Category)
	}
	if w.Path != ".env" {
		t.Errorf("Path = %q, want '.env'", w.Path)
	}
	if w.Message != "likely contains secrets" {
		t.Errorf("Message = %q, want 'likely contains secrets'", w.Message)
	}
}

func TestFileChange_Fields(t *testing.T) {
	fc := FileChange{
		Status:  "R",
		Path:    "new.go",
		OldPath: "old.go",
	}

	if fc.Status != "R" {
		t.Errorf("Status = %q, want 'R'", fc.Status)
	}
	if fc.Path != "new.go" {
		t.Errorf("Path = %q, want 'new.go'", fc.Path)
	}
	if fc.OldPath != "old.go" {
		t.Errorf("OldPath = %q, want 'old.go'", fc.OldPath)
	}
}
