package commands

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/diffsignal"
)

func shardsOfSize(highSignalBytes int) diffsignal.ShardResult {
	return diffsignal.ShardResult{HighSignalBytes: highSignalBytes, HighSignalFiles: 1}
}

// TestIsCoreAuthor_TableDriven covers every accepted association value, the
// fail-closed cases, and the allowlist override.
//
// Mutation: change the fail-closed branch to `if associationErr != nil {
// return true }` (treat an unknown author as core on lookup failure) and the
// "association lookup fails" case fails, since it currently expects false.
func TestIsCoreAuthor_TableDriven(t *testing.T) {
	cfg := DefaultPostingGateConfig()
	cfg.AllowlistUsernames = map[string]bool{"trustedcontributor": true}

	tests := []struct {
		name        string
		username    string
		association string
		err         error
		want        bool
	}{
		{"OWNER is core", "octocat", "OWNER", nil, true},
		{"MEMBER is core", "octocat", "MEMBER", nil, true},
		{"COLLABORATOR is core", "octocat", "COLLABORATOR", nil, true},
		{"CONTRIBUTOR is not core", "octocat", "CONTRIBUTOR", nil, false},
		{"FIRST_TIME_CONTRIBUTOR is not core", "octocat", "FIRST_TIME_CONTRIBUTOR", nil, false},
		{"FIRST_TIMER is not core", "octocat", "FIRST_TIMER", nil, false},
		{"NONE is not core", "octocat", "NONE", nil, false},
		{"unrecognized value is not core", "octocat", "SOMETHING_NEW", nil, false},
		{"empty association is not core", "octocat", "", nil, false},
		{"association lookup fails: fail closed", "octocat", "OWNER", errors.New("API error"), false},
		{"allowlisted username overrides a non-core association", "TrustedContributor", "CONTRIBUTOR", nil, true},
		{"allowlist is case-insensitive", "TRUSTEDCONTRIBUTOR", "NONE", nil, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsCoreAuthor(cfg, tc.username, tc.association, tc.err); got != tc.want {
				t.Errorf("IsCoreAuthor(%q, %q, err=%v) = %v, want %v", tc.username, tc.association, tc.err, got, tc.want)
			}
		})
	}
}

// TestEvaluatePostingGate_LocalReviewIsUnrestricted proves a dry-run/local
// review of a huge PR from a non-core author never gates, at any size.
//
// Mutation: drop the `if !willPost { return decision }` early return and
// this test fails because a huge local review from a non-core author would
// come back Blocked.
func TestEvaluatePostingGate_LocalReviewIsUnrestricted(t *testing.T) {
	cfg := DefaultPostingGateConfig()
	huge := shardsOfSize(cfg.HighSignalByteThreshold * 100)

	decision := EvaluatePostingGate(cfg, false, huge, "randomcontributor", "NONE", nil)
	if decision.Blocked {
		t.Fatalf("local review of a huge PR from a non-core author was blocked: %+v", decision)
	}
}

// TestEvaluatePostingGate_PostedHugeCoreAuthorProceeds covers every accepted
// association value on the posted path: a huge PR from a core author must
// never be blocked.
func TestEvaluatePostingGate_PostedHugeCoreAuthorProceeds(t *testing.T) {
	cfg := DefaultPostingGateConfig()
	huge := shardsOfSize(cfg.HighSignalByteThreshold * 100)

	for _, association := range []string{"OWNER", "MEMBER", "COLLABORATOR"} {
		t.Run(association, func(t *testing.T) {
			decision := EvaluatePostingGate(cfg, true, huge, "maintainer", association, nil)
			if decision.Blocked {
				t.Errorf("posted huge PR from %s was blocked: %+v", association, decision)
			}
		})
	}
}

// TestEvaluatePostingGate_PostedHugeNonCoreDeclines is the load-bearing
// cost-avoidance assertion: a posted, huge, non-core-author review must
// decline, and the decision it returns must be the thing a caller uses to
// avoid running any shard.
//
// Mutation: change the threshold comparison from `<=` to `<` (or otherwise
// invert the gate) and the "runs zero shards" contract described in
// EvaluatePostingGate's doc breaks silently; the caller-side assertion for
// "zero shards run" lives in TestPRReviewPostingFlow_NonCoreHugePR (this
// package's orchestration test) which directly checks that no shard runner
// was invoked when decision.Blocked is true.
func TestEvaluatePostingGate_PostedHugeNonCoreDeclines(t *testing.T) {
	cfg := DefaultPostingGateConfig()
	huge := shardsOfSize(cfg.HighSignalByteThreshold * 100)

	decision := EvaluatePostingGate(cfg, true, huge, "randomcontributor", "NONE", nil)
	if !decision.Blocked {
		t.Fatalf("posted huge PR from a non-core author was not blocked: %+v", decision)
	}
	if decision.Reason == "" {
		t.Error("Blocked decision has an empty Reason")
	}
	if decision.HighSignalBytes != huge.HighSignalBytes {
		t.Errorf("decision.HighSignalBytes = %d, want %d", decision.HighSignalBytes, huge.HighSignalBytes)
	}
}

// TestEvaluatePostingGate_PostedSmallNonCoreProceeds proves the gate is
// size-triggered only: a small PR from a non-core author must review
// normally, posted or not.
//
// Mutation: remove the `if shards.HighSignalBytes <= threshold { return
// decision }` check and this test fails because a small PR from a non-core
// author would be blocked.
func TestEvaluatePostingGate_PostedSmallNonCoreProceeds(t *testing.T) {
	cfg := DefaultPostingGateConfig()
	small := shardsOfSize(1024)

	decision := EvaluatePostingGate(cfg, true, small, "randomcontributor", "NONE", nil)
	if decision.Blocked {
		t.Fatalf("posted small PR from a non-core author was blocked: %+v", decision)
	}
}

// TestEvaluatePostingGate_AssociationLookupFailureFailsClosed proves that
// when the association cannot be determined, the huge/posted path still
// declines rather than silently proceeding.
func TestEvaluatePostingGate_AssociationLookupFailureFailsClosed(t *testing.T) {
	cfg := DefaultPostingGateConfig()
	huge := shardsOfSize(cfg.HighSignalByteThreshold * 100)

	decision := EvaluatePostingGate(cfg, true, huge, "someone", "OWNER", errors.New("rate limited"))
	if !decision.Blocked {
		t.Fatalf("posted huge PR with a failed association lookup was not blocked: %+v", decision)
	}
}

// TestBuildDeclineComment_ContainsRequiredContent checks the decline comment
// states size, threshold, rationale, splitting guidance, and the maintainer
// override, in ASD-STE100-plain prose.
func TestBuildDeclineComment_ContainsRequiredContent(t *testing.T) {
	decision := PostingGateDecision{Blocked: true, HighSignalBytes: 5_000_000, HighSignalFiles: 400}
	comment := BuildDeclineComment(decision, 1_000_000)

	for _, want := range []string{
		strconv.Itoa(decision.HighSignalBytes), strconv.Itoa(decision.HighSignalFiles), "1000000",
		"directory or package",
		"generated or bundled artifacts",
		"maintainer can run the full review locally",
		"re-trigger",
	} {
		if !strings.Contains(comment, want) {
			t.Errorf("decline comment is missing %q:\n%s", want, comment)
		}
	}
}

// TestDeclineTracker_OncePerHeadCommit proves duplicate webhook events for
// the same repository/PR/head-commit produce exactly one recorded decline,
// while a new head commit (a new push) is free to decline again.
//
// Mutation: make RecordDecline a no-op (or AlreadyDeclined always return
// false) and this test fails because the second event for the same head
// commit would not be recognized as a duplicate.
func TestDeclineTracker_OncePerHeadCommit(t *testing.T) {
	tracker := NewInMemoryDeclineTracker()
	repo, pr, head := "m31labs/gosx", 95, "abc123"

	if tracker.AlreadyDeclined(repo, pr, head) {
		t.Fatal("AlreadyDeclined = true before any RecordDecline call")
	}

	declineCount := 0
	for i := 0; i < 5; i++ {
		// Simulate 5 duplicate webhook deliveries for the same push.
		if tracker.AlreadyDeclined(repo, pr, head) {
			continue
		}
		declineCount++
		tracker.RecordDecline(repo, pr, head)
	}
	if declineCount != 1 {
		t.Errorf("declineCount = %d across 5 duplicate events, want exactly 1", declineCount)
	}

	// A new push (new head commit) is a distinct decision.
	newHead := "def456"
	if tracker.AlreadyDeclined(repo, pr, newHead) {
		t.Error("AlreadyDeclined = true for a new head commit that was never declined")
	}
}

func TestDeclineTracker_DistinguishesRepositoriesAndPRs(t *testing.T) {
	tracker := NewInMemoryDeclineTracker()
	tracker.RecordDecline("org/repo-a", 1, "sha1")

	cases := []struct {
		repo string
		pr   int
		sha  string
		want bool
	}{
		{"org/repo-a", 1, "sha1", true},
		{"org/repo-b", 1, "sha1", false},
		{"org/repo-a", 2, "sha1", false},
		{"org/repo-a", 1, "sha2", false},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%s#%d@%s", tc.repo, tc.pr, tc.sha), func(t *testing.T) {
			if got := tracker.AlreadyDeclined(tc.repo, tc.pr, tc.sha); got != tc.want {
				t.Errorf("AlreadyDeclined(%s, %d, %s) = %v, want %v", tc.repo, tc.pr, tc.sha, got, tc.want)
			}
		})
	}
}
