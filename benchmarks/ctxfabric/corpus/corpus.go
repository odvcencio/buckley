// Package corpus loads the PR 0 agent evaluation corpus
// (testdata/agent_eval/corpus.yaml) and turns each scenario's declared
// fixture shape into a deterministic model.ChatRequest, so the same
// manifest always produces the same measured metrics.
package corpus

import (
	"fmt"
	"math/rand"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"m31labs.dev/buckley/pkg/model"
)

// Manifest is the top-level corpus.yaml document.
type Manifest struct {
	Version   int        `yaml:"version"`
	Scenarios []Scenario `yaml:"scenarios"`
}

// Scenario is one curated evaluation task. Every scenario carries the
// metadata required by spec section 31 (acceptance criteria, expected
// evidence, allowed nondeterminism) regardless of whether it has a runnable
// Fixture yet.
type Scenario struct {
	ID                    string   `yaml:"id"`
	Category              string   `yaml:"category"`
	Name                  string   `yaml:"name"`
	Description           string   `yaml:"description"`
	AcceptanceCriteria    []string `yaml:"acceptance_criteria"`
	ExpectedEvidence      []string `yaml:"expected_evidence"`
	AllowedNondeterminism []string `yaml:"allowed_nondeterminism"`
	Fixture               *Fixture `yaml:"fixture"`
	FixtureStatus         string   `yaml:"fixture_status,omitempty"`
}

// Fixture declares the deterministic shape of a synthetic conversation:
// a system prompt of approximately SystemPromptBytes, followed by Turns in
// order.
type Fixture struct {
	Seed              int64  `yaml:"seed"`
	SystemPromptBytes int    `yaml:"system_prompt_bytes"`
	Turns             []Turn `yaml:"turns"`
	Note              string `yaml:"note,omitempty"`
}

// Turn is one message in a synthetic conversation. Role is one of "user",
// "assistant", or "tool". ToolCalls (assistant only) lists illustrative
// tool-call labels; Name (tool only) is the tool the result belongs to.
type Turn struct {
	Role      string   `yaml:"role"`
	Bytes     int      `yaml:"bytes"`
	ToolCalls []string `yaml:"tool_calls,omitempty"`
	Name      string   `yaml:"name,omitempty"`
}

// Load reads and parses a corpus manifest from path.
func Load(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading corpus manifest: %w", err)
	}
	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parsing corpus manifest: %w", err)
	}
	for i := range m.Scenarios {
		if m.Scenarios[i].ID == "" {
			return nil, fmt.Errorf("scenario %d has no id", i)
		}
	}
	return &m, nil
}

// WithFixtures returns only the scenarios that carry a runnable Fixture.
func (m *Manifest) WithFixtures() []Scenario {
	out := make([]Scenario, 0, len(m.Scenarios))
	for _, s := range m.Scenarios {
		if s.Fixture != nil {
			out = append(out, s)
		}
	}
	return out
}

// BuildRequest deterministically synthesizes a model.ChatRequest for a
// scenario's fixture. tools is attached verbatim, mirroring how a real
// request always carries the full current tool schema regardless of which
// tools a given turn happens to call. It returns the request and the number
// of tool calls the fixture declares.
func (s Scenario) BuildRequest(tools []map[string]any) (model.ChatRequest, int, error) {
	if s.Fixture == nil {
		return model.ChatRequest{}, 0, fmt.Errorf("scenario %s has no fixture", s.ID)
	}
	rng := rand.New(rand.NewSource(s.Fixture.Seed))

	messages := make([]model.Message, 0, len(s.Fixture.Turns)+1)
	messages = append(messages, model.Message{
		Role:    "system",
		Content: loremBytes(rng, s.Fixture.SystemPromptBytes),
	})

	pendingToolNames := make([]string, 0, 4)
	callCounter := 0
	toolCallCount := 0

	for i, turn := range s.Fixture.Turns {
		switch turn.Role {
		case "user":
			messages = append(messages, model.Message{
				Role:    "user",
				Content: loremBytes(rng, turn.Bytes),
			})
		case "assistant":
			if len(turn.ToolCalls) == 0 {
				messages = append(messages, model.Message{
					Role:    "assistant",
					Content: loremBytes(rng, turn.Bytes),
				})
				continue
			}
			calls := make([]model.ToolCall, 0, len(turn.ToolCalls))
			for _, name := range turn.ToolCalls {
				callCounter++
				id := fmt.Sprintf("call_%d", callCounter)
				calls = append(calls, model.ToolCall{
					ID:   id,
					Type: "function",
					Function: model.FunctionCall{
						Name:      name,
						Arguments: loremBytes(rng, 48),
					},
				})
				pendingToolNames = append(pendingToolNames, id)
			}
			toolCallCount += len(calls)
			content := ""
			if turn.Bytes > 0 {
				content = loremBytes(rng, turn.Bytes)
			}
			messages = append(messages, model.Message{
				Role:      "assistant",
				Content:   content,
				ToolCalls: calls,
			})
		case "tool":
			if len(pendingToolNames) == 0 {
				return model.ChatRequest{}, 0, fmt.Errorf("scenario %s turn %d: tool result with no pending tool call", s.ID, i)
			}
			id := pendingToolNames[0]
			pendingToolNames = pendingToolNames[1:]
			messages = append(messages, model.Message{
				Role:       "tool",
				Content:    loremBytes(rng, turn.Bytes),
				ToolCallID: id,
				Name:       turn.Name,
			})
		default:
			return model.ChatRequest{}, 0, fmt.Errorf("scenario %s turn %d: unknown role %q", s.ID, i, turn.Role)
		}
	}
	if len(pendingToolNames) != 0 {
		return model.ChatRequest{}, 0, fmt.Errorf("scenario %s: %d tool call(s) never received a result turn", s.ID, len(pendingToolNames))
	}

	req := model.ChatRequest{
		Model:    "benchmark/estimate",
		Messages: messages,
		Tools:    tools,
	}
	return req, toolCallCount, nil
}

var loremWords = []string{
	"context", "fabric", "evidence", "receipt", "bundle", "canopy", "buckley",
	"projection", "compaction", "checkpoint", "renderer", "policy", "budget",
	"pressure", "dedupe", "emergency", "signature", "symbol", "diff", "review",
	"commit", "task", "plan", "tool", "call", "result", "file", "path",
	"repository", "index", "graph", "blast", "radius", "governor", "loop",
	"model", "provider", "tokens", "estimate", "schema", "manifest",
}

// loremBytes deterministically generates a string of exactly n bytes of
// filler content from rng. Reproducibility depends only on the seed and
// requested length, not on wall-clock time or map iteration order.
func loremBytes(rng *rand.Rand, n int) string {
	if n <= 0 {
		return ""
	}
	var b strings.Builder
	b.Grow(n + 16)
	for b.Len() < n {
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(loremWords[rng.Intn(len(loremWords))])
	}
	s := b.String()
	if len(s) > n {
		s = s[:n]
	}
	return s
}
