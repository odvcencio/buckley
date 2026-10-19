package tool

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/mock/gomock"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

func TestToOpenAIFunction(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	tool := NewMockTool(ctrl)
	tool.EXPECT().Name().Return("coverage_check")
	tool.EXPECT().Description().Return("Report test coverage")
	tool.EXPECT().Parameters().Return(builtin.ParameterSchema{
		Type: "object",
	})

	fn := ToOpenAIFunction(tool)
	function, ok := fn["function"].(map[string]any)
	if !ok {
		t.Fatalf("expected function map in response")
	}
	if function["name"] != "coverage_check" {
		t.Fatalf("expected function name coverage_check, got %v", function["name"])
	}
	if function["description"] != "Report test coverage" {
		t.Fatalf("unexpected description: %v", function["description"])
	}
}

func TestJSONRoundTrip(t *testing.T) {
	SetResultEncoding(false)
	t.Cleanup(func() { SetResultEncoding(true) })
	result := &builtin.Result{Success: true, Data: map[string]any{"files": 2}}
	jsonStr, err := ToJSON(result)
	if err != nil {
		t.Fatalf("ToJSON returned err: %v", err)
	}
	parsed, err := FromJSON(jsonStr)
	if err != nil {
		t.Fatalf("FromJSON returned err: %v", err)
	}
	if parsed.Success != result.Success {
		t.Fatalf("parsed result mismatch: %+v", parsed)
	}
	if parsed.Data["files"].(float64) != 2 {
		t.Fatalf("metadata not preserved: %+v", parsed.Data)
	}
}

func TestToJSONUsesToonByDefault(t *testing.T) {
	SetResultEncoding(true)
	jsonStr, err := ToJSON(&builtin.Result{Success: true})
	if err != nil {
		t.Fatalf("ToJSON returned err: %v", err)
	}
	if strings.HasPrefix(jsonStr, "{") {
		t.Fatalf("expected TOON payload, got %s", jsonStr)
	}
}

func TestToModelOutputWithLimit_ReturnsValidBoundedPayload(t *testing.T) {
	SetResultEncoding(false)
	t.Cleanup(func() { SetResultEncoding(true) })
	result := &builtin.Result{
		Success: true,
		Data:    map[string]any{"output": strings.Repeat("large tool output ", 4000)},
	}
	const limit = 4096
	encoded, err := ToModelOutputWithLimit(result, limit)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > limit {
		t.Fatalf("model output = %d bytes, want <= %d", len(encoded), limit)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(encoded), &payload); err != nil {
		t.Fatalf("bounded payload is invalid JSON: %v", err)
	}
	if payload["truncated"] != true {
		t.Fatalf("bounded payload does not identify truncation: %#v", payload)
	}
	if payload["output_head"] == "" || payload["output_tail"] == "" {
		t.Fatalf("bounded payload omitted useful boundaries: %#v", payload)
	}
}

func TestReadFileModelOutputKeepsPageContinuationForLongFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "campaign.md")
	lines := make([]string, 1051)
	for i := range lines {
		lines[i] = fmt.Sprintf("section %04d %s", i+1, strings.Repeat("x", 72))
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := (&builtin.ReadFileTool{}).Execute(map[string]any{"path": path})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success || !result.ShouldAbridge {
		t.Fatalf("read result = %#v, want successful abridged page", result)
	}
	encoded, err := ToModelOutput(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(encoded, `"truncated":true`) {
		t.Fatalf("first page of long file was itself truncated: %s", encoded)
	}
	for _, want := range []string{"section 0001", "section 0100", "next_start_line", "1051"} {
		if !strings.Contains(encoded, want) {
			t.Fatalf("long-file page omitted %q: %s", want, encoded)
		}
	}
}

func TestSearchModelOutput_PagesPreserveStructuredEvidence(t *testing.T) {
	for _, useToon := range []bool{false, true} {
		for _, escaped := range []bool{false, true} {
			t.Run(fmt.Sprintf("toon=%v/escaped=%v", useToon, escaped), func(t *testing.T) {
				SetResultEncoding(useToon)
				t.Cleanup(func() { SetResultEncoding(true) })
				root := t.TempDir()
				padding := strings.Repeat("x", 8000)
				if escaped {
					padding = strings.Repeat("\x01", 4000)
				}
				if err := os.WriteFile(filepath.Join(root, "data.txt"), []byte(strings.Repeat("needle"+padding+"\n", 32)), 0o600); err != nil {
					t.Fatal(err)
				}
				search := &builtin.SearchTextTool{}
				search.SetWorkDir(root)
				for offset := 0; offset < 32; {
					result, err := search.Execute(map[string]any{"query": "needle", "path": "data.txt", "offset": offset, "limit": 500})
					if err != nil || !result.Success {
						t.Fatalf("search: %+v %v", result, err)
					}
					encoded, err := ToModelOutput(result)
					if err != nil || len(encoded) > DefaultModelOutputBytes {
						t.Fatalf("model encoding: bytes=%d error=%v", len(encoded), err)
					}
					if useToon {
						records := result.Data["matches"].([]map[string]any)
						if strings.Contains(encoded, "output_head") || !strings.Contains(encoded, fmt.Sprintf("matches[%d]", len(records))) || strings.Count(encoded, "needle") != len(records) {
							t.Fatalf("TOON page lost structured evidence at offset %d", offset)
						}
						offset += len(records)
						if strings.Contains(encoded, "next_offset") != (offset < 32) {
							t.Fatalf("TOON page lost continuation at offset %d", offset)
						}
						continue
					}
					decoded, err := FromJSON(encoded)
					if err != nil || !decoded.Success {
						t.Fatalf("model evidence could not be decoded: %v", err)
					}
					records, _ := decoded.Data["matches"].([]any)
					if len(records) == 0 || len(records) != len(result.Data["matches"].([]map[string]any)) || fmt.Sprint(decoded.Data["count"]) != "32" {
						t.Fatalf("model lost structured records or counts: offset=%d records=%d", offset, len(records))
					}
					for i, item := range records {
						record := item.(map[string]any)
						if fmt.Sprint(record["line"]) != fmt.Sprint(offset+i+1) || !strings.Contains(record["match"].(string), "needle") {
							t.Fatalf("model lost record identity or match at offset %d", offset+i)
						}
					}
					offset += len(records)
					if next, more := decoded.Data["next_offset"]; (offset < 32) != more || (more && fmt.Sprint(next) != fmt.Sprint(offset)) {
						t.Fatalf("model lost continuation: offset=%d next=%v", offset, next)
					}
				}
			})
		}
	}
}
