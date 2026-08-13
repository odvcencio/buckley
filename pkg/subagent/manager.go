package subagent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"

	"m31labs.dev/buckley/pkg/agentcoord"
	"m31labs.dev/buckley/pkg/persona"
	"m31labs.dev/buckley/pkg/telemetry"
)

const (
	DefaultMaxConcurrent = 4
	maxCapturedOutput    = 256 * 1024
	// DefaultOutputSpoolLimit bounds child output retained on disk. Process
	// runners should stream into a spool instead of accumulating output in RAM.
	DefaultOutputSpoolLimit int64 = 32 * 1024 * 1024
	defaultCommandBuffer          = 64
	// maxTaskTelemetryBytes matches boundedTask's snapshot-level bound so the
	// telemetry copy of a task description is never larger than the
	// snapshot value it was derived from.
	maxTaskTelemetryBytes = 4096
)

type State string

const (
	StateRunning   State = "running"
	StateCompleted State = "completed"
	StateFailed    State = "failed"
	StateCancelled State = "cancelled"
)

type Request struct {
	ID              string
	ParentSessionID string
	ParentRunID     string
	TaskID          string
	Agent           string
	Spec            string
	Task            string
	TimeoutSeconds  int
	Budget          agentcoord.Budget
	// Persona is the persona name resolved for this spawn, empty when the
	// spawn did not request one. Set via SpawnOptions.Persona.
	Persona string
	// Model is the model alias pinned by the resolved persona, empty when
	// the persona pins no model (a tier-only pin, or no persona at all).
	Model string
	// Tier is the tier persona.ValidateEscalation resolved for this spawn:
	// the persona's own pin, or persona.DefaultTier for an unpinned
	// persona. Empty when no persona was requested.
	Tier persona.Tier
	// SystemPrompt is the resolved persona's prompt body. A Runner that
	// builds a model request should prepend it ahead of Task as system
	// context; Task itself stays the task instruction, unmodified.
	SystemPrompt string
	// StepCap is the resolved persona's iteration budget (0 means unset).
	// The local Buckley process runner carries it in ChildContract so the
	// child's shared ACP controller enforces it.
	StepCap int
	// AllowedTools is the resolved capability allowlist. Nil means the
	// persona and caller left it unconstrained; an empty non-nil list means
	// explicitly no tools.
	AllowedTools []string
	// The remaining fields are adapter-neutral execution constraints supplied
	// by a coordinator.
	Effort          string
	WorkspaceClaims []string
	Isolation       string
	OutputSchema    string
	ApprovalPosture string
}

type Runner interface {
	Run(ctx context.Context, request Request, started func(pid int)) (string, error)
}

// InteractiveRunner is an optional local adapter capability. Existing runners
// remain valid; capable runners acknowledge each command after transporting it
// into the live child process.
type InteractiveRunner interface {
	RunInteractive(ctx context.Context, request Request, started func(pid int), commands <-chan CommandDelivery) (string, error)
}

// CapturedOutput transfers ownership of a bounded temporary output spool to
// Manager. Preview is safe for snapshots and telemetry; SpoolPath is available
// only while the terminal lifecycle observer is running and is then removed.
type CapturedOutput struct {
	Preview       string
	SpoolPath     string
	ObservedBytes int64
	CapturedBytes int64
	LimitBytes    int64
	Truncated     bool
}

// CapturedRunner is an optional process-adapter capability that avoids
// materializing an unbounded child transcript in memory.
type CapturedRunner interface {
	RunCaptured(ctx context.Context, request Request, started func(pid int)) (CapturedOutput, error)
}

// InteractiveCapturedRunner combines bounded output capture with live command
// delivery. Manager prefers it over the legacy string-returning interface.
type InteractiveCapturedRunner interface {
	RunInteractiveCaptured(ctx context.Context, request Request, started func(pid int), commands <-chan CommandDelivery) (CapturedOutput, error)
}

// ErrLiveDeliveryUnavailable means a command remains safely queued because no
// attached adapter can transport it into the running child.
var ErrLiveDeliveryUnavailable = errors.New("live subagent command delivery is unavailable")

// CommandDelivery pairs a durable message with a one-shot transport ack.
type CommandDelivery struct {
	Message agentcoord.Message
	ack     chan error
}

// Acknowledge reports whether the live adapter accepted the command.
func (d CommandDelivery) Acknowledge(err error) {
	if d.ack == nil {
		return
	}
	select {
	case d.ack <- err:
	default:
	}
}

type Snapshot struct {
	ID              string    `json:"id"`
	ParentSessionID string    `json:"parent_session_id,omitempty"`
	ParentRunID     string    `json:"parent_run_id,omitempty"`
	TaskID          string    `json:"task_id,omitempty"`
	Agent           string    `json:"agent,omitempty"`
	Spec            string    `json:"spec,omitempty"`
	Task            string    `json:"task,omitempty"`
	State           State     `json:"state"`
	PID             int       `json:"pid,omitempty"`
	StartedAt       time.Time `json:"started_at"`
	FinishedAt      time.Time `json:"finished_at,omitempty"`
	Output          string    `json:"output,omitempty"`
	Error           string    `json:"error,omitempty"`
	OutputBytes     int64     `json:"output_bytes,omitempty"`
	CapturedBytes   int64     `json:"captured_output_bytes,omitempty"`
	OutputTruncated bool      `json:"output_truncated,omitempty"`
	// OutputSpoolPath is intentionally process-local and never serialized. A
	// lifecycle observer may consume it synchronously before Manager removes it.
	OutputSpoolPath string `json:"-"`
	// Persona, Model, and Tier are empty unless the spawn resolved a
	// persona via SpawnOptions.Persona; see Request for their meaning.
	Persona         string            `json:"persona,omitempty"`
	Model           string            `json:"model,omitempty"`
	Tier            persona.Tier      `json:"tier,omitempty"`
	StepCap         int               `json:"step_cap,omitempty"`
	AllowedTools    []string          `json:"allowed_tools,omitempty"`
	Effort          string            `json:"effort,omitempty"`
	WorkspaceClaims []string          `json:"workspace_claims,omitempty"`
	Isolation       string            `json:"isolation,omitempty"`
	OutputSchema    string            `json:"output_schema,omitempty"`
	ApprovalPosture string            `json:"approval_posture,omitempty"`
	TimeoutSeconds  int               `json:"timeout_seconds,omitempty"`
	Budget          agentcoord.Budget `json:"budget,omitempty"`
}

// LifecycleObserver receives a copy of a child snapshot whenever its PID or
// terminal state becomes known. It is invoked outside the manager lock so a
// durable adapter can record lifecycle facts without blocking peers.
type LifecycleObserver func(Snapshot)

type run struct {
	snapshot Snapshot
	cancel   context.CancelFunc
	deadline bool
	done     chan struct{}
	commands chan CommandDelivery
}

type Manager struct {
	mu            sync.RWMutex
	runner        Runner
	runs          map[string]*run
	maxConcurrent int
	parentSession string
	hub           *telemetry.Hub
	closed        bool
	wg            sync.WaitGroup
	personas      *persona.Registry
	parentPersona persona.Persona
	observer      LifecycleObserver
}

func NewManager(runner Runner, maxConcurrent int) *Manager {
	if maxConcurrent <= 0 {
		maxConcurrent = DefaultMaxConcurrent
	}
	return &Manager{
		runner:        runner,
		runs:          make(map[string]*run),
		maxConcurrent: maxConcurrent,
	}
}

func (m *Manager) SetTelemetry(hub *telemetry.Hub, parentSession string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.hub = hub
	m.parentSession = strings.TrimSpace(parentSession)
	m.mu.Unlock()
}

// SetPersonaContext registers the persona registry SpawnOptions.Persona
// resolves against, and records parent as the persona this manager's own
// spawns run under. parent feeds pkg/persona.ValidateEscalation, tiller's
// DenyImplicitReasonInheritance discipline: a spawned persona pinning a
// tier more capable than parent's tier is denied with a
// *persona.EscalationError rather than spawned. Both arguments may be zero
// values; a Manager that never calls SetPersonaContext keeps Spawn's
// legacy, persona-free behavior.
func (m *Manager) SetPersonaContext(registry *persona.Registry, parent persona.Persona) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.personas = registry
	m.parentPersona = parent
	m.mu.Unlock()
}

// SetLifecycleObserver installs the optional observer used by durable
// coordinator adapters. Passing nil disables observation.
func (m *Manager) SetLifecycleObserver(observer LifecycleObserver) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.observer = observer
	m.mu.Unlock()
}

// SpawnOptions is the additive, persona-aware counterpart to Spawn's
// positional arguments. Spawn builds one from its arguments and calls
// SpawnWithOptions; existing Spawn call sites are unaffected.
type SpawnOptions struct {
	// ID is the stable child-run identity supplied by a durable coordinator.
	// Empty keeps legacy ULID generation.
	ID              string
	ParentSessionID string
	ParentRunID     string
	TaskID          string
	Agent           string
	Spec            string
	Task            string
	TimeoutSeconds  int
	Budget          agentcoord.Budget
	// Persona is an optional persona name (bare or "@name") resolved
	// against the registry passed to SetPersonaContext. Empty means "no
	// persona": Spawn's legacy behavior, no escalation check performed.
	Persona string
	// The remaining fields are already-resolved execution constraints. A
	// persona may narrow or replace its model, tier, prompt, tool allowlist,
	// and step cap; the local manager threads the final values to Runner.
	Model           string
	Tier            persona.Tier
	SystemPrompt    string
	AllowedTools    []string
	StepCap         int
	Effort          string
	WorkspaceClaims []string
	Isolation       string
	OutputSchema    string
	ApprovalPosture string
}

func (m *Manager) Spawn(agent, spec, task string, timeoutSeconds int) (Snapshot, error) {
	return m.SpawnWithOptions(SpawnOptions{
		Agent:          agent,
		Spec:           spec,
		Task:           task,
		TimeoutSeconds: timeoutSeconds,
	})
}

// SpawnWithOptions spawns a child under opts, resolving opts.Persona
// against the manager's persona registry when set. A named persona that
// cannot be resolved is a spawn error. A resolved persona is validated
// against the manager's parent persona via pkg/persona.ValidateEscalation
// before the child run starts: an escalating pin returns the resulting
// *persona.EscalationError (wrapped, so errors.As still finds it) instead
// of spawning.
func (m *Manager) SpawnWithOptions(opts SpawnOptions) (Snapshot, error) {
	if m == nil || m.runner == nil {
		return Snapshot{}, fmt.Errorf("subagent manager is unavailable")
	}
	task := strings.TrimSpace(opts.Task)
	if task == "" {
		return Snapshot{}, fmt.Errorf("subagent task is required")
	}

	var (
		personaName  string
		model        = strings.TrimSpace(opts.Model)
		tier         = opts.Tier
		systemPrompt = strings.TrimSpace(opts.SystemPrompt)
		stepCap      = opts.StepCap
		allowedTools = copyStrings(opts.AllowedTools)
	)
	if personaName = strings.TrimSpace(opts.Persona); personaName != "" {
		m.mu.RLock()
		registry := m.personas
		parentPersona := m.parentPersona
		m.mu.RUnlock()

		if registry == nil {
			return Snapshot{}, fmt.Errorf("subagent: persona registry is unavailable")
		}
		child, ok := registry.Resolve(personaName)
		if !ok {
			return Snapshot{}, fmt.Errorf("subagent: persona not found: %s", personaName)
		}
		resolvedTier, err := persona.ValidateEscalation(parentPersona, child)
		if err != nil {
			return Snapshot{}, fmt.Errorf("subagent: spawn denied: %w", err)
		}
		if strings.TrimSpace(child.Model) != "" {
			model = child.Model
		}
		tier = resolvedTier
		if strings.TrimSpace(child.Prompt) != "" {
			systemPrompt = child.Prompt
		}
		if child.StepCap > 0 && (stepCap <= 0 || child.StepCap < stepCap) {
			stepCap = child.StepCap
		}
		allowedTools = intersectTools(allowedTools, child.AllowedTools)
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return Snapshot{}, fmt.Errorf("subagent manager is closed")
	}
	if m.activeLocked() >= m.maxConcurrent {
		m.mu.Unlock()
		return Snapshot{}, fmt.Errorf("subagent concurrency limit reached: %d", m.maxConcurrent)
	}
	id := strings.TrimSpace(opts.ID)
	if id == "" {
		id = ulid.Make().String()
	}
	if _, exists := m.runs[id]; exists {
		m.mu.Unlock()
		return Snapshot{}, fmt.Errorf("subagent run already exists: %s", id)
	}
	ctx, cancel := context.WithCancel(context.Background())
	deadlineBound := false
	if timeoutSeconds := minPositiveInt(opts.TimeoutSeconds, opts.Budget.MaxElapsedSecond); timeoutSeconds > 0 {
		ctx, cancel = context.WithTimeout(context.Background(), time.Duration(timeoutSeconds)*time.Second)
		deadlineBound = true
	}
	current := &run{
		snapshot: Snapshot{
			ID:              id,
			ParentSessionID: firstNonEmpty(strings.TrimSpace(opts.ParentSessionID), m.parentSession),
			ParentRunID:     strings.TrimSpace(opts.ParentRunID),
			TaskID:          strings.TrimSpace(opts.TaskID),
			Agent:           strings.TrimSpace(opts.Agent),
			Spec:            strings.TrimSpace(opts.Spec),
			Task:            boundedTask(task),
			State:           StateRunning,
			StartedAt:       time.Now(),
			Persona:         personaName,
			Model:           model,
			Tier:            tier,
			StepCap:         stepCap,
			AllowedTools:    copyStrings(allowedTools),
			Effort:          strings.TrimSpace(opts.Effort),
			WorkspaceClaims: copyStrings(opts.WorkspaceClaims),
			Isolation:       strings.TrimSpace(opts.Isolation),
			OutputSchema:    strings.TrimSpace(opts.OutputSchema),
			ApprovalPosture: strings.TrimSpace(opts.ApprovalPosture),
			TimeoutSeconds:  opts.TimeoutSeconds,
			Budget:          opts.Budget,
		},
		cancel:   cancel,
		deadline: deadlineBound,
		done:     make(chan struct{}),
		commands: make(chan CommandDelivery, defaultCommandBuffer),
	}
	m.runs[id] = current
	snapshot := current.snapshot
	m.wg.Add(1)
	m.mu.Unlock()

	m.publish(telemetry.EventSubagentSpawned, snapshot, "")
	go m.run(ctx, current, Request{
		ID:              id,
		ParentSessionID: snapshot.ParentSessionID,
		ParentRunID:     snapshot.ParentRunID,
		TaskID:          snapshot.TaskID,
		Agent:           snapshot.Agent,
		Spec:            snapshot.Spec,
		Task:            task,
		TimeoutSeconds:  opts.TimeoutSeconds,
		Budget:          opts.Budget,
		Persona:         personaName,
		Model:           model,
		Tier:            tier,
		SystemPrompt:    systemPrompt,
		StepCap:         stepCap,
		AllowedTools:    copyStrings(allowedTools),
		Effort:          snapshot.Effort,
		WorkspaceClaims: copyStrings(snapshot.WorkspaceClaims),
		Isolation:       snapshot.Isolation,
		OutputSchema:    snapshot.OutputSchema,
		ApprovalPosture: snapshot.ApprovalPosture,
	})
	return snapshot, nil
}

func minPositiveInt(values ...int) int {
	minimum := 0
	for _, value := range values {
		if value > 0 && (minimum == 0 || value < minimum) {
			minimum = value
		}
	}
	return minimum
}

func (m *Manager) run(ctx context.Context, current *run, request Request) {
	defer m.wg.Done()
	defer close(current.done)

	started := func(pid int) {
		m.mu.Lock()
		current.snapshot.PID = pid
		snapshot := current.snapshot
		m.mu.Unlock()
		m.publish(telemetry.EventSubagentState, snapshot, "")
		m.observe(snapshot)
	}
	capture, err := m.runCaptured(ctx, request, started, current.commands)

	m.mu.Lock()
	current.snapshot.FinishedAt = time.Now()
	current.snapshot.Output = boundedOutput(capture.Preview)
	current.snapshot.OutputBytes = capture.ObservedBytes
	current.snapshot.CapturedBytes = capture.CapturedBytes
	current.snapshot.OutputTruncated = capture.Truncated
	current.snapshot.OutputSpoolPath = capture.SpoolPath
	eventType := telemetry.EventSubagentCompleted
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded) && current.deadline:
		current.snapshot.State = StateFailed
		current.snapshot.Error = "subagent elapsed-time limit exceeded: " + ctx.Err().Error()
		eventType = telemetry.EventSubagentFailed
	case ctx.Err() != nil:
		current.snapshot.State = StateCancelled
		current.snapshot.Error = ctx.Err().Error()
		eventType = telemetry.EventSubagentCancelled
	case err != nil:
		current.snapshot.State = StateFailed
		current.snapshot.Error = err.Error()
		eventType = telemetry.EventSubagentFailed
	case capture.Truncated:
		current.snapshot.State = StateFailed
		current.snapshot.Error = outputTruncationMessage(capture)
		eventType = telemetry.EventSubagentFailed
	default:
		current.snapshot.State = StateCompleted
	}
	if capture.Truncated && !strings.Contains(current.snapshot.Error, "output capture") {
		current.snapshot.Error = firstNonEmpty(current.snapshot.Error+"; "+outputTruncationMessage(capture), outputTruncationMessage(capture))
	}
	snapshot := current.snapshot
	m.mu.Unlock()
	m.publish(eventType, snapshot, snapshot.Error)
	m.observe(snapshot)
	if capture.SpoolPath != "" {
		_ = os.Remove(capture.SpoolPath)
		m.mu.Lock()
		current.snapshot.OutputSpoolPath = ""
		m.mu.Unlock()
	}
}

func (m *Manager) runCaptured(ctx context.Context, request Request, started func(int), commands <-chan CommandDelivery) (CapturedOutput, error) {
	if interactive, ok := m.runner.(InteractiveCapturedRunner); ok {
		return interactive.RunInteractiveCaptured(ctx, request, started, commands)
	}
	if captured, ok := m.runner.(CapturedRunner); ok {
		return captured.RunCaptured(ctx, request, started)
	}
	var output string
	var err error
	if interactive, ok := m.runner.(InteractiveRunner); ok {
		output, err = interactive.RunInteractive(ctx, request, started, commands)
	} else {
		output, err = m.runner.Run(ctx, request, started)
	}
	return captureLegacyOutput(output), err
}

func captureLegacyOutput(output string) CapturedOutput {
	capture := CapturedOutput{
		Preview:       boundedOutput(output),
		ObservedBytes: int64(len(output)),
		CapturedBytes: int64(len(output)),
		LimitBytes:    DefaultOutputSpoolLimit,
	}
	if len(output) <= maxCapturedOutput {
		return capture
	}
	file, err := os.CreateTemp("", "buckley-subagent-output-*.log")
	if err != nil {
		capture.Truncated = true
		capture.CapturedBytes = 0
		return capture
	}
	limit := int64(len(output))
	if limit > DefaultOutputSpoolLimit {
		limit = DefaultOutputSpoolLimit
		capture.Truncated = true
	}
	written, writeErr := file.WriteString(output[:int(limit)])
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil || int64(written) != limit {
		_ = os.Remove(file.Name())
		capture.Truncated = true
		capture.CapturedBytes = int64(written)
		return capture
	}
	capture.SpoolPath = file.Name()
	capture.CapturedBytes = int64(written)
	return capture
}

func outputTruncationMessage(capture CapturedOutput) string {
	limit := capture.LimitBytes
	if limit <= 0 {
		limit = capture.CapturedBytes
	}
	return fmt.Sprintf("subagent output capture exceeded its %d-byte disk ceiling after observing %d bytes; result is incomplete", limit, capture.ObservedBytes)
}

// Deliver transports one command to an attached interactive child. It does
// not persist the command; Coordinator owns durable mailbox ordering first.
func (m *Manager) Deliver(ctx context.Context, id string, message agentcoord.Message) error {
	if m == nil {
		return ErrLiveDeliveryUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	id = strings.TrimSpace(id)
	m.mu.RLock()
	current, ok := m.runs[id]
	_, interactive := m.runner.(InteractiveRunner)
	if !ok || !interactive || current.snapshot.State != StateRunning {
		m.mu.RUnlock()
		return fmt.Errorf("%w: %s", ErrLiveDeliveryUnavailable, id)
	}
	commands, done := current.commands, current.done
	m.mu.RUnlock()

	delivery := CommandDelivery{Message: message, ack: make(chan error, 1)}
	select {
	case commands <- delivery:
	case <-done:
		return fmt.Errorf("%w: %s", ErrLiveDeliveryUnavailable, id)
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-delivery.ack:
		return err
	case <-done:
		return fmt.Errorf("%w: %s", ErrLiveDeliveryUnavailable, id)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Manager) List() []Snapshot {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	out := make([]Snapshot, 0, len(m.runs))
	for _, current := range m.runs {
		out = append(out, current.snapshot)
	}
	m.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	return out
}

func (m *Manager) Status(id string) (Snapshot, bool) {
	if m == nil {
		return Snapshot{}, false
	}
	m.mu.RLock()
	current, ok := m.runs[strings.TrimSpace(id)]
	if !ok {
		m.mu.RUnlock()
		return Snapshot{}, false
	}
	snapshot := current.snapshot
	m.mu.RUnlock()
	return snapshot, true
}

func (m *Manager) Wait(ctx context.Context, id string) (Snapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.RLock()
	current, ok := m.runs[strings.TrimSpace(id)]
	m.mu.RUnlock()
	if !ok {
		return Snapshot{}, fmt.Errorf("subagent not found: %s", strings.TrimSpace(id))
	}
	select {
	case <-ctx.Done():
		return Snapshot{}, ctx.Err()
	case <-current.done:
		snapshot, _ := m.Status(id)
		return snapshot, nil
	}
}

func (m *Manager) Cancel(id string) (Snapshot, error) {
	if m == nil {
		return Snapshot{}, fmt.Errorf("subagent manager is unavailable")
	}
	m.mu.RLock()
	current, ok := m.runs[strings.TrimSpace(id)]
	if !ok {
		m.mu.RUnlock()
		return Snapshot{}, fmt.Errorf("subagent not found: %s", strings.TrimSpace(id))
	}
	snapshot := current.snapshot
	cancel := current.cancel
	m.mu.RUnlock()
	if snapshot.State != StateRunning {
		return snapshot, nil
	}
	cancel()
	return snapshot, nil
}

func (m *Manager) Close() error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	var cancels []context.CancelFunc
	for _, current := range m.runs {
		if current.snapshot.State == StateRunning {
			cancels = append(cancels, current.cancel)
		}
	}
	m.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	m.wg.Wait()
	return nil
}

func (m *Manager) activeLocked() int {
	active := 0
	for _, current := range m.runs {
		if current.snapshot.State == StateRunning {
			active++
		}
	}
	return active
}

func (m *Manager) publish(eventType telemetry.EventType, snapshot Snapshot, errText string) {
	m.mu.RLock()
	hub := m.hub
	m.mu.RUnlock()
	if hub == nil {
		return
	}
	data := map[string]any{
		"agent_id":          snapshot.ID,
		"parent_session_id": snapshot.ParentSessionID,
		"agent":             snapshot.Agent,
		"state":             string(snapshot.State),
		"pid":               snapshot.PID,
		"provider":          "buckley",
	}
	if snapshot.ParentRunID != "" {
		data["parent_run_id"] = snapshot.ParentRunID
	}
	if snapshot.Persona != "" {
		data["persona"] = snapshot.Persona
	}
	if snapshot.Model != "" {
		data["model"] = snapshot.Model
	}
	if snapshot.Tier != "" {
		data["tier"] = string(snapshot.Tier)
	}
	if snapshot.Effort != "" {
		data["effort"] = snapshot.Effort
	}
	if snapshot.StepCap > 0 {
		data["step_cap"] = snapshot.StepCap
	}
	if snapshot.TimeoutSeconds > 0 {
		data["timeout_seconds"] = snapshot.TimeoutSeconds
	}
	if snapshot.Budget.MaxToolCalls > 0 {
		data["max_tool_calls"] = snapshot.Budget.MaxToolCalls
	}
	if snapshot.Budget.MaxModelRequests > 0 {
		data["max_model_requests"] = snapshot.Budget.MaxModelRequests
	}
	if snapshot.Budget.MaxElapsedSecond > 0 {
		data["max_elapsed_seconds"] = snapshot.Budget.MaxElapsedSecond
	}
	if snapshot.Budget.MaxCostUSD > 0 {
		data["max_cost_usd"] = snapshot.Budget.MaxCostUSD
	}
	if snapshot.Isolation != "" {
		data["isolation"] = snapshot.Isolation
	}
	if snapshot.OutputSchema != "" {
		data["output_schema"] = snapshot.OutputSchema
	}
	if snapshot.ApprovalPosture != "" {
		data["approval_posture"] = snapshot.ApprovalPosture
	}
	if len(snapshot.AllowedTools) > 0 {
		data["allowed_tool_count"] = len(snapshot.AllowedTools)
	}
	if len(snapshot.WorkspaceClaims) > 0 {
		data["workspace_claims"] = telemetry.SanitizeValue(copyStrings(snapshot.WorkspaceClaims), "workspace_claims", maxTaskTelemetryBytes)
	}
	if snapshot.Task != "" {
		data["task"] = telemetry.SanitizeText(snapshot.Task, maxTaskTelemetryBytes)
	}
	if snapshot.Output != "" {
		data["output"] = telemetry.SanitizeText(snapshot.Output, telemetry.MaxResultBytes)
	}
	if snapshot.OutputBytes > 0 {
		data["output_bytes"] = snapshot.OutputBytes
		data["captured_output_bytes"] = snapshot.CapturedBytes
	}
	if snapshot.OutputTruncated {
		data["output_truncated"] = true
	}
	if snapshot.Spec != "" {
		data["spec"] = snapshot.Spec
	}
	if !snapshot.FinishedAt.IsZero() {
		data["duration_ms"] = snapshot.FinishedAt.Sub(snapshot.StartedAt).Milliseconds()
	}
	if errText != "" {
		data["error"] = boundedMessage(errText)
	}
	hub.Publish(telemetry.Event{
		Type:      eventType,
		SessionID: snapshot.ParentSessionID,
		TaskID:    snapshot.ID,
		Data:      data,
	})
}

func (m *Manager) observe(snapshot Snapshot) {
	if m == nil {
		return
	}
	m.mu.RLock()
	observer := m.observer
	m.mu.RUnlock()
	if observer != nil {
		observer(snapshot)
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func copyStrings(values []string) []string {
	if values == nil {
		return nil
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

// intersectTools preserves nil's "unconstrained" semantics and otherwise
// applies the persona allowlist as a non-broadening capability boundary.
func intersectTools(requested, personaTools []string) []string {
	if personaTools == nil {
		return copyStrings(requested)
	}
	personaSet := make(map[string]struct{}, len(personaTools))
	for _, value := range copyStrings(personaTools) {
		personaSet[value] = struct{}{}
	}
	if requested == nil {
		return copyStrings(personaTools)
	}
	out := make([]string, 0, len(requested))
	for _, value := range copyStrings(requested) {
		if _, ok := personaSet[value]; ok {
			out = append(out, value)
		}
	}
	return out
}

func boundedOutput(output string) string {
	if len(output) <= maxCapturedOutput {
		return strings.TrimSpace(output)
	}
	const marker = "\n... subagent output truncated ...\n"
	half := (maxCapturedOutput - len(marker)) / 2
	return strings.TrimSpace(output[:half] + marker + output[len(output)-half:])
}

func boundedMessage(message string) string {
	message = strings.TrimSpace(message)
	if len(message) <= 1024 {
		return message
	}
	return message[:1021] + "..."
}

func boundedTask(task string) string {
	task = strings.TrimSpace(task)
	if len(task) <= 4096 {
		return task
	}
	return task[:4093] + "..."
}
