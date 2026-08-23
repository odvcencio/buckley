package workspaceguard

import (
	"fmt"
	"strings"
	"time"
)

const (
	LaunchStateAdmissionPending = "admission_pending"
	LaunchPricePolicyFreeOnly   = "free_only"
	LaunchGlobalCapacity        = 2
	LaunchPerRunParallelism     = 2
)

type LaunchLimits struct {
	ModelRequests       int           `json:"modelRequests"`
	InputTokens         int64         `json:"inputTokens"`
	OutputTokens        int64         `json:"outputTokens"`
	TotalTokens         int64         `json:"totalTokens"`
	MaxOutputPerRequest int64         `json:"maxOutputPerRequest"`
	RequestTimeout      time.Duration `json:"-"`
	RequestTimeoutText  string        `json:"requestTimeout"`
	TurnTimeout         time.Duration `json:"-"`
	TurnTimeoutText     string        `json:"turnTimeout"`
	AbsoluteRunTimeout  time.Duration `json:"-"`
	AbsoluteRunTimeText string        `json:"absoluteRunTimeout"`
	PricePolicy         string        `json:"pricePolicy"`
	GlobalCapacity      int           `json:"globalCapacity"`
	PerRunParallelism   int           `json:"perRunParallelism"`
	Enforced            bool          `json:"enforced"`
	State               string        `json:"state"`
}

type LaunchProfile struct {
	ID     string       `json:"id"`
	Limits LaunchLimits `json:"limits"`
}

func ResolveLaunchProfile(value string) (LaunchProfile, error) {
	id := strings.ToLower(strings.TrimSpace(value))
	profile := LaunchProfile{ID: id}
	switch id {
	case "gsxmail":
		profile.Limits = newLaunchLimits(12, 6_000_000, 393_216, 6_393_216, 15*time.Minute, 30*time.Minute, 90*time.Minute)
	case "gosx", "tqwebp":
		profile.Limits = newLaunchLimits(24, 12_000_000, 786_432, 12_786_432, 20*time.Minute, 45*time.Minute, 4*time.Hour)
	default:
		return LaunchProfile{}, fmt.Errorf("unknown launch profile (want gsxmail, gosx, or tqwebp)")
	}
	return profile, nil
}

func newLaunchLimits(requests int, input, output, total int64, requestTimeout, turnTimeout, runTimeout time.Duration) LaunchLimits {
	return LaunchLimits{
		ModelRequests:       requests,
		InputTokens:         input,
		OutputTokens:        output,
		TotalTokens:         total,
		MaxOutputPerRequest: 32_768,
		RequestTimeout:      requestTimeout,
		RequestTimeoutText:  requestTimeout.String(),
		TurnTimeout:         turnTimeout,
		TurnTimeoutText:     turnTimeout.String(),
		AbsoluteRunTimeout:  runTimeout,
		AbsoluteRunTimeText: runTimeout.String(),
		PricePolicy:         LaunchPricePolicyFreeOnly,
		GlobalCapacity:      LaunchGlobalCapacity,
		PerRunParallelism:   LaunchPerRunParallelism,
		Enforced:            false,
		State:               LaunchStateAdmissionPending,
	}
}
