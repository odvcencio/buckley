package agentcoord

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSourceScopeValidate_FullFile(t *testing.T) {
	s := &SourceScope{Files: []SourceFile{{Path: "pkg/agentcoord/contract.go"}}}
	if err := ValidateSourceScope(s); err != nil {
		t.Fatalf("full-file scope should validate: %v", err)
	}
}

func TestSourceScopeValidate_Bounded(t *testing.T) {
	s := &SourceScope{Files: []SourceFile{{Path: "a/b.go", StartLine: 3, EndLine: 9}}}
	if err := ValidateSourceScope(s); err != nil {
		t.Fatalf("bounded scope should validate: %v", err)
	}
}

func TestSourceScopeValidate_Empty(t *testing.T) {
	if err := ValidateSourceScope(&SourceScope{}); err != nil {
		t.Fatalf("explicit empty scope should validate: %v", err)
	}
	if err := ValidateSourceScope(nil); err != nil {
		t.Fatalf("nil scope should validate: %v", err)
	}
}

func TestSourceScopeValidate_LiteralSpecialFilenames(t *testing.T) {
	for _, p := range []string{"pkg/[a]b!.go", "report[1].txt", "weird!name.go"} {
		s := &SourceScope{Files: []SourceFile{{Path: p}}}
		if err := ValidateSourceScope(s); err != nil {
			t.Fatalf("literal path %q should validate: %v", p, err)
		}
	}
}

func TestSourceScopeValidate_Invalid(t *testing.T) {
	cases := []struct {
		name string
		s    *SourceScope
	}{
		{"half-bounded start", &SourceScope{Files: []SourceFile{{Path: "a.go", StartLine: 5}}}},
		{"half-bounded end", &SourceScope{Files: []SourceFile{{Path: "a.go", EndLine: 5}}}},
		{"negative start", &SourceScope{Files: []SourceFile{{Path: "a.go", StartLine: -1, EndLine: 3}}}},
		{"negative end", &SourceScope{Files: []SourceFile{{Path: "a.go", StartLine: 1, EndLine: -3}}}},
		{"start after end", &SourceScope{Files: []SourceFile{{Path: "a.go", StartLine: 9, EndLine: 3}}}},
		{"empty path", &SourceScope{Files: []SourceFile{{Path: ""}}}},
		{"whitespace-only path", &SourceScope{Files: []SourceFile{{Path: "   "}}}},
		{"padded path", &SourceScope{Files: []SourceFile{{Path: " a.go"}}}},
		{"nul byte", &SourceScope{Files: []SourceFile{{Path: "a\x00.go"}}}},
		{"non-utf8", &SourceScope{Files: []SourceFile{{Path: "a\xff.go"}}}},
		{"backslash", &SourceScope{Files: []SourceFile{{Path: `a\b.go`}}}},
		{"windows drive", &SourceScope{Files: []SourceFile{{Path: "C:/tmp/a.go"}}}},
		{"absolute", &SourceScope{Files: []SourceFile{{Path: "/etc/passwd"}}}},
		{"parent escape", &SourceScope{Files: []SourceFile{{Path: "../secrets.go"}}}},
		{"dot path", &SourceScope{Files: []SourceFile{{Path: "."}}}},
		{"directory", &SourceScope{Files: []SourceFile{{Path: "pkg/"}}}},
		{"duplicate exact", &SourceScope{Files: []SourceFile{{Path: "a.go"}, {Path: "a.go"}}}},
		{"duplicate normalized", &SourceScope{Files: []SourceFile{{Path: "./a.go"}, {Path: "a.go"}}}},
		{"max count exceeded", func() *SourceScope {
			files := make([]SourceFile, MaxSourceScopeFiles+1)
			for i := range files {
				files[i] = SourceFile{Path: "f.go"}
			}
			return &SourceScope{Files: files}
		}()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateSourceScope(tc.s); err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
		})
	}
}

func TestSourceScopeUnmarshalJSON_OK(t *testing.T) {
	var s SourceScope
	in := `{"files":[{"path":"a.go"},{"path":"./b/c.go","start_line":2,"end_line":4}]}`
	if err := json.Unmarshal([]byte(in), &s); err != nil {
		t.Fatalf("valid json rejected: %v", err)
	}
	if len(s.Files) != 2 || s.Files[0].Path != "a.go" || s.Files[1].StartLine != 2 || s.Files[1].EndLine != 4 {
		t.Fatalf("unexpected decode: %+v", s)
	}
}

func TestSourceScopeUnmarshalJSON_Rejected(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"unknown scope field", `{"files":[],"extra":1}`},
		{"unknown file field", `{"files":[{"path":"a.go","glob":"*.go"}]}`},
		{"bad range", `{"files":[{"path":"a.go","start_line":9,"end_line":3}]}`},
		{"half range", `{"files":[{"path":"a.go","start_line":2}]}`},
		{"escape path", `{"files":[{"path":"../x"}]}`},
		{"wrong type", `{"files":"nope"}`},
		{"null bounds", `{"files":[{"path":"a.go","start_line":null,"end_line":null}]}`},
		{"null start", `{"files":[{"path":"a.go","start_line":null}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var s SourceScope
			if err := json.Unmarshal([]byte(tc.in), &s); err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
		})
	}
}

func TestSourceScopeUnmarshalJSON_TrailingValue(t *testing.T) {
	// json.Unmarshal rejects trailing data before UnmarshalJSON sees it, so
	// exercise the trailing-value check through the method directly.
	s := &SourceScope{}
	if err := s.UnmarshalJSON([]byte(`{"files":[]} {"files":[]}`)); err == nil {
		t.Fatal("expected trailing-data error")
	}
	if err := s.UnmarshalJSON([]byte(`{"files":[{"path":"a.go"}]}{}`)); err == nil {
		t.Fatal("expected trailing-data error after file entry")
	}
	if err := s.UnmarshalJSON([]byte(`{"files":[]} null`)); err == nil {
		t.Fatal("expected trailing null to be rejected")
	}
}

func TestSourceScopeUnmarshalJSON_InvalidLeavesReceiverUnchanged(t *testing.T) {
	s := &SourceScope{Files: []SourceFile{{Path: "a.go", StartLine: 1, EndLine: 2}}}
	before := len(s.Files)
	if err := s.UnmarshalJSON([]byte(`{"files":[{"path":"../x"}]}`)); err == nil {
		t.Fatal("expected decode error")
	}
	if len(s.Files) != before || s.Files[0].Path != "a.go" || s.Files[0].StartLine != 1 {
		t.Fatalf("receiver mutated on failed decode: %+v", s.Files)
	}
}

func TestSourceScopeUnmarshalJSON_EmptyAndNull(t *testing.T) {
	var empty SourceScope
	if err := json.Unmarshal([]byte(`{"files":[]}`), &empty); err != nil {
		t.Fatalf("empty files rejected: %v", err)
	}
	if empty.Files == nil {
		t.Fatal("explicit empty files must stay non-nil")
	}
	var nul SourceScope
	if err := json.Unmarshal([]byte(`null`), &nul); err != nil {
		t.Fatalf("null rejected: %v", err)
	}
}

func TestSourceScopeUnmarshalJSON_OmitEmptyRange(t *testing.T) {
	out, err := json.Marshal(&SourceScope{Files: []SourceFile{{Path: "a.go"}}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(out), "start_line") || strings.Contains(string(out), "end_line") {
		t.Fatalf("full-file entry should omit range fields: %s", out)
	}
}

func TestCloneSourceScope_Isolation(t *testing.T) {
	orig := &SourceScope{Files: []SourceFile{{Path: "./a.go", StartLine: 1, EndLine: 2}}}
	clone := CloneSourceScope(orig)
	if clone == orig {
		t.Fatal("clone must be a new value")
	}
	if clone.Files[0].Path != "./a.go" {
		t.Fatalf("clone must keep exact path, got %q", clone.Files[0].Path)
	}
	clone.Files[0].StartLine = 99
	clone.Files[0].Path = "mutated"
	if orig.Files[0].StartLine != 1 || orig.Files[0].Path != "./a.go" {
		t.Fatalf("input mutated by clone change: %+v", orig.Files[0])
	}
	orig.Files[0].StartLine = 5
	orig.Files[0].Path = "changed-orig"
	if clone.Files[0].StartLine != 99 || clone.Files[0].Path != "mutated" {
		t.Fatalf("clone mutated by input change: %+v", clone.Files[0])
	}
}

func TestCloneSourceScope_SliceIsolation(t *testing.T) {
	orig := &SourceScope{Files: []SourceFile{{Path: "a.go"}, {Path: "b.go"}}}
	clone := CloneSourceScope(orig)
	orig.Files = append(orig.Files, SourceFile{Path: "c.go"})
	if len(clone.Files) != 2 {
		t.Fatalf("clone shares backing array: %+v", clone.Files)
	}
	clone.Files[1].Path = "mutated"
	if orig.Files[1].Path != "b.go" {
		t.Fatalf("input mutated through clone: %+v", orig.Files[1])
	}
}

func TestCloneSourceScope_NilAndEmpty(t *testing.T) {
	if got := CloneSourceScope(nil); got != nil {
		t.Fatal("nil clone must stay nil")
	}
	empty := &SourceScope{}
	got := CloneSourceScope(empty)
	if got == nil || got.Files == nil {
		t.Fatal("explicit empty must clone to non-nil with non-nil Files")
	}
}

func TestCloneSourceScope_InvalidStaysNonNil(t *testing.T) {
	invalid := &SourceScope{Files: []SourceFile{{Path: "../x"}, {Path: "a\x00.go", StartLine: 9, EndLine: 3}}}
	got := CloneSourceScope(invalid)
	if got == nil {
		t.Fatal("invalid scope must clone to non-nil (nil means unconstrained)")
	}
	if err := ValidateSourceScope(got); err == nil {
		t.Fatal("clone of invalid scope must still fail validation")
	}
	if len(got.Files) != 2 || got.Files[0].Path != "../x" || got.Files[1].StartLine != 9 || got.Files[1].EndLine != 3 {
		t.Fatalf("clone must preserve constraints exactly: %+v", got.Files)
	}
}

func TestSourceScopeRoundtrip(t *testing.T) {
	in := `{"files":[{"path":"a.go"},{"path":"b.go","start_line":1,"end_line":1}]}`
	var s SourceScope
	if err := json.Unmarshal([]byte(in), &s); err != nil {
		t.Fatalf("decode: %v", err)
	}
	out, err := json.Marshal(CloneSourceScope(&s))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(out), `"path":"a.go"`) || !strings.Contains(string(out), `"start_line":1`) {
		t.Fatalf("unexpected roundtrip: %s", out)
	}
}
