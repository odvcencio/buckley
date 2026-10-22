package conversation

import (
	"fmt"
	"m31labs.dev/buckley/pkg/model"
	"strings"
	"testing"
)

func BenchmarkCompactModelMessages_RecentToolHistory(b *testing.B) {
	for _, pairs := range []int{1, 8, 16} {
		b.Run(fmt.Sprintf("pairs=%d", pairs), func(b *testing.B) {
			messages := []model.Message{{Role: "system", Content: "Use exact recent evidence."}, {Role: "user", Content: "Inspect the sources."}}
			for i := range pairs {
				id := fmt.Sprintf("call-%d", i)
				messages = append(messages, model.Message{Role: "assistant", ToolCalls: []model.ToolCall{{ID: id, Type: "function", Function: model.FunctionCall{Name: "read_file", Arguments: fmt.Sprintf(`{"path":"file-%d.go"}`, i)}}}}, model.Message{Role: "tool", Name: "read_file", ToolCallID: id, Content: strings.Repeat("recent observed evidence\n", 48)})
			}
			messages = append(messages, model.Message{Role: "user", Content: "Continue."})
			opts := DefaultEfficientContextOptions()
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				projected := CompactModelMessages(messages, opts)
				if len(projected) != len(messages) || projected[len(projected)-2].Content != messages[len(messages)-2].Content {
					b.Fatal("recent evidence changed")
				}
			}
		})
	}
}
