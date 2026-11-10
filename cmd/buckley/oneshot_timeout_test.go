package main

import (
	"strings"
	"testing"
	"time"
)

// The limit was two minutes for the whole generation, retries included, and
// could only be raised with a flag on each call.
func TestResolveOneshotTimeout(t *testing.T) {
	cases := []struct {
		name    string
		flag    time.Duration
		command string
		oneshot string
		want    time.Duration
		wantErr string
	}{
		{name: "default", want: 5 * time.Minute},
		{name: "the flag wins", flag: 90 * time.Second, command: "9m", oneshot: "8m", want: 90 * time.Second},
		{name: "the command variable beats the shared one", command: "9m", oneshot: "8m", want: 9 * time.Minute},
		{name: "the shared variable", oneshot: "8m", want: 8 * time.Minute},
		{name: "a variable that is not a duration", command: "soon", wantErr: `BUCKLEY_TIMEOUT_PR="soon"`},
		{name: "a zero variable", oneshot: "0s", wantErr: "BUCKLEY_ONESHOT_TIMEOUT"},
		{name: "a negative flag", flag: -time.Second, wantErr: "--timeout must be a positive duration"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("BUCKLEY_TIMEOUT_PR", tc.command)
			t.Setenv("BUCKLEY_ONESHOT_TIMEOUT", tc.oneshot)
			got, err := resolveOneshotTimeout("pr", tc.flag)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want it to contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("resolveOneshotTimeout() = %s, %v; want %s", got, err, tc.want)
			}
		})
	}
}

func TestOneshotCommandsTakeTheTimeoutFromTheEnvironment(t *testing.T) {
	t.Setenv(envCommitBackend, "")
	t.Setenv(envPRBackend, "")
	t.Setenv(envOneshotBackend, "")
	t.Setenv("BUCKLEY_TIMEOUT_PR", "")
	t.Setenv("BUCKLEY_TIMEOUT_COMMIT", "7m")
	t.Setenv("BUCKLEY_ONESHOT_TIMEOUT", "6m")

	commit, err := parseCommitCommandOptions(nil)
	if err != nil || commit.timeout != 7*time.Minute {
		t.Errorf("commit timeout = %s, %v; want BUCKLEY_TIMEOUT_COMMIT's 7m", commit.timeout, err)
	}
	pr, err := parsePRCommandOptions(nil)
	if err != nil || pr.timeout != 6*time.Minute {
		t.Errorf("pr timeout = %s, %v; want BUCKLEY_ONESHOT_TIMEOUT's 6m", pr.timeout, err)
	}
	merge, err := parsePRMergeOptions([]string{"7", "--squash"})
	if err != nil || merge.timeout != 6*time.Minute {
		t.Errorf("pr merge timeout = %s, %v; want 6m", merge.timeout, err)
	}
	if pr, err = parsePRCommandOptions([]string{"--timeout", "45s"}); err != nil || pr.timeout != 45*time.Second {
		t.Errorf("pr --timeout 45s = %s, %v", pr.timeout, err)
	}

	t.Setenv("BUCKLEY_TIMEOUT_COMMIT", "never")
	if _, err := parseCommitCommandOptions(nil); err == nil {
		t.Error("a malformed BUCKLEY_TIMEOUT_COMMIT must be an error, not a silent default")
	}
}
