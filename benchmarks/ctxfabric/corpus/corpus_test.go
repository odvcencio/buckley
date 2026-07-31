package corpus

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func manifestPath(t *testing.T) string {
	t.Helper()
	return filepath.Join("..", "..", "..", "testdata", "agent_eval", "corpus.yaml")
}

func TestLoad_ParsesCorpusManifest(t *testing.T) {
	m, err := Load(manifestPath(t))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if m.Version != 1 {
		t.Fatalf("unexpected manifest version: %d", m.Version)
	}
	if len(m.Scenarios) < 20 {
		t.Fatalf("expected at least 20 scenarios (spec section 31 minimum), got %d", len(m.Scenarios))
	}

	requiredCategories := []string{"exploration", "implementation", "debug", "review", "resume"}
	seen := map[string]bool{}
	for _, s := range m.Scenarios {
		seen[s.Category] = true
		if len(s.AcceptanceCriteria) == 0 {
			t.Errorf("scenario %s has no acceptance criteria", s.ID)
		}
		if len(s.ExpectedEvidence) == 0 {
			t.Errorf("scenario %s has no expected evidence", s.ID)
		}
		if len(s.AllowedNondeterminism) == 0 {
			t.Errorf("scenario %s has no allowed nondeterminism", s.ID)
		}
	}
	for _, cat := range requiredCategories {
		if !seen[cat] {
			t.Errorf("corpus is missing required category %q", cat)
		}
	}
}

func TestWithFixtures_CoversRequiredCategories(t *testing.T) {
	m, err := Load(manifestPath(t))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	withFixtures := m.WithFixtures()
	if len(withFixtures) == 0 {
		t.Fatal("expected at least one scenario with a runnable fixture")
	}

	requiredCategories := []string{"exploration", "implementation", "debug", "review", "resume"}
	seen := map[string]bool{}
	for _, s := range withFixtures {
		seen[s.Category] = true
	}
	for _, cat := range requiredCategories {
		if !seen[cat] {
			t.Errorf("no runnable fixture covers required category %q", cat)
		}
	}
}

func TestBuildRequest_IsDeterministic(t *testing.T) {
	m, err := Load(manifestPath(t))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	tools := []map[string]any{{"type": "function", "function": map[string]any{"name": "read_file"}}}

	for _, s := range m.WithFixtures() {
		s := s
		t.Run(s.ID, func(t *testing.T) {
			req1, calls1, err := s.BuildRequest(tools)
			if err != nil {
				t.Fatalf("BuildRequest: %v", err)
			}
			req2, calls2, err := s.BuildRequest(tools)
			if err != nil {
				t.Fatalf("BuildRequest (second run): %v", err)
			}
			if calls1 != calls2 {
				t.Fatalf("tool call count not deterministic: %d vs %d", calls1, calls2)
			}
			b1, err := json.Marshal(req1)
			if err != nil {
				t.Fatalf("marshal req1: %v", err)
			}
			b2, err := json.Marshal(req2)
			if err != nil {
				t.Fatalf("marshal req2: %v", err)
			}
			if string(b1) != string(b2) {
				t.Fatalf("BuildRequest is not deterministic for scenario %s", s.ID)
			}
			if len(req1.Messages) < 2 {
				t.Fatalf("expected at least a system and user message, got %d", len(req1.Messages))
			}
			if req1.Messages[0].Role != "system" {
				t.Fatalf("expected first message to be system, got %s", req1.Messages[0].Role)
			}
		})
	}
}

func TestBuildRequest_RejectsUnpairedToolResult(t *testing.T) {
	s := Scenario{
		ID: "broken",
		Fixture: &Fixture{
			Seed:              1,
			SystemPromptBytes: 10,
			Turns: []Turn{
				{Role: "tool", Name: "read_file", Bytes: 10},
			},
		},
	}
	if _, _, err := s.BuildRequest(nil); err == nil {
		t.Fatal("expected error for tool result with no pending call")
	}
}

func TestBuildRequest_RejectsUnresolvedToolCall(t *testing.T) {
	s := Scenario{
		ID: "broken",
		Fixture: &Fixture{
			Seed:              1,
			SystemPromptBytes: 10,
			Turns: []Turn{
				{Role: "assistant", Bytes: 10, ToolCalls: []string{"read_file"}},
			},
		},
	}
	if _, _, err := s.BuildRequest(nil); err == nil {
		t.Fatal("expected error for tool call with no result turn")
	}
}

func TestLoremBytes_ProducesExactLength(t *testing.T) {
	m, err := Load(manifestPath(t))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	for _, s := range m.WithFixtures() {
		req, _, err := s.BuildRequest(nil)
		if err != nil {
			t.Fatalf("BuildRequest(%s): %v", s.ID, err)
		}
		sys, ok := req.Messages[0].Content.(string)
		if !ok {
			t.Fatalf("system content is not a string for %s", s.ID)
		}
		if len(sys) != s.Fixture.SystemPromptBytes {
			t.Errorf("%s: system prompt length = %d, want %d", s.ID, len(sys), s.Fixture.SystemPromptBytes)
		}
	}
}
