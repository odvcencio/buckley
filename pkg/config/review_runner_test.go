package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestReviewVerificationRunner_CleanupConfig(t *testing.T) {
	cfg := DefaultConfig()
	if len(cfg.Review.Verification.Runner.Cleanup) != 0 {
		t.Fatal("cleanup enabled by default")
	}
	if err := yaml.Unmarshal([]byte("review:\n  verification:\n    runner:\n      cleanup: [buildbox-run, --cleanup]\n"), cfg); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	argv := cfg.Review.Verification.Runner.Cleanup
	if len(argv) != 2 || argv[0] != "buildbox-run" || argv[1] != "--cleanup" {
		t.Fatalf("cleanup = %v", argv)
	}
	for _, argv := range [][]string{{""}, {"  "}, {"cmd", "bad\x00arg"}} {
		cfg.Review.Verification.Runner.Cleanup = argv
		if err := cfg.Validate(); err == nil {
			t.Fatalf("accepted invalid argv: %q", argv)
		}
	}
}
