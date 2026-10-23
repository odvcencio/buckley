package main

import (
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/config"
)

func TestHostEvidenceTimeoutConfigAndOverride(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Buckbot.HostEvidenceTimeoutSeconds = 90
	base := defaultAutomatedReviewOptions(cfg)
	if base.hostEvidenceTimeout != 90*time.Second {
		t.Fatalf("config timeout = %s, want 90s", base.hostEvidenceTimeout)
	}
	if got := base.withOverrides(automatedReviewOptions{}).hostEvidenceTimeout; got != 90*time.Second {
		t.Fatalf("no override kept %s, want 90s", got)
	}
	if got := base.withOverrides(automatedReviewOptions{hostEvidenceTimeout: 30 * time.Second}).hostEvidenceTimeout; got != 30*time.Second {
		t.Fatalf("flag override = %s, want 30s", got)
	}
}
