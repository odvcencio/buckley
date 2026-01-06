package builtin

import (
	"sort"
	"strings"
	"testing"
)

func TestIsValidEnvKey(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want bool
	}{
		{name: "empty string", key: "", want: false},
		{name: "whitespace only", key: "   ", want: false},
		{name: "simple key", key: "FOO", want: true},
		{name: "lowercase", key: "foo", want: true},
		{name: "mixed case", key: "FooBar", want: true},
		{name: "with underscore", key: "FOO_BAR", want: true},
		{name: "starts with underscore", key: "_FOO", want: true},
		{name: "with numbers", key: "FOO123", want: true},
		{name: "underscore and numbers", key: "FOO_123_BAR", want: true},
		{name: "starts with number", key: "123FOO", want: false},
		{name: "with dash", key: "FOO-BAR", want: false},
		{name: "with space", key: "FOO BAR", want: false},
		{name: "with equals", key: "FOO=BAR", want: false},
		{name: "with dot", key: "FOO.BAR", want: false},
		{name: "unicode letter", key: "FOO_", want: true},
		{name: "leading whitespace (trimmed)", key: "  FOO", want: true},
		{name: "trailing whitespace (trimmed)", key: "FOO  ", want: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isValidEnvKey(tc.key)
			if got != tc.want {
				t.Errorf("isValidEnvKey(%q) = %v, want %v", tc.key, got, tc.want)
			}
		})
	}
}

func TestSanitizeEnvMap(t *testing.T) {
	tests := []struct {
		name     string
		input    map[string]string
		wantNil  bool
		wantKeys []string
	}{
		{
			name:    "nil input",
			input:   nil,
			wantNil: true,
		},
		{
			name:    "empty map",
			input:   map[string]string{},
			wantNil: true,
		},
		{
			name:     "valid keys only",
			input:    map[string]string{"FOO": "1", "BAR": "2"},
			wantKeys: []string{"BAR", "FOO"},
		},
		{
			name:     "mixed valid and invalid",
			input:    map[string]string{"FOO": "1", "123BAD": "2", "GOOD": "3"},
			wantKeys: []string{"FOO", "GOOD"},
		},
		{
			name:    "all invalid",
			input:   map[string]string{"123": "1", "foo-bar": "2"},
			wantNil: true,
		},
		{
			name:     "key with leading whitespace",
			input:    map[string]string{"  FOO": "1"},
			wantKeys: []string{"FOO"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeEnvMap(tc.input)
			if tc.wantNil {
				if got != nil {
					t.Errorf("sanitizeEnvMap(%v) = %v, want nil", tc.input, got)
				}
				return
			}
			if got == nil {
				t.Errorf("sanitizeEnvMap(%v) = nil, want non-nil", tc.input)
				return
			}
			gotKeys := make([]string, 0, len(got))
			for k := range got {
				gotKeys = append(gotKeys, k)
			}
			sort.Strings(gotKeys)
			if len(gotKeys) != len(tc.wantKeys) {
				t.Errorf("got %d keys, want %d", len(gotKeys), len(tc.wantKeys))
				return
			}
			for i, want := range tc.wantKeys {
				if gotKeys[i] != want {
					t.Errorf("key[%d] = %q, want %q", i, gotKeys[i], want)
				}
			}
		})
	}
}

func TestEnvPairs(t *testing.T) {
	tests := []struct {
		name      string
		input     map[string]string
		wantPairs []string
	}{
		{
			name:      "nil input",
			input:     nil,
			wantPairs: nil,
		},
		{
			name:      "empty map",
			input:     map[string]string{},
			wantPairs: nil,
		},
		{
			name:      "single entry",
			input:     map[string]string{"FOO": "bar"},
			wantPairs: []string{"FOO=bar"},
		},
		{
			name:      "multiple entries sorted",
			input:     map[string]string{"ZZZ": "3", "AAA": "1", "MMM": "2"},
			wantPairs: []string{"AAA=1", "MMM=2", "ZZZ=3"},
		},
		{
			name:      "value with equals sign",
			input:     map[string]string{"FOO": "a=b=c"},
			wantPairs: []string{"FOO=a=b=c"},
		},
		{
			name:      "empty value",
			input:     map[string]string{"FOO": ""},
			wantPairs: []string{"FOO="},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := envPairs(tc.input)
			if tc.wantPairs == nil {
				if got != nil {
					t.Errorf("envPairs(%v) = %v, want nil", tc.input, got)
				}
				return
			}
			if len(got) != len(tc.wantPairs) {
				t.Errorf("got %d pairs, want %d", len(got), len(tc.wantPairs))
				return
			}
			for i, want := range tc.wantPairs {
				if got[i] != want {
					t.Errorf("pairs[%d] = %q, want %q", i, got[i], want)
				}
			}
		})
	}
}

func TestMergeEnv(t *testing.T) {
	t.Run("empty overrides returns base", func(t *testing.T) {
		base := []string{"FOO=1", "BAR=2"}
		got := mergeEnv(base, nil)
		if len(got) != len(base) {
			t.Errorf("mergeEnv with nil overrides changed length")
		}
	})

	t.Run("empty map overrides returns base", func(t *testing.T) {
		base := []string{"FOO=1"}
		got := mergeEnv(base, map[string]string{})
		if len(got) != len(base) {
			t.Errorf("mergeEnv with empty overrides changed length")
		}
	})

	t.Run("overrides are appended", func(t *testing.T) {
		base := []string{"FOO=1"}
		got := mergeEnv(base, map[string]string{"BAR": "2"})
		if len(got) != 2 {
			t.Errorf("expected 2 entries, got %d", len(got))
		}
		// Last entry should be the new one
		found := false
		for _, pair := range got {
			if strings.HasPrefix(pair, "BAR=") {
				found = true
				break
			}
		}
		if !found {
			t.Error("BAR should be in merged env")
		}
	})

	t.Run("invalid overrides are filtered", func(t *testing.T) {
		base := []string{"FOO=1"}
		got := mergeEnv(base, map[string]string{"123INVALID": "bad"})
		if len(got) != 1 {
			t.Errorf("expected 1 entry (invalid filtered), got %d", len(got))
		}
	})
}
