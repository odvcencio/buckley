package builtin

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	artifactv1 "m31labs.dev/buckley/pkg/artifact/v1"
)

func sourceReadResult(path, content string, start, end int) *Result {
	return &Result{Success: true, Data: map[string]any{"path": path, "content": content, "page": map[string]any{"start_line": start, "end_line": end}}}
}

func TestCapturedReadPagePreservesBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, content, want            string
		start, end, wantStart, wantEnd int
	}{
		{"LF", "before\n\tvalue\n\nafter\n", "\tvalue\n\n", 2, 3, 2, 3},
		{"CRLF", "before\r\n\tvalue\r\n\r\nafter\r\n", "\tvalue\r\n\r\n", 2, 3, 2, 3},
		{"unterminated", "before\nlast", "last", 2, 2, 2, 2},
		{"blank line", "\n", "\n", 1, 1, 1, 1},
		{"empty", "", "", 1, 0, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := capturedReadPage(sourceReadResult("/source.go", tc.content, tc.start, tc.end).Data)
			if err != nil || got.Content != tc.want || got.StartLine != tc.wantStart || got.EndLine != tc.wantEnd {
				t.Fatalf("got=%+v err=%v want=%q [%d,%d]", got, err, tc.want, tc.wantStart, tc.wantEnd)
			}
		})
	}
	for _, data := range []map[string]any{
		nil, {"path": "/source"},
		sourceReadResult("/source", "one\n", 2, 2).Data,
		sourceReadResult("/source", "one\n", 1, 2).Data,
		sourceReadResult("/source", "one\n", 1, 0).Data,
		sourceReadResult("/source", "one\n", 2, 1).Data,
	} {
		if _, err := capturedReadPage(data); err == nil {
			t.Fatalf("invalid page accepted: %#v", data)
		}
	}
}

func TestCapturedSourcesRejectUnknownAndBoundStorage(t *testing.T) {
	s := &ArtifactSubmission{}
	first, err := s.CaptureReadSource(sourceReadResult("/source", "value\n", 1, 1))
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.CaptureReadSource(sourceReadResult("/source", "value\n", 1, 1))
	if err != nil || again != first || len(s.sources) != 1 {
		t.Fatal("identical reads should reuse their capture")
	}
	artifact := artifactv1.New(artifactv1.KindSubagentResult, artifactv1.StatusIncomplete, "source", "one item missing")
	artifact.IncompleteReasons = []string{"missing item"}
	for name, refs := range map[string][]string{"unknown": {"src_" + strings.Repeat("0", 64)}, "duplicate": {first, first}} {
		if err := s.SubmitWithSources(artifact, refs); err == nil {
			t.Fatalf("%s references accepted", name)
		}
		if _, ok := s.Artifact(); ok {
			t.Fatal("rejected submission finalized sink")
		}
	}
	if err := (&ArtifactSubmission{}).SubmitWithSources(artifact, []string{first}); err == nil {
		t.Fatal("uncaptured cross-run reference accepted")
	}
	mixed := artifact
	mixed.Blocks = []artifactv1.Block{{Kind: artifactv1.BlockProse, Text: "model-written source"}}
	if err := s.SubmitWithSources(mixed, []string{first}); err == nil {
		t.Fatal("mixed model/captured blocks accepted")
	}
	if err := s.SubmitWithSources(artifact, []string{first}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Artifact()
	if got.Status != artifactv1.StatusIncomplete || len(got.IncompleteReasons) != 1 || got.ArtifactID == artifact.ArtifactID || got.Blocks[0].Table.Rows[0][4] != "value\n" {
		t.Fatalf("bad materialization: %+v", got)
	}
	if _, err := s.CaptureReadSource(sourceReadResult("/after", "after", 1, 1)); err == nil {
		t.Fatal("capture allowed after finalization")
	}
	limited := &ArtifactSubmission{}
	for i := 0; i < maxCapturedSources; i++ {
		if _, err := limited.CaptureReadSource(sourceReadResult(fmt.Sprintf("/s%d", i), "x", 1, 1)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := limited.CaptureReadSource(sourceReadResult("/extra", "x", 1, 1)); err == nil {
		t.Fatal("count budget not enforced")
	}
	if _, err := (&ArtifactSubmission{}).CaptureReadSource(sourceReadResult("/big", strings.Repeat("x", maxCapturedSourceBytes+1), 1, 1)); err == nil {
		t.Fatal("page byte bound not enforced")
	}
	if _, err := (&ArtifactSubmission{}).CaptureReadSource(sourceReadResult("/binary", string([]byte{0xff}), 1, 1)); err == nil {
		t.Fatal("invalid UTF-8 accepted")
	}
	total := &ArtifactSubmission{}
	for i := 0; i < 100; i++ {
		_, err := total.CaptureReadSource(sourceReadResult(fmt.Sprintf("/big%d", i), strings.Repeat("x", maxCapturedSourceBytes), 1, 1))
		if err != nil {
			break
		}
	}
	if total.sourceBytes > maxCapturedTotalBytes || len(total.sources) >= maxCapturedSources {
		t.Fatalf("total-byte bound ineffective: %d bytes %d pages", total.sourceBytes, len(total.sources))
	}
}

func TestCapturedArtifactBoundsEncodedOutput(t *testing.T) {
	s := &ArtifactSubmission{}
	var refs []string
	for i := 0; i < 6; i++ {
		ref, err := s.CaptureReadSource(sourceReadResult(fmt.Sprintf("/control%d", i), strings.Repeat("\x01", 9000), 1, 1))
		if err != nil {
			t.Fatal(err)
		}
		refs = append(refs, ref)
	}
	if err := s.SubmitWithSources(artifactv1.New(artifactv1.KindSubagentResult, artifactv1.StatusCompleted, "sources", "selected sources"), refs); err == nil {
		t.Fatal("oversized encoded artifact accepted")
	}
	if _, ok := s.Artifact(); ok {
		t.Fatal("oversized artifact finalized sink")
	}
}

func TestCapturedSourceJSONNumbersAndEmptyFile(t *testing.T) {
	for _, content := range []string{"", "first\nlast\n"} {
		end := 2
		if content == "" {
			end = 0
		}
		raw, err := json.Marshal(sourceReadResult("/source", content, 1, end))
		if err != nil {
			t.Fatal(err)
		}
		var result Result
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatal(err)
		}
		sink := &ArtifactSubmission{}
		ref, err := sink.CaptureReadSource(&result)
		if err != nil {
			t.Fatal(err)
		}
		if err := sink.SubmitWithSources(artifactv1.New(artifactv1.KindSubagentResult, artifactv1.StatusCompleted, "source", "captured source"), []string{ref}); err != nil {
			t.Fatal(err)
		}
		got, _ := sink.Artifact()
		if got.Blocks[0].Table.Rows[0][4] != content {
			t.Fatal("JSON-number page altered content")
		}
		if sink.sources != nil || sink.sourceBytes != 0 {
			t.Fatal("transient captures retained after submission")
		}
	}
}

func TestParseSourceRefsRejectsMalformedAndOversizedInput(t *testing.T) {
	for _, value := range []any{nil, "src_bad", true, []any{nil}, []any{1}, []string{"short"}, make([]string, maxCapturedSources+1)} {
		if _, err := parseSourceRefs(value); err == nil {
			t.Fatalf("accepted invalid source_refs %#v", value)
		}
	}
	ref := "src_" + strings.Repeat("a", 64)
	for _, value := range []any{[]string{}, []any{}, []string{ref}, []any{ref}} {
		if _, err := parseSourceRefs(value); err != nil {
			t.Fatal(err)
		}
	}
}
