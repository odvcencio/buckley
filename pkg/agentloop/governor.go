// Package agentloop provides shared safety and progress detection for model/tool loops.
package agentloop

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// Config controls bounded agent-loop execution and stagnation detection.
type Config struct {
	MaxRounds          int
	MaxToolCalls       int
	ExactRepeatLimit   int
	OutcomeRepeatLimit int
	CycleMaxLength     int
	CycleRepeats       int
}

// DefaultConfig is deliberately generous for productive agent work while
// preventing an unattended harness from spending indefinitely.
func DefaultConfig() Config {
	return Config{
		MaxRounds:          32,
		MaxToolCalls:       96,
		ExactRepeatLimit:   3,
		OutcomeRepeatLimit: 5,
		CycleMaxLength:     4,
		CycleRepeats:       3,
	}
}

// Decision describes a loop-governor intervention.
type Decision struct {
	Stop   bool
	Kind   string
	Reason string
	Nudge  string
	Count  int
}

// Governor tracks tool-loop progress independently of any model provider.
type Governor struct {
	config Config

	rounds    int
	toolCalls int

	exactCounts   map[string]int
	outcomeCounts map[string]int
	actionHistory []string
}

// New constructs a progress-aware loop governor.
func New(config Config) *Governor {
	config = normalizedConfig(config)
	return &Governor{
		config:        config,
		exactCounts:   make(map[string]int),
		outcomeCounts: make(map[string]int),
		actionHistory: make([]string, 0, config.CycleMaxLength*config.CycleRepeats*2),
	}
}

// BeginRound records the start of a model round and enforces the hard ceiling.
func (g *Governor) BeginRound() Decision {
	if g == nil {
		return Decision{}
	}
	g.rounds++
	if g.rounds <= g.config.MaxRounds {
		return Decision{}
	}
	reason := fmt.Sprintf("tool loop exceeded the %d-round harness limit", g.config.MaxRounds)
	return stopDecision("round_limit", reason, g.rounds)
}

// Observe records one completed tool action and detects repeated evidence or a
// short action/evidence cycle. Arguments and results are canonicalized as JSON
// when possible so key order and insignificant whitespace do not defeat detection.
func (g *Governor) Observe(name, arguments, result string, success bool) Decision {
	if g == nil {
		return Decision{}
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "tool"
	}
	arguments = canonicalJSON(arguments)
	result = canonicalEvidence(result)

	actionKey := digest(name + "\x00" + arguments)
	exactKey := digest(actionKey + "\x00" + fmt.Sprintf("%t", success) + "\x00" + result)
	outcomeKey := digest(name + "\x00" + fmt.Sprintf("%t", success) + "\x00" + result)

	g.toolCalls++
	g.exactCounts[exactKey]++
	g.outcomeCounts[outcomeKey]++
	// Cycle detection includes the result. Repeating a polling action while its
	// evidence changes is progress; repeating the same action/evidence sequence is not.
	g.appendAction(exactKey)

	if g.toolCalls >= g.config.MaxToolCalls {
		reason := fmt.Sprintf("tool loop reached the %d-call harness limit", g.config.MaxToolCalls)
		return stopDecision("tool_call_limit", reason, g.toolCalls)
	}

	exactCount := g.exactCounts[exactKey]
	if exactCount >= g.config.ExactRepeatLimit {
		reason := fmt.Sprintf("%s repeated the same action and received the same result %d times", name, exactCount)
		return stopDecision("exact_repeat", reason, exactCount)
	}

	if width, ok := repeatedSuffix(g.actionHistory, g.config.CycleMaxLength, g.config.CycleRepeats); ok {
		reason := fmt.Sprintf("tool actions and evidence entered a repeating %d-step cycle", width)
		return stopDecision("action_cycle", reason, g.config.CycleRepeats)
	}

	outcomeCount := g.outcomeCounts[outcomeKey]
	if outcomeCount >= g.config.OutcomeRepeatLimit {
		reason := fmt.Sprintf("%s produced the same outcome %d times despite repeated attempts", name, outcomeCount)
		return stopDecision("outcome_repeat", reason, outcomeCount)
	}

	if exactCount == g.config.ExactRepeatLimit-1 {
		return Decision{
			Kind:  "exact_repeat_warning",
			Count: exactCount,
			Nudge: "Harness notice: this exact action produced the same result twice. Do not repeat it again; change strategy, use different evidence, or finish with the information already available.",
		}
	}
	if outcomeCount == g.config.OutcomeRepeatLimit-1 {
		return Decision{
			Kind:  "outcome_repeat_warning",
			Count: outcomeCount,
			Nudge: "Harness notice: this tool keeps producing the same outcome. Reassess the plan before making another tool call.",
		}
	}
	return Decision{}
}

// Rounds reports the number of model rounds observed.
func (g *Governor) Rounds() int {
	if g == nil {
		return 0
	}
	return g.rounds
}

// ToolCalls reports the number of completed tool calls observed.
func (g *Governor) ToolCalls() int {
	if g == nil {
		return 0
	}
	return g.toolCalls
}

func normalizedConfig(config Config) Config {
	defaults := DefaultConfig()
	if config.MaxRounds <= 0 {
		config.MaxRounds = defaults.MaxRounds
	}
	if config.MaxToolCalls <= 0 {
		config.MaxToolCalls = defaults.MaxToolCalls
	}
	if config.ExactRepeatLimit < 2 {
		config.ExactRepeatLimit = defaults.ExactRepeatLimit
	}
	if config.OutcomeRepeatLimit < 2 {
		config.OutcomeRepeatLimit = defaults.OutcomeRepeatLimit
	}
	if config.CycleMaxLength <= 0 {
		config.CycleMaxLength = defaults.CycleMaxLength
	}
	if config.CycleRepeats < 2 {
		config.CycleRepeats = defaults.CycleRepeats
	}
	return config
}

func stopDecision(kind, reason string, count int) Decision {
	return Decision{
		Stop:   true,
		Kind:   kind,
		Reason: reason,
		Count:  count,
		Nudge:  "Harness stopped further tool execution because no new progress was being made.",
	}
}

func (g *Governor) appendAction(action string) {
	g.actionHistory = append(g.actionHistory, action)
	maxHistory := g.config.CycleMaxLength * g.config.CycleRepeats * 2
	if maxHistory < 12 {
		maxHistory = 12
	}
	if len(g.actionHistory) > maxHistory {
		copy(g.actionHistory, g.actionHistory[len(g.actionHistory)-maxHistory:])
		g.actionHistory = g.actionHistory[:maxHistory]
	}
}

func repeatedSuffix(actions []string, maxWidth, repeats int) (int, bool) {
	if repeats < 2 || maxWidth <= 0 {
		return 0, false
	}
	for width := 1; width <= maxWidth; width++ {
		required := width * repeats
		if len(actions) < required {
			continue
		}
		start := len(actions) - required
		matches := true
		for block := 1; block < repeats && matches; block++ {
			for offset := 0; offset < width; offset++ {
				if actions[start+offset] != actions[start+block*width+offset] {
					matches = false
					break
				}
			}
		}
		if matches {
			return width, true
		}
	}
	return 0, false
}

func canonicalJSON(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "{}"
	}
	var value any
	if json.Unmarshal([]byte(raw), &value) != nil {
		return strings.Join(strings.Fields(raw), " ")
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return strings.Join(strings.Fields(raw), " ")
	}
	return string(encoded)
}

func canonicalEvidence(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "<empty>"
	}
	var value any
	if json.Unmarshal([]byte(raw), &value) == nil {
		if encoded, err := json.Marshal(value); err == nil {
			return string(encoded)
		}
	}
	return strings.Join(strings.Fields(raw), " ")
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:16])
}
