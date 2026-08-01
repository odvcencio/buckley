package policy

import (
	"os"
	"strings"
	"sync"
	"time"
)

// PostureEnvVar selects the active posture, overriding the configured
// default (postures.default in pkg/config).
const PostureEnvVar = "BUCKLEY_POSTURE"

// PostureInteractive is the default posture: an empty rule layer that
// preserves today's approval behavior.
const PostureInteractive = "interactive"

// PostureUnattended flags outward-facing bash (git push, gh, mutation
// curls) as "ask" and, since this posture parks ask decisions instead of
// blocking on human approval, those calls are recorded as ParkedDecisions
// and never execute unattended.
const PostureUnattended = "unattended"

// SelectPosture resolves the active posture name: BUCKLEY_POSTURE takes
// precedence over the configured default; "interactive" is the final
// fallback when neither is set.
func SelectPosture(configuredDefault string) string {
	if v := strings.TrimSpace(os.Getenv(PostureEnvVar)); v != "" {
		return v
	}
	if v := strings.TrimSpace(configuredDefault); v != "" {
		return v
	}
	return PostureInteractive
}

// UnattendedPostureRules flags outward-facing bash that would touch
// systems outside this workspace unattended as "ask": pushing to a remote,
// using the gh CLI, and mutating HTTP requests via curl. Paired with
// PostureConfig.ParkAskDecisions, these calls are parked (recorded, not
// executed) rather than blocked on a human who isn't present to approve.
func UnattendedPostureRules() []PermissionRule {
	return []PermissionRule{
		{Tool: "run_shell", ArgPattern: "git push*", Action: PermissionAsk},
		{Tool: "run_shell", ArgPattern: "* git push*", Action: PermissionAsk},
		{Tool: "run_shell", ArgPattern: "gh *", Action: PermissionAsk},
		{Tool: "run_shell", ArgPattern: "* gh *", Action: PermissionAsk},
		{Tool: "run_shell", ArgPattern: "*curl*-X POST*", Action: PermissionAsk},
		{Tool: "run_shell", ArgPattern: "*curl*-X PUT*", Action: PermissionAsk},
		{Tool: "run_shell", ArgPattern: "*curl*-X DELETE*", Action: PermissionAsk},
		{Tool: "run_shell", ArgPattern: "*curl*-X PATCH*", Action: PermissionAsk},
		{Tool: "run_shell", ArgPattern: "*curl*-X post*", Action: PermissionAsk},
		{Tool: "run_shell", ArgPattern: "*curl*-X put*", Action: PermissionAsk},
		{Tool: "run_shell", ArgPattern: "*curl*-X delete*", Action: PermissionAsk},
		{Tool: "run_shell", ArgPattern: "*curl*-X patch*", Action: PermissionAsk},
	}
}

// UnattendedPostureLayer wraps UnattendedPostureRules as a named
// PermissionLayer.
func UnattendedPostureLayer() PermissionLayer {
	return PermissionLayer{Name: "posture:unattended", Rules: UnattendedPostureRules()}
}

// ParkedDecision records an "ask" decision that a posture chose to park
// instead of blocking on human approval (used by the unattended posture,
// where nobody is present to answer a prompt).
type ParkedDecision struct {
	ID        string
	Tool      string
	Arg       string
	Layer     string
	Rule      PermissionRule
	Posture   string
	CreatedAt time.Time
}

// ParkedDecisionSink receives parked decisions as they occur.
type ParkedDecisionSink interface {
	RecordParkedDecision(ParkedDecision)
}

// ParkedDecisionLog is a simple in-memory ParkedDecisionSink. Callers (for
// example an overnight goal-loop run) collect its contents for a morning
// report.
type ParkedDecisionLog struct {
	mu    sync.Mutex
	items []ParkedDecision
}

// NewParkedDecisionLog creates an empty log.
func NewParkedDecisionLog() *ParkedDecisionLog {
	return &ParkedDecisionLog{}
}

// RecordParkedDecision appends a parked decision to the log.
func (l *ParkedDecisionLog) RecordParkedDecision(d ParkedDecision) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.items = append(l.items, d)
}

// List returns a snapshot of every parked decision recorded so far.
func (l *ParkedDecisionLog) List() []ParkedDecision {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]ParkedDecision, len(l.items))
	copy(out, l.items)
	return out
}
