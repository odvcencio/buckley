package builtin

import (
	"encoding/json"
	"testing"

	artifactv1 "m31labs.dev/buckley/pkg/artifact/v1"
)

func TestSubmitArtifactPreservesLiteralSource(t *testing.T) {
	const literal = "\tinput: `line\\n`,\r\n\r\n"
	artifact := artifactv1.New(artifactv1.KindSubagentResult, artifactv1.StatusCompleted, "source", "observed source")
	artifact.Blocks = []artifactv1.Block{
		{Kind: artifactv1.BlockCode, Code: &artifactv1.CodeBlock{Content: literal}},
		{Kind: artifactv1.BlockTable, Table: &artifactv1.Table{Headers: []string{"source"}, Rows: [][]string{{literal}}}},
	}
	raw, err := json.Marshal(artifact)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	sink := &ArtifactSubmission{}
	tool := &SubmitArtifactTool{Submission: sink}
	result, err := tool.Execute(map[string]any{"artifact": payload})
	if err != nil || result == nil || !result.Success {
		t.Fatalf("submission failed: result=%+v err=%v", result, err)
	}
	got, ok := sink.Artifact()
	if !ok || got.Blocks[0].Code.Content != literal || got.Blocks[1].Table.Rows[0][0] != literal {
		t.Fatalf("submission changed source: %+v", got.Blocks)
	}
	got.Blocks[0].Code.Content = "caller mutation"
	got.Blocks[1].Table.Rows[0][0] = "caller mutation"
	again, ok := sink.Artifact()
	if !ok || again.Blocks[0].Code.Content != literal || again.Blocks[1].Table.Rows[0][0] != literal {
		t.Fatal("caller mutation changed stored artifact")
	}
}
