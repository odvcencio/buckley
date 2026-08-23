package headless

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/giturl"
	"m31labs.dev/buckley/pkg/ipc/command"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/runledger"
	"m31labs.dev/buckley/pkg/session"
	"m31labs.dev/buckley/pkg/sessionexec"
	"m31labs.dev/buckley/pkg/storage"
	"m31labs.dev/buckley/pkg/telemetry"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

// CreateSessionRequest contains parameters for creating a headless session.
type CreateSessionRequest struct {
	Principal        string            `json:"-"`
	Project          string            `json:"project"`
	Branch           string            `json:"branch,omitempty"`
	Env              map[string]string `json:"env,omitempty"`
	Model            string            `json:"model,omitempty"`
	Agent            string            `json:"agent,omitempty"`
	Subagent         string            `json:"subagent,omitempty"`
	Prompt           string            `json:"prompt,omitempty"`
	InitialCommandID string            `json:"-"`
	IdleTimeout      string            `json:"idleTimeout,omitempty"`
	Limits           *ResourceLimits   `json:"limits,omitempty"`
	ToolPolicy       *ToolPolicy       `json:"toolPolicy,omitempty"`

	// AgentProfile is resolved by the authenticated IPC layer from the
	// project-local agent catalog. It is intentionally not accepted from JSON
	// so callers cannot smuggle arbitrary system instructions into a session.
	AgentProfile string `json:"-"`
}

// SessionInfo provides summary information about a headless session.
type SessionInfo struct {
	ID             string               `json:"id"`
	Project        string               `json:"project"`
	Branch         string               `json:"branch,omitempty"`
	Model          string               `json:"model,omitempty"`
	State          RunnerState          `json:"state"`
	CreatedAt      time.Time            `json:"createdAt"`
	LastActive     time.Time            `json:"lastActive"`
	WebSocketURL   string               `json:"websocketUrl,omitempty"`
	InitialReceipt *sessionexec.Receipt `json:"-"`
}

// Registry manages multiple headless session runners.
type Registry struct {
	mu sync.RWMutex

	runners        map[string]*Runner
	pendingRunners map[string]*runnerReservation
	store          *storage.Store
	modelManager   *model.Manager
	config         *config.Config
	projectRoot    string
	telemetry      *telemetry.Hub
	emitter        EventEmitter
	agentProfile   string
	ledger         runledger.Store
	evidence       evidence.Store
	journal        sessionexec.Journal
	durabilityErr  error
	activeBuilds   int
	lifecycle      registryLifecycleState
	buildDrain     *sync.Cond
	stopDone       chan struct{}
	startOnce      sync.Once
	cleanupWG      sync.WaitGroup
	prepareHooks   configuredHookFactory
	activateRunner func(*Runner) error

	// Cleanup settings
	cleanupInterval time.Duration
	maxIdleTime     time.Duration
	stopChan        chan struct{}
}

const defaultHeadlessMaxOutputBytes = 100_000

type registryLifecycleState uint8

const (
	registryAccepting registryLifecycleState = iota
	registryClosing
	registryStopped
)

var errRegistryShuttingDown = errors.New("headless registry is shutting down")

// ErrInitialCommandAcceptance identifies a failed durable initial command boundary.
var ErrInitialCommandAcceptance = errors.New("accept initial prompt")

type configuredHookPlan interface {
	io.Closer
	Activate() error
}

type configuredHookFactory func(*tool.Registry, bool, time.Duration) (configuredHookPlan, error)

type runnerReservation struct {
	done      chan struct{}
	runner    *Runner
	err       error
	completed bool
}

// HandleSessionCommand satisfies the ipc/command.Handler interface.
// It will lazily start a runner for an existing session if needed.
func (r *Registry) HandleSessionCommand(cmd command.SessionCommand) error {
	if r == nil {
		return fmt.Errorf("headless registry unavailable")
	}
	if cmd.SessionID == "" {
		return fmt.Errorf("session ID required")
	}
	_, err := r.AcceptCommand(context.Background(), cmd)
	return err
}

// RegistryConfig configures the session registry.
type RegistryConfig struct {
	Store           *storage.Store
	ModelManager    *model.Manager
	Config          *config.Config
	ProjectRoot     string
	Telemetry       *telemetry.Hub
	Emitter         EventEmitter
	AgentProfile    string
	RunLedger       runledger.Store
	EvidenceStore   evidence.Store
	CleanupInterval time.Duration
	MaxIdleTime     time.Duration
}

// NewRegistry creates a new headless session registry.
func NewRegistry(cfg RegistryConfig) *Registry {
	cleanupInterval := cfg.CleanupInterval
	if cleanupInterval <= 0 {
		cleanupInterval = 5 * time.Minute
	}

	maxIdleTime := cfg.MaxIdleTime
	if maxIdleTime <= 0 {
		maxIdleTime = 30 * time.Minute
	}
	ledger, evidenceStore, durabilityErr := normalizeRegistryDurableStores(cfg.RunLedger, cfg.EvidenceStore)
	var journal sessionexec.Journal
	if durabilityErr == nil && ledger != nil && evidenceStore != nil && cfg.Store != nil {
		journal = cfg.Store
	}

	r := &Registry{
		runners:         make(map[string]*Runner),
		pendingRunners:  make(map[string]*runnerReservation),
		store:           cfg.Store,
		modelManager:    cfg.ModelManager,
		config:          cfg.Config,
		projectRoot:     strings.TrimSpace(cfg.ProjectRoot),
		telemetry:       cfg.Telemetry,
		emitter:         cfg.Emitter,
		agentProfile:    strings.TrimSpace(cfg.AgentProfile),
		ledger:          ledger,
		evidence:        evidenceStore,
		journal:         journal,
		durabilityErr:   durabilityErr,
		cleanupInterval: cleanupInterval,
		maxIdleTime:     maxIdleTime,
		stopChan:        make(chan struct{}),
		stopDone:        make(chan struct{}),
		prepareHooks:    prepareConfiguredHooks,
		activateRunner:  func(runner *Runner) error { return runner.activate() },
	}
	r.buildDrain = sync.NewCond(&r.mu)

	return r
}

// SetDurableStores attaches the canonical run ledger and evidence stores to
// sessions created by this registry. Both stores are required together so a
// durable child can never be launched with an incomplete audit trail.
func (r *Registry) SetDurableStores(ledger runledger.Store, store evidence.Store) error {
	if r == nil {
		return fmt.Errorf("headless registry unavailable")
	}
	ledger, store, err := normalizeRegistryDurableStores(ledger, store)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensureLifecycleLocked()
	if r.lifecycle != registryAccepting {
		return errRegistryShuttingDown
	}
	var journal sessionexec.Journal
	if ledger != nil && store != nil && r.store != nil {
		journal = r.store
	}
	if r.durabilityErr == nil && sameRegistryDurableStores(r.ledger, r.evidence, ledger, store) {
		r.journal = journal
		return nil
	}
	if len(r.runners) > 0 || r.activeBuilds > 0 {
		return fmt.Errorf("cannot change headless durability after runner creation has started")
	}
	r.ledger = ledger
	r.evidence = store
	r.journal = journal
	r.durabilityErr = nil
	return nil
}

func (r *Registry) beginRunnerBuild() (runledger.Store, evidence.Store, error) {
	if r == nil {
		return nil, nil, fmt.Errorf("headless registry unavailable")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensureLifecycleLocked()
	if r.lifecycle != registryAccepting {
		return nil, nil, errRegistryShuttingDown
	}
	if r.durabilityErr != nil {
		return nil, nil, r.durabilityErr
	}
	ledger, store, err := normalizeRegistryDurableStores(r.ledger, r.evidence)
	if err != nil {
		return nil, nil, err
	}
	r.activeBuilds++
	return ledger, store, nil
}

func (r *Registry) finishRunnerBuild() {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.activeBuilds > 0 {
		r.activeBuilds--
	}
	if r.activeBuilds == 0 && r.buildDrain != nil {
		r.buildDrain.Broadcast()
	}
	r.mu.Unlock()
}

func (r *Registry) reserveRunner(sessionID string) (*runnerReservation, *Runner, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensureLifecycleLocked()
	if r.lifecycle != registryAccepting {
		return nil, nil, false, errRegistryShuttingDown
	}
	if runner := r.runners[sessionID]; runner != nil {
		return nil, runner, false, nil
	}
	if reservation := r.pendingRunners[sessionID]; reservation != nil {
		return reservation, nil, false, nil
	}
	reservation := &runnerReservation{done: make(chan struct{})}
	r.pendingRunners[sessionID] = reservation
	return reservation, nil, true, nil
}

func (r *Registry) reserveNewRunner(sessionID string) (*runnerReservation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensureLifecycleLocked()
	if r.lifecycle != registryAccepting {
		return nil, errRegistryShuttingDown
	}
	if r.runners[sessionID] != nil {
		return nil, fmt.Errorf("generated session ID already has an active runner: %s", sessionID)
	}
	if r.pendingRunners[sessionID] != nil {
		return nil, fmt.Errorf("generated session ID already has a pending runner: %s", sessionID)
	}
	reservation := &runnerReservation{done: make(chan struct{})}
	r.pendingRunners[sessionID] = reservation
	return reservation, nil
}

func (r *Registry) waitRunnerReservation(reservation *runnerReservation) (*Runner, error) {
	if reservation == nil {
		return nil, fmt.Errorf("runner reservation unavailable")
	}
	<-reservation.done
	r.mu.RLock()
	accepting := r.lifecycle == registryAccepting
	r.mu.RUnlock()
	if !accepting {
		return nil, errRegistryShuttingDown
	}
	if reservation.err != nil {
		return nil, reservation.err
	}
	if reservation.runner == nil {
		return nil, fmt.Errorf("runner reservation completed without a runner")
	}
	return reservation.runner, nil
}

func (r *Registry) completeRunnerReservation(sessionID string, reservation *runnerReservation, runner *Runner, err error) {
	r.mu.Lock()
	r.completeRunnerReservationLocked(sessionID, reservation, runner, err)
	r.mu.Unlock()
}

func (r *Registry) completeRunnerReservationLocked(sessionID string, reservation *runnerReservation, runner *Runner, err error) {
	if reservation == nil || reservation.completed {
		return
	}
	reservation.runner = runner
	reservation.err = err
	reservation.completed = true
	if r.pendingRunners[sessionID] == reservation {
		delete(r.pendingRunners, sessionID)
	}
	close(reservation.done)
}

func (r *Registry) rollbackUnpublishedSession(sessionID string, runner *Runner, ledger runledger.Store, foregroundReady bool, cause error) error {
	runner.disposeBeforeStart()
	if foregroundReady && ledger != nil {
		r.mu.RLock()
		journal := r.journal
		r.mu.RUnlock()
		if journal == nil && r.store != nil {
			journal = r.store
		}
		if journal == nil {
			return fmt.Errorf("%w (retain unpublished session: durable command journal unavailable)", cause)
		}
		ctx, cancel := context.WithTimeout(context.Background(), defaultDurableJournalOperationTTL)
		_, quiesceErr := journal.QuiesceSession(ctx, sessionID, sessionexec.ExecutionModeDetached, "session_creation_rolled_back")
		cancel()
		if quiesceErr != nil {
			return fmt.Errorf("%w (retain unpublished session after rollback quiesce failed: %v)", cause, quiesceErr)
		}

		endCtx, endCancel := context.WithTimeout(context.Background(), defaultDurableJournalOperationTTL)
		endErr := ledger.EndRun(endCtx, sessionexec.RunIDForSession(sessionID), "cancelled", time.Now().UTC(), map[string]any{
			"code": "session_creation_rolled_back",
		})
		endCancel()
		if endErr != nil && !errors.Is(endErr, runledger.ErrNotFound) {
			return fmt.Errorf("%w (retain unpublished session after foreground run cancellation failed: %v)", cause, endErr)
		}
	}
	rollbackErr := r.store.DeleteSessionUnpublished(sessionID)
	if rollbackErr != nil {
		return fmt.Errorf("%w (rollback unpublished session: %v)", cause, rollbackErr)
	}
	return cause
}

func validateHeadlessExecutionState(journal sessionexec.Journal, sessionID string) error {
	if journal == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultDurableJournalOperationTTL)
	state, err := journal.GetExecutionState(ctx, sessionID)
	cancel()
	if err != nil {
		return fmt.Errorf("read headless execution state: %w", err)
	}
	if state.Mode != sessionexec.ExecutionModeHeadless {
		return fmt.Errorf("%w: mode %s", sessionexec.ErrSessionQuiesced, state.Mode)
	}
	return nil
}

func (r *Registry) ensureLifecycleLocked() {
	if r.runners == nil {
		r.runners = make(map[string]*Runner)
	}
	if r.pendingRunners == nil {
		r.pendingRunners = make(map[string]*runnerReservation)
	}
	if r.stopChan == nil {
		r.stopChan = make(chan struct{})
	}
	if r.stopDone == nil {
		r.stopDone = make(chan struct{})
	}
	if r.buildDrain == nil {
		r.buildDrain = sync.NewCond(&r.mu)
	}
	if r.prepareHooks == nil {
		r.prepareHooks = prepareConfiguredHooks
	}
	if r.activateRunner == nil {
		r.activateRunner = func(runner *Runner) error { return runner.activate() }
	}
}

func (r *Registry) accepting() bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	accepting := r.lifecycle == registryAccepting
	r.mu.RUnlock()
	return accepting
}

func sameRegistryDurableStores(currentLedger runledger.Store, currentEvidence evidence.Store, ledger runledger.Store, store evidence.Store) bool {
	return sameRegistryStoreIdentity(currentLedger, ledger) && sameRegistryStoreIdentity(currentEvidence, store)
}

func sameRegistryStoreIdentity(left, right any) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	leftType, rightType := reflect.TypeOf(left), reflect.TypeOf(right)
	if leftType != rightType || !leftType.Comparable() {
		return false
	}
	return left == right
}

func normalizeRegistryDurableStores(ledger runledger.Store, store evidence.Store) (runledger.Store, evidence.Store, error) {
	if isRegistryTypedNil(ledger) {
		return nil, nil, fmt.Errorf("headless run ledger is typed nil")
	}
	if isRegistryTypedNil(store) {
		return nil, nil, fmt.Errorf("headless evidence store is typed nil")
	}
	if (ledger == nil) != (store == nil) {
		return nil, nil, fmt.Errorf("headless durability requires both run ledger and evidence stores")
	}
	return ledger, store, nil
}

func isRegistryTypedNil(value any) bool {
	if value == nil {
		return false
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

// Start begins the registry's background cleanup goroutine.
func (r *Registry) Start(ctx context.Context) {
	if r == nil {
		return
	}
	r.startOnce.Do(func() {
		r.mu.Lock()
		r.ensureLifecycleLocked()
		start := r.lifecycle == registryAccepting
		if start {
			r.cleanupWG.Add(1)
		}
		r.mu.Unlock()
		if !start {
			return
		}
		go func() {
			defer r.cleanupWG.Done()
			r.cleanupLoop(ctx)
		}()
	})
}

// Stop shuts down all runners and stops the cleanup loop.
func (r *Registry) Stop() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.ensureLifecycleLocked()
	if r.lifecycle != registryAccepting {
		done := r.stopDone
		r.mu.Unlock()
		<-done
		return
	}
	r.lifecycle = registryClosing
	close(r.stopChan)
	for r.activeBuilds > 0 {
		r.buildDrain.Wait()
	}
	runners := make([]*Runner, 0, len(r.runners))
	for id, runner := range r.runners {
		runners = append(runners, runner)
		delete(r.runners, id)
	}
	r.mu.Unlock()

	for _, runner := range runners {
		runner.Stop()
	}
	r.cleanupWG.Wait()

	r.mu.Lock()
	r.lifecycle = registryStopped
	close(r.stopDone)
	r.mu.Unlock()
}

// CreateSession creates a new headless session.
func (r *Registry) CreateSession(req CreateSessionRequest) (*SessionInfo, error) {
	if r == nil {
		return nil, fmt.Errorf("registry unavailable")
	}
	ledger, evidenceStore, err := r.beginRunnerBuild()
	if err != nil {
		return nil, err
	}
	defer r.finishRunnerBuild()
	if r.store == nil {
		return nil, fmt.Errorf("storage not configured")
	}
	if r.modelManager == nil {
		return nil, fmt.Errorf("model manager not configured")
	}
	journal, stepJournal, err := r.resolveRunnerDurability(ledger, evidenceStore)
	if err != nil {
		return nil, err
	}
	if req.Limits != nil {
		if strings.TrimSpace(req.Limits.CPU) != "" || strings.TrimSpace(req.Limits.Memory) != "" || strings.TrimSpace(req.Limits.Storage) != "" {
			return nil, fmt.Errorf("resource limits cpu/memory/storage are not supported in this deployment (only timeoutSeconds is enforced)")
		}
	}

	// Generate session ID
	sessionID := session.GenerateSessionID(session.DefaultSessionID())

	projectPath, gitRepo, gitBranch, err := r.resolveProject(sessionID, req)
	if err != nil {
		return nil, err
	}

	// Parse idle timeout
	idleTimeout := r.maxIdleTime
	if req.IdleTimeout != "" {
		if d, err := time.ParseDuration(req.IdleTimeout); err == nil && d > 0 {
			idleTimeout = d
		}
	}

	maxRuntime := time.Duration(0)
	if req.Limits != nil && req.Limits.TimeoutSeconds > 0 {
		maxRuntime = time.Duration(req.Limits.TimeoutSeconds) * time.Second
	}

	// Determine model
	modelID := req.Model
	if modelID == "" && r.config != nil {
		modelID = r.config.Models.Execution
		if modelID == "" {
			modelID = r.config.Models.Planning
		}
	}

	// Create storage session
	sess := &storage.Session{
		ID:          sessionID,
		Principal:   strings.TrimSpace(req.Principal),
		ProjectPath: projectPath,
		GitRepo:     gitRepo,
		GitBranch:   gitBranch,
		Model:       modelID,
		CreatedAt:   time.Now(),
		LastActive:  time.Now(),
		Status:      storage.SessionStatusActive,
	}

	tools, hooks, err := r.buildToolRegistry(sessionID, projectPath, ledger, evidenceStore)
	if err != nil {
		return nil, err
	}
	if req.ToolPolicy != nil {
		applyToolPolicy(tools, req.ToolPolicy)
	}
	if len(req.Env) > 0 {
		tools.SetEnv(req.Env)
	}
	if req.ToolPolicy != nil && req.ToolPolicy.MaxFileSizeBytes > 0 {
		tools.SetMaxFileSizeBytes(req.ToolPolicy.MaxFileSizeBytes)
	}
	if req.ToolPolicy != nil && req.ToolPolicy.MaxExecTimeSeconds > 0 {
		tools.SetMaxExecTimeSeconds(req.ToolPolicy.MaxExecTimeSeconds)
	}

	// Create runner. A per-session profile resolved by IPC takes precedence over
	// the server-wide profile; this lets the browser launch different agents and
	// subagents without changing daemon configuration.
	agentProfile := strings.TrimSpace(req.AgentProfile)
	if agentProfile == "" {
		agentProfile = r.agentProfile
	}
	runner, err := newInertRunner(RunnerConfig{
		Session:        sess,
		ModelManager:   r.modelManager,
		Tools:          tools,
		Store:          r.store,
		Config:         r.config,
		Emitter:        r.emitter,
		Telemetry:      r.telemetry,
		IdleTimeout:    idleTimeout,
		ModelOverride:  modelID,
		ToolPolicy:     req.ToolPolicy,
		MaxRuntime:     maxRuntime,
		AgentProfile:   agentProfile,
		CommandJournal: journal,
		RunLedger:      ledger,
		EvidenceStore:  evidenceStore,
		StepJournal:    stepJournal,
	})
	if err != nil {
		if hooks != nil {
			_ = hooks.Close()
		}
		return nil, fmt.Errorf("create runner: %w", err)
	}
	runner.hookCloser = hooks

	reservation, err := r.reserveNewRunner(sessionID)
	if err != nil {
		runner.disposeBeforeStart()
		return nil, err
	}
	foregroundReady := false
	completeFailure := func(cause error, persisted bool) error {
		if persisted {
			cause = r.rollbackUnpublishedSession(sessionID, runner, ledger, foregroundReady, cause)
		} else {
			runner.disposeBeforeStart()
		}
		r.completeRunnerReservation(sessionID, reservation, nil, cause)
		return cause
	}

	if !r.accepting() {
		return nil, completeFailure(errRegistryShuttingDown, false)
	}
	if err := r.store.CreateSessionUnpublished(sess); err != nil {
		return nil, completeFailure(fmt.Errorf("create session: %w", err), false)
	}
	if !r.accepting() {
		return nil, completeFailure(errRegistryShuttingDown, true)
	}
	if err := validateHeadlessExecutionState(journal, sessionID); err != nil {
		return nil, completeFailure(err, true)
	}
	var acceptedInitial *command.SessionCommand
	var initialReceipt *sessionexec.Receipt
	if journal != nil && strings.TrimSpace(req.Prompt) != "" {
		initial := command.SessionCommand{
			SessionID: sessionID, ID: strings.TrimSpace(req.InitialCommandID), Type: "input",
			Content: req.Prompt, AcceptedBy: strings.TrimSpace(req.Principal),
		}
		initial.EnsureID()
		receipt, err := runner.acceptDurableCommand(context.Background(), initial, false, true, false)
		if err != nil {
			return nil, completeFailure(fmt.Errorf("%w: %w", ErrInitialCommandAcceptance, err), true)
		}
		initial.ID = receipt.CommandID
		initialReceipt = &receipt
		if !receipt.Duplicate {
			acceptedInitial = &initial
		}
	}
	if hooks != nil {
		if err := hooks.Activate(); err != nil {
			return nil, completeFailure(fmt.Errorf("activate plugin hooks: %w", err), true)
		}
	}
	if journal != nil {
		if _, err := ensureForegroundRun(context.Background(), ledger, sessionID, modelID); err != nil {
			return nil, completeFailure(err, true)
		}
		foregroundReady = true
		if err := validateHeadlessExecutionState(journal, sessionID); err != nil {
			return nil, completeFailure(err, true)
		}
	}

	r.mu.Lock()
	if r.lifecycle != registryAccepting {
		r.mu.Unlock()
		return nil, completeFailure(errRegistryShuttingDown, true)
	}
	if r.pendingRunners[sessionID] != reservation {
		r.mu.Unlock()
		return nil, completeFailure(fmt.Errorf("generated session runner reservation changed before publication: %s", sessionID), true)
	}
	if r.runners[sessionID] != nil {
		r.mu.Unlock()
		return nil, completeFailure(fmt.Errorf("generated session runner already exists before publication: %s", sessionID), true)
	}
	r.runners[sessionID] = runner
	err = r.activateRunner(runner)
	if err == nil {
		r.store.PublishSessionCreated(sess)
		r.completeRunnerReservationLocked(sessionID, reservation, runner, nil)
	} else {
		delete(r.runners, sessionID)
	}
	r.mu.Unlock()
	if err != nil {
		return nil, completeFailure(fmt.Errorf("activate runner: %w", err), true)
	}
	if journal != nil {
		if acceptedInitial != nil {
			runner.emitCommandEvent(EventCommandQueued, *acceptedInitial, nil)
		}
		runner.wakeDurableLanes()
	}

	// Legacy sessions retain their historical asynchronous initial prompt.
	if journal == nil && req.Prompt != "" {
		initial := command.SessionCommand{
			SessionID: sessionID, Type: "input", Content: req.Prompt,
			AcceptedBy: strings.TrimSpace(req.Principal),
		}
		initial.EnsureID()
		go func(cmd command.SessionCommand) {
			_ = runner.HandleSessionCommand(cmd)
		}(initial)
	}

	return &SessionInfo{
		ID:             sessionID,
		Project:        projectPath,
		Branch:         gitBranch,
		Model:          modelID,
		State:          StateIdle,
		CreatedAt:      sess.CreatedAt,
		LastActive:     sess.LastActive,
		WebSocketURL:   fmt.Sprintf("/ws?session=%s", sessionID),
		InitialReceipt: initialReceipt,
	}, nil
}

// EnsureSession starts a runner for an existing stored session if one is not already active.
func (r *Registry) EnsureSession(sessionID string) (*Runner, error) {
	if r == nil {
		return nil, fmt.Errorf("registry unavailable")
	}
	ledger, evidenceStore, err := r.beginRunnerBuild()
	if err != nil {
		return nil, err
	}
	defer r.finishRunnerBuild()
	if r.store == nil {
		return nil, fmt.Errorf("storage not configured")
	}
	if r.modelManager == nil {
		return nil, fmt.Errorf("model manager not configured")
	}
	journal, stepJournal, err := r.resolveRunnerDurability(ledger, evidenceStore)
	if err != nil {
		return nil, err
	}
	if sessionID == "" {
		return nil, fmt.Errorf("session ID required")
	}

	reservation, runner, owner, err := r.reserveRunner(sessionID)
	if err != nil {
		return nil, err
	}
	if runner != nil {
		return runner, nil
	}
	if !owner {
		return r.waitRunnerReservation(reservation)
	}

	sess, err := r.store.GetSession(sessionID)
	if err != nil {
		err = fmt.Errorf("load session: %w", err)
		r.completeRunnerReservation(sessionID, reservation, nil, err)
		return nil, err
	}
	if sess == nil {
		err = fmt.Errorf("session not found: %s", sessionID)
		r.completeRunnerReservation(sessionID, reservation, nil, err)
		return nil, err
	}
	if err := validateHeadlessExecutionState(journal, sessionID); err != nil {
		r.completeRunnerReservation(sessionID, reservation, nil, err)
		return nil, err
	}
	project := sess.ProjectPath
	if project == "" {
		project = sess.GitRepo
	}

	idleTimeout := r.maxIdleTime
	modelID := strings.TrimSpace(sess.Model)
	if modelID == "" && r.config != nil {
		modelID = r.config.Models.Execution
		if modelID == "" {
			modelID = r.config.Models.Planning
		}
	}

	tools, hooks, err := r.buildToolRegistry(sessionID, project, ledger, evidenceStore)
	if err != nil {
		r.completeRunnerReservation(sessionID, reservation, nil, err)
		return nil, err
	}
	runner, err = newInertRunner(RunnerConfig{
		Session:        sess,
		ModelManager:   r.modelManager,
		Tools:          tools,
		Store:          r.store,
		Config:         r.config,
		Emitter:        r.emitter,
		Telemetry:      r.telemetry,
		IdleTimeout:    idleTimeout,
		ModelOverride:  modelID,
		AgentProfile:   r.agentProfile,
		CommandJournal: journal,
		RunLedger:      ledger,
		EvidenceStore:  evidenceStore,
		StepJournal:    stepJournal,
	})
	if err != nil {
		if hooks != nil {
			_ = hooks.Close()
		}
		err = fmt.Errorf("create runner: %w", err)
		r.completeRunnerReservation(sessionID, reservation, nil, err)
		return nil, err
	}
	runner.hookCloser = hooks

	if !r.accepting() {
		runner.disposeBeforeStart()
		r.completeRunnerReservation(sessionID, reservation, nil, errRegistryShuttingDown)
		return nil, errRegistryShuttingDown
	}
	if journal != nil {
		if _, err := ensureForegroundRun(context.Background(), ledger, sessionID, modelID); err != nil {
			runner.disposeBeforeStart()
			r.completeRunnerReservation(sessionID, reservation, nil, err)
			return nil, err
		}
	}
	if hooks != nil {
		if err := hooks.Activate(); err != nil {
			runner.disposeBeforeStart()
			err = fmt.Errorf("activate plugin hooks: %w", err)
			r.completeRunnerReservation(sessionID, reservation, nil, err)
			return nil, err
		}
	}
	if err := validateHeadlessExecutionState(journal, sessionID); err != nil {
		runner.disposeBeforeStart()
		r.completeRunnerReservation(sessionID, reservation, nil, err)
		return nil, err
	}

	r.mu.Lock()
	if r.lifecycle != registryAccepting {
		r.completeRunnerReservationLocked(sessionID, reservation, nil, errRegistryShuttingDown)
		r.mu.Unlock()
		runner.disposeBeforeStart()
		return nil, errRegistryShuttingDown
	}
	if existing, ok := r.runners[sessionID]; ok && existing != nil {
		r.completeRunnerReservationLocked(sessionID, reservation, existing, nil)
		r.mu.Unlock()
		runner.disposeBeforeStart()
		return existing, nil
	}
	r.runners[sessionID] = runner
	err = r.activateRunner(runner)
	if err != nil {
		delete(r.runners, sessionID)
		err = fmt.Errorf("activate runner: %w", err)
		r.completeRunnerReservationLocked(sessionID, reservation, nil, err)
	} else {
		r.completeRunnerReservationLocked(sessionID, reservation, runner, nil)
	}
	r.mu.Unlock()
	if err != nil {
		runner.disposeBeforeStart()
		return nil, err
	}
	if journal != nil {
		runner.wakeDurableLanes()
	}

	return runner, nil
}

func (r *Registry) buildToolRegistry(sessionID string, project string, ledger runledger.Store, evidenceStore evidence.Store) (*tool.Registry, configuredHookPlan, error) {
	tools := tool.NewRegistry()
	tool.ApplyToolMiddlewareConfig(tools, r.config)
	if r.config == nil || r.config.ToolMiddleware.MaxResultBytes <= 0 {
		tools.SetMaxOutputBytes(defaultHeadlessMaxOutputBytes)
	}
	if strings.TrimSpace(project) != "" && r.config != nil {
		tools.ConfigureContainers(r.config, project)
	}
	if r.store != nil {
		tools.SetTodoStore(&todoStoreAdapter{store: r.store})
		tools.EnableCodeIndex(r.store)
	}
	// Runner owns the durable approval gate for headless sessions. Installing
	// the legacy Mission middleware here would create a second, client-invisible
	// approval after Runner has already approved the same tool call.
	if err := tools.LoadDefaultPlugins(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to load some plugins: %v\n", err)
	}
	if strings.TrimSpace(sessionID) != "" {
		// Apply session lineage after plugin registration so a plugin cannot
		// replace the configured builtin with an unscoped spawn tool.
		tools.EnableTelemetry(r.telemetry, sessionID)
	}
	hooksEnabled := false
	hooksTimeout := time.Duration(0)
	if r.config != nil {
		hooksEnabled = r.config.Hooks.Enabled
		hooksTimeout = time.Duration(r.config.Hooks.DefaultTimeoutMs) * time.Millisecond
	}
	if strings.TrimSpace(project) != "" {
		tools.SetWorkDir(project)
	}
	tools.EnableDynamicDiscovery(nil)
	if err := configureSubagentDurability(tools, ledger, evidenceStore); err != nil {
		return nil, nil, err
	}
	prepareHooks := r.prepareHooks
	if prepareHooks == nil {
		prepareHooks = prepareConfiguredHooks
	}
	hooks, err := prepareHooks(tools, hooksEnabled, hooksTimeout)
	if err != nil {
		return nil, nil, fmt.Errorf("prepare plugin hooks: %w", err)
	}
	return tools, hooks, nil
}

func prepareConfiguredHooks(tools *tool.Registry, enabled bool, timeout time.Duration) (configuredHookPlan, error) {
	if tools == nil {
		return nil, fmt.Errorf("tool registry unavailable")
	}
	return tools.PrepareConfiguredHooks(enabled, timeout)
}

func configureSubagentDurability(tools *tool.Registry, ledger runledger.Store, store evidence.Store) error {
	if (ledger == nil) != (store == nil) {
		return fmt.Errorf("headless durability requires both run ledger and evidence stores")
	}
	if ledger == nil {
		return nil
	}
	if tools == nil {
		return fmt.Errorf("configure headless durability: tool registry unavailable")
	}
	candidate, ok := tools.Get("spawn_subagent")
	if !ok {
		return fmt.Errorf("configure headless durability: spawn_subagent tool unavailable")
	}
	subagents, ok := candidate.(*builtin.SubagentTool)
	if !ok {
		return fmt.Errorf("configure headless durability: spawn_subagent tool has unexpected type %T", candidate)
	}
	subagents.SetDurability(ledger, store)
	return nil
}

func applyToolPolicy(registry *tool.Registry, policy *ToolPolicy) {
	if registry == nil || policy == nil {
		return
	}

	allowed := make(map[string]struct{})
	for _, name := range policy.AllowedTools {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		allowed[name] = struct{}{}
	}

	denied := make(map[string]struct{})
	for _, name := range policy.DeniedTools {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		denied[name] = struct{}{}
	}

	if policy.AllowedTools == nil && len(denied) == 0 {
		return
	}

	registry.Filter(func(t tool.Tool) bool {
		if t == nil {
			return false
		}
		name := strings.TrimSpace(t.Name())
		if name == "" {
			return false
		}
		if _, ok := denied[name]; ok {
			return false
		}
		if policy.AllowedTools != nil {
			_, ok := allowed[name]
			return ok
		}
		return true
	})
}

// GetSession returns a runner by session ID.
func (r *Registry) GetSession(sessionID string) (*Runner, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.lifecycle != registryAccepting {
		return nil, false
	}
	runner, ok := r.runners[sessionID]
	return runner, ok
}

// GetSessionInfo returns session info by ID.
func (r *Registry) GetSessionInfo(sessionID string) (*SessionInfo, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	runner, ok := r.runners[sessionID]
	if !ok {
		return nil, false
	}

	return &SessionInfo{
		ID:           sessionID,
		Project:      runnerProjectPath(runner),
		Branch:       strings.TrimSpace(runner.session.GitBranch),
		Model:        runner.Model(),
		State:        runner.State(),
		CreatedAt:    runner.session.CreatedAt,
		LastActive:   runner.LastActive(),
		WebSocketURL: fmt.Sprintf("/ws?session=%s", sessionID),
	}, true
}

// ListSessions returns info about all active headless sessions.
func (r *Registry) ListSessions() []SessionInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	sessions := make([]SessionInfo, 0, len(r.runners))
	for id, runner := range r.runners {
		sessions = append(sessions, SessionInfo{
			ID:           id,
			Project:      runnerProjectPath(runner),
			Branch:       strings.TrimSpace(runner.session.GitBranch),
			Model:        runner.Model(),
			State:        runner.State(),
			CreatedAt:    runner.session.CreatedAt,
			LastActive:   runner.LastActive(),
			WebSocketURL: fmt.Sprintf("/ws?session=%s", id),
		})
	}
	return sessions
}

// RemoveSession stops and removes a session.
func (r *Registry) RemoveSession(sessionID string) error {
	_, err := r.removeSessionQuiesced(sessionID, sessionexec.ExecutionModeDetached, "session_detached")
	return err
}

// RemoveSessionWithCleanup stops and removes a session, optionally deleting its managed workspace.
func (r *Registry) RemoveSessionWithCleanup(sessionID string, cleanupWorkspace bool) error {
	if !cleanupWorkspace {
		return r.RemoveSession(sessionID)
	}
	sess, err := r.removeSessionQuiesced(sessionID, sessionexec.ExecutionModeDetached, "session_detached")
	if err != nil {
		return err
	}
	if sess == nil {
		return nil
	}
	return r.cleanupWorkspace(sess)
}

func (r *Registry) removeSessionQuiesced(sessionID string, mode sessionexec.ExecutionMode, reasonCode string) (*storage.Session, error) {
	if r == nil {
		return nil, fmt.Errorf("registry unavailable")
	}
	if r.store == nil {
		return nil, fmt.Errorf("storage unavailable")
	}
	sess, err := r.store.GetSession(sessionID)
	if err != nil {
		return nil, fmt.Errorf("load session: %w", err)
	}
	if sess == nil {
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}
	r.mu.RLock()
	runner := r.runners[sessionID]
	journal := r.journal
	r.mu.RUnlock()
	if journal == nil && runner == nil {
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}
	if journal != nil {
		timeout := defaultDurableJournalOperationTTL
		if runner != nil && runner.durableTiming.OperationTimeout > 0 {
			timeout = runner.durableTiming.OperationTimeout
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		_, err := journal.QuiesceSession(ctx, sessionID, mode, reasonCode)
		cancel()
		if err != nil {
			return nil, fmt.Errorf("quiesce headless session: %w", err)
		}
	}
	if runner != nil {
		r.mu.Lock()
		if r.runners[sessionID] == runner {
			delete(r.runners, sessionID)
		}
		r.mu.Unlock()
		runner.Stop()
	}
	return sess, nil
}

// DispatchCommand dispatches a command to a session.
func (r *Registry) DispatchCommand(cmd command.SessionCommand) error {
	_, err := r.AcceptCommand(context.Background(), cmd)
	return err
}

// AcceptCommand durably accepts a command before returning when the target
// runner has foreground durability enabled. Legacy runners retain their
// existing in-memory queue behavior.
func (r *Registry) AcceptCommand(ctx context.Context, cmd command.SessionCommand) (sessionexec.Receipt, error) {
	if r == nil {
		return sessionexec.Receipt{}, fmt.Errorf("headless registry unavailable")
	}
	if strings.TrimSpace(cmd.SessionID) == "" {
		return sessionexec.Receipt{}, fmt.Errorf("session ID required")
	}
	runner, ok := r.GetSession(cmd.SessionID)
	if !ok || runner == nil {
		var err error
		runner, err = r.EnsureSession(cmd.SessionID)
		if err != nil {
			return sessionexec.Receipt{}, err
		}
	}
	return runner.AcceptCommand(ctx, cmd)
}

// AdoptSession allows a TUI to take over a headless session.
// Returns the session data for the TUI to continue with.
func (r *Registry) AdoptSession(sessionID string) (*storage.Session, error) {
	sess, err := r.removeSessionQuiesced(sessionID, sessionexec.ExecutionModeAdopted, "session_adopted")
	if err != nil {
		return nil, err
	}
	return sess, nil
}

// Count returns the number of active headless sessions.
func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.runners)
}

func (r *Registry) cleanupLoop(ctx context.Context) {
	ticker := time.NewTicker(r.cleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-r.stopChan:
			return
		case <-ticker.C:
			r.cleanupIdleSessions()
		}
	}
}

func (r *Registry) cleanupIdleSessions() {
	r.mu.RLock()
	snapshot := make(map[string]*Runner, len(r.runners))
	for id, runner := range r.runners {
		snapshot[id] = runner
	}
	r.mu.RUnlock()

	candidates := make(map[string]*Runner)
	for id, runner := range snapshot {
		if runner.IsIdle() || runner.State() == StateStopped {
			candidates[id] = runner
		}
	}

	r.mu.Lock()
	toRemove := make([]*Runner, 0, len(candidates))
	for id, candidate := range candidates {
		if r.runners[id] == candidate {
			delete(r.runners, id)
			toRemove = append(toRemove, candidate)
		}
	}
	r.mu.Unlock()

	for _, runner := range toRemove {
		runner.Stop()
	}
}

func runnerProjectPath(runner *Runner) string {
	if runner == nil || runner.session == nil {
		return ""
	}
	project := strings.TrimSpace(runner.session.ProjectPath)
	if project != "" {
		return project
	}
	return strings.TrimSpace(runner.session.GitRepo)
}

func (r *Registry) cleanupWorkspace(sess *storage.Session) error {
	projectPath := strings.TrimSpace(sess.ProjectPath)
	gitRepo := strings.TrimSpace(sess.GitRepo)

	root := strings.TrimSpace(r.projectRoot)
	if root == "" {
		root = config.ResolveProjectRoot(r.config)
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve project root: %w", err)
	}
	rootAbs = filepath.Clean(rootAbs)

	if IsGitURL(gitRepo) {
		base := filepath.Join(rootAbs, ".buckley", "headless", "workspaces", sess.ID)
		base = filepath.Clean(base)
		if !isWithinDir(rootAbs, base) {
			return fmt.Errorf("refusing to cleanup workspace outside project root: %s", base)
		}
		if projectPath != "" && !isWithinDir(base, projectPath) {
			return nil
		}
		if err := os.RemoveAll(base); err != nil {
			return fmt.Errorf("cleanup workspace: %w", err)
		}
		return nil
	}

	if gitRepo == "" || projectPath == "" {
		return nil
	}

	expectedWorktree := filepath.Join(gitRepo, ".buckley", "worktrees", "headless", sess.ID)
	expectedWorktreeAbs, err := filepath.Abs(expectedWorktree)
	if err != nil {
		return fmt.Errorf("resolve worktree path: %w", err)
	}
	projectAbs, err := filepath.Abs(projectPath)
	if err != nil {
		return fmt.Errorf("resolve session project path: %w", err)
	}
	expectedWorktreeAbs = filepath.Clean(expectedWorktreeAbs)
	projectAbs = filepath.Clean(projectAbs)

	if expectedWorktreeAbs != projectAbs {
		return nil
	}

	_ = configureGitSafeDirectory(gitRepo)
	if err := runGit(gitRepo, "worktree", "remove", "--force", expectedWorktreeAbs); err != nil {
		return err
	}
	return nil
}

func (r *Registry) resolveProject(sessionID string, req CreateSessionRequest) (projectPath string, gitRepo string, gitBranch string, err error) {
	root := strings.TrimSpace(r.projectRoot)
	if root == "" {
		root = config.ResolveProjectRoot(r.config)
	}
	project := strings.TrimSpace(req.Project)
	if project == "" {
		project = root
	}
	branch := strings.TrimSpace(req.Branch)

	if IsGitURL(project) {
		policy := giturl.ClonePolicy{}
		if r.config != nil {
			policy = r.config.GitClone
		}
		if err := giturl.ValidateCloneURL(policy, project); err != nil {
			return "", "", "", fmt.Errorf("git clone blocked by policy: %w", err)
		}

		if root == "" {
			return "", "", "", fmt.Errorf("project root required to clone git URL")
		}
		rootAbs, err := filepath.Abs(root)
		if err != nil {
			return "", "", "", fmt.Errorf("resolve project root: %w", err)
		}
		workspace := filepath.Join(rootAbs, ".buckley", "headless", "workspaces", sessionID, "source")
		if err := os.MkdirAll(filepath.Dir(workspace), 0o755); err != nil {
			return "", "", "", fmt.Errorf("create workspace: %w", err)
		}
		if _, statErr := os.Stat(workspace); statErr == nil {
			if !isGitRepoDir(workspace) {
				return "", "", "", fmt.Errorf("workspace exists but is not a git repo: %s", workspace)
			}
		} else if !os.IsNotExist(statErr) {
			return "", "", "", fmt.Errorf("stat workspace: %w", statErr)
		} else {
			if err := cloneRepo(project, workspace); err != nil {
				return "", "", "", err
			}
		}
		if branch != "" {
			if err := checkoutBranch(workspace, branch); err != nil {
				return "", "", "", err
			}
			gitBranch = branch
		}
		_ = configureGitSafeDirectory(workspace)
		return workspace, project, gitBranch, nil
	}

	if root != "" && !filepath.IsAbs(project) {
		project = filepath.Join(root, project)
	}
	projectAbs, err := filepath.Abs(project)
	if err != nil {
		return "", "", "", fmt.Errorf("invalid project path: %w", err)
	}
	projectAbs = filepath.Clean(projectAbs)

	if root != "" {
		rootAbs, err := filepath.Abs(root)
		if err != nil {
			return "", "", "", fmt.Errorf("resolve project root: %w", err)
		}
		rootAbs = filepath.Clean(rootAbs)
		if !isWithinDir(rootAbs, projectAbs) {
			return "", "", "", fmt.Errorf("project path must be within %s", rootAbs)
		}
	}

	_ = configureGitSafeDirectory(projectAbs)

	gitRepo = projectAbs
	projectPath = projectAbs
	if branch != "" {
		if !isGitRepoDir(projectAbs) {
			return "", "", "", fmt.Errorf("project is not a git repository: %s", projectAbs)
		}
		worktreePath := filepath.Join(projectAbs, ".buckley", "worktrees", "headless", sessionID)
		if err := createWorktree(projectAbs, worktreePath, branch); err != nil {
			return "", "", "", err
		}
		projectPath = worktreePath
		gitBranch = branch
		_ = configureGitSafeDirectory(worktreePath)
	}

	return projectPath, gitRepo, gitBranch, nil
}

func isWithinDir(base, target string) bool {
	rel, err := filepath.Rel(filepath.Clean(base), filepath.Clean(target))
	if err != nil {
		return false
	}
	rel = filepath.Clean(rel)
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func isGitRepoDir(path string) bool {
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	cmd.Dir = path
	return cmd.Run() == nil
}

func cloneRepo(repoURL, destPath string) error {
	cmd := exec.Command("git", "clone", "--", repoURL, destPath)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git clone %s: %w\n%s", repoURL, err, strings.TrimSpace(string(output)))
	}
	return nil
}

func checkoutBranch(repoDir, branch string) error {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return nil
	}

	if gitRefExists(repoDir, "refs/heads/"+branch) {
		return runGit(repoDir, "checkout", branch)
	}
	if gitRefExists(repoDir, "refs/remotes/origin/"+branch) {
		if err := runGit(repoDir, "checkout", "-b", branch, "--track", "origin/"+branch); err == nil {
			return nil
		}
	}
	return runGit(repoDir, "checkout", "-b", branch)
}

func createWorktree(repoDir, worktreePath, branch string) error {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return nil
	}

	if _, err := os.Stat(worktreePath); err == nil {
		return fmt.Errorf("worktree path already exists: %s", worktreePath)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat worktree: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(worktreePath), 0o755); err != nil {
		return fmt.Errorf("create worktree dir: %w", err)
	}

	var args []string
	switch {
	case gitRefExists(repoDir, "refs/heads/"+branch):
		args = []string{"worktree", "add", worktreePath, branch}
	case gitRefExists(repoDir, "refs/remotes/origin/"+branch):
		args = []string{"worktree", "add", "--track", "-b", branch, worktreePath, "origin/" + branch}
	default:
		args = []string{"worktree", "add", "-b", branch, worktreePath, "HEAD"}
	}

	if err := runGit(repoDir, args...); err != nil && strings.Contains(err.Error(), "already checked out") {
		args = append([]string{"worktree", "add", "--force"}, args[2:]...)
		if retryErr := runGit(repoDir, args...); retryErr == nil {
			return nil
		}
		return err
	} else if err != nil {
		return err
	}

	return nil
}

func gitRefExists(repoDir, ref string) bool {
	cmd := exec.Command("git", "show-ref", "--verify", "--quiet", ref)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	cmd.Dir = repoDir
	return cmd.Run() == nil
}

func runGit(repoDir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	cmd.Dir = repoDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}

func configureGitSafeDirectory(repoRoot string) error {
	repoRoot = strings.TrimSpace(repoRoot)
	if repoRoot == "" {
		return nil
	}
	if !runningInContainer() {
		return nil
	}
	cmd := exec.Command("git", "config", "--global", "--add", "safe.directory", repoRoot)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	return cmd.Run()
}

func runningInContainer() bool {
	if strings.TrimSpace(os.Getenv("KUBERNETES_SERVICE_HOST")) != "" {
		return true
	}
	_, err := os.Stat("/.dockerenv")
	return err == nil
}
