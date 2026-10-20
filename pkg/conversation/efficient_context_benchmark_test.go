package conversation

import (
	"fmt"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/model"
)

func BenchmarkCompactModelMessages_LargeToolHistory(b *testing.B) {
	for _, matched := range []bool{true, false} {
		b.Run(fmt.Sprintf("matched_calls=%t", matched), func(b *testing.B) {
			messages := []model.Message{{Role: "system", Content: "Keep results grounded."}, {Role: "user", Content: "Inspect and verify the project."}}
			for i := 0; i < 128; i++ {
				id := fmt.Sprintf("call-%d", i)
				if matched {
					messages = append(messages, model.Message{Role: "assistant", ToolCalls: []model.ToolCall{{ID: id, Type: "function", Function: model.FunctionCall{Name: "read_file", Arguments: fmt.Sprintf(`{"path":"file-%d.txt"}`, i)}}}})
				}
				messages = append(messages, model.Message{Role: "tool", Name: "read_file", ToolCallID: id, Content: fmt.Sprintf("file %d\n", i) + strings.Repeat("observed data ", 1890)})
			}
			messages = append(messages, model.Message{Role: "user", Content: "Continue."})
			opts := DefaultEfficientContextOptions()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				result := CompactModelMessages(messages, opts)
				if len(result) != len(messages) {
					b.Fatal("unexpected history collapse")
				}
			}
		})
	}
}
