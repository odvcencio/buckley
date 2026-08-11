// Package modelprofile owns provider-neutral, versioned empirical model facts.
// It has no dependency on protocol, storage, providers, or UI adapters.
package modelprofile

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

const SchemaVersion = "buckley.model-behavior/v1"

type Class string

const (
	ClassWeak     Class = "weak"
	ClassBalanced Class = "balanced"
	ClassFrontier Class = "frontier"
)

type Capabilities struct {
	ToolCalls            bool `json:"tool_calls"`
	NativeJSONSchema     bool `json:"native_json_schema"`
	ParallelToolCalls    bool `json:"parallel_tool_calls"`
	Continuation         bool `json:"continuation"`
	Reasoning            bool `json:"reasoning"`
	CodeMode             bool `json:"code_mode"`
	ContextWindowTokens  int  `json:"context_window_tokens,omitempty"`
	SafeVisibleToolCount int  `json:"safe_visible_tool_count,omitempty"`
}

type Metrics struct {
	ToolReliability             float64 `json:"tool_reliability"`
	ArgumentRepairReliability   float64 `json:"argument_repair_reliability"`
	StructuredOutputReliability float64 `json:"structured_output_reliability"`
	ParallelCallReliability     float64 `json:"parallel_call_reliability"`
	EditFidelity                float64 `json:"edit_fidelity"`
	VerificationPassRate        float64 `json:"verification_pass_rate"`
	EffectiveContextTokens      int     `json:"effective_context_tokens,omitempty"`
	ContinuationReliability     float64 `json:"continuation_reliability"`
	LatencyP50MS                int64   `json:"latency_p50_ms,omitempty"`
	LatencyP95MS                int64   `json:"latency_p95_ms,omitempty"`
	CostUSDPerMTokens           float64 `json:"cost_usd_per_m_tokens,omitempty"`
}

// Profile is immutable once stored. Version and Digest give every compiled
// protocol a replayable measurement identity.
type Profile struct {
	SchemaVersion string       `json:"schema_version"`
	ModelID       string       `json:"model_id"`
	Provider      string       `json:"provider,omitempty"`
	Version       string       `json:"version"`
	Class         Class        `json:"class,omitempty"`
	SampleSize    int          `json:"sample_size"`
	Confidence    float64      `json:"confidence"`
	MeasuredAt    time.Time    `json:"measured_at"`
	Capabilities  Capabilities `json:"capabilities"`
	Metrics       Metrics      `json:"metrics"`
}

func (p Profile) Normalize() Profile {
	p.SchemaVersion = strings.TrimSpace(p.SchemaVersion)
	if p.SchemaVersion == "" {
		p.SchemaVersion = SchemaVersion
	}
	p.ModelID = strings.TrimSpace(p.ModelID)
	p.Provider = strings.TrimSpace(p.Provider)
	p.Version = strings.TrimSpace(p.Version)
	p.Class = Class(strings.ToLower(strings.TrimSpace(string(p.Class))))
	p.MeasuredAt = p.MeasuredAt.UTC().Round(0)
	return p
}

func (p Profile) Validate() error {
	p = p.Normalize()
	if p.SchemaVersion != SchemaVersion {
		return fmt.Errorf("profile schema_version must equal %s", SchemaVersion)
	}
	if p.ModelID == "" {
		return fmt.Errorf("profile model_id is required")
	}
	if p.Version == "" {
		return fmt.Errorf("profile version is required")
	}
	if p.SampleSize < 0 {
		return fmt.Errorf("profile sample_size must not be negative")
	}
	if !ratio(p.Confidence) {
		return fmt.Errorf("profile confidence must be between 0 and 1")
	}
	if p.Class != "" && p.Class != ClassWeak && p.Class != ClassBalanced && p.Class != ClassFrontier {
		return fmt.Errorf("profile class must be weak, balanced, or frontier")
	}
	for name, value := range map[string]float64{
		"tool_reliability": p.Metrics.ToolReliability, "argument_repair_reliability": p.Metrics.ArgumentRepairReliability,
		"structured_output_reliability": p.Metrics.StructuredOutputReliability, "parallel_call_reliability": p.Metrics.ParallelCallReliability,
		"edit_fidelity": p.Metrics.EditFidelity, "verification_pass_rate": p.Metrics.VerificationPassRate,
		"continuation_reliability": p.Metrics.ContinuationReliability,
	} {
		if !ratio(value) {
			return fmt.Errorf("profile %s must be between 0 and 1", name)
		}
	}
	if p.Capabilities.ContextWindowTokens < 0 || p.Capabilities.SafeVisibleToolCount < 0 || p.Metrics.EffectiveContextTokens < 0 || p.Metrics.LatencyP50MS < 0 || p.Metrics.LatencyP95MS < 0 || p.Metrics.CostUSDPerMTokens < 0 {
		return fmt.Errorf("profile measurements must not be negative")
	}
	return nil
}

func (p Profile) ResolvedClass() Class {
	p = p.Normalize()
	if p.Class != "" {
		return p.Class
	}
	if p.SampleSize < 20 || p.Confidence < 0.70 || p.Metrics.ToolReliability < 0.85 || p.Metrics.StructuredOutputReliability < 0.90 {
		return ClassWeak
	}
	if p.Capabilities.Continuation && p.Capabilities.ParallelToolCalls && p.Metrics.ContinuationReliability >= 0.90 && p.Metrics.ParallelCallReliability >= 0.90 && p.Metrics.EffectiveContextTokens >= 96*1024 {
		return ClassFrontier
	}
	return ClassBalanced
}

func (p Profile) Digest() (string, error) {
	p = p.Normalize()
	if err := p.Validate(); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("encode behavior profile: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

// Observation contains only aggregate evaluation signals, never user source.
type Observation struct {
	Succeeded          bool
	ToolSucceeded      *bool
	ArgumentRepaired   *bool
	StructuredOutput   *bool
	ParallelCall       *bool
	EditFaithful       *bool
	VerificationPassed *bool
	ContinuationWorked *bool
	LatencyMS          int64
}

func Aggregate(base Profile, observations []Observation, measuredAt time.Time) (Profile, error) {
	base = base.Normalize()
	if err := base.Validate(); err != nil {
		return Profile{}, err
	}
	if len(observations) == 0 {
		return base, nil
	}
	next := base
	next.SampleSize += len(observations)
	next.MeasuredAt = measuredAt.UTC().Round(0)
	next.Metrics.ToolReliability = aggregateRatio(base.Metrics.ToolReliability, base.SampleSize, observations, func(o Observation) *bool { return o.ToolSucceeded })
	next.Metrics.ArgumentRepairReliability = aggregateRatio(base.Metrics.ArgumentRepairReliability, base.SampleSize, observations, func(o Observation) *bool { return o.ArgumentRepaired })
	next.Metrics.StructuredOutputReliability = aggregateRatio(base.Metrics.StructuredOutputReliability, base.SampleSize, observations, func(o Observation) *bool { return o.StructuredOutput })
	next.Metrics.ParallelCallReliability = aggregateRatio(base.Metrics.ParallelCallReliability, base.SampleSize, observations, func(o Observation) *bool { return o.ParallelCall })
	next.Metrics.EditFidelity = aggregateRatio(base.Metrics.EditFidelity, base.SampleSize, observations, func(o Observation) *bool { return o.EditFaithful })
	next.Metrics.VerificationPassRate = aggregateRatio(base.Metrics.VerificationPassRate, base.SampleSize, observations, func(o Observation) *bool { return o.VerificationPassed })
	next.Metrics.ContinuationReliability = aggregateRatio(base.Metrics.ContinuationReliability, base.SampleSize, observations, func(o Observation) *bool { return o.ContinuationWorked })
	latencies := make([]int64, 0, len(observations))
	for _, observation := range observations {
		if observation.LatencyMS > 0 {
			latencies = append(latencies, observation.LatencyMS)
		}
	}
	if len(latencies) > 0 {
		sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
		next.Metrics.LatencyP50MS = percentile(latencies, 0.50)
		next.Metrics.LatencyP95MS = percentile(latencies, 0.95)
	}
	if err := next.Validate(); err != nil {
		return Profile{}, err
	}
	return next, nil
}

func aggregateRatio(previous float64, prior int, observations []Observation, extract func(Observation) *bool) float64 {
	weighted, count := previous*float64(prior), prior
	for _, observation := range observations {
		if value := extract(observation); value != nil {
			count++
			if *value {
				weighted++
			}
		}
	}
	if count == 0 {
		return 0
	}
	return weighted / float64(count)
}

func percentile(values []int64, p float64) int64 {
	if len(values) == 0 {
		return 0
	}
	index := int(float64(len(values)-1)*p + 0.5)
	if index < 0 {
		index = 0
	}
	if index >= len(values) {
		index = len(values) - 1
	}
	return values[index]
}

func ratio(value float64) bool { return value >= 0 && value <= 1 }
