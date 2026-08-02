package commands

import (
	"fmt"
	"strings"
	"sync"

	"m31labs.dev/buckley/pkg/diffsignal"
)

// PostingGateConfig controls whether a review that will be posted back to a
// pull request may run unbounded (proceed) or must decline and ask for a
// smaller PR. It has no effect on a local, non-posting review: that path is
// always unrestricted, because its cost is the caller's own to spend.
type PostingGateConfig struct {
	// HighSignalByteThreshold is the same metric and default that triggers
	// shard fan-out (diffsignal.ReviewShardBudget). Reusing it means a PR
	// whose bulk is generated bundles never trips the gate, and a PR of
	// genuinely large hand-written source always does, consistently with
	// what actually costs money to review.
	HighSignalByteThreshold int

	// CoreAssociations lists the GitHub authorAssociation values treated as
	// "core" for gate purposes: a core author's posted review may run past
	// the threshold without a decline. Comparison is case-sensitive against
	// GitHub's own enum values (OWNER, MEMBER, COLLABORATOR, CONTRIBUTOR,
	// FIRST_TIME_CONTRIBUTOR, FIRST_TIMER, NONE).
	CoreAssociations map[string]bool

	// AllowlistUsernames names authors treated as core regardless of their
	// association (for example, a maintainer who authors PRs from a fork
	// GitHub reports as CONTRIBUTOR). Comparison is case-insensitive.
	AllowlistUsernames map[string]bool
}

// DefaultPostingGateConfig returns the default gate: OWNER, MEMBER, and
// COLLABORATOR are core; everything else (including an association that
// could not be determined) is not.
func DefaultPostingGateConfig() PostingGateConfig {
	return PostingGateConfig{
		HighSignalByteThreshold: diffsignal.ReviewShardBudget,
		CoreAssociations: map[string]bool{
			"OWNER":        true,
			"MEMBER":       true,
			"COLLABORATOR": true,
		},
		AllowlistUsernames: map[string]bool{},
	}
}

// IsCoreAuthor decides whether an author counts as a core maintainer or
// owner for the posting gate. It fails closed: an association that is
// empty, unrecognized, or reported alongside a non-nil lookup error is
// never treated as core, because an unknown author must never unlock
// unbounded spend. The allowlist is checked first and can override a
// non-core association (for example, a maintainer whose fork PRs GitHub
// reports as CONTRIBUTOR).
func IsCoreAuthor(cfg PostingGateConfig, username, association string, associationErr error) bool {
	if cfg.AllowlistUsernames != nil && cfg.AllowlistUsernames[strings.ToLower(strings.TrimSpace(username))] {
		return true
	}
	if associationErr != nil {
		return false
	}
	association = strings.TrimSpace(association)
	if association == "" {
		return false
	}
	return cfg.CoreAssociations != nil && cfg.CoreAssociations[association]
}

// PostingGateDecision is the result of evaluating whether a review that
// will be posted must decline instead of running.
type PostingGateDecision struct {
	// Blocked is true when the review must decline: gate applies (posting +
	// over threshold) and the author is not core.
	Blocked bool

	// Reason is a short machine-readable explanation, non-empty only when
	// Blocked is true.
	Reason string

	// HighSignalBytes and HighSignalFiles are the measured size that the
	// decision (and, when blocked, the decline comment) is based on.
	HighSignalBytes int
	HighSignalFiles int
}

// EvaluatePostingGate decides whether a review may proceed. The gate fires
// only when both hold: the review will be posted (willPost), and the PR's
// HighSignalBytes exceeds cfg.HighSignalByteThreshold. A local/dry-run
// review (willPost false) is always unrestricted; so is a small PR from any
// author, posted or not.
func EvaluatePostingGate(cfg PostingGateConfig, willPost bool, shards diffsignal.ShardResult, username, association string, associationErr error) PostingGateDecision {
	decision := PostingGateDecision{
		HighSignalBytes: shards.HighSignalBytes,
		HighSignalFiles: shards.HighSignalFiles,
	}
	if !willPost {
		return decision
	}
	threshold := cfg.HighSignalByteThreshold
	if threshold <= 0 {
		threshold = diffsignal.ReviewShardBudget
	}
	if shards.HighSignalBytes <= threshold {
		return decision
	}
	if IsCoreAuthor(cfg, username, association, associationErr) {
		return decision
	}
	decision.Blocked = true
	decision.Reason = fmt.Sprintf(
		"posted review declined: %d high-signal bytes across %d files exceeds the %d-byte threshold, and author %q (association %q) is not a core maintainer or owner",
		shards.HighSignalBytes, shards.HighSignalFiles, threshold, username, association,
	)
	return decision
}

// BuildDeclineComment renders the courteous, ASD-STE100 decline comment
// posted when the gate blocks a review. It states the measured size and
// threshold, the reason the limit exists, concrete guidance for splitting
// the PR, and the maintainer override.
func BuildDeclineComment(decision PostingGateDecision, threshold int) string {
	var sb strings.Builder
	sb.WriteString("## Automated review declined\n\n")
	fmt.Fprintf(&sb, "This pull request has %d high-signal bytes across %d files. ", decision.HighSignalBytes, decision.HighSignalFiles)
	fmt.Fprintf(&sb, "That is over the %d-byte review threshold for automated posting.\n\n", threshold)
	sb.WriteString("Review cost scales with the size of the change. ")
	sb.WriteString("The threshold protects the project's review budget from an unbounded cost on a single pull request.\n\n")
	sb.WriteString("Please split this pull request. Two options help most:\n\n")
	sb.WriteString("- Split the change by directory or package. Submit each part as its own pull request.\n")
	sb.WriteString("- Move generated or bundled artifacts to their own commit or pull request. Automated review cannot review generated content.\n\n")
	sb.WriteString("A core maintainer can run the full review locally with `buckley review-pr`, with no size limit. ")
	sb.WriteString("A core maintainer can also re-trigger this review if the current size is intentional.\n")
	return sb.String()
}

// DeclineTracker records which pull request head commits already received a
// decline comment, so repeated webhook events for the same push do not spam
// the pull request with duplicate comments.
type DeclineTracker interface {
	// AlreadyDeclined reports whether a decline was already recorded for
	// this repository, PR number, and head commit.
	AlreadyDeclined(repository string, prNumber int, headSHA string) bool

	// RecordDecline marks a decline as posted for this repository, PR
	// number, and head commit. It is idempotent.
	RecordDecline(repository string, prNumber int, headSHA string)
}

// InMemoryDeclineTracker is a process-local DeclineTracker. It is safe for
// concurrent use.
type InMemoryDeclineTracker struct {
	mu       sync.Mutex
	declined map[string]bool
}

// NewInMemoryDeclineTracker returns an empty tracker.
func NewInMemoryDeclineTracker() *InMemoryDeclineTracker {
	return &InMemoryDeclineTracker{declined: make(map[string]bool)}
}

func declineTrackerKey(repository string, prNumber int, headSHA string) string {
	return fmt.Sprintf("%s#%d@%s", repository, prNumber, headSHA)
}

// AlreadyDeclined implements DeclineTracker.
func (t *InMemoryDeclineTracker) AlreadyDeclined(repository string, prNumber int, headSHA string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.declined[declineTrackerKey(repository, prNumber, headSHA)]
}

// RecordDecline implements DeclineTracker.
func (t *InMemoryDeclineTracker) RecordDecline(repository string, prNumber int, headSHA string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.declined[declineTrackerKey(repository, prNumber, headSHA)] = true
}
