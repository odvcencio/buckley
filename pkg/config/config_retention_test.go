package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRetentionConfig_DefaultsAndOverrides(t *testing.T) {
	cfg := DefaultConfig()
	if err := validateRetention(cfg); err != nil {
		t.Fatal(err)
	}
	if !cfg.Retention.Enabled || cfg.Retention.HistoryTTL != 30*24*time.Hour || cfg.Retention.MaxCompletedSessions != 100 || cfg.Retention.MaxCompletedRuns != 1000 {
		t.Fatalf("defaults=%+v", cfg.Retention)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("retention:\n  enabled: false\n  interval: 4h\n  history_ttl: 72h\n  max_completed_sessions: 5\n  max_completed_runs: 0\n  ephemeral_evidence_ttl: 0s\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadFromPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Retention.Enabled || cfg.Retention.Interval != 4*time.Hour || cfg.Retention.HistoryTTL != 72*time.Hour || cfg.Retention.MaxCompletedSessions != 5 || cfg.Retention.MaxCompletedRuns != 0 || cfg.Retention.EphemeralEvidenceTTL != 0 {
		t.Fatalf("overrides=%+v", cfg.Retention)
	}
}

func TestValidateRetention_RejectsNegativeLimitsAndEnabledZeroInterval(t *testing.T) {
	for _, tc := range []struct {
		name      string
		retention RetentionConfig
		invalid   bool
	}{
		{"disabled", RetentionConfig{}, false},
		{"enabled zero interval", RetentionConfig{Enabled: true}, true},
		{"negative interval", RetentionConfig{Interval: -time.Second}, true},
		{"negative history", RetentionConfig{HistoryTTL: -time.Second}, true},
		{"negative session cap", RetentionConfig{MaxCompletedSessions: -1}, true},
		{"negative run cap", RetentionConfig{MaxCompletedRuns: -1}, true},
		{"negative ephemeral evidence", RetentionConfig{EphemeralEvidenceTTL: -time.Second}, true},
		{"negative durable evidence", RetentionConfig{DurableEvidenceTTL: -time.Second}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateRetention(&Config{Retention: tc.retention})
			if (err != nil) != tc.invalid {
				t.Fatalf("invalid=%v err=%v", tc.invalid, err)
			}
		})
	}
}

func TestRetentionConfig_ProjectCannotOverrideGlobalHistoryPolicy(t *testing.T) {
	cfg := DefaultConfig()
	before := cfg.Retention
	data := []byte("retention:\n  enabled: false\n  history_ttl: 1s\n  max_completed_sessions: 1\n")
	if err := mergeConfigData(cfg, data, "project config", true); err != nil {
		t.Fatal(err)
	}
	if cfg.Retention != before {
		t.Fatalf("project changed global retention: %+v", cfg.Retention)
	}
	if err := mergeConfigData(cfg, data, "user config", false); err != nil {
		t.Fatal(err)
	}
	if cfg.Retention.Enabled || cfg.Retention.HistoryTTL != time.Second || cfg.Retention.MaxCompletedSessions != 1 || cfg.Retention.MaxCompletedRuns != before.MaxCompletedRuns {
		t.Fatalf("user override=%+v", cfg.Retention)
	}
}
