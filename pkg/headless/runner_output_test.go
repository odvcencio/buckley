package headless

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/conversation"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/storage"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

func TestRunner_ReadPageSurvivesToolDispatchAndReload(t *testing.T) {
	for _, useToon := range []bool{false, true} {
		t.Run(fmt.Sprintf("toon=%t", useToon), func(t *testing.T) {
			tool.SetResultEncoding(useToon)
			t.Cleanup(func() { tool.SetResultEncoding(true) })
			root := t.TempDir()
			lines := make([]string, 250)
			for i := range lines {
				lines[i] = fmt.Sprintf("line_%03d %s", i+1, strings.Repeat("x", 80))
			}
			if err := os.WriteFile(filepath.Join(root, "page.txt"), []byte(strings.Join(lines, "\n")), 0600); err != nil {
				t.Fatal(err)
			}
			store := newTestStore(t)
			session := &storage.Session{ID: "read-page", ProjectPath: root, CreatedAt: time.Now(), LastActive: time.Now(), Status: storage.SessionStatusActive}
			if err := store.CreateSession(session); err != nil {
				t.Fatal(err)
			}
			reader := &builtin.ReadFileTool{}
			reader.SetWorkDir(root)
			registry := tool.NewEmptyRegistry()
			registry.Register(reader)
			runner := &Runner{sessionID: session.ID, session: session, conv: conversation.New(session.ID), store: store, tools: registry}
			call := model.ToolCall{ID: "page-call", Type: "function", Function: model.FunctionCall{
				Name: "read_file", Arguments: `{"path":"page.txt","start_line":101,"end_line":200,"line_numbers":true}`,
			}}
			if err := runner.handleToolCalls(context.Background(), model.Message{ToolCalls: []model.ToolCall{call}}); err != nil {
				t.Fatal(err)
			}
			loaded := conversation.New(session.ID)
			if err := loaded.LoadFromStorage(store); err != nil {
				t.Fatal(err)
			}
			messages := loaded.ToModelMessages()
			if len(messages) != 2 || messages[1].Role != "tool" || messages[1].ToolCallID != call.ID {
				t.Fatalf("persisted tool exchange = %+v", messages)
			}
			output := model.ExtractTextContentOrEmpty(messages[1].Content)
			for _, want := range []string{"line_101", "line_200", "next_start_line", "201"} {
				if !strings.Contains(output, want) {
					t.Errorf("model output omitted %q", want)
				}
			}
			for _, absent := range []string{"line_001", "line_201", "line_250", "101: line_101"} {
				if strings.Contains(output, absent) {
					t.Errorf("model output contains unrequested or display-only content %q", absent)
				}
			}
			if len(output) > tool.DefaultModelOutputBytes {
				t.Fatalf("model output = %d bytes, limit = %d", len(output), tool.DefaultModelOutputBytes)
			}
			if !useToon && !json.Valid([]byte(output)) {
				t.Fatal("model output is not valid JSON")
			}
		})
	}
}

func TestRunner_ToolOutputIsBounded(t *testing.T) {
	for _, useToon := range []bool{false, true} {
		t.Run(fmt.Sprintf("toon=%t", useToon), func(t *testing.T) {
			tool.SetResultEncoding(useToon)
			t.Cleanup(func() { tool.SetResultEncoding(true) })
			result := &builtin.Result{Success: true, Data: map[string]any{
				"stdout":    "BEGIN_OUTPUT\n" + strings.Repeat("large output 行\n", 10000) + "END_OUTPUT",
				"exit_code": 0,
			}}
			output := (&Runner{}).formatToolResult(result)
			if len(output) > tool.DefaultModelOutputBytes {
				t.Errorf("model output = %d bytes, limit = %d", len(output), tool.DefaultModelOutputBytes)
			}
			for _, want := range []string{"truncated", "original_bytes", "BEGIN_OUTPUT", "END_OUTPUT"} {
				if !strings.Contains(output, want) {
					t.Errorf("bounded output omitted %q", want)
				}
			}
			if !useToon && !json.Valid([]byte(output)) {
				t.Fatal("bounded output is not valid JSON")
			}
		})
	}
}
