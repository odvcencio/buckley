package main

import "testing"

func TestGlobsOverlap(t *testing.T) {
	tests := []struct {
		name      string
		requested []string
		declared  []string
		want      bool
	}{
		{"same glob", []string{"client/**"}, []string{"client/**"}, true},
		{"requested contains declared", []string{"pkg/**"}, []string{"pkg/sub/**"}, true},
		{"declared contains requested subdirectory", []string{"pkg/sub/**"}, []string{"pkg/**"}, true},
		{"declared glob matches a requested file", []string{"pkg/sub/file.go"}, []string{"pkg/**"}, true},
		{"requested glob matches a declared file", []string{"pkg/**"}, []string{"pkg/sub/file.go"}, true},
		{"blank globs never match", []string{" "}, []string{"pkg/**", ""}, false},
		{"disjoint trees", []string{"cmd/**"}, []string{"pkg/**"}, false},
		{"sibling with a shared name prefix", []string{"pkg/sub/**"}, []string{"pkg/subway/**"}, false},
		{"nothing requested overlaps nothing declared", []string{"pkg/**"}, nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := globsOverlap(tc.requested, tc.declared); got != tc.want {
				t.Fatalf("globsOverlap(%v, %v) = %v, want %v", tc.requested, tc.declared, got, tc.want)
			}
		})
	}
}
