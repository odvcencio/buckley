package model

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestAnthropicProvider_OpusSignedToolLoop(t *testing.T) {
	const blocks = `[{"type":"thinking","thinking":"","signature":"signed-first","future_field":true},{"type":"tool_use","id":"toolu_1","name":"read_file","input":{"path":"README.md"}},{"type":"thinking","thinking":"","signature":"signed-second"},{"type":"text","text":"Reading."}]`
	turn := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		turn++
		if r.URL.Path != "/v1/messages" || r.Header.Get("x-api-key") != "test-key" {
			t.Errorf("unexpected request path or credential: %s", r.URL.Path)
		}
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if string(body["output_config"]) != `{"effort":"xhigh"}` {
			t.Errorf("output_config = %s", body["output_config"])
		}
		if _, ok := body["temperature"]; ok {
			t.Error("Opus 5.5 rejects temperature")
		}
		var tools []struct {
			InputSchema struct {
				Type string `json:"type"`
			} `json:"input_schema"`
		}
		if err := json.Unmarshal(body["tools"], &tools); err != nil || len(tools) != 1 || tools[0].InputSchema.Type != "object" {
			t.Fatalf("typed tool schema lost: %s (%v)", body["tools"], err)
		}
		w.Header().Set("Content-Type", "application/json")
		if turn == 1 {
			_, _ = io.WriteString(w, `{"id":"msg_1","model":"claude-opus-5-5","content":`+blocks+`,"stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":20}}`)
			return
		}
		var messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(body["messages"], &messages); err != nil {
			t.Fatal(err)
		}
		if len(messages) != 3 {
			t.Fatalf("messages = %s", body["messages"])
		}
		var want, got any
		_ = json.Unmarshal([]byte(blocks), &want)
		_ = json.Unmarshal(messages[1].Content, &got)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("signed blocks were changed: %s", messages[1].Content)
		}
		_, _ = io.WriteString(w, `{"id":"msg_2","model":"claude-opus-5-5","content":[{"type":"text","text":"Done."}],"stop_reason":"end_turn","usage":{"input_tokens":30,"output_tokens":5}}`)
	}))
	defer server.Close()
	p := NewAnthropicProvider("test-key", server.URL+"/v1/", false)
	req := ChatRequest{Model: "anthropic/claude-opus-5-5", Temperature: 0.7, MaxTokens: 64000, Reasoning: &ReasoningConfig{Effort: "xhigh"}, Messages: []Message{{Role: "user", Content: "Read README.md."}}}
	req.Tools = []map[string]any{{"type": "function", "function": map[string]any{"name": "read_file", "parameters": struct {
		Type string `json:"type"`
	}{Type: "object"}}}}
	response, err := p.ChatCompletion(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	assistant := response.Choices[0].Message
	if len(toStreamDelta(assistant).ReasoningDetails) != 1 {
		t.Fatal("stream adapter discarded native content")
	}
	encoded, err := json.Marshal(assistant)
	if err != nil {
		t.Fatal(err)
	}
	var restored Message
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	req.Messages = append(req.Messages, restored, Message{Role: "tool", ToolCallID: "toolu_1", Content: "readme contents"})
	req = applyProviderTransforms(req, "anthropic")
	response, err = p.ChatCompletion(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if response.Choices[0].Message.Content != "Done." || turn != 2 {
		t.Fatalf("tool loop did not complete: %#v, turns=%d", response, turn)
	}
}

func TestAnthropicProvider_OpusRejectsUnsupportedControls(t *testing.T) {
	for _, choice := range []string{"required", "read_file"} {
		if _, err := (&AnthropicProvider{}).toAnthropicRequest(ChatRequest{Model: "anthropic/claude-opus-5-5", ToolChoice: choice}, false); err == nil {
			t.Errorf("expected unsupported tool choice %q to fail before dispatch", choice)
		}
	}
	disabled := false
	for _, reasoning := range []*ReasoningConfig{{Enabled: &disabled}, {Effort: "ultra"}} {
		if _, err := (&AnthropicProvider{}).toAnthropicRequest(ChatRequest{Model: "anthropic/claude-opus-5-5", Reasoning: reasoning}, false); err == nil {
			t.Errorf("expected unsupported reasoning %#v to fail before dispatch", reasoning)
		}
	}
}
