package acp

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// TestMcpServer_WireShapes locks the three transport shapes the ACP schema
// allows for session/new's mcpServers array: stdio is untagged (no "type"
// on the wire), http and sse carry an explicit "type" discriminator plus a
// url. A single flat McpServer struct must decode all three correctly.
func TestMcpServer_WireShapes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		json string
		want McpServer
	}{
		{
			name: "stdio (untagged)",
			json: `{"name":"filesystem","command":"/usr/bin/mcp-fs","args":["--root","/tmp"],"env":[{"name":"FOO","value":"bar"}]}`,
			want: McpServer{
				Name:    "filesystem",
				Command: "/usr/bin/mcp-fs",
				Args:    []string{"--root", "/tmp"},
				Env:     []EnvVariable{{Name: "FOO", Value: "bar"}},
			},
		},
		{
			name: "http",
			json: `{"type":"http","name":"remote","url":"https://example.com/mcp"}`,
			want: McpServer{Type: "http", Name: "remote", URL: "https://example.com/mcp"},
		},
		{
			name: "sse",
			json: `{"type":"sse","name":"remote-sse","url":"https://example.com/mcp/sse"}`,
			want: McpServer{Type: "sse", Name: "remote-sse", URL: "https://example.com/mcp/sse"},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var got McpServer
			if err := json.Unmarshal([]byte(tc.json), &got); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got.Type != tc.want.Type || got.Name != tc.want.Name || got.Command != tc.want.Command ||
				got.URL != tc.want.URL || len(got.Args) != len(tc.want.Args) || len(got.Env) != len(tc.want.Env) {
				t.Fatalf("decoded = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestAgent_SessionNew_StoresMcpServers locks S5's prerequisite: session/new
// mcpServers must land on the created AgentSession instead of being parsed
// and discarded, so a session-scoped MCP manager can be built from it.
func TestAgent_SessionNew_StoresMcpServers(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	agent := NewAgent("test", "0.1", AgentHandlers{})
	agent.transport = NewTransport(strings.NewReader(""), &out)

	params := NewSessionParams{
		Cwd: "/workspace",
		McpServers: []McpServer{
			{Name: "filesystem", Command: "/usr/bin/mcp-fs", Args: []string{"--root", "/workspace"}},
			{Type: "http", Name: "remote", URL: "https://example.com/mcp"},
		},
	}
	req := &Request{JSONRPC: "2.0", ID: "1", Method: "session/new", Params: mustMarshal(t, params)}
	agent.handleSessionNew(nil, req) //nolint:staticcheck // ctx unused by this handler's fast path

	var resp Response
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	data, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var result NewSessionResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("unmarshal NewSessionResult: %v", err)
	}

	agent.sessionsMu.RLock()
	session, ok := agent.sessions[result.SessionID]
	agent.sessionsMu.RUnlock()
	if !ok {
		t.Fatalf("session %q not found", result.SessionID)
	}
	if len(session.McpServers) != 2 {
		t.Fatalf("session.McpServers = %+v, want 2 entries", session.McpServers)
	}
	if session.McpServers[0].Name != "filesystem" || session.McpServers[0].Command != "/usr/bin/mcp-fs" {
		t.Fatalf("session.McpServers[0] = %+v, want stdio filesystem server", session.McpServers[0])
	}
	if session.McpServers[1].Type != McpServerKindHTTP || session.McpServers[1].Name != "remote" {
		t.Fatalf("session.McpServers[1] = %+v, want http remote server", session.McpServers[1])
	}
}
