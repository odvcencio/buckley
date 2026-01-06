package experiment

import (
	"strings"
	"testing"
	"time"
)

func TestParseTimeout(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want time.Duration
	}{
		{
			name: "empty string returns 0",
			raw:  "",
			want: 0,
		},
		{
			name: "whitespace returns 0",
			raw:  "   ",
			want: 0,
		},
		{
			name: "invalid number returns 0",
			raw:  "abc",
			want: 0,
		},
		{
			name: "negative number returns 0",
			raw:  "-1000",
			want: 0,
		},
		{
			name: "zero returns 0",
			raw:  "0",
			want: 0,
		},
		{
			name: "valid milliseconds",
			raw:  "5000",
			want: 5 * time.Second,
		},
		{
			name: "value with whitespace",
			raw:  "  10000  ",
			want: 10 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseTimeout(tt.raw)
			if got != tt.want {
				t.Errorf("parseTimeout() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseToolAllowList(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want map[string]struct{}
	}{
		{
			name: "empty string returns empty map",
			raw:  "",
			want: map[string]struct{}{},
		},
		{
			name: "single tool",
			raw:  "read_file",
			want: map[string]struct{}{"read_file": {}},
		},
		{
			name: "multiple tools",
			raw:  "read_file,write_file,execute",
			want: map[string]struct{}{
				"read_file":  {},
				"write_file": {},
				"execute":    {},
			},
		},
		{
			name: "trims whitespace",
			raw:  "  read_file  ,  write_file  ",
			want: map[string]struct{}{
				"read_file":  {},
				"write_file": {},
			},
		},
		{
			name: "skips empty entries",
			raw:  "read_file,,write_file,",
			want: map[string]struct{}{
				"read_file":  {},
				"write_file": {},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseToolAllowList(tt.raw)
			if len(got) != len(tt.want) {
				t.Errorf("parseToolAllowList() length = %d, want %d", len(got), len(tt.want))
			}
			for k := range tt.want {
				if _, ok := got[k]; !ok {
					t.Errorf("parseToolAllowList() missing key %q", k)
				}
			}
		})
	}
}

func TestCollectFiles(t *testing.T) {
	tests := []struct {
		name string
		set  map[string]struct{}
		want []string
	}{
		{
			name: "nil returns nil",
			set:  nil,
			want: nil,
		},
		{
			name: "empty returns nil",
			set:  map[string]struct{}{},
			want: nil,
		},
		{
			name: "single file",
			set:  map[string]struct{}{"file.go": {}},
			want: []string{"file.go"},
		},
		{
			name: "multiple files sorted",
			set: map[string]struct{}{
				"c.go": {},
				"a.go": {},
				"b.go": {},
			},
			want: []string{"a.go", "b.go", "c.go"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := collectFiles(tt.set)

			if tt.want == nil {
				if got != nil {
					t.Errorf("collectFiles() = %v, want nil", got)
				}
				return
			}

			if len(got) != len(tt.want) {
				t.Errorf("collectFiles() length = %d, want %d", len(got), len(tt.want))
				return
			}

			for i, want := range tt.want {
				if got[i] != want {
					t.Errorf("collectFiles()[%d] = %q, want %q", i, got[i], want)
				}
			}
		})
	}
}

func TestNormalizeFilePath(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{
			name: "empty string",
			path: "",
			want: "",
		},
		{
			name: "whitespace only",
			path: "   ",
			want: "",
		},
		{
			name: "simple path",
			path: "file.go",
			want: "file.go",
		},
		{
			name: "path with directory",
			path: "pkg/experiment/file.go",
			want: "pkg/experiment/file.go",
		},
		{
			name: "path with double slashes",
			path: "pkg//experiment//file.go",
			want: "pkg/experiment/file.go",
		},
		{
			name: "path with dots",
			path: "pkg/./experiment/../experiment/file.go",
			want: "pkg/experiment/file.go",
		},
		{
			name: "trims whitespace",
			path: "  pkg/file.go  ",
			want: "pkg/file.go",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeFilePath(tt.path)
			if got != tt.want {
				t.Errorf("normalizeFilePath() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildImplementationPrompt(t *testing.T) {
	tests := []struct {
		name        string
		prompt      string
		wantContain []string
	}{
		{
			name:   "empty prompt",
			prompt: "",
			wantContain: []string{
				"No prompt provided.",
			},
		},
		{
			name:   "whitespace prompt",
			prompt: "   ",
			wantContain: []string{
				"No prompt provided.",
			},
		},
		{
			name:   "valid prompt",
			prompt: "Add a new feature",
			wantContain: []string{
				"Implement this task:",
				"**Task:** Add a new feature",
				"filepath:/path/to/file.go",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildImplementationPrompt(tt.prompt)
			for _, want := range tt.wantContain {
				if !strings.Contains(got, want) {
					t.Errorf("buildImplementationPrompt() missing %q in:\n%s", want, got)
				}
			}
		})
	}
}

func TestMergeFiles(t *testing.T) {
	tests := []struct {
		name  string
		base  []string
		extra []string
		want  []string
	}{
		{
			name:  "empty extra returns base",
			base:  []string{"a.go", "b.go"},
			extra: nil,
			want:  []string{"a.go", "b.go"},
		},
		{
			name:  "empty base with extra",
			base:  nil,
			extra: []string{"a.go", "b.go"},
			want:  []string{"a.go", "b.go"},
		},
		{
			name:  "no duplicates",
			base:  []string{"a.go", "b.go"},
			extra: []string{"c.go", "d.go"},
			want:  []string{"a.go", "b.go", "c.go", "d.go"},
		},
		{
			name:  "with duplicates",
			base:  []string{"a.go", "b.go"},
			extra: []string{"b.go", "c.go"},
			want:  []string{"a.go", "b.go", "c.go"},
		},
		{
			name:  "skips empty paths",
			base:  []string{"a.go", ""},
			extra: []string{"", "b.go"},
			want:  []string{"a.go", "b.go"},
		},
		{
			name:  "normalizes paths",
			base:  []string{"  pkg/a.go  "},
			extra: []string{"pkg//b.go"},
			want:  []string{"pkg/a.go", "pkg/b.go"},
		},
		{
			name:  "sorted output",
			base:  []string{"z.go"},
			extra: []string{"a.go"},
			want:  []string{"a.go", "z.go"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mergeFiles(tt.base, tt.extra)

			if len(got) != len(tt.want) {
				t.Errorf("mergeFiles() length = %d, want %d; got %v", len(got), len(tt.want), got)
				return
			}

			for i, want := range tt.want {
				if got[i] != want {
					t.Errorf("mergeFiles()[%d] = %q, want %q", i, got[i], want)
				}
			}
		})
	}
}

func TestParseFloat(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    float64
		wantOK  bool
	}{
		{
			name:   "empty string",
			raw:    "",
			want:   0,
			wantOK: false,
		},
		{
			name:   "whitespace only",
			raw:    "   ",
			want:   0,
			wantOK: false,
		},
		{
			name:   "invalid number",
			raw:    "abc",
			want:   0,
			wantOK: false,
		},
		{
			name:   "valid integer",
			raw:    "42",
			want:   42,
			wantOK: true,
		},
		{
			name:   "valid float",
			raw:    "0.7",
			want:   0.7,
			wantOK: true,
		},
		{
			name:   "negative float",
			raw:    "-1.5",
			want:   -1.5,
			wantOK: true,
		},
		{
			name:   "with whitespace",
			raw:    "  0.5  ",
			want:   0.5,
			wantOK: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseFloat(tt.raw)
			if ok != tt.wantOK {
				t.Errorf("parseFloat() ok = %v, want %v", ok, tt.wantOK)
			}
			if got != tt.want {
				t.Errorf("parseFloat() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseInt(t *testing.T) {
	tests := []struct {
		name   string
		raw    string
		want   int
		wantOK bool
	}{
		{
			name:   "empty string",
			raw:    "",
			want:   0,
			wantOK: false,
		},
		{
			name:   "whitespace only",
			raw:    "   ",
			want:   0,
			wantOK: false,
		},
		{
			name:   "invalid number",
			raw:    "abc",
			want:   0,
			wantOK: false,
		},
		{
			name:   "valid integer",
			raw:    "42",
			want:   42,
			wantOK: true,
		},
		{
			name:   "negative integer",
			raw:    "-10",
			want:   -10,
			wantOK: true,
		},
		{
			name:   "with whitespace",
			raw:    "  100  ",
			want:   100,
			wantOK: true,
		},
		{
			name:   "float fails",
			raw:    "3.14",
			want:   0,
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseInt(tt.raw)
			if ok != tt.wantOK {
				t.Errorf("parseInt() ok = %v, want %v", ok, tt.wantOK)
			}
			if got != tt.want {
				t.Errorf("parseInt() = %v, want %v", got, tt.want)
			}
		})
	}
}
