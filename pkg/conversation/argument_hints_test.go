package conversation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/model"
)

func TestCompactToolArguments_CompatibleHints(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       map[string]any
	}{
		{"ordinary", `{"path":"src/main.go","content":"ignored"}`, map[string]any{"path": "src/main.go"}},
		{"escaped_key", `{"\u0070ath":"src/main.go"}`, map[string]any{"path": "src/main.go"}},
		{"case_sensitive", `{"PATH":"ignored","path":"lower","Path":"ignored too"}`, map[string]any{"path": "lower"}},
		{"last_duplicate", `{"path":"first","path":"last"}`, map[string]any{"path": "last"}},
		{"duplicate_null", `{"path":"first","path":null}`, nil},
		{"scalar_types", `{"path":4,"file":true,"name":false,"url":null,"pattern":{},"query":[]}`, map[string]any{"path": float64(4), "file": true, "name": false}},
		{"unknown_overflow", `{"path":"discard","future":1e999}`, nil},
		{"nested_overflow", `{"path":"discard","future":[{"nested":1e999}]}`, nil},
		{"selected_overflow", `{"path":"discard","command":1e999}`, nil},
		{"invalid", `{"path":"discard",`, nil},
		{"trailing", `{"path":"discard"} true`, nil},
		{"array", `["ignored"]`, nil},
		{"null", `null`, nil},
		{"unicode", `{"path":"日本語/é.go","command":"echo \"hello\"\nnext"}`, map[string]any{"path": "日本語/é.go", "command": "echo \"hello\"\nnext"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			arguments := strings.Repeat(" ", 4096) + tc.body
			digest := sha256.Sum256([]byte(arguments))
			want := map[string]any{"_buckley_compacted": true, "original_bytes": len(arguments), "sha256": hex.EncodeToString(digest[:6])}
			for key, value := range tc.want {
				want[key] = value
			}
			encoded, err := json.Marshal(want)
			if err != nil {
				t.Fatal(err)
			}
			if got := compactToolArguments(arguments, 2048); got != string(encoded) {
				t.Fatalf("summary = %s, want %s", got, encoded)
			}
		})
	}
}

func TestCompactToolArguments_StringHintPrefix(t *testing.T) {
	for _, value := range []string{
		strings.Repeat("ascii", 1000),
		strings.Repeat("é日本語🚀", 1000),
		strings.Repeat("\x00", 1000),
		strings.Repeat("x", 199) + "🚀" + strings.Repeat("\x00", 1000),
		strings.Repeat("x", 200) + "🚀" + strings.Repeat("y", 3000),
		strings.Repeat("\\\"\n\t", 1000),
	} {
		body, err := json.Marshal(map[string]any{"path": value, "content": strings.Repeat("source\n", 1000)})
		if err != nil {
			t.Fatal(err)
		}
		var summary map[string]any
		if err := json.Unmarshal([]byte(compactToolArguments(string(body), 2048)), &summary); err != nil {
			t.Fatal(err)
		}
		if summary["path"] != utf8Prefix(value, 200) {
			t.Fatalf("hint prefix changed: %q, want %q", summary["path"], utf8Prefix(value, 200))
		}
	}
	for _, prefix := range []int{198, 199, 200, 201, 202} {
		body := `{"path":"` + strings.Repeat(`\u0078`, prefix) + `\ud83d\ude80` + strings.Repeat(`\u0000`, 300) + `"}`
		var decoded map[string]string
		if err := json.Unmarshal([]byte(body), &decoded); err != nil {
			t.Fatal(err)
		}
		var summary map[string]any
		if err := json.Unmarshal([]byte(compactToolArguments(body, 1024)), &summary); err != nil {
			t.Fatal(err)
		}
		if summary["path"] != utf8Prefix(decoded["path"], 200) {
			t.Fatalf("escaped surrogate boundary %d changed: %q", prefix, summary["path"])
		}
	}
}

func BenchmarkCompactToolArguments_LargeWrite(b *testing.B) {
	for _, size := range []int{128 << 10, 1 << 20} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			body, err := json.Marshal(map[string]any{"path": "src/example.go", "content": strings.Repeat("source\n", size/7)})
			if err != nil {
				b.Fatal(err)
			}
			arguments := string(body)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if got := compactToolArguments(arguments, 512); len(got) > 512 || !strings.Contains(got, "src/example.go") {
					b.Fatal("summary changed")
				}
			}
		})
	}
}

func BenchmarkProjectModelMessages_LargeWrites(b *testing.B) {
	messages := []model.Message{{Role: "user", Content: "Implement and verify."}}
	for i := range 8 {
		body, err := json.Marshal(map[string]any{"path": fmt.Sprintf("src/file-%d.go", i), "content": strings.Repeat("source\n", 80<<10)})
		if err != nil {
			b.Fatal(err)
		}
		id := fmt.Sprintf("write-%d", i)
		messages = append(messages, model.Message{Role: "assistant", ToolCalls: []model.ToolCall{{ID: id, Function: model.FunctionCall{Name: "write_file", Arguments: string(body)}}}}, model.Message{Role: "tool", ToolCallID: id, Name: "write_file", Content: "ok"})
	}
	for range 20 {
		messages = append(messages, model.Message{Role: "user", Content: "Continue."}, model.Message{Role: "assistant", Content: "Completed."})
	}
	req := model.ChatRequest{Model: "fixture", MaxTokens: 2048, Messages: messages}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		got, stats := ProjectModelMessagesForRequest(messages, req, 32768, 1)
		if len(got) != len(messages) || !stats.Compacted {
			b.Fatal("projection shape changed")
		}
	}
}
