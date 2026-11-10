package config

import (
	"fmt"
	"time"
)

const (
	DefaultRetentionInterval    = 6 * time.Hour
	DefaultHistoryRetention     = 30 * 24 * time.Hour
	DefaultMaxCompletedSessions = 100
	DefaultMaxCompletedRuns     = 1000
)

// RetentionConfig is user-owned policy for disposable history. Zero TTLs or counts disable that
// rule; active/paused sessions, unfinished runs and pinned evidence are protected.
type RetentionConfig struct {
	Enabled              bool          `yaml:"enabled"`
	Interval             time.Duration `yaml:"interval"`
	HistoryTTL           time.Duration `yaml:"history_ttl"`
	MaxCompletedSessions int           `yaml:"max_completed_sessions"`
	MaxCompletedRuns     int           `yaml:"max_completed_runs"`
	EphemeralEvidenceTTL time.Duration `yaml:"ephemeral_evidence_ttl"`
	DurableEvidenceTTL   time.Duration `yaml:"durable_evidence_ttl"`
}

func defaultRetentionConfig() RetentionConfig {
	return RetentionConfig{
		Enabled: true, Interval: DefaultRetentionInterval, HistoryTTL: DefaultHistoryRetention,
		MaxCompletedSessions: DefaultMaxCompletedSessions, MaxCompletedRuns: DefaultMaxCompletedRuns,
		EphemeralEvidenceTTL: 30 * 24 * time.Hour, DurableEvidenceTTL: 180 * 24 * time.Hour,
	}
}

func validateRetention(c *Config) error {
	r := c.Retention
	if r.Enabled && r.Interval <= 0 {
		return fmt.Errorf("retention.interval must be positive when enabled")
	}
	if r.Interval < 0 || r.HistoryTTL < 0 || r.MaxCompletedSessions < 0 || r.MaxCompletedRuns < 0 || r.EphemeralEvidenceTTL < 0 || r.DurableEvidenceTTL < 0 {
		return fmt.Errorf("retention limits must be nonnegative")
	}
	return nil
}
