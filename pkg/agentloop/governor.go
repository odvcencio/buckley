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
	MaxRounds    int
	MaxToolCalls int
	// ReadOnlyWarningAt, ReadOnlyActionAt, and MaxReadOnlyCalls form an
	// escalation ladder for discovery that produces evidence but never changes
	// state. Zero MaxReadOnlyCalls disables this fuse.
	ReadOnlyWarningAt  int
	ReadOnlyActionAt   int
	MaxReadOnlyCalls   int
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

	maxExactCount   int
	maxOutcomeCount int
	readOnlyCalls   int
}

// ObserveEffect tracks whether tool work is advancing beyond discovery.
// It is separate from Observe so callers that do not classify effects retain
// the existing governor behavior.
func (g *Governor) ObserveEffect(effectClass string, success bool) Decision {
	return g.ObserveProgress(effectClass, success, false, false)
}

// ObserveProgress tracks convergence using observed workspace change when a
// dispatcher can provide it. Effect metadata remains the conservative
// fallback for adapters that do not yet observe workspace state.
func (g *Governor) ObserveProgress(effectClass string, success, stateObserved, stateChanged bool) Decision {
	if g == nil || g.config.MaxReadOnlyCalls <= 0 {
		return Decision{}
	}
	effectClass = strings.ToLower(strings.TrimSpace(effectClass))
	if effectClass == "control" || effectClass == "" {
		return Decision{}
	}
	if stateObserved {
		if success && stateChanged {
			g.readOnlyCalls = 0
			return Decision{}
		}
		g.readOnlyCalls++
	} else {
		switch effectClass {
		case "readonly":
			g.readOnlyCalls++
		default:
			if success {
				g.readOnlyCalls = 0
				return Decision{}
			}
			g.readOnlyCalls++
		}
	}
	if g.readOnlyCalls >= g.config.MaxReadOnlyCalls {
		reason := fmt.Sprintf("tool loop used %d calls without a successful state-changing action", g.readOnlyCalls)
		return stopDecision("read_only_budget", reason, g.readOnlyCalls)
	}
	if g.config.ReadOnlyWarningAt > 0 && g.readOnlyCalls == g.config.ReadOnlyWarningAt {
		return Decision{
			Kind:  "read_only_budget_warning",
			Count: g.readOnlyCalls,
			Nudge: "Harness checkpoint: discovery has consumed half of its bounded budget without changing state. Preserve your creative latitude, but now choose and state the smallest viable implementation slice supported by the evidence. Prefer executing that slice over broadening discovery; otherwise complete a read-only task or report a concrete blocker.",
		}
	}
	if g.config.ReadOnlyActionAt > 0 && g.readOnlyCalls == g.config.ReadOnlyActionAt {
		return Decision{
			Kind:  "read_only_action_required",
			Count: g.readOnlyCalls,
			Nudge: "Harness action boundary: the evidence budget is nearly exhausted. The next tool work must make an observable workspace change, complete the task if it is read-only, or report a concrete blocker. Further discovery without action will be parked deterministically.",
		}
	}
	return Decision{}
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
	// A successful result is evidence about the specific action that produced
	// it. Different searches can legitimately return the same empty result,
	// and treating those as one repeated outcome can stop broad discovery
	// before the agent reaches an edit. Failed outcomes remain argument-agnostic
	// so changing paths or queries cannot evade a persistent tool failure.
	outcomeScope := name
	if success {
		outcomeScope += "\x00" + arguments
	}
	outcomeKey := digest(outcomeScope + "\x00" + fmt.Sprintf("%t", success) + "\x00" + result)

	g.toolCalls++
	g.exactCounts[exactKey]++
	g.outcomeCounts[outcomeKey]++
	if g.exactCounts[exactKey] > g.maxExactCount {
		g.maxExactCount = g.exactCounts[exactKey]
	}
	if g.outcomeCounts[outcomeKey] > g.maxOutcomeCount {
		g.maxOutcomeCount = g.outcomeCounts[outcomeKey]
	}
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

// RemainingToolCalls reports how many additional calls the Governor can
// admit before its hard tool ceiling. Controller uses this before dispatching
// a parallel batch so the last batch cannot overshoot an explicit budget.
func (g *Governor) RemainingToolCalls() int {
	if g == nil {
		return 0
	}
	return max(g.config.MaxToolCalls-g.toolCalls, 0)
}

// RepetitionPressure reports how close the loop is to a repeat-based stop
// as a 0..1 ratio: the highest observed repeat count relative to its
// configured limit, across the exact-repeat and outcome-repeat detectors.
// It is the ProgressController's repetition signal (the design's
// stagnation nudge, folded into one scalar).
func (g *Governor) RepetitionPressure() float64 {
	if g == nil {
		return 0
	}
	pressure := 0.0
	if g.config.ExactRepeatLimit > 0 {
		if p := float64(g.maxExactCount) / float64(g.config.ExactRepeatLimit); p > pressure {
			pressure = p
		}
	}
	if g.config.OutcomeRepeatLimit > 0 {
		if p := float64(g.maxOutcomeCount) / float64(g.config.OutcomeRepeatLimit); p > pressure {
			pressure = p
		}
	}
	if pressure > 1 {
		pressure = 1
	}
	return pressure
}

// evidenceNoveltyMinSamples is the number of observed outcomes below which
// the novelty signal is reported as unobserved: two tool calls that were
// both "new" say nothing about stagnation yet.
const evidenceNoveltyMinSamples = 4

// EvidenceNovelty reports the fraction of observed tool outcomes that were
// first occurrences (0 = every outcome was a repeat, 1 = all new), and
// whether enough outcomes exist for the signal to mean anything. Outcome
// identity uses the same canonicalized evidence hashing the repeat
// detectors use: a changed content hash is new evidence, a re-read is not.
func (g *Governor) EvidenceNovelty() (float64, bool) {
	if g == nil || g.toolCalls == 0 {
		return 0, false
	}
	novelty := float64(len(g.outcomeCounts)) / float64(g.toolCalls)
	return novelty, g.toolCalls >= evidenceNoveltyMinSamples
}

// ActionRequired reports whether discovery has crossed the action boundary.
// Dispatchers can use this signal to narrow capabilities without interpreting
// model prose. A successful observed state change resets the boundary.
func (g *Governor) ActionRequired() bool {
	return g != nil && g.config.ReadOnlyActionAt > 0 && g.readOnlyCalls >= g.config.ReadOnlyActionAt
}

func normalizedConfig(config Config) Config {
	defaults := DefaultConfig()
	if config.MaxRounds <= 0 {
		config.MaxRounds = defaults.MaxRounds
	}
	if config.MaxToolCalls <= 0 {
		config.MaxToolCalls = defaults.MaxToolCalls
	}
	if config.MaxReadOnlyCalls > 0 {
		if config.ReadOnlyWarningAt <= 0 {
			config.ReadOnlyWarningAt = max(config.MaxReadOnlyCalls/2, 1)
		}
		if config.ReadOnlyWarningAt >= config.MaxReadOnlyCalls {
			config.ReadOnlyWarningAt = max(config.MaxReadOnlyCalls-1, 1)
		}
		if config.ReadOnlyActionAt <= config.ReadOnlyWarningAt {
			config.ReadOnlyActionAt = config.MaxReadOnlyCalls - 4
		}
		if config.ReadOnlyActionAt <= config.ReadOnlyWarningAt || config.ReadOnlyActionAt >= config.MaxReadOnlyCalls {
			config.ReadOnlyActionAt = 0
		}
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
