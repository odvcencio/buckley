package builtin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"

	artifactv1 "m31labs.dev/buckley/pkg/artifact/v1"
)

func TestAllCapturedSourcesMatchesExplicitSelection(t *testing.T) {
	for _, throughTool := range []bool{false, true} {
		t.Run(fmt.Sprintf("tool=%t", throughTool), func(t *testing.T) {
			sink, explicit := &ArtifactSubmission{}, &ArtifactSubmission{}
			var ids []string
			want := map[string]string{}
			for i, content := range []string{"\tfirst\r\n", "unterminated", ""} {
				end := 1
				if content == "" {
					end = 0
				}
				result := sourceReadResult(fmt.Sprintf("/never-reread/page-%d", i), content, 1, end)
				ref, err := sink.CaptureReadSource(result)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := explicit.CaptureReadSource(result); err != nil {
					t.Fatal(err)
				}
				ids = append(ids, ref)
				want[ref] = content
			}
			a := artifactv1.New(artifactv1.KindAnalysis, artifactv1.StatusIncomplete, "Partial", "Observed source summary")
			a.IncompleteReasons = []string{"missing requested context"}
			selector := []string{"all"}
			if throughTool {
				result, err := (&SubmitArtifactTool{Submission: sink}).Execute(map[string]any{"artifact": a, "source_refs": []any{"all"}})
				if err != nil || result == nil || !result.Success {
					t.Fatalf("all selector rejected: %+v %v", result, err)
				}
			} else if err := sink.SubmitWithSources(a, selector); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(selector, []string{"all"}) {
				t.Fatal("submission mutated caller selector")
			}
			if err := explicit.SubmitWithSources(a, ids); err != nil {
				t.Fatal(err)
			}
			got, ok := sink.Artifact()
			expected, _ := explicit.Artifact()
			body, err := artifactv1.RenderJSON(got)
			if err != nil {
				t.Fatal(err)
			}
			gold, err := artifactv1.RenderJSON(expected)
			if err != nil {
				t.Fatal(err)
			}
			if !ok || !bytes.Equal(body, gold) {
				t.Fatal("all differs from path/line ordered explicit selection")
			}
			for _, row := range got.Blocks[0].Table.Rows {
				if row[4] != want[row[0]] || row[0] == "all" {
					t.Fatalf("snapshot identity or bytes changed: %#v", row)
				}
			}
			if got.Status != a.Status || got.Summary != a.Summary || !reflect.DeepEqual(got.IncompleteReasons, a.IncompleteReasons) {
				t.Fatal("all changed completion claims")
			}
			if err := sink.SubmitWithSources(a, []string{"all"}); err == nil {
				t.Fatal("all replaced finalized sink")
			}
		})
	}
}

func TestAllCapturedSourcesRejectsEmptyMixedAndModelEvidence(t *testing.T) {
	a := artifactv1.New(artifactv1.KindAnalysis, artifactv1.StatusCompleted, "Source", "unverified summary")
	for _, refs := range [][]string{{"all"}, {"all", "all"}, {"all", "src_unknown"}, {"ALL"}} {
		sink := &ArtifactSubmission{}
		if err := sink.SubmitWithSources(a, refs); err == nil {
			t.Fatalf("invalid or empty selector accepted: %v", refs)
		}
		if _, ok := sink.Artifact(); ok {
			t.Fatal("rejection finalized sink")
		}
	}
	for _, refs := range []any{[]any{"all", "all"}, []any{"all", "src_" + strings.Repeat("0", 64)}, "all", []any{"ALL"}} {
		if _, err := parseSourceRefs(refs); err == nil {
			t.Fatalf("invalid selector syntax accepted: %#v", refs)
		}
	}
	for _, forged := range []bool{false, true} {
		sink := &ArtifactSubmission{}
		ref, err := sink.CaptureReadSource(sourceReadResult("/source", "actual\n", 1, 1))
		if err != nil {
			t.Fatal(err)
		}
		modelArtifact := a
		if forged {
			modelArtifact.EvidenceRefs = []artifactv1.EvidenceRef{{ID: ref, Kind: "captured_source", URI: "file:///source"}}
		} else {
			modelArtifact.Blocks = []artifactv1.Block{{Kind: artifactv1.BlockProse, Text: "invented excerpt"}}
		}
		if err := sink.SubmitWithSources(modelArtifact, []string{"all"}); err == nil {
			t.Fatal("all bypassed host-owned evidence boundary")
		}
		if err := sink.SubmitWithSources(a, []string{ref}); err != nil {
			t.Fatalf("rejection lost valid subset: %v", err)
		}
	}
}

func TestAllCapturedSourcesPreservesOutputLimitAndCanSelectSubset(t *testing.T) {
	sink := &ArtifactSubmission{}
	var first string
	for i := 0; i < 10; i++ {
		ref, err := sink.CaptureReadSource(sourceReadResult(fmt.Sprintf("/page-%d", i), strings.Repeat("\x01", 10000), 1, 1))
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = ref
		}
	}
	a := artifactv1.New(artifactv1.KindAnalysis, artifactv1.StatusIncomplete, "Large", "bounded snapshots")
	a.IncompleteReasons = []string{"context incomplete"}
	if err := sink.SubmitWithSources(a, []string{"all"}); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("all bypassed aggregate bound: %v", err)
	}
	if _, ok := sink.Artifact(); ok || len(sink.sources) != 10 {
		t.Fatal("oversized submission finalized sink or discarded captures")
	}
	if err := sink.SubmitWithSources(a, []string{first}); err != nil {
		t.Fatal(err)
	}
	got, _ := sink.Artifact()
	body, _ := json.Marshal(got)
	if len(body) > artifactv1.MaxProviderBytes || len(got.Blocks[0].Table.Rows) != 1 || got.EvidenceRefs[0].ID != first {
		t.Fatal("subset selection lost bound or identity")
	}
}

func TestAllCapturedSourcesOrdersAllSelectionByPathAndLines(t *testing.T) {
	full := "one\ntwo\n" + strings.Repeat("pad\n", 7) + "ten\n"
	specs := []struct {
		path, full, visible string
		start, end          int
	}{
		{"/source-order/z.go", "z\n", "z", 1, 1},
		{"/source-order/a.go", full, "ten", 10, 10},
		{"/source-order/a.go", full, "two", 2, 2},
		{"/source-order/a.go", full, "one", 1, 1},
		{"/source-order/a.go", full, "one\ntwo", 1, 2},
		{"/source-order/a.go", "alternate\n", "alternate", 1, 1},
	}
	for _, mode := range []string{"all", "recovery", "explicit-reverse"} {
		t.Run(mode, func(t *testing.T) {
			sink := &ArtifactSubmission{}
			ids := make([]string, len(specs))
			for i, spec := range specs {
				result := sourceReadResult(spec.path, spec.full, spec.start, spec.end)
				result.ShouldAbridge = true
				result.DisplayData = map[string]any{"path": spec.path, "content": spec.visible, "page": result.Data["page"]}
				id, err := sink.CaptureReadSource(result)
				if err != nil {
					t.Fatal(err)
				}
				ids[i] = id
			}
			order := []int{3, 5, 4, 2, 1, 0}
			if ids[3] > ids[5] {
				order[0], order[1] = order[1], order[0]
			}
			a := artifactv1.New(artifactv1.KindAnalysis, artifactv1.StatusIncomplete, "Order", "unverified summary")
			a.IncompleteReasons = []string{"context incomplete"}
			var got artifactv1.Artifact
			if mode == "recovery" {
				got = sink.RecoveryArtifact()
				if _, submitted := sink.Artifact(); submitted || len(sink.sources) != len(specs) {
					t.Fatal("recovery finalized or discarded captures")
				}
			} else {
				refs := []string{"all"}
				if mode == "explicit-reverse" {
					refs = nil
					for i := len(order) - 1; i >= 0; i-- {
						refs = append(refs, ids[order[i]])
					}
					for i, j := 0, len(order)-1; i < j; i, j = i+1, j-1 {
						order[i], order[j] = order[j], order[i]
					}
				}
				if err := sink.SubmitWithSources(a, refs); err != nil {
					t.Fatal(err)
				}
				got, _ = sink.Artifact()
			}
			if got.Status != artifactv1.StatusIncomplete || len(got.Blocks) != 1 || got.Blocks[0].Table == nil || len(got.Blocks[0].Table.Rows) != len(specs) || len(got.EvidenceRefs) != len(specs) {
				t.Fatalf("unexpected artifact shape: %+v", got)
			}
			for i, index := range order {
				spec := specs[index]
				want := []string{ids[index], spec.path, strconv.Itoa(spec.start), strconv.Itoa(spec.end), spec.visible + "\n"}
				if !reflect.DeepEqual(got.Blocks[0].Table.Rows[i], want) || got.EvidenceRefs[i].ID != ids[index] {
					t.Fatalf("row %d differs from expected order/identity/bytes: got=%#v want=%#v", i, got.Blocks[0].Table.Rows[i], want)
				}
			}
		})
	}
}
