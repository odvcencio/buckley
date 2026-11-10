package conversation

import (
	"fmt"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/model"
)

var benchmarkProjectedMessages []model.Message

func BenchmarkContextProjection(b *testing.B) {
	for _, size := range []int{1024, 64 * 1024} {
		b.Run(fmt.Sprintf("tool_bytes_%d", size), func(b *testing.B) {
			messages := []model.Message{{Role: "system", Content: "Work carefully."}, {Role: "user", Content: "Investigate and repair the repository."}}
			for i := 0; i < 200; i++ {
				id := fmt.Sprintf("call-%d", i)
				messages = append(messages,
					model.Message{Role: "assistant", ToolCalls: []model.ToolCall{{ID: id, Type: "function", Function: model.FunctionCall{Name: "read_file", Arguments: fmt.Sprintf(`{"path":"file-%d.go"}`, i)}}}},
					model.Message{Role: "tool", ToolCallID: id, Name: "read_file", Content: fmt.Sprintf("file-%d\n", i) + strings.Repeat("repository evidence\n", size/20)},
				)
			}
			request := model.ChatRequest{Model: "test", MaxTokens: 4096}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				benchmarkProjectedMessages, _ = ProjectModelMessagesForRequest(messages, request, 128*1024, 1)
			}
		})
	}
}
