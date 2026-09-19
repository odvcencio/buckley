package artifactv1

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

func TestArtifactV1_LiteralPayloadsSurviveNormalizationAndJSON(t *testing.T) {
	for _, literal := range []string{"\t value  \n\n", "\tvalue\r\n\r\n", "literal \\n and `ticks`\n", "\u2003value\u00a0"} {
		t.Run(literal, func(t *testing.T) {
			source := Artifact{
				SchemaVersion: SchemaVersion, Kind: KindSubagentResult, Status: StatusCompleted,
				Title: " Source ", Summary: " Observed source ",
				Blocks: []Block{
					{Kind: BlockTable, Table: &Table{Headers: []string{" Literal "}, Rows: [][]string{{literal}, {" \t "}, {""}}}},
					{Kind: BlockCode, Code: &CodeBlock{Language: " go ", Content: literal}},
					{Kind: BlockDiff, Diff: &DiffBlock{Path: " change.diff ", Content: literal}},
					{Kind: BlockFacts, Facts: []Fact{{Label: " Literal ", Value: literal}}},
					{Kind: BlockOperationSummary, Operation: &OperationSummary{Operation: " read ", Status: "completed", Metrics: []Fact{{Label: " Literal ", Value: literal}}}},
				},
			}
			before, err := json.Marshal(source)
			if err != nil {
				t.Fatal(err)
			}
			normalized := source.Normalized()
			assertLiteralPayload := func(stage string, got Artifact) {
				t.Helper()
				values := []string{got.Blocks[0].Table.Rows[0][0], got.Blocks[0].Table.Rows[1][0], got.Blocks[0].Table.Rows[2][0], got.Blocks[1].Code.Content, got.Blocks[2].Diff.Content, got.Blocks[3].Facts[0].Value, got.Blocks[4].Operation.Metrics[0].Value}
				want := []string{literal, " \t ", "", literal, literal, literal, literal}
				if !reflect.DeepEqual(values, want) {
					t.Errorf("%s changed literal payloads: got %q, want %q", stage, values, want)
				}
			}
			assertLiteralPayload("normalization", normalized)
			if normalized.Title != "Source" || normalized.Blocks[0].Table.Headers[0] != "Literal" || normalized.Blocks[1].Code.Language != "go" || normalized.Blocks[2].Diff.Path != "change.diff" || normalized.Blocks[3].Facts[0].Label != "Literal" {
				t.Fatal("presentation labels should still be normalized")
			}
			if !reflect.DeepEqual(normalized, normalized.Normalized()) {
				t.Fatal("normalization is not idempotent")
			}
			after, err := json.Marshal(source)
			if err != nil {
				t.Fatal(err)
			}
			if string(before) != string(after) {
				t.Fatal("normalization mutated input")
			}
			rendered, err := RenderJSON(normalized)
			if err != nil {
				t.Fatal(err)
			}
			decoded, report, err := DecodeProviderOutput(context.Background(), rendered, OutputPromptJSON, DecodeOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if report.Repaired {
				t.Fatal("valid artifact unexpectedly required repair")
			}
			assertLiteralPayload("JSON round trip", decoded)
			// A missing ID triggers local omission repair, which must not rewrite evidence.
			source.ArtifactID = ""
			repairInput, err := json.Marshal(source)
			if err != nil {
				t.Fatal(err)
			}
			repaired, repairReport, err := DecodeProviderOutput(context.Background(), repairInput, OutputPromptJSON, DecodeOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if !repairReport.Repaired {
				t.Fatal("missing ID should require repair")
			}
			assertLiteralPayload("local omission repair", repaired)
		})
	}
}

func TestArtifactV1_ContentIdentityDistinguishesLiteralWhitespace(t *testing.T) {
	for _, kind := range []BlockKind{BlockCode, BlockDiff, BlockTable, BlockFacts} {
		t.Run(string(kind), func(t *testing.T) {
			makeArtifact := func(value string) Artifact {
				block := Block{Kind: kind}
				switch kind {
				case BlockCode:
					block.Code = &CodeBlock{Content: value}
				case BlockDiff:
					block.Diff = &DiffBlock{Content: value}
				case BlockTable:
					block.Table = &Table{Headers: []string{"source"}, Rows: [][]string{{value}}}
				case BlockFacts:
					block.Facts = []Fact{{Label: "source", Value: value}}
				}
				return (Artifact{Kind: KindSubagentResult, Status: StatusCompleted, Title: "source", Summary: "source", Blocks: []Block{block}}).Normalized()
			}
			if makeArtifact("value").ArtifactID == makeArtifact("value\n").ArtifactID {
				t.Fatal("distinct source bytes collapsed to one artifact ID")
			}
		})
	}
}
