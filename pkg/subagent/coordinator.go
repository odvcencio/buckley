package subagent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"

	"m31labs.dev/buckley/pkg/agentcoord"
	artifactv1 "m31labs.dev/buckley/pkg/artifact/v1"
	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/persona"
	"m31labs.dev/buckley/pkg/runledger"
)

const (
	defaultMailboxLimit      = 256
	defaultAttachmentLease   = runledger.AttachmentDefaultLease
	maxTaskCollectionItems   = 256
	maxTaskTextBytes         = 256 * 1024
	maxTaskContractBytes     = 1024 * 1024
	maxCoordinatorIdentifier = runledger.AttachmentMaxID
	minimumHeartbeatInterval = 10 * time.Millisecond
	minimumAttachmentLease   = 3 * minimumHeartbeatInterval
)

// AdmissionDecision is the policy-owned narrowing applied before a child is
// started. Positive policy limits are hard maxima, including when the caller
// omitted its own limit.
type AdmissionDecision struct {
	Allowed        bool
	Reason         string
	TimeoutSeconds int
	StepCap        int
}

// AdmissionPolicy separates governance from the coordinator mechanism. An
// Arbiter adapter can deny, bound timeout, and bound iterations without
// embedding policy decisions in a process adapter.
type AdmissionPolicy interface {
	Admit(ctx context.Context, spec agentcoord.AgentTaskSpec) (AdmissionDecision, error)
}

// AdmissionPolicyFunc adapts a function to AdmissionPolicy.
type AdmissionPolicyFunc func(context.Context, agentcoord.AgentTaskSpec) (AdmissionDecision, error)

// Admit implements AdmissionPolicy.
func (f AdmissionPolicyFunc) Admit(ctx context.Context, spec agentcoord.AgentTaskSpec) (AdmissionDecision, error) {
	return f(ctx, spec)
}

// CoordinatorOption configures the local-process AgentCoordinator adapter.
type CoordinatorOption func(*Coordinator)

// WithRunLedger enables durable lifecycle projections. Durable coordination
// additionally requires WithEvidence so task, mailbox, and result bodies can
// survive a worker process rather than being copied into event payloads.
func WithRunLedger(ledger runledger.Store) CoordinatorOption {
	return func(c *Coordinator) { c.ledger = ledger }
}

// WithClaimJournal overrides the claim backend. Normally NewCoordinator uses
// the ClaimJournal implemented by a SQLite run ledger automatically.
func WithClaimJournal(journal runledger.ClaimJournal) CoordinatorOption {
	return func(c *Coordinator) { c.claims = journal }
}

// WithEvidence enables replayable task, mailbox, and report bodies.
func WithEvidence(store evidence.Store) CoordinatorOption {
	return func(c *Coordinator) { c.evidence = store }
}

// WithMailboxStore overrides the operational durable mailbox. It is useful
// for composition tests and keeps the mailbox port independent from the
// immutable run ledger port.
func WithMailboxStore(store agentcoord.MailboxStore) CoordinatorOption {
	return func(c *Coordinator) { c.mailbox = store }
}

// WithAttachmentStore overrides the durable process-attachment fence.
func WithAttachmentStore(store agentcoord.AttachmentStore) CoordinatorOption {
	return func(c *Coordinator) { c.attachments = store }
}

// WithAttachmentLease sets the renewable durable ownership window. The
// coordinator canonicalizes it to the same bounds as the storage adapter.
func WithAttachmentLease(duration time.Duration) CoordinatorOption {
	return func(c *Coordinator) {
		c.attachmentLease = duration
	}
}

// WithHeartbeatInterval injects the manager renewal cadence. NewCoordinator
// narrows unsafe values to remain below the configured attachment lease.
func WithHeartbeatInterval(interval time.Duration) CoordinatorOption {
	return func(c *Coordinator) {
		if interval > 0 {
			c.heartbeatInterval = interval
		}
	}
}

// WithAdmissionPolicy applies a governed admission policy to every spawn.
func WithAdmissionPolicy(policy AdmissionPolicy) CoordinatorOption {
	return func(c *Coordinator) { c.policy = policy }
}

// WithAdapterName changes the durable adapter identity shown in projections.
func WithAdapterName(name string) CoordinatorOption {
	return func(c *Coordinator) {
		if name = strings.TrimSpace(name); name != "" {
			c.adapter = name
		}
	}
}

// WithMailboxLimit bounds in-memory mailbox retention for coordinator modes
// without a durable ledger. Durable mail remains limited by the query surface.
func WithMailboxLimit(limit int) CoordinatorOption {
	return func(c *Coordinator) {
		if limit > 0 {
			c.mailboxLimit = limit
		}
	}
}

// Coordinator adapts Manager's local process lifecycle to the shared domain
// port. Manager remains the process adapter; this type owns stable run IDs,
// dependency checks, durable lifecycle facts, mailbox semantics, and claims.
type Coordinator struct {
	manager     *Manager
	ledger      runledger.Store
	claims      runledger.ClaimJournal
	evidence    evidence.Store
	mailbox     agentcoord.MailboxStore
	attachments agentcoord.AttachmentStore
	contracts   runledger.RoutineRunJournal
	finalizer   runledger.AttemptFinalizer
	policy      AdmissionPolicy
	adapter     string

	mu                sync.RWMutex
	runs              map[string]agentcoord.AgentTaskSpec
	mailboxes         map[string][]agentcoord.AgentMessage
	fallbackClaims    map[string]string
	claimsByRun       map[string]map[string]struct{}
	evidenceByRun     map[string][]string
	durabilityError   map[string]string
	ownedAttempts     map[string]agentcoord.AttachmentLease
	mailboxLimit      int
	attachmentLease   time.Duration
	heartbeatInterval time.Duration
	configurationErr  error
	now               func() time.Time
}

var _ agentcoord.AgentCoordinator = (*Coordinator)(nil)

// NewCoordinator constructs the local-process implementation. Passing a nil
// manager is allowed for status-only recovery projections, but Spawn will
// report that no live adapter is available.
func NewCoordinator(manager *Manager, opts ...CoordinatorOption) *Coordinator {
	c := &Coordinator{
		manager:           manager,
		adapter:           "local-process",
		runs:              make(map[string]agentcoord.AgentTaskSpec),
		mailboxes:         make(map[string][]agentcoord.AgentMessage),
		fallbackClaims:    make(map[string]string),
		claimsByRun:       make(map[string]map[string]struct{}),
		evidenceByRun:     make(map[string][]string),
		durabilityError:   make(map[string]string),
		ownedAttempts:     make(map[string]agentcoord.AttachmentLease),
		mailboxLimit:      defaultMailboxLimit,
		attachmentLease:   defaultAttachmentLease,
		heartbeatInterval: DefaultAttachmentHeartbeatInterval,
		now:               func() time.Time { return time.Now().UTC() },
	}
	for _, opt := range opts {
		if opt != nil {
			opt(c)
		}
	}
	if nilPort(c.claims) {
		if journal, ok := c.ledger.(runledger.ClaimJournal); ok {
			if !nilPort(journal) {
				c.claims = journal
			}
		}
	}
	if nilPort(c.mailbox) {
		if mailbox, ok := c.ledger.(agentcoord.MailboxStore); ok {
			if !nilPort(mailbox) {
				c.mailbox = mailbox
			}
		}
	}
	if nilPort(c.attachments) {
		if attachments, ok := c.ledger.(agentcoord.AttachmentStore); ok {
			if !nilPort(attachments) {
				c.attachments = attachments
			}
		}
	}
	if journal, ok := c.ledger.(runledger.RoutineRunJournal); ok && !nilPort(journal) {
		c.contracts = journal
	}
	if finalizer, ok := c.ledger.(runledger.AttemptFinalizer); ok && !nilPort(finalizer) {
		c.finalizer = finalizer
	}
	effectiveLease, timingErr := canonicalAttachmentLease(c.attachmentLease)
	if timingErr == nil {
		c.attachmentLease = effectiveLease
		c.heartbeatInterval, _, timingErr = heartbeatTimingForLease(effectiveLease, c.heartbeatInterval)
	}
	c.configurationErr = timingErr
	if manager != nil {
		manager.SetLifecycleObserver(c.observeLifecycle)
		manager.SetHeartbeatObserver(c.heartbeatAttachment, c.heartbeatInterval)
	}
	return c
}

func nilPort(value any) bool {
	if value == nil {
		return true
	}
	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return rv.IsNil()
	default:
		return false
	}
}

// SetAdmissionPolicy swaps the optional policy at a safe synchronization
// boundary. Existing runs retain their resolved constraints.
func (c *Coordinator) SetAdmissionPolicy(policy AdmissionPolicy) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.policy = policy
	c.mu.Unlock()
}

// Spawn creates a durable child run before starting the local worker. When a
// durable ledger is configured, no child is launched until its task contract,
// claims, and spawn event have all been recorded.
func (c *Coordinator) Spawn(ctx context.Context, spec agentcoord.AgentTaskSpec) (agentcoord.AgentRun, error) {
	if c == nil || c.manager == nil {
		return agentcoord.AgentRun{}, fmt.Errorf("subagent coordinator has no live adapter")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if c.ledger != nil && c.configurationErr != nil {
		return agentcoord.AgentRun{}, fmt.Errorf("subagent coordinator attachment timing: %w", c.configurationErr)
	}
	var err error
	spec, err = normalizeTaskSpec(spec)
	if err != nil {
		return agentcoord.AgentRun{}, err
	}
	if err := c.applyAdmission(ctx, &spec); err != nil {
		return agentcoord.AgentRun{}, err
	}
	if err := c.requireDependencies(ctx, spec.Dependencies); err != nil {
		return agentcoord.AgentRun{}, err
	}
	if strings.TrimSpace(spec.RunID) == "" {
		spec.RunID = "run_" + ulid.Make().String()
	}
	if strings.TrimSpace(spec.ID) == "" {
		spec.ID = spec.RunID
	}
	durable := c.ledger != nil
	if !durable {
		if err := c.registerRun(spec); err != nil {
			return agentcoord.AgentRun{}, err
		}
		if len(spec.WorkspaceClaims) > 0 {
			if _, err := c.Claim(ctx, agentcoord.AgentClaimRequest{RunID: spec.RunID, Resources: spec.WorkspaceClaims}); err != nil {
				c.unregisterRun(spec.RunID)
				return agentcoord.AgentRun{}, err
			}
		}
		snapshot, err := c.manager.SpawnWithOptions(spawnOptionsFromTask(spec))
		if err != nil {
			_ = c.releaseClaims(ctx, spec.RunID, spec.WorkspaceClaims, "spawn failed")
			c.unregisterRun(spec.RunID)
			return agentcoord.AgentRun{}, fmt.Errorf("spawn local subagent: %w", err)
		}
		return c.agentRunFromSnapshot(snapshot), nil
	}

	if err := c.requireDurableKernel(); err != nil {
		return agentcoord.AgentRun{}, err
	}
	sessionID := firstNonEmpty(spec.SessionID, spec.ParentSessionID)
	if sessionID == "" {
		return agentcoord.AgentRun{}, fmt.Errorf("subagent coordinator durable spawn requires session_id")
	}
	spec.SessionID = sessionID
	spec.ParentSessionID = firstNonEmpty(spec.ParentSessionID, sessionID)
	logicalSpec, taskEvidence, inputDigest, err := c.persistTaskContract(ctx, spec)
	if err != nil {
		return agentcoord.AgentRun{}, err
	}
	durableRun, _, err := c.contracts.EnsureRunContract(ctx, runledger.AgentRun{
		RunID:       spec.RunID,
		SessionID:   sessionID,
		ParentRunID: spec.ParentRunID,
		TaskID:      spec.ID,
		AgentID:     spec.Agent,
		ModelID:     spec.Model,
		Backend:     c.adapter,
		Status:      string(agentcoord.AgentRunQueued),
		StartedAt:   c.now(),
		Budget:      budgetMap(spec.Budget),
	}, inputDigest, taskEvidence[0])
	if err != nil {
		return agentcoord.AgentRun{}, fmt.Errorf("ensure durable subagent run: %w", err)
	}
	if err := c.registerRun(logicalSpec); err != nil {
		return agentcoord.AgentRun{}, err
	}
	if durableRun.EndedAt != nil || stateFromLedger(durableRun.Status).Terminal() {
		return c.agentRunFromLedger(durableRun, true), nil
	}

	lease, owned, err := c.ensureSpawnAttachment(ctx, spec)
	if err != nil {
		return agentcoord.AgentRun{}, err
	}
	spec.AttemptID = lease.AttemptID
	spec.LeaseGeneration = lease.LeaseGeneration
	c.setTaskSpec(spec)

	if len(spec.WorkspaceClaims) > 0 {
		if _, err := c.Claim(ctx, agentcoord.AgentClaimRequest{RunID: spec.RunID, Resources: spec.WorkspaceClaims}); err != nil {
			return agentcoord.AgentRun{}, err
		}
	}
	if owned {
		lease, err = c.renewLaunchAttachment(ctx, lease)
		if err != nil {
			return agentcoord.AgentRun{}, err
		}
		spec.AttemptID = lease.AttemptID
		spec.LeaseGeneration = lease.LeaseGeneration
		c.setTaskSpec(spec)
	}
	spawnEventID := runledger.StableEventID("subagent.spawn", spec.SessionID, spec.RunID, spec.AttemptID, fmt.Sprint(spec.LeaseGeneration))
	if err := c.appendWithID(ctx, spec, runledger.EventSubagentSpawned, spawnEventID, map[string]any{
		"state":            string(agentcoord.AgentRunQueued),
		"task_summary":     boundedCoordinatorText(spec.Task, 512),
		"persona":          spec.Persona,
		"tier":             spec.Tier,
		"effort":           spec.Effort,
		"timeout_seconds":  spec.TimeoutSeconds,
		"workspace_claims": spec.WorkspaceClaims,
		"session_id":       spec.SessionID,
		"attempt_id":       spec.AttemptID,
		"lease_generation": spec.LeaseGeneration,
	}, taskEvidence); err != nil {
		return agentcoord.AgentRun{}, err
	}

	if snapshot, ok := c.manager.Status(spec.RunID); ok {
		if snapshot.AttemptID != spec.AttemptID || snapshot.LeaseGeneration != spec.LeaseGeneration {
			return agentcoord.AgentRun{}, fmt.Errorf("subagent run %s is locally attached under a different generation", spec.RunID)
		}
		return c.agentRunFromSnapshot(snapshot), nil
	}
	if !owned {
		return c.agentRunFromLedger(durableRun, true), nil
	}
	if _, err := c.renewLaunchAttachment(ctx, lease); err != nil {
		return agentcoord.AgentRun{}, err
	}
	snapshot, err := c.manager.SpawnWithOptions(spawnOptionsFromTask(spec))
	if err != nil {
		return agentcoord.AgentRun{}, fmt.Errorf("spawn local subagent: %w", err)
	}
	return c.agentRunFromSnapshot(snapshot), nil
}

func (c *Coordinator) renewLaunchAttachment(ctx context.Context, lease agentcoord.AttachmentLease) (agentcoord.AttachmentLease, error) {
	leaseDuration, err := validateCanonicalAttachmentLease(c.attachmentLease)
	if err != nil {
		return agentcoord.AttachmentLease{}, err
	}
	renewed, err := c.attachments.Heartbeat(ctx, agentcoord.AttachmentHeartbeatRequest{
		SessionID:       lease.SessionID,
		RunID:           lease.RunID,
		AttemptID:       lease.AttemptID,
		LeaseGeneration: lease.LeaseGeneration,
		LeaseDuration:   leaseDuration,
	})
	if err != nil {
		return agentcoord.AttachmentLease{}, fmt.Errorf("renew durable subagent attachment before launch: %w", err)
	}
	c.rememberOwnedAttempt(renewed)
	return renewed, nil
}

func (c *Coordinator) requireDurableKernel() error {
	if c == nil || nilPort(c.ledger) || nilPort(c.evidence) || nilPort(c.mailbox) || nilPort(c.attachments) || nilPort(c.contracts) || nilPort(c.finalizer) || nilPort(c.claims) {
		return fmt.Errorf("subagent coordinator durable spawn requires evidence, mailbox, attachment, contract, finalization, and claim stores")
	}
	return nil
}

func (c *Coordinator) persistTaskContract(ctx context.Context, spec agentcoord.AgentTaskSpec) (agentcoord.AgentTaskSpec, []string, string, error) {
	logical := spec
	logical.AttemptID = ""
	logical.LeaseGeneration = 0
	body, err := json.Marshal(logical)
	if err != nil {
		return agentcoord.AgentTaskSpec{}, nil, "", fmt.Errorf("marshal subagent task contract: %w", err)
	}
	if len(body) > maxTaskContractBytes {
		return agentcoord.AgentTaskSpec{}, nil, "", fmt.Errorf("subagent task contract exceeds %d bytes", maxTaskContractBytes)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(body))
	obj, err := c.evidence.Put(ctx, evidence.Object{
		Kind:       evidence.KindSubagentTask,
		MediaType:  "application/json",
		InlineBody: body,
		Metadata: map[string]any{
			evidence.MetaSessionID: logical.SessionID,
			evidence.MetaRunID:     logical.RunID,
			evidence.MetaTaskID:    logical.ID,
			evidence.MetaEntityID:  logical.Agent,
		},
	})
	if err != nil {
		return agentcoord.AgentTaskSpec{}, nil, "", fmt.Errorf("store subagent task contract: %w", err)
	}
	if err := c.evidence.Pin(ctx, obj.ID, "run:"+logical.RunID); err != nil {
		return agentcoord.AgentTaskSpec{}, nil, "", fmt.Errorf("pin subagent task contract: %w", err)
	}
	c.addEvidence(logical.RunID, obj.ID)
	return logical, []string{obj.ID}, digest, nil
}

func (c *Coordinator) ensureSpawnAttachment(ctx context.Context, spec agentcoord.AgentTaskSpec) (agentcoord.AttachmentLease, bool, error) {
	leaseDuration, err := validateCanonicalAttachmentLease(c.attachmentLease)
	if err != nil {
		return agentcoord.AttachmentLease{}, false, err
	}
	sessionID := firstNonEmpty(spec.SessionID, spec.ParentSessionID)
	current, err := c.attachments.Current(ctx, sessionID, spec.RunID)
	if err == nil {
		return current, c.ownsAttempt(current), nil
	}
	if !errors.Is(err, runledger.ErrAttachmentNotFound) && !errors.Is(err, runledger.ErrAttachmentExpired) {
		return agentcoord.AttachmentLease{}, false, fmt.Errorf("inspect durable subagent attachment: %w", err)
	}
	attemptID := strings.TrimSpace(spec.AttemptID)
	if attemptID == "" {
		attemptID = "attempt_" + ulid.Make().String()
	}
	lease, err := c.attachments.Attach(ctx, agentcoord.AttachmentRequest{
		SessionID:     sessionID,
		RunID:         spec.RunID,
		ParentRunID:   spec.ParentRunID,
		TaskID:        spec.ID,
		TurnID:        spec.TurnID,
		AttemptID:     attemptID,
		LeaseDuration: leaseDuration,
	})
	if err != nil {
		if errors.Is(err, runledger.ErrAttachmentConflict) {
			if elected, currentErr := c.attachments.Current(ctx, sessionID, spec.RunID); currentErr == nil {
				return elected, c.ownsAttempt(elected), nil
			}
		}
		return agentcoord.AttachmentLease{}, false, fmt.Errorf("attach durable subagent run: %w", err)
	}
	c.rememberOwnedAttempt(lease)
	return lease, true, nil
}

func (c *Coordinator) rememberOwnedAttempt(lease agentcoord.AttachmentLease) {
	c.mu.Lock()
	c.ownedAttempts[lease.RunID] = lease
	c.mu.Unlock()
}

func (c *Coordinator) ownsAttempt(lease agentcoord.AttachmentLease) bool {
	c.mu.RLock()
	owned, ok := c.ownedAttempts[lease.RunID]
	c.mu.RUnlock()
	return ok && owned.AttemptID == lease.AttemptID && owned.LeaseGeneration == lease.LeaseGeneration
}

func (c *Coordinator) forgetOwnedAttempt(runID, attemptID string, generation int64) {
	c.mu.Lock()
	owned, ok := c.ownedAttempts[runID]
	if ok && owned.AttemptID == attemptID && owned.LeaseGeneration == generation {
		delete(c.ownedAttempts, runID)
	}
	c.mu.Unlock()
}

func (c *Coordinator) applyAdmission(ctx context.Context, spec *agentcoord.AgentTaskSpec) error {
	if c == nil || spec == nil {
		return nil
	}
	c.mu.RLock()
	policy := c.policy
	c.mu.RUnlock()
	if policy == nil {
		return nil
	}
	if nilPort(policy) {
		return fmt.Errorf("evaluate subagent admission: policy is unavailable")
	}
	decision, err := policy.Admit(ctx, *spec)
	if err != nil {
		return fmt.Errorf("evaluate subagent admission: %w", err)
	}
	if !decision.Allowed {
		reason := strings.TrimSpace(decision.Reason)
		if reason == "" {
			reason = "policy denied subagent spawn"
		}
		return fmt.Errorf("subagent spawn denied: %s", reason)
	}
	if decision.TimeoutSeconds > 0 && (spec.TimeoutSeconds == 0 || spec.TimeoutSeconds > decision.TimeoutSeconds) {
		spec.TimeoutSeconds = decision.TimeoutSeconds
	}
	if decision.TimeoutSeconds > 0 && (spec.Budget.MaxElapsedSecond == 0 || spec.Budget.MaxElapsedSecond > decision.TimeoutSeconds) {
		spec.Budget.MaxElapsedSecond = decision.TimeoutSeconds
	}
	if decision.StepCap > 0 && (spec.StepCap == 0 || spec.StepCap > decision.StepCap) {
		spec.StepCap = decision.StepCap
	}
	if decision.StepCap > 0 && (spec.Budget.MaxModelRequests == 0 || spec.Budget.MaxModelRequests > decision.StepCap) {
		spec.Budget.MaxModelRequests = decision.StepCap
	}
	return nil
}

func (c *Coordinator) requireDependencies(ctx context.Context, dependencies []string) error {
	for _, dependency := range uniqueStrings(dependencies) {
		run, err := c.Status(ctx, dependency)
		if err != nil {
			return fmt.Errorf("subagent dependency %s: %w", dependency, err)
		}
		if run.State != agentcoord.AgentRunCompleted {
			return fmt.Errorf("subagent dependency %s is %s, not completed", dependency, run.State)
		}
	}
	return nil
}

func (c *Coordinator) registerRun(spec agentcoord.AgentTaskSpec) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, exists := c.runs[spec.RunID]; exists {
		if logicalTaskDigest(existing) != logicalTaskDigest(spec) {
			return fmt.Errorf("subagent run already exists with a different task contract: %s", spec.RunID)
		}
		return nil
	}
	c.runs[spec.RunID] = spec
	return nil
}

func logicalTaskDigest(spec agentcoord.AgentTaskSpec) string {
	spec.AttemptID = ""
	spec.LeaseGeneration = 0
	body, _ := json.Marshal(spec)
	return fmt.Sprintf("%x", sha256.Sum256(body))
}

func (c *Coordinator) unregisterRun(runID string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	delete(c.runs, strings.TrimSpace(runID))
	c.mu.Unlock()
}

func (c *Coordinator) setTaskSpec(spec agentcoord.AgentTaskSpec) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.runs[spec.RunID] = spec
	c.mu.Unlock()
}

func spawnOptionsFromTask(spec agentcoord.AgentTaskSpec) SpawnOptions {
	return SpawnOptions{
		ID:              spec.RunID,
		SessionID:       firstNonEmpty(spec.SessionID, spec.ParentSessionID),
		ParentSessionID: spec.ParentSessionID,
		ParentRunID:     spec.ParentRunID,
		TaskID:          spec.ID,
		TurnID:          spec.TurnID,
		AttemptID:       spec.AttemptID,
		LeaseGeneration: spec.LeaseGeneration,
		Agent:           spec.Agent,
		Spec:            spec.Spec,
		Task:            spec.Task,
		TimeoutSeconds:  spec.TimeoutSeconds,
		Budget:          spec.Budget,
		Persona:         spec.Persona,
		Model:           spec.Model,
		Tier:            persona.Tier(spec.Tier),
		SystemPrompt:    spec.SystemPrompt,
		AllowedTools:    copyStrings(spec.AllowedTools),
		StepCap:         spec.StepCap,
		Effort:          spec.Effort,
		WorkspaceClaims: copyStrings(spec.WorkspaceClaims),
		Isolation:       spec.Isolation,
		OutputSchema:    spec.OutputSchema,
		ApprovalPosture: spec.ApprovalPosture,
	}
}

func normalizeTaskSpec(spec agentcoord.AgentTaskSpec) (agentcoord.AgentTaskSpec, error) {
	spec.SessionID = strings.TrimSpace(spec.SessionID)
	spec.RunID = strings.TrimSpace(spec.RunID)
	spec.ID = strings.TrimSpace(spec.ID)
	spec.ParentRunID = strings.TrimSpace(spec.ParentRunID)
	spec.ParentSessionID = strings.TrimSpace(spec.ParentSessionID)
	spec.TurnID = strings.TrimSpace(spec.TurnID)
	spec.AttemptID = strings.TrimSpace(spec.AttemptID)
	spec.Agent = strings.TrimSpace(spec.Agent)
	spec.Spec = strings.TrimSpace(spec.Spec)
	spec.Task = strings.TrimSpace(spec.Task)
	spec.Persona = strings.TrimSpace(spec.Persona)
	spec.Model = strings.TrimSpace(spec.Model)
	spec.Tier = strings.TrimSpace(spec.Tier)
	spec.Effort = strings.TrimSpace(spec.Effort)
	spec.SystemPrompt = strings.TrimSpace(spec.SystemPrompt)
	spec.Isolation = strings.TrimSpace(spec.Isolation)
	spec.OutputSchema = strings.TrimSpace(spec.OutputSchema)
	spec.ApprovalPosture = strings.TrimSpace(spec.ApprovalPosture)
	if spec.SessionID == "" {
		spec.SessionID = spec.ParentSessionID
	}
	if spec.ParentSessionID == "" {
		spec.ParentSessionID = spec.SessionID
	}
	if spec.Task == "" {
		return agentcoord.AgentTaskSpec{}, fmt.Errorf("subagent task is required")
	}
	for field, value := range map[string]string{
		"session_id": spec.SessionID, "run_id": spec.RunID, "task_id": spec.ID,
		"parent_run_id": spec.ParentRunID, "parent_session_id": spec.ParentSessionID,
		"turn_id": spec.TurnID, "attempt_id": spec.AttemptID, "agent": spec.Agent,
		"persona": spec.Persona, "model": spec.Model, "tier": spec.Tier, "effort": spec.Effort,
		"isolation": spec.Isolation, "approval_posture": spec.ApprovalPosture,
	} {
		if len(value) > maxCoordinatorIdentifier {
			return agentcoord.AgentTaskSpec{}, fmt.Errorf("subagent %s exceeds %d bytes", field, maxCoordinatorIdentifier)
		}
	}
	for field, value := range map[string]string{
		"spec": spec.Spec, "task": spec.Task, "system_prompt": spec.SystemPrompt, "output_schema": spec.OutputSchema,
	} {
		if len(value) > maxTaskTextBytes {
			return agentcoord.AgentTaskSpec{}, fmt.Errorf("subagent %s exceeds %d bytes", field, maxTaskTextBytes)
		}
	}
	if len(spec.Dependencies) > maxTaskCollectionItems || len(spec.AllowedTools) > maxTaskCollectionItems || len(spec.WorkspaceClaims) > maxTaskCollectionItems {
		return agentcoord.AgentTaskSpec{}, fmt.Errorf("subagent dependencies, tools, or claims exceed %d items", maxTaskCollectionItems)
	}
	for _, value := range append(append(append([]string(nil), spec.Dependencies...), spec.AllowedTools...), spec.WorkspaceClaims...) {
		if len(strings.TrimSpace(value)) > maxCoordinatorIdentifier {
			return agentcoord.AgentTaskSpec{}, fmt.Errorf("subagent dependency, tool, or claim exceeds %d bytes", maxCoordinatorIdentifier)
		}
	}
	if math.IsNaN(spec.Budget.MaxCostUSD) || math.IsInf(spec.Budget.MaxCostUSD, 0) {
		return agentcoord.AgentTaskSpec{}, fmt.Errorf("subagent max_cost_usd must be finite")
	}
	if spec.TimeoutSeconds < 0 || spec.StepCap < 0 || spec.DelegationDepth < 0 || spec.Budget.MaxToolCalls < 0 || spec.Budget.MaxModelRequests < 0 || spec.Budget.MaxElapsedSecond < 0 || spec.Budget.MaxCostUSD < 0 {
		return agentcoord.AgentTaskSpec{}, fmt.Errorf("subagent limits cannot be negative")
	}
	spec.Dependencies = uniqueStrings(spec.Dependencies)
	spec.AllowedTools = copyStrings(spec.AllowedTools)
	claims, err := normalizeWorkspaceClaims(spec.WorkspaceClaims)
	if err != nil {
		return agentcoord.AgentTaskSpec{}, err
	}
	spec.WorkspaceClaims = claims
	return spec, nil
}

func normalizeWorkspaceClaims(resources []string) ([]string, error) {
	seen := make(map[string]struct{}, len(resources))
	out := make([]string, 0, len(resources))
	for _, resource := range resources {
		resource = strings.ReplaceAll(strings.TrimSpace(resource), "\\", "/")
		if resource == "" {
			continue
		}
		if strings.HasPrefix(resource, "/") {
			return nil, fmt.Errorf("workspace claim must be relative: %s", resource)
		}
		resource = path.Clean(resource)
		if resource == ".." || strings.HasPrefix(resource, "../") {
			return nil, fmt.Errorf("workspace claim escapes workspace: %s", resource)
		}
		if _, exists := seen[resource]; exists {
			continue
		}
		seen[resource] = struct{}{}
		out = append(out, resource)
	}
	sort.Strings(out)
	return out, nil
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func budgetMap(budget agentcoord.AgentBudget) map[string]any {
	return map[string]any{
		"max_tool_calls":      budget.MaxToolCalls,
		"max_model_requests":  budget.MaxModelRequests,
		"max_elapsed_seconds": budget.MaxElapsedSecond,
		"max_cost_usd":        budget.MaxCostUSD,
	}
}

// List returns the authoritative durable projection when a ledger is present,
// overlaying a locally attached process where it has fresher lifecycle state.
func (c *Coordinator) List(ctx context.Context, filter agentcoord.AgentRunFilter) ([]agentcoord.AgentRun, error) {
	if c == nil {
		return nil, fmt.Errorf("subagent coordinator is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if c.ledger != nil && nilPort(c.ledger) {
		return nil, fmt.Errorf("subagent coordinator durable ledger is unavailable")
	}
	if c.ledger == nil {
		return c.listLocal(filter), nil
	}
	runs, err := c.ledger.ListRuns(ctx, runledger.RunQuery{
		SessionID:   firstNonEmpty(strings.TrimSpace(filter.SessionID), strings.TrimSpace(filter.ParentSessionID)),
		TaskID:      strings.TrimSpace(filter.TaskID),
		ParentRunID: strings.TrimSpace(filter.ParentRunID),
		Limit:       filter.Limit,
	})
	if err != nil {
		return nil, fmt.Errorf("list durable subagent runs: %w", err)
	}
	out := make([]agentcoord.AgentRun, 0, len(runs))
	for _, durable := range runs {
		if durable.Backend != c.adapter {
			continue
		}
		if _, err := c.taskSpecForRun(ctx, durable.RunID); err != nil {
			return nil, err
		}
		if snapshot, ok := c.localSnapshot(durable.RunID); ok {
			out = append(out, c.agentRunFromSnapshot(snapshot))
			continue
		}
		out = append(out, c.agentRunFromLedger(durable, true))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	return out, nil
}

func (c *Coordinator) listLocal(filter agentcoord.AgentRunFilter) []agentcoord.AgentRun {
	if c == nil || c.manager == nil {
		return nil
	}
	snapshots := c.manager.List()
	out := make([]agentcoord.AgentRun, 0, len(snapshots))
	for _, snapshot := range snapshots {
		run := c.agentRunFromSnapshot(snapshot)
		if !matchesRunFilter(run, filter) {
			continue
		}
		out = append(out, run)
		if filter.Limit > 0 && len(out) >= filter.Limit {
			break
		}
	}
	return out
}

// Status returns a current process snapshot when attached, otherwise a
// durable status. A durable running run with no attached local process is
// explicitly projected as resumable rather than silently disappearing.
func (c *Coordinator) Status(ctx context.Context, id string) (agentcoord.AgentRun, error) {
	if c == nil {
		return agentcoord.AgentRun{}, fmt.Errorf("subagent coordinator is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return agentcoord.AgentRun{}, fmt.Errorf("subagent run id is required")
	}
	if snapshot, ok := c.localSnapshot(id); ok {
		return c.agentRunFromSnapshot(snapshot), nil
	}
	if c.ledger == nil {
		return agentcoord.AgentRun{}, fmt.Errorf("subagent not found: %s", id)
	}
	if nilPort(c.ledger) {
		return agentcoord.AgentRun{}, fmt.Errorf("subagent coordinator durable ledger is unavailable")
	}
	durable, err := c.ledger.GetRun(ctx, id)
	if err != nil {
		return agentcoord.AgentRun{}, fmt.Errorf("get durable subagent run %s: %w", id, err)
	}
	if durable.Backend != c.adapter {
		return agentcoord.AgentRun{}, fmt.Errorf("subagent not found: %s", id)
	}
	if _, err := c.taskSpecForRun(ctx, id); err != nil {
		return agentcoord.AgentRun{}, err
	}
	return c.agentRunFromLedger(durable, true), nil
}

// Wait waits for an attached local worker. A run that survived a worker loss
// remains discoverable, but cannot be falsely reported as waited or complete.
func (c *Coordinator) Wait(ctx context.Context, id string) (agentcoord.AgentRun, error) {
	if c == nil {
		return agentcoord.AgentRun{}, fmt.Errorf("subagent coordinator is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	id = strings.TrimSpace(id)
	if c.manager != nil {
		if _, ok := c.manager.Status(id); ok {
			snapshot, err := c.manager.Wait(ctx, id)
			if err != nil {
				return agentcoord.AgentRun{}, err
			}
			return c.agentRunFromSnapshot(snapshot), nil
		}
	}
	run, err := c.Status(ctx, id)
	if err != nil {
		return agentcoord.AgentRun{}, err
	}
	if run.State.Terminal() {
		return run, nil
	}
	return run, fmt.Errorf("subagent run %s is durable but has no attached local worker; resume it before waiting", id)
}

// Steer persists a high-priority human direction for a child. Interactive
// adapters may then claim live delivery; detached workers remain queued.
func (c *Coordinator) Steer(ctx context.Context, id, content string) (agentcoord.AgentMessage, error) {
	return c.send(ctx, agentcoord.AgentMessage{
		RunID:    strings.TrimSpace(id),
		To:       strings.TrimSpace(id),
		From:     agentcoord.OperatorIdentity,
		Kind:     agentcoord.OperatorSteerKind,
		Content:  strings.TrimSpace(content),
		Delivery: "queued",
	}, messageAuthorityOperator)
}

// Publish authorizes an exact current child attempt before enqueueing into
// that child's recorded parent mailbox. Source and target delivery leases are
// deliberately separate identities.
func (c *Coordinator) Publish(ctx context.Context, message agentcoord.AgentMessage) (agentcoord.AgentMessage, error) {
	if c == nil {
		return agentcoord.AgentMessage{}, fmt.Errorf("subagent coordinator is unavailable")
	}
	if c.ledger == nil {
		return c.send(ctx, message, messageAuthorityPublication)
	}
	if nilPort(c.ledger) || nilPort(c.attachments) {
		return agentcoord.AgentMessage{}, fmt.Errorf("subagent coordinator durable publication requires a ledger and attachment store")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	sourceRunID := strings.TrimSpace(message.From)
	targetRunID := firstNonEmpty(strings.TrimSpace(message.RunID), strings.TrimSpace(message.To))
	if sourceRunID == "" || targetRunID == "" {
		return agentcoord.AgentMessage{}, fmt.Errorf("subagent publication requires source and target run ids")
	}
	source, err := c.ledger.GetRun(ctx, sourceRunID)
	if err != nil || source.Backend != c.adapter {
		return agentcoord.AgentMessage{}, fmt.Errorf("authorize subagent publication source %s: %w", sourceRunID, firstError(err, fmt.Errorf("foreign source run")))
	}
	if source.ParentRunID == "" || source.ParentRunID != targetRunID {
		return agentcoord.AgentMessage{}, fmt.Errorf("subagent publication target must equal source run %s recorded parent", sourceRunID)
	}
	target, err := c.ledger.GetRun(ctx, targetRunID)
	if err != nil || target.SessionID != source.SessionID {
		return agentcoord.AgentMessage{}, fmt.Errorf("authorize subagent publication target %s: %w", targetRunID, firstError(err, fmt.Errorf("cross-session target")))
	}
	if supplied := strings.TrimSpace(message.SessionID); supplied != "" && supplied != source.SessionID {
		return agentcoord.AgentMessage{}, fmt.Errorf("subagent publication session does not match durable source")
	}
	sourceAttempt := firstNonEmpty(message.SourceAttemptID, message.AttemptID)
	sourceGeneration := message.SourceLeaseGeneration
	if sourceGeneration <= 0 {
		sourceGeneration = message.LeaseGeneration
	}
	if sourceAttempt == "" || sourceGeneration <= 0 {
		return agentcoord.AgentMessage{}, fmt.Errorf("subagent publication requires the source attempt and generation")
	}
	current, err := c.attachments.Current(ctx, source.SessionID, source.RunID)
	if err != nil || current.AttemptID != sourceAttempt || current.LeaseGeneration != sourceGeneration {
		return agentcoord.AgentMessage{}, fmt.Errorf("authorize subagent publication attachment: %w", firstError(err, runledger.ErrAttachmentStale))
	}
	message.SessionID = source.SessionID
	message.From = source.RunID
	message.RunID = target.RunID
	message.To = target.RunID
	message.ParentRunID = target.ParentRunID
	message.SourceAttemptID = sourceAttempt
	message.SourceLeaseGeneration = sourceGeneration
	message.AttemptID = ""
	message.LeaseGeneration = 0
	return c.send(ctx, message, messageAuthorityPublication)
}

type messageAuthority uint8

const (
	messageAuthorityParent messageAuthority = iota
	messageAuthorityPublication
	messageAuthorityOperator
)

// Send writes a durable mailbox entry before attempting live delivery. Message
// bodies live in evidence; events carry bounded previews and evidence
// references so telemetry never becomes a hidden content store.
func (c *Coordinator) Send(ctx context.Context, message agentcoord.AgentMessage) (agentcoord.AgentMessage, error) {
	return c.send(ctx, message, messageAuthorityParent)
}

func (c *Coordinator) send(ctx context.Context, message agentcoord.AgentMessage, authority messageAuthority) (agentcoord.AgentMessage, error) {
	if c == nil {
		return agentcoord.AgentMessage{}, fmt.Errorf("subagent coordinator is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	durable := c.ledger != nil
	if durable && (nilPort(c.ledger) || nilPort(c.mailbox) || nilPort(c.attachments) || nilPort(c.evidence)) {
		return agentcoord.AgentMessage{}, fmt.Errorf("subagent coordinator durable mailbox requires evidence, mailbox, and attachment stores")
	}
	var operatorMailbox agentcoord.OperatorMailboxStore
	if durable && authority == messageAuthorityOperator {
		var ok bool
		operatorMailbox, ok = c.mailbox.(agentcoord.OperatorMailboxStore)
		if !ok || nilPort(operatorMailbox) {
			return agentcoord.AgentMessage{}, fmt.Errorf("subagent coordinator durable steering requires a trusted operator mailbox store")
		}
	}
	message.RunID = strings.TrimSpace(message.RunID)
	message.To = strings.TrimSpace(message.To)
	message.From = strings.TrimSpace(message.From)
	message.Kind = strings.TrimSpace(message.Kind)
	message.Content = strings.TrimSpace(message.Content)
	if message.RunID == "" {
		message.RunID = message.To
	}
	if message.To == "" {
		message.To = message.RunID
	}
	if message.RunID == "" || message.To == "" {
		return agentcoord.AgentMessage{}, fmt.Errorf("subagent message target is required")
	}
	if message.Kind == "" {
		message.Kind = "message"
	}
	if message.Content == "" {
		return agentcoord.AgentMessage{}, fmt.Errorf("subagent message content is required")
	}
	if !durable && message.ID == "" && message.MessageID == "" {
		message.ID = "msg_" + ulid.Make().String()
	}
	if message.Delivery == "" {
		message.Delivery = "queued"
	}
	if message.CreatedAt.IsZero() {
		message.CreatedAt = c.now()
	}
	if err := validateCoordinatorMessageBounds(message); err != nil {
		return agentcoord.AgentMessage{}, err
	}
	if _, err := c.Status(ctx, message.RunID); err != nil {
		return agentcoord.AgentMessage{}, err
	}
	spec, err := c.taskSpecForRun(ctx, message.RunID)
	if err != nil {
		return agentcoord.AgentMessage{}, err
	}
	if durable {
		message, err = c.authorizeDurableMessage(ctx, message, spec, authority)
		if err != nil {
			return agentcoord.AgentMessage{}, err
		}
	}
	message, err = c.enrichMessageIdentity(ctx, message, spec)
	if err != nil {
		return agentcoord.AgentMessage{}, err
	}
	// Keep the freshly authorized target attachment fence separate from the
	// persisted envelope. A queued row intentionally clears its mutable target
	// fence on Nack, but an identical retry is still eligible for immediate
	// delivery under the newly authorized current attachment.
	authorizedTargetAttemptID := message.AttemptID
	authorizedTargetGeneration := message.LeaseGeneration
	if err := validateCoordinatorMessageBounds(message); err != nil {
		return agentcoord.AgentMessage{}, err
	}
	refs, err := c.storeMessage(ctx, message)
	if err != nil {
		return agentcoord.AgentMessage{}, err
	}
	message.EvidenceRefs = refs
	if durable && len(refs) > 0 && !nilPort(c.evidence) {
		object, getErr := c.evidence.Get(ctx, refs[0])
		if getErr != nil {
			return agentcoord.AgentMessage{}, fmt.Errorf("verify durable subagent message evidence: %w", getErr)
		}
		message.ContentRef = object.ID
		message.ContentDigest = object.ContentSHA256
		message.MediaType = object.MediaType
		message.ByteCount = object.ByteCount
	}
	if durable && message.ID == "" && message.MessageID == "" && strings.TrimSpace(message.IdempotencyKey) == "" {
		message.IdempotencyKey = implicitCoordinatorMessageKey(message)
	}
	if err := validateCoordinatorMessageBounds(message); err != nil {
		return agentcoord.AgentMessage{}, err
	}
	created := true
	if durable {
		var persisted agentcoord.AgentMessage
		if authority == messageAuthorityOperator {
			persisted, created, err = operatorMailbox.EnqueueOperatorSteer(ctx, message)
		} else {
			persisted, created, err = c.mailbox.Enqueue(ctx, message)
		}
		if err != nil {
			return agentcoord.AgentMessage{}, fmt.Errorf("enqueue durable subagent message: %w", err)
		}
		if len(persisted.EvidenceRefs) == 0 && persisted.ContentRef != "" {
			persisted.EvidenceRefs = []string{persisted.ContentRef}
		}
		if len(persisted.EvidenceRefs) == 0 {
			persisted.EvidenceRefs = refs
		}
		if persisted.Content == "" && persisted.Preview != "" {
			persisted.Content = persisted.Preview
		}
		message = persisted
	}
	// Enqueue and audit are separately idempotent. Repeating a logical send
	// always repairs a previously failed audit append, even when the mailbox row
	// was already committed and created=false.
	if err := c.appendMessage(ctx, message); err != nil {
		return agentcoord.AgentMessage{}, err
	}
	if created {
		c.addMailbox(message)
	}
	if c.manager != nil {
		owner := "coordinator:" + c.adapter
		claimed := false
		localTarget := true
		if durable {
			localTarget = false
			if snapshot, ok := c.manager.Status(message.RunID); ok {
				localTarget = authorizedTargetAttemptID != "" && authorizedTargetGeneration > 0 &&
					snapshot.AttemptID == authorizedTargetAttemptID && snapshot.LeaseGeneration == authorizedTargetGeneration
			}
		}
		if durable && localTarget {
			claims, claimErr := c.mailbox.Claim(ctx, agentcoord.MailboxClaimRequest{
				SessionID:       message.SessionID,
				RunID:           message.RunID,
				MessageID:       message.ID,
				Owner:           owner,
				AttemptID:       authorizedTargetAttemptID,
				LeaseGeneration: authorizedTargetGeneration,
				Limit:           1,
			})
			if claimErr == nil && len(claims) > 0 {
				claimed = true
				message = claims[0]
				if err := c.hydrateMessageContent(ctx, &message); err != nil {
					_ = c.mailbox.Nack(ctx, agentcoord.MailboxNackRequest{
						MailboxAckRequest: agentcoord.MailboxAckRequest{
							SessionID:       message.SessionID,
							RunID:           message.RunID,
							MessageID:       message.ID,
							Owner:           owner,
							AttemptID:       message.AttemptID,
							LeaseGeneration: message.LeaseGeneration,
						},
						Reason: "read message evidence",
					})
					return agentcoord.AgentMessage{}, err
				}
			} else if claimErr != nil && created {
				c.noteDurabilityError(message.RunID, claimErr)
			}
		}
		if !durable || claimed {
			if err := c.manager.Deliver(ctx, message.RunID, message); err == nil {
				if durable {
					if err := c.mailbox.Ack(ctx, agentcoord.MailboxAckRequest{
						SessionID:       message.SessionID,
						RunID:           message.RunID,
						MessageID:       message.ID,
						Owner:           owner,
						AttemptID:       message.AttemptID,
						LeaseGeneration: message.LeaseGeneration,
					}); err != nil {
						c.noteDurabilityError(message.RunID, err)
					}
				}
				message.Delivery = "delivered"
				message.State = agentcoord.MessageProcessed
				c.updateMailboxDelivery(message.ID, message.RunID, message.Delivery)
				if err := c.appendMessageDelivery(ctx, message); err != nil {
					c.noteDurabilityError(message.RunID, err)
				}
			} else if durable && claimed {
				_ = c.mailbox.Nack(ctx, agentcoord.MailboxNackRequest{
					MailboxAckRequest: agentcoord.MailboxAckRequest{
						SessionID:       message.SessionID,
						RunID:           message.RunID,
						MessageID:       message.ID,
						Owner:           owner,
						AttemptID:       message.AttemptID,
						LeaseGeneration: message.LeaseGeneration,
					},
					Reason: "live delivery failed",
				})
				message.State = agentcoord.MessageQueued
				message.Delivery = agentcoord.MessageQueued
			}
		}
	}
	return message, nil
}

func (c *Coordinator) authorizeDurableMessage(ctx context.Context, message agentcoord.AgentMessage, target agentcoord.AgentTaskSpec, authority messageAuthority) (agentcoord.AgentMessage, error) {
	sessionID := firstNonEmpty(target.SessionID, target.ParentSessionID)
	if supplied := strings.TrimSpace(message.SessionID); supplied != "" && supplied != sessionID {
		return agentcoord.AgentMessage{}, fmt.Errorf("subagent message session does not match durable target")
	}
	switch authority {
	case messageAuthorityOperator:
		message.From = agentcoord.OperatorIdentity
		message.Kind = agentcoord.OperatorSteerKind
		message.SourceAttemptID = ""
		message.SourceLeaseGeneration = 0
		return message, nil
	case messageAuthorityPublication:
		if message.From == "" || message.SourceAttemptID == "" || message.SourceLeaseGeneration <= 0 {
			return agentcoord.AgentMessage{}, fmt.Errorf("subagent publication requires its exact source attachment fence")
		}
		return message, nil
	case messageAuthorityParent:
		parentRunID := strings.TrimSpace(target.ParentRunID)
		if parentRunID == "" {
			return agentcoord.AgentMessage{}, fmt.Errorf("subagent message target %s has no recorded parent", target.RunID)
		}
		if strings.TrimSpace(message.From) == "" || strings.TrimSpace(message.From) != parentRunID {
			return agentcoord.AgentMessage{}, fmt.Errorf("subagent message source must equal target %s recorded parent", target.RunID)
		}
		parent, err := c.ledger.GetRun(ctx, parentRunID)
		if err != nil {
			return agentcoord.AgentMessage{}, fmt.Errorf("authorize subagent message parent %s: %w", parentRunID, err)
		}
		if parent.SessionID != sessionID {
			return agentcoord.AgentMessage{}, fmt.Errorf("subagent message parent belongs to a different session")
		}
		if strings.TrimSpace(message.SourceAttemptID) == "" || message.SourceLeaseGeneration <= 0 {
			return agentcoord.AgentMessage{}, fmt.Errorf("subagent message requires the parent attempt and generation")
		}
		lease, err := c.attachments.Current(ctx, sessionID, parentRunID)
		if err != nil || lease.AttemptID != message.SourceAttemptID || lease.LeaseGeneration != message.SourceLeaseGeneration {
			return agentcoord.AgentMessage{}, fmt.Errorf("authorize subagent message parent attachment: %w", firstError(err, runledger.ErrAttachmentStale))
		}
		message.SessionID = sessionID
		message.From = parentRunID
		return message, nil
	default:
		return agentcoord.AgentMessage{}, fmt.Errorf("subagent message authority is invalid")
	}
}

func (c *Coordinator) hydrateMessageContent(ctx context.Context, message *agentcoord.AgentMessage) error {
	if message == nil || message.ContentRef == "" || c == nil || nilPort(c.evidence) {
		return nil
	}
	object, err := c.evidence.Get(ctx, message.ContentRef)
	if err != nil {
		return fmt.Errorf("read subagent message evidence %s: %w", message.ContentRef, err)
	}
	message.Content = string(object.InlineBody)
	message.EvidenceRefs = []string{object.ID}
	return nil
}

func (c *Coordinator) enrichMessageIdentity(ctx context.Context, message agentcoord.AgentMessage, spec agentcoord.AgentTaskSpec) (agentcoord.AgentMessage, error) {
	authoritativeSession := firstNonEmpty(spec.SessionID, spec.ParentSessionID)
	if supplied := strings.TrimSpace(message.SessionID); supplied != "" && supplied != authoritativeSession {
		return agentcoord.AgentMessage{}, fmt.Errorf("subagent message session does not match durable target")
	}
	if strings.TrimSpace(message.To) != "" && strings.TrimSpace(message.To) != spec.RunID {
		return agentcoord.AgentMessage{}, fmt.Errorf("subagent message to must equal its target mailbox run")
	}
	if supplied := strings.TrimSpace(message.ParentRunID); supplied != "" && supplied != spec.ParentRunID {
		return agentcoord.AgentMessage{}, fmt.Errorf("subagent message parent_run_id does not match durable target")
	}
	if supplied := strings.TrimSpace(message.TaskID); supplied != "" && supplied != spec.ID {
		return agentcoord.AgentMessage{}, fmt.Errorf("subagent message task_id does not match durable target")
	}
	message.ID = firstNonEmpty(message.ID, message.MessageID)
	message.MessageID = firstNonEmpty(message.MessageID, message.ID)
	message.SessionID = authoritativeSession
	message.ParentRunID = spec.ParentRunID
	message.TaskID = spec.ID
	message.TurnID = firstNonEmpty(message.TurnID, spec.TurnID)
	if message.AttemptID != "" || message.LeaseGeneration > 0 {
		if nilPort(c.attachments) {
			return agentcoord.AgentMessage{}, fmt.Errorf("subagent message has an attachment fence but no attachment store is configured")
		}
		lease, err := c.attachments.Current(ctx, message.SessionID, message.RunID)
		if err != nil || lease.AttemptID != message.AttemptID || lease.LeaseGeneration != message.LeaseGeneration {
			if err == nil {
				err = fmt.Errorf("attachment attempt %s generation %d is not current", message.AttemptID, message.LeaseGeneration)
			}
			return agentcoord.AgentMessage{}, fmt.Errorf("stale subagent message attachment: %w", err)
		}
	} else if !nilPort(c.attachments) {
		if lease, err := c.attachments.Current(ctx, message.SessionID, message.RunID); err == nil {
			message.AttemptID = lease.AttemptID
			message.LeaseGeneration = lease.LeaseGeneration
		}
	}
	return message, nil
}

// Messages reads the durable mailbox when present; otherwise it returns the
// bounded local queue. A failed evidence read leaves the preview available but
// never fabricates the missing body.
func (c *Coordinator) Messages(ctx context.Context, id string) ([]agentcoord.AgentMessage, error) {
	if c == nil {
		return nil, fmt.Errorf("subagent coordinator is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	id = strings.TrimSpace(id)
	if _, err := c.Status(ctx, id); err != nil {
		return nil, err
	}
	if c.ledger == nil {
		return c.localMessages(id), nil
	}
	if nilPort(c.ledger) {
		return nil, fmt.Errorf("subagent coordinator durable ledger is unavailable")
	}
	if nilPort(c.mailbox) || nilPort(c.evidence) {
		return nil, fmt.Errorf("subagent coordinator durable mailbox requires mailbox and evidence stores")
	}
	run, err := c.Status(ctx, id)
	if err != nil {
		return nil, err
	}
	messages, err := c.mailbox.List(ctx, agentcoord.MailboxQuery{
		SessionID: run.SessionID,
		RunID:     id,
		Limit:     defaultMailboxLimit,
	})
	if err != nil {
		return nil, fmt.Errorf("list durable subagent mailbox: %w", err)
	}
	for index := range messages {
		if messages[index].ContentRef != "" {
			object, getErr := c.evidence.Get(ctx, messages[index].ContentRef)
			if getErr != nil {
				return nil, fmt.Errorf("read durable subagent message %s: %w", messages[index].ID, getErr)
			}
			messages[index].Content = string(object.InlineBody)
			messages[index].EvidenceRefs = []string{object.ID}
		}
		if messages[index].State == agentcoord.MessageProcessed {
			messages[index].Delivery = "delivered"
		}
	}
	return messages, nil
}

// Cancel requests cancellation from an attached local worker. Durable runs
// without a worker stay visible as resumable rather than pretending a control
// signal was delivered.
func (c *Coordinator) Cancel(ctx context.Context, id, reason string) (agentcoord.AgentRun, error) {
	if c == nil || c.manager == nil {
		return agentcoord.AgentRun{}, fmt.Errorf("subagent coordinator has no live adapter")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	id = strings.TrimSpace(id)
	snapshot, err := c.manager.Cancel(id)
	if err != nil {
		if run, statusErr := c.Status(ctx, id); statusErr == nil && !run.State.Terminal() {
			return run, fmt.Errorf("subagent run %s is durable but has no attached local worker; cannot cancel it", id)
		}
		return agentcoord.AgentRun{}, err
	}
	spec, _ := c.taskSpec(id)
	if err := c.append(ctx, spec, runledger.EventSubagentUpdated, map[string]any{
		"state":  "cancellation_requested",
		"reason": boundedCoordinatorText(reason, 512),
	}, nil); err != nil {
		return c.agentRunFromSnapshot(snapshot), err
	}
	return c.agentRunFromSnapshot(snapshot), nil
}

// Claim reserves workspace resources before a child begins mutable work. With
// a durable ledger it requires the atomic ClaimJournal; falling back to
// process-local locks would silently weaken the cross-worker safety contract.
func (c *Coordinator) Claim(ctx context.Context, request agentcoord.AgentClaimRequest) (agentcoord.AgentClaimResult, error) {
	if c == nil {
		return agentcoord.AgentClaimResult{}, fmt.Errorf("subagent coordinator is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	request.RunID = strings.TrimSpace(request.RunID)
	if request.RunID == "" {
		return agentcoord.AgentClaimResult{}, fmt.Errorf("subagent claim run id is required")
	}
	resources, err := normalizeWorkspaceClaims(request.Resources)
	if err != nil {
		return agentcoord.AgentClaimResult{}, err
	}
	if len(resources) == 0 {
		return agentcoord.AgentClaimResult{RunID: request.RunID}, nil
	}
	if err := c.requireKnownRun(ctx, request.RunID); err != nil {
		return agentcoord.AgentClaimResult{}, err
	}

	if c.ledger != nil && nilPort(c.claims) {
		return agentcoord.AgentClaimResult{}, fmt.Errorf("subagent coordinator durable claims require a claim journal")
	}
	if !nilPort(c.claims) {
		claims, err := c.claims.AcquireClaims(ctx, request.RunID, resources)
		if err != nil {
			return agentcoord.AgentClaimResult{}, err
		}
		resources = claimResources(claims)
	} else if err := c.acquireFallbackClaims(request.RunID, resources); err != nil {
		return agentcoord.AgentClaimResult{}, err
	}

	spec, _ := c.taskSpec(request.RunID)
	if err := c.append(ctx, spec, runledger.EventSubagentClaimed, map[string]any{
		"resources": resources,
	}, nil); err != nil {
		_ = c.releaseClaims(ctx, request.RunID, resources, "claim event failed")
		return agentcoord.AgentClaimResult{}, err
	}
	c.recordClaims(request.RunID, resources)
	return agentcoord.AgentClaimResult{RunID: request.RunID, Resources: resources}, nil
}

// Release releases workspace resources. It is intentionally explicit so a
// parent can retain a completed child's claims until synthesis when needed.
func (c *Coordinator) Release(ctx context.Context, request agentcoord.AgentClaimRequest, reason string) error {
	if c == nil {
		return fmt.Errorf("subagent coordinator is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	request.RunID = strings.TrimSpace(request.RunID)
	if request.RunID == "" {
		return fmt.Errorf("subagent claim run id is required")
	}
	resources, err := normalizeWorkspaceClaims(request.Resources)
	if err != nil {
		return err
	}
	if err := c.releaseClaims(ctx, request.RunID, resources, reason); err != nil {
		return err
	}
	spec, _ := c.taskSpec(request.RunID)
	if err := c.append(ctx, spec, runledger.EventSubagentReleased, map[string]any{
		"resources": resources,
		"reason":    boundedCoordinatorText(reason, 512),
	}, nil); err != nil {
		return err
	}
	c.forgetClaims(request.RunID, resources)
	return nil
}

func (c *Coordinator) requireKnownRun(ctx context.Context, runID string) error {
	c.mu.RLock()
	_, known := c.runs[runID]
	c.mu.RUnlock()
	if known {
		return nil
	}
	if c.ledger == nil {
		return fmt.Errorf("subagent not found: %s", runID)
	}
	if nilPort(c.ledger) {
		return fmt.Errorf("subagent coordinator durable ledger is unavailable")
	}
	if _, err := c.ledger.GetRun(ctx, runID); err != nil {
		return fmt.Errorf("get subagent run %s: %w", runID, err)
	}
	return nil
}

func (c *Coordinator) acquireFallbackClaims(runID string, resources []string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, resource := range resources {
		for held, holder := range c.fallbackClaims {
			if holder != runID && workspaceClaimsOverlap(resource, held) {
				return fmt.Errorf("workspace claim conflict: %q is held by run %s", resource, holder)
			}
		}
	}
	for _, resource := range resources {
		c.fallbackClaims[resource] = runID
	}
	return nil
}

func (c *Coordinator) releaseClaims(ctx context.Context, runID string, resources []string, reason string) error {
	if !nilPort(c.claims) {
		return c.claims.ReleaseClaims(ctx, runID, resources, reason)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(resources) == 0 {
		for resource, holder := range c.fallbackClaims {
			if holder == runID {
				delete(c.fallbackClaims, resource)
			}
		}
		return nil
	}
	for _, resource := range resources {
		if c.fallbackClaims[resource] == runID {
			delete(c.fallbackClaims, resource)
		}
	}
	return nil
}

func (c *Coordinator) recordClaims(runID string, resources []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.claimsByRun[runID] == nil {
		c.claimsByRun[runID] = make(map[string]struct{})
	}
	for _, resource := range resources {
		c.claimsByRun[runID][resource] = struct{}{}
	}
}

func (c *Coordinator) forgetClaims(runID string, resources []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(resources) == 0 {
		delete(c.claimsByRun, runID)
		return
	}
	for _, resource := range resources {
		delete(c.claimsByRun[runID], resource)
	}
	if len(c.claimsByRun[runID]) == 0 {
		delete(c.claimsByRun, runID)
	}
}

func claimResources(claims []runledger.AgentClaim) []string {
	resources := make([]string, 0, len(claims))
	for _, claim := range claims {
		resources = append(resources, claim.Resource)
	}
	sort.Strings(resources)
	return resources
}

func workspaceClaimsOverlap(left, right string) bool {
	if left == "." || right == "." {
		return true
	}
	return left == right || strings.HasPrefix(left, right+"/") || strings.HasPrefix(right, left+"/")
}

func (c *Coordinator) heartbeatAttachment(ctx context.Context, snapshot Snapshot) error {
	if c == nil || nilPort(c.attachments) {
		return fmt.Errorf("durable attachment store is unavailable")
	}
	if snapshot.ID == "" || snapshot.SessionID == "" || snapshot.AttemptID == "" || snapshot.LeaseGeneration <= 0 {
		return fmt.Errorf("durable heartbeat requires exact session, run, attempt, and generation")
	}
	leaseDuration, err := validateCanonicalAttachmentLease(c.attachmentLease)
	if err != nil {
		return err
	}
	renewalTimeout := heartbeatRenewalTimeout(leaseDuration)
	renewalCtx, cancelRenewal := context.WithTimeoutCause(ctx, renewalTimeout, errHeartbeatRenewalTimeout)
	defer cancelRenewal()
	_, err = c.attachments.Heartbeat(renewalCtx, agentcoord.AttachmentHeartbeatRequest{
		SessionID:       snapshot.SessionID,
		RunID:           snapshot.ID,
		AttemptID:       snapshot.AttemptID,
		LeaseGeneration: snapshot.LeaseGeneration,
		LeaseDuration:   leaseDuration,
	})
	if err != nil {
		ownershipLost := errors.Is(err, runledger.ErrAttachmentStale) || errors.Is(err, runledger.ErrAttachmentExpired) || errors.Is(err, runledger.ErrAttachmentTerminal)
		cause := context.Cause(renewalCtx)
		if !ownershipLost && errors.Is(cause, errHeartbeatStopped) && errors.Is(err, context.Canceled) {
			return context.Canceled
		}
		if errors.Is(cause, errHeartbeatRenewalTimeout) {
			err = fmt.Errorf("durable heartbeat renewal exceeded %s: %w", renewalTimeout, context.DeadlineExceeded)
		}
		c.noteDurabilityError(snapshot.ID, err)
		c.forgetOwnedAttempt(snapshot.ID, snapshot.AttemptID, snapshot.LeaseGeneration)
		return err
	}
	return nil
}

func canonicalAttachmentLease(requested time.Duration) (time.Duration, error) {
	if requested < 0 {
		return 0, fmt.Errorf("attachment lease cannot be negative")
	}
	if requested == 0 {
		requested = defaultAttachmentLease
	}
	if requested > runledger.AttachmentMaxLease {
		requested = runledger.AttachmentMaxLease
	}
	if requested < minimumAttachmentLease {
		return 0, fmt.Errorf("%w: %s is below minimum %s", ErrAttachmentLeaseTooShort, requested, minimumAttachmentLease)
	}
	return requested, nil
}

func validateCanonicalAttachmentLease(effective time.Duration) (time.Duration, error) {
	canonical, err := canonicalAttachmentLease(effective)
	if err != nil {
		return 0, err
	}
	if canonical != effective {
		return 0, fmt.Errorf("attachment lease %s is not canonical", effective)
	}
	return effective, nil
}

func heartbeatTimingForLease(lease, requested time.Duration) (time.Duration, time.Duration, error) {
	effective, err := validateCanonicalAttachmentLease(lease)
	if err != nil {
		return 0, 0, err
	}
	maximum := effective / 3
	if requested <= 0 || requested > maximum {
		requested = maximum
	}
	if requested < minimumHeartbeatInterval {
		requested = minimumHeartbeatInterval
	}
	timeout := effective / 2
	if requested <= 0 || timeout <= 0 || requested+timeout >= effective {
		return 0, 0, fmt.Errorf("attachment lease %s has no strict heartbeat margin", effective)
	}
	return requested, timeout, nil
}

func heartbeatIntervalForLease(lease, requested time.Duration) time.Duration {
	interval, _, err := heartbeatTimingForLease(lease, requested)
	if err != nil {
		return 0
	}
	return interval
}

func heartbeatRenewalTimeout(lease time.Duration) time.Duration {
	_, timeout, err := heartbeatTimingForLease(lease, 0)
	if err != nil {
		return 0
	}
	return timeout
}

func (c *Coordinator) observeLifecycle(snapshot Snapshot) {
	if c == nil {
		return
	}
	ctx := context.Background()
	spec, ok := c.taskSpec(snapshot.ID)
	if !ok {
		spec = taskSpecFromSnapshot(snapshot)
	}
	if c.ledger != nil {
		if nilPort(c.attachments) || spec.AttemptID == "" || spec.LeaseGeneration <= 0 {
			c.noteDurabilityError(snapshot.ID, fmt.Errorf("lifecycle callback has no durable attachment fence"))
			return
		}
		if !c.ownsAttempt(agentcoord.AttachmentLease{RunID: snapshot.ID, AttemptID: spec.AttemptID, LeaseGeneration: spec.LeaseGeneration}) {
			return
		}
		lease, err := c.attachments.Current(ctx, firstNonEmpty(spec.SessionID, spec.ParentSessionID), snapshot.ID)
		if err != nil || lease.AttemptID != spec.AttemptID || lease.LeaseGeneration != spec.LeaseGeneration {
			// A late callback from an expired/replaced process is diagnostic only;
			// it must not mutate the durable run or release another attempt's
			// claims.
			return
		}
	}
	if snapshot.State == StateRunning && snapshot.PID > 0 {
		if err := c.append(ctx, spec, runledger.EventSubagentUpdated, map[string]any{
			"state": string(agentcoord.AgentRunRunning),
			"pid":   snapshot.PID,
		}, nil); err != nil {
			c.noteDurabilityError(snapshot.ID, err)
		}
		return
	}
	if !snapshotTerminal(snapshot.State) {
		return
	}
	if c.ledger != nil {
		lease, err := c.attachments.Current(ctx, firstNonEmpty(spec.SessionID, spec.ParentSessionID), snapshot.ID)
		if err != nil || lease.AttemptID != spec.AttemptID || lease.LeaseGeneration != spec.LeaseGeneration {
			return
		}
	}

	refs, err := c.storeTerminalReport(ctx, spec, snapshot)
	if err != nil {
		c.noteDurabilityError(snapshot.ID, err)
		if finishErr := c.finishDurably(ctx, spec, snapshot, nil, err); finishErr != nil {
			c.noteDurabilityError(snapshot.ID, finishErr)
			return
		}
	} else {
		c.addEvidence(snapshot.ID, refs...)
		if finishErr := c.finishDurably(ctx, spec, snapshot, refs, nil); finishErr != nil {
			c.noteDurabilityError(snapshot.ID, finishErr)
			return
		}
	}
	c.forgetClaims(snapshot.ID, nil)
	c.mu.Lock()
	delete(c.ownedAttempts, snapshot.ID)
	c.mu.Unlock()
}

func (c *Coordinator) storeTerminalReport(ctx context.Context, spec agentcoord.AgentTaskSpec, snapshot Snapshot) ([]string, error) {
	if c == nil || c.ledger == nil {
		return nil, nil
	}
	if nilPort(c.ledger) || nilPort(c.evidence) {
		return nil, fmt.Errorf("subagent coordinator durable terminal report store is unavailable")
	}
	output := snapshot.Output
	if snapshot.OutputSpoolPath != "" {
		body, err := os.ReadFile(snapshot.OutputSpoolPath)
		if err != nil {
			return nil, fmt.Errorf("read subagent output spool: %w", err)
		}
		if snapshot.CapturedBytes > 0 && int64(len(body)) != snapshot.CapturedBytes {
			return nil, fmt.Errorf("read subagent output spool: got %d bytes, want %d", len(body), snapshot.CapturedBytes)
		}
		output = string(body)
	}
	rawBody, err := json.Marshal(map[string]any{
		"state":                 snapshot.State,
		"output":                output,
		"output_preview":        snapshot.Output,
		"output_bytes":          snapshot.OutputBytes,
		"captured_output_bytes": snapshot.CapturedBytes,
		"output_truncated":      snapshot.OutputTruncated,
		"error":                 firstNonEmpty(snapshot.rawError, snapshot.Error),
		"started_at":            snapshot.StartedAt,
		"finished_at":           snapshot.FinishedAt,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal subagent report: %w", err)
	}
	raw, err := c.evidence.Put(ctx, evidence.Object{
		Kind:       evidence.KindSubagentReport,
		MediaType:  "application/json",
		InlineBody: rawBody,
		Metadata: map[string]any{
			evidence.MetaSessionID: spec.ParentSessionID,
			evidence.MetaRunID:     snapshot.ID,
			evidence.MetaTaskID:    spec.ID,
			evidence.MetaEntityID:  spec.Agent,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("store subagent report: %w", err)
	}
	if err := c.evidence.Pin(ctx, raw.ID, "run:"+snapshot.ID); err != nil {
		return nil, fmt.Errorf("pin subagent report: %w", err)
	}
	artifactSnapshot := snapshot
	artifactSnapshot.Output = output
	artifactSnapshot.Error = terminalErrorProjection(snapshot.Error, nil)
	artifact, err := c.terminalArtifact(spec, artifactSnapshot, artifactv1.EvidenceRef{
		ID:    raw.ID,
		Label: "raw subagent report",
		Kind:  string(evidence.KindSubagentReport),
	})
	if err != nil {
		return nil, fmt.Errorf("build subagent artifact: %w", err)
	}
	artifactBody, err := artifactv1.RenderJSON(artifact)
	if err != nil {
		return nil, fmt.Errorf("render subagent artifact: %w", err)
	}
	typed, err := c.evidence.Put(ctx, evidence.Object{
		Kind:       evidence.KindSubagentReport,
		MediaType:  artifactv1.MediaType,
		InlineBody: artifactBody,
		Metadata: map[string]any{
			evidence.MetaSessionID: spec.ParentSessionID,
			evidence.MetaRunID:     snapshot.ID,
			evidence.MetaTaskID:    spec.ID,
			evidence.MetaEntityID:  spec.Agent,
			"schema_version":       artifactv1.SchemaVersion,
			"artifact_id":          artifact.ArtifactID,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("store subagent artifact: %w", err)
	}
	if err := c.evidence.Pin(ctx, typed.ID, "run:"+snapshot.ID); err != nil {
		return nil, fmt.Errorf("pin subagent artifact: %w", err)
	}
	return []string{raw.ID, typed.ID}, nil
}

// terminalArtifact gives a child-produced Artifact v1 precedence when it
// conforms, while retaining the coordinator's authoritative run state and a
// raw evidence link. Text-only workers receive a generated typed result with
// a visible schema diagnostic rather than silently losing their output.
func (c *Coordinator) terminalArtifact(spec agentcoord.AgentTaskSpec, snapshot Snapshot, raw artifactv1.EvidenceRef) (artifactv1.Artifact, error) {
	run := c.agentRunFromSnapshot(snapshot)
	run.Task = spec
	generated, err := artifactv1.FromSubagentRun(run)
	if err != nil {
		return artifactv1.Artifact{}, err
	}
	artifact := generated
	if strings.TrimSpace(spec.OutputSchema) == artifactv1.SchemaVersion && strings.TrimSpace(snapshot.Output) != "" {
		decoded, report, decodeErr := artifactv1.DecodeProviderOutput(context.Background(), []byte(snapshot.Output), artifactv1.OutputPromptJSON, artifactv1.DecodeOptions{MaxRepairAttempts: 1})
		if decodeErr == nil {
			artifact = decoded
			if report.Repaired {
				artifact.Diagnostics = append(artifact.Diagnostics, artifactv1.Diagnostic{
					Level:   "warning",
					Code:    "subagent.artifact_repaired",
					Message: "subagent artifact required bounded local repair",
				})
			}
		} else {
			artifact.Diagnostics = append(artifact.Diagnostics, artifactv1.Diagnostic{
				Level:   "warning",
				Code:    "subagent.output_schema",
				Message: "subagent output did not conform to requested artifact schema",
			})
		}
	}
	artifact.Status = artifactStatusFromSnapshot(snapshot.State)
	if artifact.Metadata == nil {
		artifact.Metadata = make(map[string]string)
	}
	artifact.Metadata["subagent_run_id"] = snapshot.ID
	artifact.Metadata["subagent_output_schema"] = spec.OutputSchema
	artifact.EvidenceRefs = append(artifact.EvidenceRefs, raw)
	artifact.ArtifactID = ""
	return artifactv1.NormalizeAndValidate(artifact)
}

func artifactStatusFromSnapshot(state State) artifactv1.ArtifactStatus {
	switch state {
	case StateCompleted:
		return artifactv1.StatusCompleted
	case StateFailed, StateCancelled:
		return artifactv1.StatusFailed
	default:
		return artifactv1.StatusIncomplete
	}
}

func (c *Coordinator) finishDurably(ctx context.Context, spec agentcoord.AgentTaskSpec, snapshot Snapshot, refs []string, durabilityErr error) error {
	if c == nil || c.ledger == nil {
		return nil
	}
	if nilPort(c.ledger) {
		return fmt.Errorf("subagent coordinator durable ledger is unavailable")
	}
	if nilPort(c.finalizer) {
		return fmt.Errorf("subagent coordinator durable finalizer is unavailable")
	}
	state := stateFromSnapshot(snapshot.State)
	eventType := eventTypeForState(state)
	if durabilityErr != nil {
		state = agentcoord.AgentRunFailed
		eventType = runledger.EventSubagentFailed
	}
	terminalError := terminalErrorProjection(snapshot.Error, durabilityErr)
	payload := map[string]any{
		"state":   string(state),
		"summary": boundedCoordinatorText(snapshot.Output, 4096),
		"error":   terminalError,
	}
	outcome := map[string]any{
		"summary":       boundedCoordinatorText(snapshot.Output, 4096),
		"error":         terminalError,
		"evidence_refs": refs,
	}
	endedAt := snapshot.FinishedAt
	if endedAt.IsZero() {
		endedAt = c.now()
	}
	sessionID := firstNonEmpty(spec.SessionID, spec.ParentSessionID)
	terminalID := runledger.StableEventID("subagent.terminal", sessionID, snapshot.ID, spec.AttemptID, fmt.Sprint(spec.LeaseGeneration), string(state))
	releaseID := runledger.StableEventID("subagent.terminal.release", sessionID, snapshot.ID, spec.AttemptID, fmt.Sprint(spec.LeaseGeneration))
	events := []runledger.Event{
		{
			ID:          terminalID,
			Type:        eventType,
			Timestamp:   endedAt,
			SessionID:   sessionID,
			RunID:       snapshot.ID,
			ParentRunID: spec.ParentRunID,
			TaskID:      spec.ID,
			AgentID:     spec.Agent,
			ModelID:     spec.Model,
			Backend:     c.adapter,
			EvidenceIDs: append([]string(nil), refs...),
			Payload:     payload,
		},
		{
			ID:          releaseID,
			Type:        runledger.EventSubagentReleased,
			Timestamp:   endedAt,
			SessionID:   sessionID,
			RunID:       snapshot.ID,
			ParentRunID: spec.ParentRunID,
			TaskID:      spec.ID,
			AgentID:     spec.Agent,
			ModelID:     spec.Model,
			Backend:     c.adapter,
			Payload: map[string]any{
				"resources": spec.WorkspaceClaims,
				"reason":    "terminal " + string(snapshot.State),
			},
		},
	}
	finalization := runledger.AttemptFinalization{
		SessionID:       sessionID,
		RunID:           snapshot.ID,
		AttemptID:       spec.AttemptID,
		LeaseGeneration: spec.LeaseGeneration,
		Status:          string(state),
		EndedAt:         endedAt,
		Outcome:         outcome,
		ReleaseReason:   "terminal " + string(snapshot.State),
		Events:          events,
	}
	if err := c.finalizer.FinalizeRunAttempt(ctx, finalization); err != nil {
		if errors.Is(err, runledger.ErrRalphDualWriteFailed) {
			committed, verifyErr := c.terminalFinalizationCommitted(ctx, snapshot.ID, string(state), []string{eventType, runledger.EventSubagentReleased}, terminalID, releaseID)
			if verifyErr != nil {
				return fmt.Errorf("verify durable subagent finalization after audit delivery failure: %w", verifyErr)
			}
			if committed {
				return nil
			}
		}
		return fmt.Errorf("finalize durable subagent run: %w", err)
	}
	return nil
}

func (c *Coordinator) terminalFinalizationCommitted(ctx context.Context, runID, status string, eventTypes []string, eventIDs ...string) (bool, error) {
	run, err := c.ledger.GetRun(ctx, runID)
	if err != nil {
		return false, err
	}
	if run.EndedAt == nil || run.Status != status {
		return false, nil
	}
	events, err := c.ledger.ListEvents(ctx, runledger.EventQuery{RunID: runID, Types: eventTypes, Limit: 4})
	if err != nil {
		return false, err
	}
	seen := make(map[string]struct{}, len(events))
	for _, event := range events {
		seen[event.ID] = struct{}{}
	}
	for _, eventID := range eventIDs {
		if _, ok := seen[eventID]; !ok {
			return false, nil
		}
	}
	return true, nil
}

func (c *Coordinator) storeMessage(ctx context.Context, message agentcoord.AgentMessage) ([]string, error) {
	if c == nil || c.ledger == nil {
		return nil, nil
	}
	if nilPort(c.ledger) {
		return nil, fmt.Errorf("subagent coordinator durable ledger is unavailable")
	}
	if nilPort(c.evidence) {
		return nil, fmt.Errorf("subagent coordinator requires evidence for durable mailbox delivery")
	}
	obj, err := c.evidence.Put(ctx, evidence.Object{
		Kind:       evidence.KindSubagentMessage,
		MediaType:  "text/plain",
		InlineBody: []byte(message.Content),
		Metadata: map[string]any{
			evidence.MetaSessionID: message.SessionID,
			evidence.MetaRunID:     message.RunID,
			evidence.MetaEntityID:  message.To,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("store subagent message: %w", err)
	}
	if err := c.evidence.Pin(ctx, obj.ID, "run:"+message.RunID); err != nil {
		return nil, fmt.Errorf("pin subagent message: %w", err)
	}
	c.addEvidence(message.RunID, obj.ID)
	return []string{obj.ID}, nil
}

func (c *Coordinator) appendMessage(ctx context.Context, message agentcoord.AgentMessage) error {
	spec, err := c.taskSpecForRun(ctx, message.RunID)
	if err != nil {
		return err
	}
	eventType := runledger.EventSubagentMessageSent
	if message.Kind == "steer" {
		eventType = runledger.EventSubagentSteered
	}
	eventID := ""
	if message.IdempotencyKey != "" {
		eventID = runledger.StableEventID("subagent.mailbox", eventType, message.SessionID, message.RunID, message.IdempotencyKey, message.ContentDigest)
	}
	return c.appendWithID(ctx, spec, eventType, eventID, map[string]any{
		"message_id":              message.ID,
		"idempotency_key":         message.IdempotencyKey,
		"session_id":              message.SessionID,
		"parent_run_id":           message.ParentRunID,
		"task_id":                 message.TaskID,
		"turn_id":                 message.TurnID,
		"source_attempt_id":       message.SourceAttemptID,
		"source_lease_generation": message.SourceLeaseGeneration,
		"from":                    message.From,
		"to":                      message.To,
		"kind":                    message.Kind,
		"content_preview":         boundedCoordinatorText(message.Content, 512),
	}, message.EvidenceRefs)
}

func (c *Coordinator) appendMessageDelivery(ctx context.Context, message agentcoord.AgentMessage) error {
	spec, err := c.taskSpecForRun(ctx, message.RunID)
	if err != nil {
		return err
	}
	eventID := runledger.StableEventID("subagent.mailbox.delivery", message.SessionID, message.RunID, message.ID, message.Delivery)
	return c.appendWithID(ctx, spec, runledger.EventSubagentMessageDelivered, eventID, map[string]any{
		"message_id": message.ID,
		"to":         message.To,
		"delivery":   "delivered",
	}, nil)
}

func (c *Coordinator) append(ctx context.Context, spec agentcoord.AgentTaskSpec, eventType string, payload map[string]any, evidenceIDs []string) error {
	return c.appendWithID(ctx, spec, eventType, "", payload, evidenceIDs)
}

func (c *Coordinator) appendWithID(ctx context.Context, spec agentcoord.AgentTaskSpec, eventType, eventID string, payload map[string]any, evidenceIDs []string) error {
	if c == nil || c.ledger == nil {
		return nil
	}
	if nilPort(c.ledger) {
		return fmt.Errorf("subagent coordinator durable ledger is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	_, err := c.ledger.Append(ctx, runledger.Event{
		ID:          eventID,
		Type:        eventType,
		SessionID:   firstNonEmpty(spec.SessionID, spec.ParentSessionID),
		RunID:       spec.RunID,
		ParentRunID: spec.ParentRunID,
		TaskID:      spec.ID,
		AgentID:     spec.Agent,
		ModelID:     spec.Model,
		Backend:     c.adapter,
		EvidenceIDs: append([]string(nil), evidenceIDs...),
		Payload:     payload,
	})
	if err != nil {
		return fmt.Errorf("record subagent %s: %w", eventType, err)
	}
	return nil
}

func (c *Coordinator) localSnapshot(id string) (Snapshot, bool) {
	if c == nil || c.manager == nil {
		return Snapshot{}, false
	}
	return c.manager.Status(id)
}

func (c *Coordinator) taskSpec(runID string) (agentcoord.AgentTaskSpec, bool) {
	if c == nil {
		return agentcoord.AgentTaskSpec{}, false
	}
	c.mu.RLock()
	spec, ok := c.runs[strings.TrimSpace(runID)]
	c.mu.RUnlock()
	return spec, ok
}

func (c *Coordinator) taskSpecForRun(ctx context.Context, runID string) (agentcoord.AgentTaskSpec, error) {
	runID = strings.TrimSpace(runID)
	if spec, ok := c.taskSpec(runID); ok {
		return spec, nil
	}
	if c == nil || c.ledger == nil {
		return agentcoord.AgentTaskSpec{}, fmt.Errorf("subagent not found: %s", runID)
	}
	if nilPort(c.ledger) {
		return agentcoord.AgentTaskSpec{}, fmt.Errorf("subagent coordinator durable ledger is unavailable")
	}
	durable, err := c.ledger.GetRun(ctx, runID)
	if err != nil {
		return agentcoord.AgentTaskSpec{}, fmt.Errorf("get durable subagent run %s: %w", runID, err)
	}
	if durable.Backend != c.adapter {
		return agentcoord.AgentTaskSpec{}, fmt.Errorf("subagent not found: %s", runID)
	}
	if !nilPort(c.contracts) && !nilPort(c.evidence) {
		contract, contractErr := c.contracts.GetRunContract(ctx, runID)
		if contractErr == nil {
			object, evidenceErr := c.evidence.Get(ctx, contract.TaskEvidenceID)
			if evidenceErr != nil {
				return agentcoord.AgentTaskSpec{}, fmt.Errorf("read durable task contract %s: %w", contract.TaskEvidenceID, evidenceErr)
			}
			var restored agentcoord.AgentTaskSpec
			if err := json.Unmarshal(object.InlineBody, &restored); err != nil {
				return agentcoord.AgentTaskSpec{}, fmt.Errorf("decode durable task contract %s: %w", contract.TaskEvidenceID, err)
			}
			if restored.RunID != durable.RunID || restored.SessionID != durable.SessionID || logicalTaskDigest(restored) != contract.InputDigest {
				return agentcoord.AgentTaskSpec{}, fmt.Errorf("durable task contract for run %s failed identity validation", runID)
			}
			if !nilPort(c.attachments) {
				if lease, currentErr := c.attachments.Current(ctx, restored.SessionID, restored.RunID); currentErr == nil {
					restored.AttemptID = lease.AttemptID
					restored.LeaseGeneration = lease.LeaseGeneration
				}
			}
			c.setTaskSpec(restored)
			return restored, nil
		}
		if !errors.Is(contractErr, runledger.ErrNotFound) {
			return agentcoord.AgentTaskSpec{}, fmt.Errorf("read durable run contract %s: %w", runID, contractErr)
		}
	}
	restored := agentcoord.AgentTaskSpec{
		ID:              durable.TaskID,
		SessionID:       durable.SessionID,
		RunID:           durable.RunID,
		ParentRunID:     durable.ParentRunID,
		ParentSessionID: durable.SessionID,
		Agent:           durable.AgentID,
		Model:           durable.ModelID,
		Budget:          budgetFromMap(durable.Budget),
	}
	if !nilPort(c.attachments) {
		if lease, currentErr := c.attachments.Current(ctx, durable.SessionID, durable.RunID); currentErr == nil {
			restored.AttemptID = lease.AttemptID
			restored.LeaseGeneration = lease.LeaseGeneration
		}
	}
	c.setTaskSpec(restored)
	return restored, nil
}

func (c *Coordinator) agentRunFromSnapshot(snapshot Snapshot) agentcoord.AgentRun {
	spec, ok := c.taskSpec(snapshot.ID)
	if !ok {
		spec = taskSpecFromSnapshot(snapshot)
	}
	if spec.ID == "" {
		spec.ID = snapshot.TaskID
	}
	if spec.RunID == "" {
		spec.RunID = snapshot.ID
	}
	state := stateFromSnapshot(snapshot.State)
	result := agentcoord.AgentResult{
		Summary:      boundedCoordinatorText(snapshot.Output, 4096),
		Error:        terminalErrorProjection(snapshot.Error, nil),
		EvidenceRefs: c.evidenceRefs(snapshot.ID),
	}
	if durabilityErr := c.durabilityErrorFor(snapshot.ID); durabilityErr != "" {
		state = agentcoord.AgentRunFailed
		result.Error = firstNonEmpty(result.Error, durabilityErr)
	}
	return agentcoord.AgentRun{
		ID:              snapshot.ID,
		SessionID:       firstNonEmpty(snapshot.SessionID, spec.SessionID, spec.ParentSessionID),
		ParentRunID:     firstNonEmpty(snapshot.ParentRunID, spec.ParentRunID),
		ParentSessionID: firstNonEmpty(snapshot.ParentSessionID, spec.ParentSessionID),
		Task:            spec,
		State:           state,
		Adapter:         c.adapter,
		PID:             snapshot.PID,
		StartedAt:       snapshot.StartedAt,
		FinishedAt:      snapshot.FinishedAt,
		AttemptID:       spec.AttemptID,
		LeaseGeneration: spec.LeaseGeneration,
		Claims:          c.claimsFor(snapshot.ID),
		MailboxCount:    c.mailboxCount(snapshot.ID),
		Result:          result,
	}
}

func (c *Coordinator) agentRunFromLedger(durable runledger.AgentRun, unattached bool) agentcoord.AgentRun {
	spec, ok := c.taskSpec(durable.RunID)
	if !ok {
		spec = agentcoord.AgentTaskSpec{
			ID:              durable.TaskID,
			SessionID:       durable.SessionID,
			RunID:           durable.RunID,
			ParentRunID:     durable.ParentRunID,
			ParentSessionID: durable.SessionID,
			Agent:           durable.AgentID,
			Model:           durable.ModelID,
			Budget:          budgetFromMap(durable.Budget),
		}
	}
	state := stateFromLedger(durable.Status)
	if unattached && (state == agentcoord.AgentRunQueued || state == agentcoord.AgentRunRunning) {
		state = agentcoord.AgentRunResumable
	}
	result := agentcoord.AgentResult{
		Summary:      boundedCoordinatorText(mapString(durable.Outcome, "summary"), 4096),
		Error:        terminalErrorProjection(mapString(durable.Outcome, "error"), nil),
		EvidenceRefs: mapStrings(durable.Outcome, "evidence_refs"),
	}
	if durabilityErr := c.durabilityErrorFor(durable.RunID); durabilityErr != "" {
		state = agentcoord.AgentRunFailed
		result.Error = firstNonEmpty(result.Error, durabilityErr)
	}
	finishedAt := time.Time{}
	if durable.EndedAt != nil {
		finishedAt = *durable.EndedAt
	}
	return agentcoord.AgentRun{
		ID:              durable.RunID,
		SessionID:       durable.SessionID,
		ParentRunID:     durable.ParentRunID,
		ParentSessionID: durable.SessionID,
		Task:            spec,
		State:           state,
		Adapter:         durable.Backend,
		StartedAt:       durable.StartedAt,
		FinishedAt:      finishedAt,
		AttemptID:       spec.AttemptID,
		LeaseGeneration: spec.LeaseGeneration,
		Claims:          c.claimsFor(durable.RunID),
		MailboxCount:    c.mailboxCount(durable.RunID),
		Result:          result,
	}
}

func taskSpecFromSnapshot(snapshot Snapshot) agentcoord.AgentTaskSpec {
	return agentcoord.AgentTaskSpec{
		ID:              snapshot.TaskID,
		SessionID:       snapshot.SessionID,
		RunID:           snapshot.ID,
		ParentRunID:     snapshot.ParentRunID,
		ParentSessionID: snapshot.ParentSessionID,
		TurnID:          snapshot.TurnID,
		AttemptID:       snapshot.AttemptID,
		LeaseGeneration: snapshot.LeaseGeneration,
		Agent:           snapshot.Agent,
		Spec:            snapshot.Spec,
		Task:            snapshot.Task,
		Persona:         snapshot.Persona,
		Model:           snapshot.Model,
		Tier:            string(snapshot.Tier),
		Effort:          snapshot.Effort,
		AllowedTools:    copyStrings(snapshot.AllowedTools),
		StepCap:         snapshot.StepCap,
		TimeoutSeconds:  snapshot.TimeoutSeconds,
		Budget:          snapshot.Budget,
		WorkspaceClaims: copyStrings(snapshot.WorkspaceClaims),
		Isolation:       snapshot.Isolation,
		OutputSchema:    snapshot.OutputSchema,
		ApprovalPosture: snapshot.ApprovalPosture,
	}
}

func stateFromSnapshot(state State) agentcoord.AgentRunState {
	switch state {
	case StateCompleted:
		return agentcoord.AgentRunCompleted
	case StateFailed:
		return agentcoord.AgentRunFailed
	case StateCancelled:
		return agentcoord.AgentRunCancelled
	default:
		return agentcoord.AgentRunRunning
	}
}

func stateFromLedger(status string) agentcoord.AgentRunState {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case string(agentcoord.AgentRunCompleted):
		return agentcoord.AgentRunCompleted
	case string(agentcoord.AgentRunFailed):
		return agentcoord.AgentRunFailed
	case string(agentcoord.AgentRunCancelled):
		return agentcoord.AgentRunCancelled
	case string(agentcoord.AgentRunBlocked):
		return agentcoord.AgentRunBlocked
	case string(agentcoord.AgentRunQueued), "planning":
		return agentcoord.AgentRunQueued
	case string(agentcoord.AgentRunResumable):
		return agentcoord.AgentRunResumable
	default:
		return agentcoord.AgentRunRunning
	}
}

func eventTypeForState(state agentcoord.AgentRunState) string {
	switch state {
	case agentcoord.AgentRunCompleted:
		return runledger.EventSubagentCompleted
	case agentcoord.AgentRunCancelled:
		return runledger.EventSubagentCancelled
	default:
		return runledger.EventSubagentFailed
	}
}

func snapshotTerminal(state State) bool {
	return state == StateCompleted || state == StateFailed || state == StateCancelled
}

func matchesRunFilter(run agentcoord.AgentRun, filter agentcoord.AgentRunFilter) bool {
	if value := strings.TrimSpace(filter.ParentRunID); value != "" && run.ParentRunID != value {
		return false
	}
	if value := strings.TrimSpace(filter.ParentSessionID); value != "" && run.ParentSessionID != value {
		return false
	}
	if value := strings.TrimSpace(filter.SessionID); value != "" && run.SessionID != value {
		return false
	}
	if value := strings.TrimSpace(filter.TaskID); value != "" && run.Task.ID != value {
		return false
	}
	return true
}

func (c *Coordinator) addEvidence(runID string, ids ...string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	seen := make(map[string]struct{}, len(c.evidenceByRun[runID])+len(ids))
	for _, id := range c.evidenceByRun[runID] {
		seen[id] = struct{}{}
	}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		c.evidenceByRun[runID] = append(c.evidenceByRun[runID], id)
	}
}

func (c *Coordinator) evidenceRefs(runID string) []string {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	refs := append([]string(nil), c.evidenceByRun[runID]...)
	c.mu.RUnlock()
	return refs
}

func (c *Coordinator) noteDurabilityError(runID string, err error) {
	if c == nil || err == nil {
		return
	}
	c.mu.Lock()
	c.durabilityError[runID] = "durability error: " + boundedCoordinatorText(string(evidence.Redact([]byte(err.Error()))), 1024)
	c.mu.Unlock()
}

func (c *Coordinator) durabilityErrorFor(runID string) string {
	if c == nil {
		return ""
	}
	c.mu.RLock()
	err := c.durabilityError[runID]
	c.mu.RUnlock()
	return err
}

func (c *Coordinator) claimsFor(runID string) []string {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	claims := c.claimsByRun[runID]
	out := make([]string, 0, len(claims))
	for claim := range claims {
		out = append(out, claim)
	}
	c.mu.RUnlock()
	sort.Strings(out)
	return out
}

func (c *Coordinator) addMailbox(message agentcoord.AgentMessage) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	mailbox := append(c.mailboxes[message.RunID], message)
	if len(mailbox) > c.mailboxLimit {
		mailbox = append([]agentcoord.AgentMessage(nil), mailbox[len(mailbox)-c.mailboxLimit:]...)
	}
	c.mailboxes[message.RunID] = mailbox
}

func (c *Coordinator) updateMailboxDelivery(messageID, runID, delivery string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	mailbox := c.mailboxes[runID]
	for index := len(mailbox) - 1; index >= 0; index-- {
		if mailbox[index].ID == messageID {
			mailbox[index].Delivery = delivery
			c.mailboxes[runID] = mailbox
			return
		}
	}
}

func (c *Coordinator) localMessages(runID string) []agentcoord.AgentMessage {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	messages := append([]agentcoord.AgentMessage(nil), c.mailboxes[runID]...)
	c.mu.RUnlock()
	return messages
}

func (c *Coordinator) mailboxCount(runID string) int {
	if c == nil {
		return 0
	}
	c.mu.RLock()
	count := len(c.mailboxes[runID])
	c.mu.RUnlock()
	return count
}

func messageFromEvent(event runledger.Event) agentcoord.AgentMessage {
	payload := event.Payload
	message := agentcoord.AgentMessage{
		ID:           mapString(payload, "message_id"),
		RunID:        event.RunID,
		From:         mapString(payload, "from"),
		To:           mapString(payload, "to"),
		Kind:         mapString(payload, "kind"),
		Content:      mapString(payload, "content_preview"),
		Delivery:     mapString(payload, "delivery"),
		EvidenceRefs: append([]string(nil), event.EvidenceIDs...),
		CreatedAt:    event.Timestamp,
	}
	if message.Kind == "" && event.Type == runledger.EventSubagentSteered {
		message.Kind = "steer"
	}
	if message.Delivery == "" {
		message.Delivery = "queued"
	}
	return message
}

func budgetFromMap(values map[string]any) agentcoord.AgentBudget {
	return agentcoord.AgentBudget{
		MaxToolCalls:     mapInt(values, "max_tool_calls"),
		MaxModelRequests: mapInt(values, "max_model_requests"),
		MaxElapsedSecond: mapInt(values, "max_elapsed_seconds"),
		MaxCostUSD:       mapFloat(values, "max_cost_usd"),
	}
}

func mapString(values map[string]any, key string) string {
	value, _ := values[key].(string)
	return value
}

func mapStrings(values map[string]any, key string) []string {
	value, ok := values[key]
	if !ok {
		return nil
	}
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func mapInt(values map[string]any, key string) int {
	switch value := values[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	default:
		return 0
	}
}

func mapFloat(values map[string]any, key string) float64 {
	switch value := values[key].(type) {
	case float64:
		return value
	case float32:
		return float64(value)
	case int:
		return float64(value)
	case int64:
		return float64(value)
	default:
		return 0
	}
}

func boundedCoordinatorText(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || len(value) <= limit {
		return value
	}
	if limit <= 3 {
		return value[:limit]
	}
	return value[:limit-3] + "..."
}

func terminalErrorProjection(snapshotError string, durabilityErr error) string {
	value := firstNonEmpty(snapshotError, errorText(durabilityErr))
	return boundedCoordinatorText(string(evidence.Redact([]byte(value))), 1024)
}

func validateCoordinatorMessageBounds(message agentcoord.AgentMessage) error {
	if len(message.Content) > runledger.MailboxMaxContent {
		return fmt.Errorf("subagent message content exceeds %d bytes", runledger.MailboxMaxContent)
	}
	for field, value := range map[string]string{
		"schema_version": message.Version, "message_id": firstNonEmpty(message.ID, message.MessageID), "idempotency_key": message.IdempotencyKey,
		"session_id": message.SessionID, "run_id": message.RunID, "parent_run_id": message.ParentRunID,
		"task_id": message.TaskID, "turn_id": message.TurnID, "correlation_id": message.CorrelationID,
		"causation_id": message.CausationID, "attempt_id": message.AttemptID,
		"source_attempt_id": message.SourceAttemptID, "from": message.From, "to": message.To, "kind": message.Kind,
	} {
		if len(strings.TrimSpace(value)) > runledger.MailboxMaxIdentifier {
			return fmt.Errorf("subagent message %s exceeds %d bytes", field, runledger.MailboxMaxIdentifier)
		}
	}
	return nil
}

func implicitCoordinatorMessageKey(message agentcoord.AgentMessage) string {
	contentDigest := strings.ToLower(strings.TrimSpace(message.ContentDigest))
	if contentDigest == "" {
		digest := sha256.Sum256([]byte(message.Content))
		contentDigest = fmt.Sprintf("%x", digest[:])
	}
	byteCount := message.ByteCount
	if byteCount == 0 && message.Content != "" {
		byteCount = int64(len(message.Content))
	}
	canonical := struct {
		Version, SessionID, RunID, ParentRunID, TaskID, TurnID string
		CorrelationID, CausationID, SourceAttemptID            string
		SourceLeaseGeneration                                  int64
		From, To, Kind                                         string
		ContentRef, ContentDigest, MediaType, Preview          string
		ByteCount                                              int64
	}{
		Version:   strings.TrimSpace(firstNonEmpty(message.Version, agentcoord.MessageSchemaVersion)),
		SessionID: strings.TrimSpace(message.SessionID), RunID: strings.TrimSpace(message.RunID),
		ParentRunID: strings.TrimSpace(message.ParentRunID), TaskID: strings.TrimSpace(message.TaskID),
		TurnID: strings.TrimSpace(message.TurnID), CorrelationID: strings.TrimSpace(message.CorrelationID),
		CausationID: strings.TrimSpace(message.CausationID), SourceAttemptID: strings.TrimSpace(message.SourceAttemptID),
		SourceLeaseGeneration: message.SourceLeaseGeneration, From: strings.TrimSpace(message.From),
		To: strings.TrimSpace(message.To), Kind: strings.TrimSpace(message.Kind),
		ContentRef: strings.TrimSpace(message.ContentRef), ContentDigest: contentDigest,
		MediaType: firstNonEmpty(strings.TrimSpace(message.MediaType), "application/octet-stream"),
		Preview:   runledger.CanonicalMailboxPreview(message.Preview, message.Content), ByteCount: byteCount,
	}
	body, _ := json.Marshal(canonical)
	digest := sha256.Sum256(body)
	return fmt.Sprintf("auto_%x", digest[:])
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func firstError(values ...error) error {
	for _, err := range values {
		if err != nil {
			return err
		}
	}
	return fmt.Errorf("unknown authorization failure")
}
