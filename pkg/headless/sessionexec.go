package headless

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/conversation"
	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/ipc/command"
	"m31labs.dev/buckley/pkg/orchestrator"
	"m31labs.dev/buckley/pkg/runledger"
	"m31labs.dev/buckley/pkg/sessionexec"
	"m31labs.dev/buckley/pkg/storage"
	"m31labs.dev/buckley/pkg/telemetry"
)

const foregroundBackend = "headless-foreground"

const (
	defaultDurableLeaseDuration       = 30 * time.Second
	defaultDurableHeartbeatInterval   = 10 * time.Second
	defaultDurableScanInterval        = time.Second
	defaultDurableCancellationPoll    = 500 * time.Millisecond
	defaultDurableJournalOperationTTL = 5 * time.Second
)

// DurableTiming permits deterministic short intervals in tests. Zero fields
// select production defaults.
type DurableTiming struct {
	LeaseDuration        time.Duration
	HeartbeatInterval    time.Duration
	ScanInterval         time.Duration
	CancellationInterval time.Duration
	OperationTimeout     time.Duration
}

func normalizeDurableTiming(value *DurableTiming) DurableTiming {
	result := DurableTiming{
		LeaseDuration:        defaultDurableLeaseDuration,
		HeartbeatInterval:    defaultDurableHeartbeatInterval,
		ScanInterval:         defaultDurableScanInterval,
		CancellationInterval: defaultDurableCancellationPoll,
		OperationTimeout:     defaultDurableJournalOperationTTL,
	}
	if value == nil {
		return result
	}
	if value.LeaseDuration > 0 {
		result.LeaseDuration = value.LeaseDuration
	}
	if value.HeartbeatInterval > 0 {
		result.HeartbeatInterval = value.HeartbeatInterval
	}
	if value.ScanInterval > 0 {
		result.ScanInterval = value.ScanInterval
	}
	if value.CancellationInterval > 0 {
		result.CancellationInterval = value.CancellationInterval
	}
	if value.OperationTimeout > 0 {
		result.OperationTimeout = value.OperationTimeout
	}
	return result
}

func (r *Registry) resolveRunnerDurability(ledger runledger.Store, evidenceStore evidence.Store) (sessionexec.Journal, agentloop.DurableStepJournal, error) {
	if ledger == nil && evidenceStore == nil {
		return nil, nil, nil
	}
	if r == nil || r.store == nil {
		return nil, nil, fmt.Errorf("headless durable foreground execution requires storage")
	}
	journal := sessionexec.Journal(r.store)
	stepJournal, ok := ledger.(agentloop.DurableStepJournal)
	if !ok || isRegistryTypedNil(stepJournal) {
		return nil, nil, fmt.Errorf("headless durable foreground execution requires the current fenced step journal")
	}
	return journal, stepJournal, nil
}

func ensureForegroundRun(ctx context.Context, ledger runledger.Store, sessionID, modelID string) (runledger.AgentRun, error) {
	if ledger == nil {
		return runledger.AgentRun{}, fmt.Errorf("headless foreground run ledger unavailable")
	}
	want := runledger.AgentRun{
		RunID:     sessionexec.RunIDForSession(sessionID),
		SessionID: sessionID,
		TaskID:    sessionexec.ForegroundTaskID,
		AgentID:   "headless",
		ModelID:   strings.TrimSpace(modelID),
		Backend:   foregroundBackend,
		Status:    "running",
	}
	existing, err := ledger.GetRun(ctx, want.RunID)
	if err == nil {
		return validateForegroundRun(existing, want)
	}
	if !errors.Is(err, runledger.ErrNotFound) {
		return runledger.AgentRun{}, fmt.Errorf("read headless foreground run: %w", err)
	}
	started, startErr := ledger.StartRun(ctx, want)
	if startErr == nil {
		return validateForegroundRun(started, want)
	}
	// A concurrent process may have inserted the deterministic run after our
	// read. Reread on every insertion error and accept only the exact run.
	existing, getErr := ledger.GetRun(ctx, want.RunID)
	if getErr != nil {
		return runledger.AgentRun{}, fmt.Errorf("start headless foreground run: %w", startErr)
	}
	return validateForegroundRun(existing, want)
}

func validateForegroundRun(got, want runledger.AgentRun) (runledger.AgentRun, error) {
	if got.RunID != want.RunID || got.SessionID != want.SessionID ||
		got.TaskID != want.TaskID || got.AgentID != want.AgentID || got.Backend != want.Backend {
		return runledger.AgentRun{}, fmt.Errorf("headless foreground run identity mismatch")
	}
	if got.EndedAt != nil || strings.TrimSpace(got.Status) != "running" {
		return runledger.AgentRun{}, fmt.Errorf("headless foreground run is not live")
	}
	return got, nil
}

var (
	errDurableRunnerStopping  = errors.New("headless durable runner stopping")
	errDurableCancellation    = errors.New("headless durable command cancellation requested")
	errDurabilityNotSupported = errors.New("headless command is not replay-safe")
)

func (r *Runner) startDurablePumps() {
	if r == nil {
		return
	}
	r.durableWG.Add(2)
	go r.durablePump(sessionexec.LaneWork, r.durableWorkWake)
	go r.durablePump(sessionexec.LaneControl, r.durableControlWake)
	go func() {
		r.durableWG.Wait()
		close(r.commandStopped)
	}()
}

func (r *Runner) durablePump(lane sessionexec.Lane, wake <-chan struct{}) {
	defer r.durableWG.Done()
	timer := time.NewTimer(r.durableTiming.ScanInterval)
	defer timer.Stop()
	for {
		select {
		case <-r.commandStop:
			return
		case <-wake:
		case <-timer.C:
		}
		if !r.drainDurableLane(lane) {
			return
		}
		timer.Reset(r.durableTiming.ScanInterval)
	}
}

func (r *Runner) drainDurableLane(lane sessionexec.Lane) bool {
	for {
		select {
		case <-r.commandStop:
			return false
		default:
		}
		if lane == sessionexec.LaneWork && r.durableWorkClaimsPaused() {
			return true
		}
		ctx, cancel := context.WithTimeout(context.Background(), r.durableTiming.OperationTimeout)
		claimed, err := r.commandJournal.ClaimNext(ctx, sessionexec.ClaimRequest{
			SessionID: r.sessionID, Lane: lane, Owner: r.leaseOwner,
			LeaseDuration: r.durableTiming.LeaseDuration,
		})
		cancel()
		if errors.Is(err, sessionexec.ErrNotFound) {
			return true
		}
		if err != nil {
			r.emitDurableJournalError("claim command", err)
			return true
		}
		r.executeDurableClaim(claimed)
	}
}

func (r *Runner) durableWorkClaimsPaused() bool {
	if r == nil || r.store == nil {
		return true
	}
	sess, err := r.store.GetSession(r.sessionID)
	if err != nil || sess == nil {
		if err != nil {
			r.emitDurableJournalError("read session claim state", err)
		}
		return true
	}
	return sess.Status == storage.SessionStatusPaused || sess.Status == storage.SessionStatusCompleted
}

type durableClaimMonitor struct {
	stop chan struct{}
	done chan struct{}
	err  chan error
}

func (r *Runner) startDurableClaimMonitor(cancel context.CancelFunc, command sessionexec.Command) *durableClaimMonitor {
	monitor := &durableClaimMonitor{
		stop: make(chan struct{}), done: make(chan struct{}), err: make(chan error, 1),
	}
	go func() {
		defer close(monitor.done)
		heartbeat := time.NewTicker(r.durableTiming.HeartbeatInterval)
		defer heartbeat.Stop()
		cancellation := time.NewTicker(r.durableTiming.CancellationInterval)
		defer cancellation.Stop()
		signal := func(err error) {
			select {
			case monitor.err <- err:
			default:
			}
			cancel()
		}
		for {
			select {
			case <-monitor.stop:
				return
			case <-r.commandStop:
				signal(errDurableRunnerStopping)
				return
			case <-heartbeat.C:
				ctx, done := context.WithTimeout(context.Background(), r.durableTiming.OperationTimeout)
				_, err := r.commandJournal.Heartbeat(ctx, command.Lease, r.durableTiming.LeaseDuration)
				done()
				if err != nil {
					signal(err)
					return
				}
			case <-cancellation.C:
				ctx, done := context.WithTimeout(context.Background(), r.durableTiming.OperationTimeout)
				execution, err := r.commandJournal.GetExecutionState(ctx, command.SessionID)
				if err == nil && execution.Mode != sessionexec.ExecutionModeHeadless {
					done()
					signal(errDurableCancellation)
					return
				}
				if err != nil {
					done()
					signal(err)
					return
				}
				requested, err := r.commandJournal.CancellationRequested(ctx, command.SessionID, command.CommandID)
				done()
				if err != nil {
					signal(err)
					return
				}
				if requested {
					signal(errDurableCancellation)
					return
				}
			}
		}
	}()
	return monitor
}

func stopDurableClaimMonitor(monitor *durableClaimMonitor) error {
	if monitor == nil {
		return nil
	}
	close(monitor.stop)
	<-monitor.done
	select {
	case err := <-monitor.err:
		return err
	default:
		return nil
	}
}

func (r *Runner) executeDurableClaim(command sessionexec.Command) {
	ctx, cancel := context.WithCancel(context.Background())
	work := command.Lane == sessionexec.LaneWork
	if work {
		r.mu.Lock()
		r.activeCommandID = command.CommandID
		r.cancelFunc = cancel
		r.lastActive = time.Now()
		r.mu.Unlock()
		r.setState(StateProcessing)
	}
	eventCommand := commandForDurableEvent(command)
	r.emitCommandEvent(EventCommandStarted, eventCommand, nil)
	monitor := r.startDurableClaimMonitor(cancel, command)

	var executeErr error
	transcriptReady := !work
	if work {
		executeErr = r.beginDurableTranscript(command)
		transcriptReady = executeErr == nil
	}
	if executeErr == nil {
		executeErr = r.handleDurableCommand(ctx, command)
	}
	monitorErr := stopDurableClaimMonitor(monitor)
	cancel()

	interrupted := false
	if work {
		r.mu.Lock()
		_, interrupted = r.interruptedCommands[command.CommandID]
		delete(r.interruptedCommands, command.CommandID)
		r.mu.Unlock()
	}
	entries, transcriptErr := r.finishDurableTranscript(work && transcriptReady)
	defer r.clearDurableActive(command, work)

	if errors.Is(monitorErr, errDurableRunnerStopping) || r.durableStopping() {
		r.releaseDurableLease(command.Lease)
		return
	}
	if monitorErr != nil && !errors.Is(monitorErr, errDurableCancellation) {
		if errors.Is(monitorErr, sessionexec.ErrEffectAmbiguous) {
			r.emitCanonicalAmbiguousEffect(command)
			return
		}
		if !errors.Is(monitorErr, sessionexec.ErrLeaseStale) && !errors.Is(monitorErr, sessionexec.ErrLeaseExpired) {
			r.releaseDurableLease(command.Lease)
		}
		return
	}
	if work && !transcriptReady && !durableTranscriptIntegrityError(executeErr) {
		r.releaseDurableLease(command.Lease)
		return
	}
	if errors.Is(executeErr, sessionexec.ErrEffectAmbiguous) {
		r.emitCanonicalAmbiguousEffect(command)
		return
	}
	completion := sessionexec.Completion{State: sessionexec.StateSucceeded}
	if transcriptErr != nil {
		completion.State = sessionexec.StateBlocked
		completion.ErrorCode = "transcript_encoding"
		completion.Error = telemetry.SanitizeText(transcriptErr.Error(), sessionexec.MaxErrorTextBytes)
		entries = nil
	} else if errors.Is(monitorErr, errDurableCancellation) || interrupted || errors.Is(executeErr, context.Canceled) {
		completion.State = sessionexec.StateInterrupted
		completion.ErrorCode = "interrupted"
	} else if executeErr != nil {
		completion.State = sessionexec.StateFailed
		completion.ErrorCode = "command_failed"
		if errors.Is(executeErr, errDurabilityNotSupported) {
			completion.State = sessionexec.StateBlocked
			completion.ErrorCode = "durability_not_supported"
		} else if errors.Is(executeErr, runledger.ErrStepRecoveryRequired) {
			completion.State = sessionexec.StateBlocked
			completion.ErrorCode = "durable_recovery_required"
		} else if errors.Is(executeErr, sessionexec.ErrEffectAmbiguous) {
			completion.State = sessionexec.StateBlocked
			completion.ErrorCode = "ambiguous_effect"
		} else if durableTranscriptIntegrityError(executeErr) {
			completion.State = sessionexec.StateBlocked
			completion.ErrorCode = "transcript_integrity"
		}
		completion.Error = telemetry.SanitizeText(executeErr.Error(), sessionexec.MaxErrorTextBytes)
	}
	ctxComplete, done := context.WithTimeout(context.Background(), r.durableTiming.OperationTimeout)
	receipt, err := r.commandJournal.Complete(ctxComplete, command.Lease, completion, entries)
	done()
	if err != nil {
		if !errors.Is(err, sessionexec.ErrLeaseStale) && !errors.Is(err, sessionexec.ErrLeaseExpired) {
			r.releaseDurableLease(command.Lease)
		}
		return
	}
	r.emitDurableTerminalEvent(eventCommand, receipt, executeErr)
	if command.Type == "resume" {
		r.wakeDurableLane(sessionexec.LaneWork)
	}
}

func (r *Runner) clearDurableActive(command sessionexec.Command, work bool) {
	if !work {
		return
	}
	r.mu.Lock()
	if r.activeCommandID == command.CommandID {
		r.activeCommandID = ""
		r.cancelFunc = nil
	}
	r.mu.Unlock()
	if r.State() == StateStopped || r.store == nil {
		return
	}
	sess, err := r.store.GetSession(r.sessionID)
	if err != nil || sess == nil {
		if err != nil {
			r.emitDurableJournalError("read session state after command", err)
		}
		return
	}
	if sess.Status == storage.SessionStatusPaused {
		r.setState(StatePaused)
		return
	}
	if sess.Status != storage.SessionStatusCompleted {
		r.setState(StateIdle)
	}
}

func (r *Runner) durableStopping() bool {
	select {
	case <-r.commandStop:
		return true
	default:
		return false
	}
}

func (r *Runner) releaseDurableLease(ref sessionexec.LeaseRef) {
	ctx, cancel := context.WithTimeout(context.Background(), r.durableTiming.OperationTimeout)
	_, _ = r.commandJournal.Release(ctx, ref)
	cancel()
}

func (r *Runner) emitDurableTerminalEvent(command command.SessionCommand, receipt sessionexec.Receipt, runErr error) {
	eventErr := runErr
	if strings.TrimSpace(receipt.Error) != "" {
		eventErr = errors.New(receipt.Error)
	}
	switch receipt.State {
	case sessionexec.StateSucceeded:
		r.emitCommandEvent(EventCommandCompleted, command, nil)
	case sessionexec.StateInterrupted, sessionexec.StateCancelled:
		r.emitCommandEvent(EventCommandInterrupted, command, nil)
	case sessionexec.StateBlocked:
		r.emitCommandEvent(EventCommandBlocked, command, eventErr)
	default:
		r.emitCommandEvent(EventCommandFailed, command, eventErr)
	}
}

func (r *Runner) emitDurableJournalError(operation string, err error) {
	if r == nil || err == nil {
		return
	}
	r.emit(RunnerEvent{
		Type: EventError, SessionID: r.sessionID, Timestamp: time.Now(),
		Data: map[string]any{
			"message": operation,
			"error":   telemetry.SanitizeText(err.Error(), sessionexec.MaxErrorTextBytes),
		},
	})
}

func commandForDurableEvent(value sessionexec.Command) command.SessionCommand {
	return command.SessionCommand{
		SessionID: value.SessionID, ID: value.CommandID, Type: value.Type,
	}
}

func (r *Runner) beginDurableTranscript(command sessionexec.Command) error {
	conv := conversation.New(command.SessionID)
	loader := r.transcriptLoader
	if loader == nil {
		loader = func(conv *conversation.Conversation, store *storage.Store) error {
			return conv.LoadFromStorage(store)
		}
	}
	if err := loader(conv, r.store); err != nil {
		return fmt.Errorf("reload durable conversation: %w", err)
	}
	if strings.TrimSpace(r.systemPrompt) != "" {
		hasPrompt := len(conv.Messages) > 0 && conv.Messages[0].Role == "system" &&
			conversation.GetContentAsString(conv.Messages[0].Content) == r.systemPrompt
		if !hasPrompt {
			system := conversation.New(command.SessionID)
			system.AddSystemMessage(r.systemPrompt)
			conv.Messages = append(system.Messages, conv.Messages...)
			conv.TokenCount += system.TokenCount
		}
	}
	r.mu.Lock()
	r.conv = conv
	r.durableBuffer = nil
	r.durableBufferNext = command.NextTranscriptOrdinal
	r.durableBufferErr = nil
	r.durableBuffering = true
	r.mu.Unlock()
	return nil
}

func durableTranscriptIntegrityError(err error) bool {
	return errors.Is(err, sessionexec.ErrTranscriptConflict) ||
		errors.Is(err, sessionexec.ErrIdempotencyConflict) ||
		errors.Is(err, sessionexec.ErrValidation)
}

func (r *Runner) finishDurableTranscript(enabled bool) ([]sessionexec.TranscriptEntry, error) {
	if !enabled {
		return nil, nil
	}
	r.mu.Lock()
	entries := append([]sessionexec.TranscriptEntry(nil), r.durableBuffer...)
	err := r.durableBufferErr
	r.durableBuffer = nil
	r.durableBufferErr = nil
	r.durableBuffering = false
	r.mu.Unlock()
	return entries, err
}

func (r *Runner) bufferDurableConversationMessage(message conversation.Message) (bool, error) {
	r.mu.RLock()
	buffering := r.durable && r.durableBuffering
	r.mu.RUnlock()
	if !buffering {
		return false, nil
	}
	stored, err := conversation.StorageMessage(r.sessionID, message)
	if err != nil {
		r.recordDurableBufferError(err)
		return true, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.durableBuffering {
		return true, fmt.Errorf("durable transcript buffer closed")
	}
	entry := sessionexec.TranscriptEntry{
		Ordinal:          r.durableBufferNext + len(r.durableBuffer),
		Role:             stored.Role,
		Content:          stored.Content,
		ContentJSON:      stored.ContentJSON,
		ContentType:      stored.ContentType,
		ToolCalls:        stored.ToolCalls,
		ToolCallID:       stored.ToolCallID,
		Name:             stored.Name,
		Reasoning:        stored.Reasoning,
		ReasoningDetails: stored.ReasoningDetails,
		Tokens:           int64(stored.Tokens),
		IsSummary:        stored.IsSummary,
		IsTruncated:      stored.IsTruncated,
	}
	candidate := append(append([]sessionexec.TranscriptEntry(nil), r.durableBuffer...), entry)
	canonical, err := sessionexec.ValidateTranscriptEntries(candidate, r.durableBufferNext)
	if err != nil {
		if r.durableBufferErr == nil {
			r.durableBufferErr = err
		}
		return true, err
	}
	r.durableBuffer = canonical
	return true, nil
}

func (r *Runner) recordDurableBufferError(err error) {
	if r == nil || err == nil {
		return
	}
	r.mu.Lock()
	if r.durableBufferErr == nil {
		r.durableBufferErr = err
	}
	r.mu.Unlock()
}

func (r *Runner) currentDurableBufferError() error {
	if r == nil || !r.durable {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.durableBufferErr
}

func (r *Runner) requireDurableExecutionEnabled(ctx context.Context) error {
	if r == nil || !r.durable {
		return nil
	}
	if r.commandJournal == nil {
		return fmt.Errorf("durable command journal unavailable")
	}
	checkCtx, cancel := context.WithTimeout(ctx, r.durableTiming.OperationTimeout)
	defer cancel()
	state, err := r.commandJournal.GetExecutionState(checkCtx, r.sessionID)
	if err != nil {
		return fmt.Errorf("read durable execution state: %w", err)
	}
	if state.Mode != sessionexec.ExecutionModeHeadless {
		return errDurableCancellation
	}
	return nil
}

func (r *Runner) beginDurableEffect(ctx context.Context, command sessionexec.Command, effectID string, kind sessionexec.EffectKind) (sessionexec.EffectPermit, error) {
	if r == nil || !r.durable || r.commandJournal == nil {
		return sessionexec.EffectPermit{}, fmt.Errorf("durable effect journal unavailable")
	}
	if command.SessionID != r.sessionID || command.Lease.SessionID != command.SessionID ||
		command.Lease.CommandID != command.CommandID || command.Lease.Generation != command.Generation {
		return sessionexec.EffectPermit{}, fmt.Errorf("durable effect command lease mismatch")
	}
	permitCtx, cancel := context.WithTimeout(ctx, r.durableTiming.OperationTimeout)
	permit, err := r.commandJournal.BeginEffect(permitCtx, sessionexec.EffectRequest{
		Lease: command.Lease, EffectID: effectID, Kind: kind,
	})
	cancel()
	if err != nil {
		return permit, err
	}
	if permit.Duplicate || permit.State != sessionexec.EffectStateActive {
		return permit, sessionexec.ErrEffectAmbiguous
	}
	r.mu.Lock()
	r.durableEffects++
	r.lastActive = time.Now()
	r.mu.Unlock()
	return permit, nil
}

func (r *Runner) emitCanonicalAmbiguousEffect(command sessionexec.Command) {
	if r == nil || r.commandJournal == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), r.durableTiming.OperationTimeout)
	receipt, err := r.commandJournal.Get(ctx, command.SessionID, command.CommandID)
	cancel()
	if err != nil || receipt.State != sessionexec.StateBlocked || receipt.ErrorCode != "ambiguous_effect" {
		return
	}
	r.emitDurableTerminalEvent(commandForDurableEvent(command), receipt, sessionexec.ErrEffectAmbiguous)
}

func (r *Runner) endDurableEffect(permit sessionexec.EffectPermit) error {
	if r == nil || r.commandJournal == nil {
		return fmt.Errorf("durable effect journal unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), r.durableTiming.OperationTimeout)
	err := r.commandJournal.EndEffect(ctx, permit)
	cancel()
	r.mu.Lock()
	if r.durableEffects > 0 {
		r.durableEffects--
	}
	r.lastActive = time.Now()
	r.mu.Unlock()
	return err
}

func (r *Runner) handleDurableCommand(ctx context.Context, command sessionexec.Command) error {
	switch command.Type {
	case "input", "queue", "steer":
		r.setState(StateProcessing)
		return r.runConversationLoopForCommand(ctx, &command)
	case "model":
		return r.setModel(command.Content)
	case "slash":
		return r.processDurableSlashCommand(ctx, command)
	case "interrupt":
		return nil
	case "approval":
		return r.processDurableApproval(command)
	case "pause":
		if err := r.store.SetSessionStatus(r.sessionID, storage.SessionStatusPaused); err != nil {
			return err
		}
		r.mu.Lock()
		r.session.Status = storage.SessionStatusPaused
		r.mu.Unlock()
		r.setState(StatePaused)
		return nil
	case "resume":
		if err := r.store.SetSessionStatus(r.sessionID, storage.SessionStatusActive); err != nil {
			return err
		}
		r.mu.Lock()
		r.session.Status = storage.SessionStatusActive
		r.mu.Unlock()
		r.setResumedState("")
		return nil
	default:
		return fmt.Errorf("unknown command type: %s", command.Type)
	}
}

func (r *Runner) processDurableSlashCommand(ctx context.Context, command sessionexec.Command) error {
	content := strings.TrimSpace(command.Content)
	fields := strings.Fields(content)
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return fmt.Errorf("invalid slash command")
	}
	name := strings.ToLower(strings.TrimPrefix(fields[0], "/"))
	args := fields[1:]
	if strings.Contains(name, "/") || strings.Contains(name, "\\") {
		r.conv.AddUserMessage(command.Content)
		r.persistLatestConversationMessage()
		return r.runConversationLoopForCommand(ctx, &command)
	}
	switch name {
	case "model":
		if len(args) != 1 {
			return fmt.Errorf("usage: /model <model-id>")
		}
		return r.setModel(args[0])
	case "status":
		return r.runDurableStatusCommand()
	case "plans":
		return r.runDurablePlansCommand()
	case "clear", "plan", "execute", "resume", "workflow":
		return fmt.Errorf("%w: /%s", errDurabilityNotSupported, name)
	default:
		return fmt.Errorf("unknown command: %s", name)
	}
}

func (r *Runner) runDurableStatusCommand() error {
	r.mu.RLock()
	ready := r.orchestrator != nil && r.workflow != nil
	r.mu.RUnlock()
	if !ready {
		return r.persistSystemMessage("No active plan. Use /plan to create one or /resume <plan-id> to load an existing plan.")
	}
	return r.runStatusCommand()
}

func (r *Runner) runDurablePlansCommand() error {
	cfg := resolveSessionConfig(r.config, r.session)
	plans, err := orchestrator.NewFilePlanStore(cfg.Artifacts.PlanningDir).ListPlans()
	if err != nil {
		return err
	}
	return r.persistPlanList(plans)
}

func (r *Runner) processDurableApproval(command sessionexec.Command) error {
	var response ApprovalResponse
	if err := json.Unmarshal([]byte(command.Content), &response); err != nil {
		return fmt.Errorf("decode approval decision: %w", err)
	}
	if strings.TrimSpace(response.ID) == "" {
		return fmt.Errorf("approval ID required")
	}
	status := "rejected"
	if response.Approved {
		status = "approved"
	}
	_, _, err := r.store.DecidePendingApproval(
		response.ID, r.sessionID, status, command.AcceptedBy,
		response.Reason, time.Now().UTC(),
	)
	if err != nil {
		return err
	}
	select {
	case r.approvalChan <- response:
	default:
	}
	return nil
}
