package subagent

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path"
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

const defaultMailboxLimit = 256

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
	manager  *Manager
	ledger   runledger.Store
	claims   runledger.ClaimJournal
	evidence evidence.Store
	policy   AdmissionPolicy
	adapter  string

	mu              sync.RWMutex
	runs            map[string]agentcoord.AgentTaskSpec
	mailboxes       map[string][]agentcoord.AgentMessage
	fallbackClaims  map[string]string
	claimsByRun     map[string]map[string]struct{}
	evidenceByRun   map[string][]string
	durabilityError map[string]string
	mailboxLimit    int
	now             func() time.Time
}

var _ agentcoord.AgentCoordinator = (*Coordinator)(nil)

// NewCoordinator constructs the local-process implementation. Passing a nil
// manager is allowed for status-only recovery projections, but Spawn will
// report that no live adapter is available.
func NewCoordinator(manager *Manager, opts ...CoordinatorOption) *Coordinator {
	c := &Coordinator{
		manager:         manager,
		adapter:         "local-process",
		runs:            make(map[string]agentcoord.AgentTaskSpec),
		mailboxes:       make(map[string][]agentcoord.AgentMessage),
		fallbackClaims:  make(map[string]string),
		claimsByRun:     make(map[string]map[string]struct{}),
		evidenceByRun:   make(map[string][]string),
		durabilityError: make(map[string]string),
		mailboxLimit:    defaultMailboxLimit,
		now:             func() time.Time { return time.Now().UTC() },
	}
	for _, opt := range opts {
		if opt != nil {
			opt(c)
		}
	}
	if c.claims == nil {
		if journal, ok := c.ledger.(runledger.ClaimJournal); ok {
			c.claims = journal
		}
	}
	if manager != nil {
		manager.SetLifecycleObserver(c.observeLifecycle)
	}
	return c
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
	if err := c.registerRun(spec); err != nil {
		return agentcoord.AgentRun{}, err
	}

	durable := c.ledger != nil
	if durable {
		if c.evidence == nil {
			c.unregisterRun(spec.RunID)
			return agentcoord.AgentRun{}, fmt.Errorf("subagent coordinator requires evidence when durable run ledger is configured")
		}
		if strings.TrimSpace(spec.ParentSessionID) == "" {
			c.unregisterRun(spec.RunID)
			return agentcoord.AgentRun{}, fmt.Errorf("subagent coordinator durable spawn requires parent_session_id")
		}
		if _, err := c.ledger.StartRun(ctx, runledger.AgentRun{
			RunID:       spec.RunID,
			SessionID:   spec.ParentSessionID,
			ParentRunID: spec.ParentRunID,
			TaskID:      spec.ID,
			AgentID:     spec.Agent,
			ModelID:     spec.Model,
			Backend:     c.adapter,
			Status:      string(agentcoord.AgentRunQueued),
			StartedAt:   c.now(),
			Budget:      budgetMap(spec.Budget),
		}); err != nil {
			c.unregisterRun(spec.RunID)
			return agentcoord.AgentRun{}, fmt.Errorf("start durable subagent run: %w", err)
		}
	}

	claimed := false
	if len(spec.WorkspaceClaims) > 0 {
		if _, err := c.Claim(ctx, agentcoord.AgentClaimRequest{RunID: spec.RunID, Resources: spec.WorkspaceClaims}); err != nil {
			c.failSpawn(ctx, spec, agentcoord.AgentRunBlocked, err, false)
			return agentcoord.AgentRun{}, err
		}
		claimed = true
	}

	taskEvidence, err := c.storeTaskSpec(ctx, spec)
	if err != nil {
		c.failSpawn(ctx, spec, agentcoord.AgentRunFailed, err, claimed)
		return agentcoord.AgentRun{}, err
	}
	if err := c.append(ctx, spec, runledger.EventSubagentSpawned, map[string]any{
		"state":            string(agentcoord.AgentRunQueued),
		"task_summary":     boundedCoordinatorText(spec.Task, 512),
		"persona":          spec.Persona,
		"tier":             spec.Tier,
		"effort":           spec.Effort,
		"timeout_seconds":  spec.TimeoutSeconds,
		"workspace_claims": spec.WorkspaceClaims,
	}, taskEvidence); err != nil {
		c.failSpawn(ctx, spec, agentcoord.AgentRunFailed, err, claimed)
		return agentcoord.AgentRun{}, err
	}

	snapshot, err := c.manager.SpawnWithOptions(spawnOptionsFromTask(spec))
	if err != nil {
		c.failSpawn(ctx, spec, agentcoord.AgentRunFailed, err, claimed)
		return agentcoord.AgentRun{}, fmt.Errorf("spawn local subagent: %w", err)
	}
	return c.agentRunFromSnapshot(snapshot), nil
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
	if _, exists := c.runs[spec.RunID]; exists {
		return fmt.Errorf("subagent run already exists: %s", spec.RunID)
	}
	c.runs[spec.RunID] = spec
	return nil
}

func (c *Coordinator) unregisterRun(runID string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	delete(c.runs, strings.TrimSpace(runID))
	c.mu.Unlock()
}

func spawnOptionsFromTask(spec agentcoord.AgentTaskSpec) SpawnOptions {
	return SpawnOptions{
		ID:              spec.RunID,
		ParentSessionID: spec.ParentSessionID,
		ParentRunID:     spec.ParentRunID,
		TaskID:          spec.ID,
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
	spec.RunID = strings.TrimSpace(spec.RunID)
	spec.ID = strings.TrimSpace(spec.ID)
	spec.ParentRunID = strings.TrimSpace(spec.ParentRunID)
	spec.ParentSessionID = strings.TrimSpace(spec.ParentSessionID)
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
	if spec.Task == "" {
		return agentcoord.AgentTaskSpec{}, fmt.Errorf("subagent task is required")
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

func (c *Coordinator) storeTaskSpec(ctx context.Context, spec agentcoord.AgentTaskSpec) ([]string, error) {
	if c == nil || c.ledger == nil {
		return nil, nil
	}
	body, err := json.Marshal(spec)
	if err != nil {
		return nil, fmt.Errorf("marshal subagent task contract: %w", err)
	}
	obj, err := c.evidence.Put(ctx, evidence.Object{
		Kind:       evidence.KindSubagentTask,
		MediaType:  "application/json",
		InlineBody: body,
		Metadata: map[string]any{
			evidence.MetaSessionID: spec.ParentSessionID,
			evidence.MetaRunID:     spec.RunID,
			evidence.MetaTaskID:    spec.ID,
			evidence.MetaEntityID:  spec.Agent,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("store subagent task contract: %w", err)
	}
	if err := c.evidence.Pin(ctx, obj.ID, "run:"+spec.RunID); err != nil {
		return nil, fmt.Errorf("pin subagent task contract: %w", err)
	}
	c.addEvidence(spec.RunID, obj.ID)
	return []string{obj.ID}, nil
}

func (c *Coordinator) failSpawn(ctx context.Context, spec agentcoord.AgentTaskSpec, state agentcoord.AgentRunState, cause error, releaseClaims bool) {
	if c == nil {
		return
	}
	if releaseClaims {
		_ = c.releaseClaims(ctx, spec.RunID, spec.WorkspaceClaims, "spawn failed")
	}
	if c.ledger != nil {
		_ = c.append(ctx, spec, runledger.EventSubagentFailed, map[string]any{
			"state": string(state),
			"error": boundedCoordinatorText(errorText(cause), 1024),
		}, nil)
		_ = c.ledger.EndRun(ctx, spec.RunID, string(state), c.now(), map[string]any{
			"error": errorText(cause),
		})
	}
	c.unregisterRun(spec.RunID)
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
	if c.ledger == nil {
		return c.listLocal(filter), nil
	}
	runs, err := c.ledger.ListRuns(ctx, runledger.RunQuery{
		SessionID:   strings.TrimSpace(filter.ParentSessionID),
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
	durable, err := c.ledger.GetRun(ctx, id)
	if err != nil {
		return agentcoord.AgentRun{}, fmt.Errorf("get durable subagent run %s: %w", id, err)
	}
	if durable.Backend != c.adapter {
		return agentcoord.AgentRun{}, fmt.Errorf("subagent not found: %s", id)
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
	return c.Send(ctx, agentcoord.AgentMessage{
		RunID:    strings.TrimSpace(id),
		To:       strings.TrimSpace(id),
		From:     "user",
		Kind:     "steer",
		Content:  strings.TrimSpace(content),
		Delivery: "queued",
	})
}

// Send writes a durable mailbox entry before attempting live delivery. Message
// bodies live in evidence; events carry bounded previews and evidence
// references so telemetry never becomes a hidden content store.
func (c *Coordinator) Send(ctx context.Context, message agentcoord.AgentMessage) (agentcoord.AgentMessage, error) {
	if c == nil {
		return agentcoord.AgentMessage{}, fmt.Errorf("subagent coordinator is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
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
	if message.ID == "" {
		message.ID = "msg_" + ulid.Make().String()
	}
	if message.Delivery == "" {
		message.Delivery = "queued"
	}
	if message.CreatedAt.IsZero() {
		message.CreatedAt = c.now()
	}
	if _, err := c.Status(ctx, message.RunID); err != nil {
		return agentcoord.AgentMessage{}, err
	}

	refs, err := c.storeMessage(ctx, message)
	if err != nil {
		return agentcoord.AgentMessage{}, err
	}
	message.EvidenceRefs = refs
	if err := c.appendMessage(ctx, message); err != nil {
		return agentcoord.AgentMessage{}, err
	}
	c.addMailbox(message)
	if c.manager != nil {
		if err := c.manager.Deliver(ctx, message.RunID, message); err == nil {
			message.Delivery = "delivered"
			c.updateMailboxDelivery(message.ID, message.RunID, message.Delivery)
			if err := c.appendMessageDelivery(ctx, message); err != nil {
				c.noteDurabilityError(message.RunID, err)
			}
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
	events, err := c.ledger.ListEvents(ctx, runledger.EventQuery{RunID: id, Types: []string{
		runledger.EventSubagentMessageSent,
		runledger.EventSubagentMessageDelivered,
		runledger.EventSubagentSteered,
	}})
	if err != nil {
		return nil, fmt.Errorf("list subagent mailbox: %w", err)
	}
	messages := make([]agentcoord.AgentMessage, 0, len(events))
	positions := make(map[string]int, len(events))
	delivered := make(map[string]struct{})
	for _, event := range events {
		if event.Type == runledger.EventSubagentMessageDelivered {
			messageID := mapString(event.Payload, "message_id")
			if index, ok := positions[messageID]; ok {
				messages[index].Delivery = "delivered"
			} else if messageID != "" {
				delivered[messageID] = struct{}{}
			}
			continue
		}
		message := messageFromEvent(event)
		if _, ok := delivered[message.ID]; ok {
			message.Delivery = "delivered"
		}
		if len(message.EvidenceRefs) > 0 && c.evidence != nil {
			if object, err := c.evidence.Get(ctx, message.EvidenceRefs[0]); err == nil {
				message.Content = string(object.InlineBody)
			}
		}
		positions[message.ID] = len(messages)
		messages = append(messages, message)
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

	if c.ledger != nil && c.claims == nil {
		return agentcoord.AgentClaimResult{}, fmt.Errorf("subagent coordinator durable claims require a claim journal")
	}
	if c.claims != nil {
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
	if c.claims != nil {
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

func (c *Coordinator) observeLifecycle(snapshot Snapshot) {
	if c == nil {
		return
	}
	ctx := context.Background()
	spec, ok := c.taskSpec(snapshot.ID)
	if !ok {
		spec = taskSpecFromSnapshot(snapshot)
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

	refs, err := c.storeTerminalReport(ctx, spec, snapshot)
	if err != nil {
		c.noteDurabilityError(snapshot.ID, err)
		c.finishDurably(ctx, spec, snapshot, nil, err)
	} else {
		c.addEvidence(snapshot.ID, refs...)
		c.finishDurably(ctx, spec, snapshot, refs, nil)
	}
	if err := c.Release(ctx, agentcoord.AgentClaimRequest{RunID: snapshot.ID}, "terminal "+string(snapshot.State)); err != nil {
		c.noteDurabilityError(snapshot.ID, err)
	}
}

func (c *Coordinator) storeTerminalReport(ctx context.Context, spec agentcoord.AgentTaskSpec, snapshot Snapshot) ([]string, error) {
	if c == nil || c.ledger == nil {
		return nil, nil
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
		"error":                 snapshot.Error,
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

func (c *Coordinator) finishDurably(ctx context.Context, spec agentcoord.AgentTaskSpec, snapshot Snapshot, refs []string, durabilityErr error) {
	if c == nil || c.ledger == nil {
		return
	}
	state := stateFromSnapshot(snapshot.State)
	eventType := eventTypeForState(state)
	if durabilityErr != nil {
		state = agentcoord.AgentRunFailed
		eventType = runledger.EventSubagentFailed
	}
	payload := map[string]any{
		"state":   string(state),
		"summary": boundedCoordinatorText(snapshot.Output, 4096),
		"error":   boundedCoordinatorText(firstNonEmpty(snapshot.Error, errorText(durabilityErr)), 1024),
	}
	if err := c.append(ctx, spec, eventType, payload, refs); err != nil {
		c.noteDurabilityError(snapshot.ID, err)
	}
	outcome := map[string]any{
		"summary":       boundedCoordinatorText(snapshot.Output, 4096),
		"error":         firstNonEmpty(snapshot.Error, errorText(durabilityErr)),
		"evidence_refs": refs,
	}
	if err := c.ledger.EndRun(ctx, snapshot.ID, string(state), c.now(), outcome); err != nil {
		c.noteDurabilityError(snapshot.ID, err)
	}
}

func (c *Coordinator) storeMessage(ctx context.Context, message agentcoord.AgentMessage) ([]string, error) {
	if c == nil || c.ledger == nil {
		return nil, nil
	}
	if c.evidence == nil {
		return nil, fmt.Errorf("subagent coordinator requires evidence for durable mailbox delivery")
	}
	obj, err := c.evidence.Put(ctx, evidence.Object{
		Kind:       evidence.KindSubagentMessage,
		MediaType:  "text/plain",
		InlineBody: []byte(message.Content),
		Metadata: map[string]any{
			evidence.MetaRunID:    message.RunID,
			evidence.MetaEntityID: message.To,
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
	return c.append(ctx, spec, eventType, map[string]any{
		"message_id":      message.ID,
		"from":            message.From,
		"to":              message.To,
		"kind":            message.Kind,
		"delivery":        message.Delivery,
		"content_preview": boundedCoordinatorText(message.Content, 512),
	}, message.EvidenceRefs)
}

func (c *Coordinator) appendMessageDelivery(ctx context.Context, message agentcoord.AgentMessage) error {
	spec, err := c.taskSpecForRun(ctx, message.RunID)
	if err != nil {
		return err
	}
	return c.append(ctx, spec, runledger.EventSubagentMessageDelivered, map[string]any{
		"message_id": message.ID,
		"to":         message.To,
		"delivery":   "delivered",
	}, nil)
}

func (c *Coordinator) append(ctx context.Context, spec agentcoord.AgentTaskSpec, eventType string, payload map[string]any, evidenceIDs []string) error {
	if c == nil || c.ledger == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	_, err := c.ledger.Append(ctx, runledger.Event{
		Type:        eventType,
		SessionID:   spec.ParentSessionID,
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
	durable, err := c.ledger.GetRun(ctx, runID)
	if err != nil {
		return agentcoord.AgentTaskSpec{}, fmt.Errorf("get durable subagent run %s: %w", runID, err)
	}
	if durable.Backend != c.adapter {
		return agentcoord.AgentTaskSpec{}, fmt.Errorf("subagent not found: %s", runID)
	}
	return agentcoord.AgentTaskSpec{
		ID:              durable.TaskID,
		RunID:           durable.RunID,
		ParentRunID:     durable.ParentRunID,
		ParentSessionID: durable.SessionID,
		Agent:           durable.AgentID,
		Model:           durable.ModelID,
		Budget:          budgetFromMap(durable.Budget),
	}, nil
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
		Error:        snapshot.Error,
		EvidenceRefs: c.evidenceRefs(snapshot.ID),
	}
	if durabilityErr := c.durabilityErrorFor(snapshot.ID); durabilityErr != "" {
		state = agentcoord.AgentRunFailed
		result.Error = firstNonEmpty(result.Error, durabilityErr)
	}
	return agentcoord.AgentRun{
		ID:              snapshot.ID,
		ParentRunID:     firstNonEmpty(snapshot.ParentRunID, spec.ParentRunID),
		ParentSessionID: firstNonEmpty(snapshot.ParentSessionID, spec.ParentSessionID),
		Task:            spec,
		State:           state,
		Adapter:         c.adapter,
		PID:             snapshot.PID,
		StartedAt:       snapshot.StartedAt,
		FinishedAt:      snapshot.FinishedAt,
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
		Summary:      mapString(durable.Outcome, "summary"),
		Error:        mapString(durable.Outcome, "error"),
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
		ParentRunID:     durable.ParentRunID,
		ParentSessionID: durable.SessionID,
		Task:            spec,
		State:           state,
		Adapter:         durable.Backend,
		StartedAt:       durable.StartedAt,
		FinishedAt:      finishedAt,
		Claims:          c.claimsFor(durable.RunID),
		MailboxCount:    c.mailboxCount(durable.RunID),
		Result:          result,
	}
}

func taskSpecFromSnapshot(snapshot Snapshot) agentcoord.AgentTaskSpec {
	return agentcoord.AgentTaskSpec{
		ID:              snapshot.TaskID,
		RunID:           snapshot.ID,
		ParentRunID:     snapshot.ParentRunID,
		ParentSessionID: snapshot.ParentSessionID,
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
	c.durabilityError[runID] = "durability error: " + boundedCoordinatorText(err.Error(), 1024)
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

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
