package tool

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"m31labs.dev/buckley/pkg/mcp"
)

func TestMCPTool_LargeWireResultsStayBounded(t *testing.T) {
	manager := connectedFakeManager(t, "large", "-tools=0")
	for _, name := range []string{"echo", "fail"} {
		t.Run(name, func(t *testing.T) {
			_, def, found := manager.FindTool(name)
			if !found {
				t.Fatal("fixture tool missing")
			}
			result, err := newMCPTool(manager, "fake", def, 0).Execute(map[string]any{"text": "report"})
			if err != nil || result.Success != (name == "echo") {
				t.Fatalf("tool outcome lost: %+v, %v", result, err)
			}
			text := result.Error
			if result.Success {
				text, _ = result.Data["output"].(string)
			}
			if len(text) > 8<<10 || !utf8.ValidString(text) || !strings.Contains(text, "FIRST_EVIDENCE") || !strings.Contains(text, "FINAL_EVIDENCE: expected 37, got 36") {
				t.Fatalf("bounded preview lost evidence: bytes=%d valid_utf8=%t", len(text), utf8.ValidString(text))
			}
			if result.Data["truncated"] != true || result.Data["output_truncated"] != true || result.Data["output_bytes"] != int64(4_194_401) || result.Data["blocks_total"] != 32 || result.Data["blocks_omitted"] != 16 {
				t.Fatalf("truncation counts missing: %v", result.Data["truncated"])
			}
			if blocks, _ := result.Data["blocks"].([]string); len(blocks) != 16 {
				t.Fatalf("block summaries unbounded: %d", len(blocks))
			}
			raw, err := json.Marshal(result)
			if err != nil || len(raw) > 16<<10 {
				t.Fatalf("retained result unbounded: bytes=%d err=%v", len(raw), err)
			}
			restored, err := FromJSON(string(raw))
			if err != nil || restored.Success != result.Success {
				t.Fatalf("recorded result did not round-trip: %v", err)
			}
			for _, useToon := range []bool{false, true} {
				SetResultEncoding(useToon)
				wire, err := ToModelOutput(result)
				if err != nil || len(wire) > DefaultModelOutputBytes || !strings.Contains(wire, "FINAL_EVIDENCE: expected 37, got 36") {
					t.Fatalf("model observation lost terminal evidence: bytes=%d err=%v", len(wire), err)
				}
				replayed, err := ToModelOutput(restored)
				if err != nil || replayed != wire {
					t.Fatalf("recorded model observation changed: %v", err)
				}
				t.Logf("tool=%s toon=%t retained_json_bytes=%d model_bytes=%d source_blocks=32 omitted_blocks=16 success=%t terminal_evidence=true", name, useToon, len(raw), len(wire), result.Success)
			}
		})
	}
	t.Cleanup(func() { SetResultEncoding(true) })
}

func TestFlattenMCPContent_BoundsMetadataAndUTF8(t *testing.T) {
	text := "START\n" + strings.Repeat("界", 20000) + "\nFINAL_UTF8_EVIDENCE"
	detail := strings.Repeat("界", 20000)
	content := []sdkmcp.Content{
		&sdkmcp.TextContent{Text: text},
		&sdkmcp.ImageContent{MIMEType: detail, Data: []byte{1, 2, 3}},
		&sdkmcp.AudioContent{MIMEType: detail, Data: []byte{1, 2, 3}},
		&sdkmcp.ResourceLink{URI: "https://example.test/" + detail},
		&sdkmcp.EmbeddedResource{Resource: &sdkmcp.ResourceContents{URI: "file:///" + detail}},
	}
	for range 100 {
		content = append(content, &sdkmcp.ResourceLink{URI: "https://example.test/omitted"})
	}
	summary := flattenMCPContent(content)
	if len(summary.text) > mcpOutputBudgetBytes || !utf8.ValidString(summary.text) || !strings.HasPrefix(summary.text, "START") || !strings.HasSuffix(summary.text, "FINAL_UTF8_EVIDENCE") || summary.outputBytes != int64(len(text)) {
		t.Fatalf("text boundary/count lost: bytes=%d source_bytes=%d", len(summary.text), summary.outputBytes)
	}
	if !summary.outputTruncated || !summary.blocksTruncated || len(summary.blocks) != mcpBlockSummaryLimit {
		t.Fatal("truncation not reported")
	}
	for _, block := range summary.blocks {
		if len(block) > 256 || !utf8.ValidString(block) {
			t.Fatalf("metadata unbounded or invalid: bytes=%d", len(block))
		}
	}
	if content[0].(*sdkmcp.TextContent).Text != text || content[1].(*sdkmcp.ImageContent).MIMEType != detail {
		t.Fatal("source content mutated")
	}
	t.Logf("source_blocks=%d retained_blocks=%d source_bytes=%d output_bytes=%d utf8_valid=true binary_bodies_omitted=true", len(content), len(summary.blocks), len(text), len(summary.text))
}

func TestMCPTool_TransportFailureIsBounded(t *testing.T) {
	tool := &mcpTool{manager: mcp.NewManager(), server: "missing-" + strings.Repeat("界", 20000) + "-FINAL_RPC_EVIDENCE", def: &sdkmcp.Tool{Name: "fail"}, timeout: time.Second}
	result, err := tool.Execute(nil)
	if err != nil || result.Success || len(result.Error) > mcpOutputBudgetBytes || !utf8.ValidString(result.Error) || !strings.Contains(result.Error, "mcp tool call failed") || !strings.Contains(result.Error, "FINAL_RPC_EVIDENCE") || result.Data["truncated"] != true {
		t.Fatalf("transport diagnostic lost or unbounded: bytes=%d err=%v", len(result.Error), err)
	}
}

func BenchmarkFlattenMCPContent_Size(b *testing.B) {
	for _, size := range []int{64, 1 << 20, 8 << 20} {
		b.Run(fmt.Sprintf("bytes=%d", size), func(b *testing.B) {
			const count = 64
			content := make([]sdkmcp.Content, count)
			for i := range content {
				content[i] = &sdkmcp.TextContent{Text: strings.Repeat("x", size/count)}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				flattenMCPContent(content)
			}
		})
	}
}
