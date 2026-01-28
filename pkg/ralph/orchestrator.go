// pkg/ralph/orchestrator.go
package ralph

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/odvcencio/buckley/pkg/conversation"
	"golang.org/x/sync/errgroup"
)

const (
	defaultWindowDuration  = time.Hour
	defaultErrorCooldown   = 2 * time.Minute
	defaultContextCooldown = 2 * time.Minute
)

// ModelContextProvider supplies context window sizes for models.
type ModelContextProvider interface {
	ContextLength(modelID string) int
}

// Orchestrator coordinates backend execution based on control configuration.
type Orchestrator struct {
	registry        *BackendRegistry
	config          *ControlConfig
	mu              sync.RWMutex
	currentBackend  int
	lastRotation    time.Time
	iteration       int
	backendStates   map[string]*backendState
	logger          *Logger
	contextProvider ModelContextProvider
	lastBackend     string

	// Context for "when" expression evaluation
	startTime     time.Time
	errorCount    int
	consecErrors  int
	totalCost     float64
	totalTokens   int
	lastCronCheck time.Time
}

type backendCandidate struct {
	name    string
	backend Backend
	config  BackendConfig
	state   *backendState
	model   string
}

// ScheduleAction represents an action to take based on schedule evaluation.
// These actions are triggered by schedule rules in the control configuration.
type ScheduleAction struct {
	// Action is the type of action: "rotate_backend", "next_backend", "pause",
	// "resume", "set_mode", or "set_backend".
	Action string
	// Mode is the new mode for "set_mode" action (sequential, parallel, round_robin).
	Mode string
	// Backend is the backend name for "set_backend" action.
	Backend string
	// Reason provides context for "pause" action.
	Reason string
}

// NewOrchestrator creates a new orchestrator with the given registry and config.
func NewOrchestrator(registry *BackendRegistry, config *ControlConfig) *Orchestrator {
	return &Orchestrator{
		registry:       registry,
		config:         config,
		currentBackend: 0,
		iteration:      0,
		startTime:      time.Now(),
		backendStates:  make(map[string]*backendState),
	}
}

// SetLogger attaches a logger for backend/model switch events.
func (o *Orchestrator) SetLogger(logger *Logger) {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.logger = logger
}

// SetContextProvider attaches a context window provider for model thresholds.
func (o *Orchestrator) SetContextProvider(provider ModelContextProvider) {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.contextProvider = provider
}

// Execute runs the prompt through backend(s) based on current mode.
func (o *Orchestrator) Execute(ctx context.Context, req BackendRequest) ([]*BackendResult, error) {
	if o == nil {
		return nil, fmt.Errorf("orchestrator is nil")
	}

	o.mu.RLock()
	config := o.config
	mode := ""
	rotation := RotationConfig{}
	if config != nil {
		mode = config.Mode
		rotation = config.Rotation
	}
	o.mu.RUnlock()

	candidates, nextAvailable := o.availableCandidates(req)
	if len(candidates) == 0 {
		if !nextAvailable.IsZero() {
			return nil, ErrAllBackendsParked{NextAvailable: nextAvailable}
		}
		return nil, fmt.Errorf("no available backends")
	}

	switch mode {
	case ModeParallel:
		return o.executeParallel(ctx, req, candidates)
	case ModeSequential, ModeRoundRobin, "":
		return o.executeSingle(ctx, req, candidates, rotation, mode)
	default:
		return nil, fmt.Errorf("unknown mode: %s", mode)
	}
}

func (o *Orchestrator) executeSingle(ctx context.Context, req BackendRequest, candidates []backendCandidate, rotation RotationConfig, mode string) ([]*BackendResult, error) {
	ordered := o.applyRotationOrder(candidates, rotation)
	if len(ordered) == 0 {
		return nil, fmt.Errorf("no available backends")
	}

	rotationMode := o.effectiveRotationMode(rotation, mode)
	startIdx := 0

	switch rotationMode {
	case RotationRoundRobin:
		o.mu.Lock()
		startIdx = o.currentBackend % len(ordered)
		o.currentBackend++
		o.mu.Unlock()
	case RotationTimeSliced:
		o.mu.Lock()
		now := time.Now()
		if o.lastRotation.IsZero() {
			o.lastRotation = now
		}
		if rotation.Interval > 0 && now.Sub(o.lastRotation) >= rotation.Interval {
			o.currentBackend = (o.currentBackend + 1) % len(ordered)
			o.lastRotation = now
		}
		startIdx = o.currentBackend % len(ordered)
		o.mu.Unlock()
	}

	ordered = rotateCandidates(ordered, startIdx)

	results := make([]*BackendResult, 0, len(ordered))

	for i, cand := range ordered {
		o.markBackendUse(cand.name, cand.model, rotationReason(rotationMode, i))
		result, execErr := o.executeCandidate(ctx, req, cand)
		results = append(results, result)
		rateLimited := o.recordBackendResult(cand, result)
		if execErr != nil {
			return results, execErr
		}
		if rateLimited {
			continue
		}
		return results, nil
	}

	nextAvailable := o.nextAvailableTime()
	if !nextAvailable.IsZero() {
		return results, ErrAllBackendsParked{NextAvailable: nextAvailable}
	}

	return results, fmt.Errorf("no available backends")
}

func (o *Orchestrator) executeParallel(ctx context.Context, req BackendRequest, candidates []backendCandidate) ([]*BackendResult, error) {
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no available backends")
	}

	var mu sync.Mutex
	results := make([]*BackendResult, 0, len(candidates))

	g, ctx := errgroup.WithContext(ctx)

	for _, c := range candidates {
		cand := c
		g.Go(func() error {
			result, _ := o.executeCandidate(ctx, req, cand)
			o.recordBackendResult(cand, result)

			mu.Lock()
			results = append(results, result)
			mu.Unlock()

			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return results, err
	}

	allFailed := true
	for _, r := range results {
		if r != nil && r.Error == nil {
			allFailed = false
			break
		}
	}

	if allFailed && len(results) > 0 {
		return results, fmt.Errorf("all backends failed")
	}

	return results, nil
}

func (o *Orchestrator) executeCandidate(ctx context.Context, req BackendRequest, cand backendCandidate) (*BackendResult, error) {
	backendReq := req
	backendReq.Model = cand.model

	result, err := cand.backend.Execute(ctx, backendReq)
	if err != nil && result == nil {
		result = &BackendResult{
			Backend: cand.name,
			Model:   backendReq.Model,
			Error:   err,
		}
	} else if err != nil && result != nil && result.Error == nil {
		result.Error = err
	}
	if result != nil && result.Model == "" {
		result.Model = backendReq.Model
	}
	return result, err
}

func (o *Orchestrator) availableCandidates(req BackendRequest) ([]backendCandidate, time.Time) {
	o.mu.RLock()
	config := o.config
	iteration := o.iteration
	elapsedMinutes := int(time.Since(o.startTime).Minutes())
	o.mu.RUnlock()

	if config == nil || config.Backends == nil {
		return nil, time.Time{}
	}

	promptTokens := promptTokenCount(req)
	activeOverrides := make(map[string]struct{})
	if len(config.Override.ActiveBackends) > 0 {
		for _, name := range config.Override.ActiveBackends {
			name = strings.TrimSpace(name)
			if name != "" {
				activeOverrides[name] = struct{}{}
			}
		}
	}

	now := time.Now()
	candidates := make([]backendCandidate, 0, len(config.Backends))
	var nextAvailable time.Time

	names := backendNamesInOrder(config)
	for _, name := range names {
		cfg := config.Backends[name]
		if len(activeOverrides) > 0 {
			if _, ok := activeOverrides[name]; !ok {
				continue
			}
		}

		state := o.ensureBackendState(name)
		if !cfg.Enabled {
			state.status = BackendDisabled
			continue
		}
		if state.status == BackendParked {
			if now.After(state.parkedUntil) {
				state.status = BackendActive
				state.parkedUntil = time.Time{}
			} else {
				nextAvailable = earliestTime(nextAvailable, state.parkedUntil)
				continue
			}
		}
		if state.status == BackendDisabled {
			continue
		}

		model := o.resolveModel(name, cfg, state, iteration, elapsedMinutes)
		if o.applyThresholds(now, cfg, state, promptTokens, model) {
			nextAvailable = earliestTime(nextAvailable, state.parkedUntil)
			continue
		}

		backend, ok := o.registry.Get(name)
		if !ok {
			continue
		}
		if !backend.Available() {
			continue
		}

		candidates = append(candidates, backendCandidate{
			name:    name,
			backend: backend,
			config:  cfg,
			state:   state,
			model:   model,
		})
	}

	return candidates, nextAvailable
}

func (o *Orchestrator) resolveModel(name string, cfg BackendConfig, state *backendState, iteration int, elapsedMinutes int) string {
	model := strings.TrimSpace(cfg.Models.Default)
	if model == "" {
		if value, ok := cfg.Options["model"]; ok {
			model = strings.TrimSpace(value)
		}
	}

	ctx := WhenContext{
		Iteration:      iteration,
		ErrorCount:     state.errorCount,
		ConsecErrors:   state.consecErrors,
		TotalCost:      state.totalCost,
		TotalTokens:    state.totalTokens,
		ElapsedMinutes: elapsedMinutes,
		HasError:       state.lastError != "",
	}

	for _, rule := range cfg.Models.Rules {
		if EvalWhen(rule.When, ctx) {
			model = rule.Model
			break
		}
	}

	return model
}

func (o *Orchestrator) applyThresholds(now time.Time, cfg BackendConfig, state *backendState, promptTokens int, model string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()

	thresholds := cfg.Thresholds
	if state.windowStart.IsZero() || now.Sub(state.windowStart) >= defaultWindowDuration || (!state.windowReset.IsZero() && now.After(state.windowReset)) {
		state.windowStart = now
		state.requestsCount = 0
		state.costInWindow = 0
		state.windowReset = time.Time{}
	}

	if thresholds.MaxRequestsPerWindow > 0 && state.requestsCount >= thresholds.MaxRequestsPerWindow {
		state.status = BackendParked
		state.parkedUntil = state.windowStart.Add(defaultWindowDuration)
		return true
	}

	if thresholds.MaxCostPerHour > 0 && state.costInWindow >= thresholds.MaxCostPerHour {
		state.status = BackendParked
		state.parkedUntil = state.windowStart.Add(defaultWindowDuration)
		return true
	}

	if thresholds.MaxConsecutiveErrors > 0 && state.consecErrors >= thresholds.MaxConsecutiveErrors {
		state.status = BackendParked
		state.parkedUntil = now.Add(defaultErrorCooldown)
		return true
	}

	if thresholds.MaxContextPct > 0 && promptTokens > 0 && model != "" {
		provider := o.contextProvider
		if provider != nil {
			contextLen := provider.ContextLength(model)
			if contextLen > 0 {
				usedPct := (promptTokens * 100) / contextLen
				if usedPct >= thresholds.MaxContextPct {
					state.status = BackendParked
					state.parkedUntil = now.Add(defaultContextCooldown)
					return true
				}
			}
		}
	}

	return false
}

func (o *Orchestrator) recordBackendResult(cand backendCandidate, result *BackendResult) bool {
	if result == nil {
		return false
	}

	cost := result.Cost
	if cost == 0 {
		cost = result.CostEstimate
	}

	now := time.Now()
	rateLimited := false
	rateInfo := ParseRateLimitResponse(result.Output, nil)
	if result.Error != nil {
		if errInfo := ParseRateLimitResponse(result.Error.Error(), nil); errInfo != nil {
			rateInfo = errInfo
		}
	}
	if rateInfo != nil {
		rateLimited = true
	}

	o.mu.Lock()
	state := o.ensureBackendStateLocked(cand.name)
	previousModel := state.lastModel

	if state.windowStart.IsZero() || now.Sub(state.windowStart) >= defaultWindowDuration || (!state.windowReset.IsZero() && now.After(state.windowReset)) {
		state.windowStart = now
		state.requestsCount = 0
		state.costInWindow = 0
		state.windowReset = time.Time{}
	}

	state.lastUsed = now
	state.lastModel = result.Model
	state.totalCost += cost
	state.totalTokens += result.TokensIn + result.TokensOut
	state.requestsCount++
	state.costInWindow += cost

	if result.Error != nil {
		state.errorCount++
		state.consecErrors++
		state.lastError = result.Error.Error()
		o.errorCount++
		o.consecErrors++
	} else {
		state.consecErrors = 0
		state.lastError = ""
		o.consecErrors = 0
	}

	o.totalCost += cost
	o.totalTokens += result.TokensIn + result.TokensOut

	logger := o.logger
	o.mu.Unlock()

	if logger != nil && previousModel != "" && result.Model != "" && previousModel != result.Model {
		logger.LogModelSwitch(cand.name, previousModel, result.Model, "rule")
	}

	if rateInfo != nil {
		o.parkBackend(cand.name, rateInfo)
	}

	return rateLimited
}

func (o *Orchestrator) parkBackend(name string, info *RateLimitInfo) {
	if info == nil {
		return
	}

	now := time.Now()
	parkUntil := time.Time{}
	if !info.WindowResets.IsZero() {
		parkUntil = info.WindowResets
	} else if info.RetryAfter > 0 {
		parkUntil = now.Add(info.RetryAfter)
	}
	if parkUntil.IsZero() {
		parkUntil = now.Add(defaultRateLimitBackoff)
	}

	o.mu.Lock()
	state := o.ensureBackendStateLocked(name)
	state.status = BackendParked
	state.parkedUntil = parkUntil
	state.windowReset = parkUntil
	o.mu.Unlock()
}

func (o *Orchestrator) effectiveRotationMode(rotation RotationConfig, mode string) string {
	if rotation.Mode != "" && rotation.Mode != RotationNone {
		return rotation.Mode
	}
	if mode == ModeRoundRobin {
		return RotationRoundRobin
	}
	return RotationNone
}

func (o *Orchestrator) applyRotationOrder(candidates []backendCandidate, rotation RotationConfig) []backendCandidate {
	if len(candidates) == 0 {
		return nil
	}
	if len(rotation.Order) == 0 {
		return candidates
	}

	order := make(map[string]int, len(rotation.Order))
	for i, name := range rotation.Order {
		name = strings.TrimSpace(name)
		if name != "" {
			order[name] = i
		}
	}

	ordered := make([]backendCandidate, 0, len(candidates))
	remaining := make([]backendCandidate, 0, len(candidates))

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].name < candidates[j].name
	})

	for _, cand := range candidates {
		if idx, ok := order[cand.name]; ok {
			for len(ordered) <= idx {
				ordered = append(ordered, backendCandidate{})
			}
			ordered[idx] = cand
		} else {
			remaining = append(remaining, cand)
		}
	}

	filtered := ordered[:0]
	for _, cand := range ordered {
		if cand.name != "" {
			filtered = append(filtered, cand)
		}
	}

	return append(filtered, remaining...)
}

func (o *Orchestrator) markBackendUse(name, model, reason string) {
	if name == "" {
		return
	}

	o.mu.Lock()
	previous := o.lastBackend
	logger := o.logger
	o.lastBackend = name
	o.mu.Unlock()

	if logger != nil && previous != "" && previous != name {
		logger.LogBackendSwitch(previous, name, reason)
	}
}

func (o *Orchestrator) ensureBackendState(name string) *backendState {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.ensureBackendStateLocked(name)
}

func (o *Orchestrator) ensureBackendStateLocked(name string) *backendState {
	if o.backendStates == nil {
		o.backendStates = make(map[string]*backendState)
	}
	state, ok := o.backendStates[name]
	if !ok {
		state = &backendState{status: BackendActive}
		o.backendStates[name] = state
	}
	return state
}

func (o *Orchestrator) nextAvailableTime() time.Time {
	o.mu.RLock()
	defer o.mu.RUnlock()
	var next time.Time
	for _, state := range o.backendStates {
		if state == nil {
			continue
		}
		if state.status == BackendParked {
			next = earliestTime(next, state.parkedUntil)
		}
	}
	return next
}

// UpdateConfig hot-reloads the configuration.
func (o *Orchestrator) UpdateConfig(config *ControlConfig) {
	if o == nil {
		return
	}

	o.mu.Lock()
	defer o.mu.Unlock()
	o.config = config
}

// Config returns the current control configuration.
func (o *Orchestrator) Config() *ControlConfig {
	if o == nil {
		return nil
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.config
}

// ContextProvider returns the configured model context provider.
func (o *Orchestrator) ContextProvider() ModelContextProvider {
	if o == nil {
		return nil
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.contextProvider
}

// EvaluateSchedule checks schedule rules and returns action to take (if any).
func (o *Orchestrator) EvaluateSchedule(lastError error) *ScheduleAction {
	if o == nil {
		return nil
	}

	o.mu.RLock()
	config := o.config
	iteration := o.iteration
	o.mu.RUnlock()

	if config == nil || len(config.Schedule) == 0 {
		return nil
	}

	for _, rule := range config.Schedule {
		if o.triggerMatches(rule.Trigger, iteration, lastError) {
			return &ScheduleAction{
				Action:  rule.Action,
				Mode:    rule.Mode,
				Backend: rule.Backend,
				Reason:  rule.Reason,
			}
		}
	}

	return nil
}

// triggerMatches checks if a trigger condition is satisfied.
func (o *Orchestrator) triggerMatches(trigger ScheduleTrigger, iteration int, lastError error) bool {
	// Check at_iteration trigger (exact match)
	if trigger.AtIteration > 0 {
		if iteration == trigger.AtIteration {
			return true
		}
	}

	// Check every_iterations trigger
	if trigger.EveryIterations > 0 {
		if iteration > 0 && iteration%trigger.EveryIterations == 0 {
			return true
		}
	}

	// Check on_error trigger
	if trigger.OnError != "" && lastError != nil {
		errMsg := strings.ToLower(lastError.Error())
		triggerPattern := strings.ToLower(trigger.OnError)
		if strings.Contains(errMsg, triggerPattern) {
			return true
		}
	}

	// Check "when" expressions
	if trigger.When != "" {
		o.mu.RLock()
		ctx := WhenContext{
			Iteration:      o.iteration,
			ErrorCount:     o.errorCount,
			ConsecErrors:   o.consecErrors,
			TotalCost:      o.totalCost,
			TotalTokens:    o.totalTokens,
			ElapsedMinutes: int(time.Since(o.startTime).Minutes()),
			HasError:       lastError != nil,
		}
		o.mu.RUnlock()

		if EvalWhen(trigger.When, ctx) {
			return true
		}
	}

	// Check cron expressions
	if trigger.Cron != "" {
		spec, err := ParseCron(trigger.Cron)
		if err == nil {
			now := time.Now()
			// Only trigger once per minute (check if we haven't checked in this minute)
			o.mu.Lock()
			shouldCheck := o.lastCronCheck.Minute() != now.Minute() ||
				o.lastCronCheck.Hour() != now.Hour() ||
				o.lastCronCheck.Day() != now.Day()
			if shouldCheck {
				o.lastCronCheck = now
			}
			o.mu.Unlock()

			if shouldCheck && spec.Matches(now) {
				return true
			}
		}
	}

	return false
}

// RecordError records an error for when-expression evaluation.
func (o *Orchestrator) RecordError(err error) {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if err != nil {
		o.errorCount++
		o.consecErrors++
	} else {
		o.consecErrors = 0 // Reset consecutive errors on success
	}
}

// RecordCost records cost and tokens for when-expression evaluation.
func (o *Orchestrator) RecordCost(cost float64, tokens int) {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.totalCost += cost
	o.totalTokens += tokens
}

// NextIteration increments the iteration counter.
func (o *Orchestrator) NextIteration() {
	if o == nil {
		return
	}

	o.mu.Lock()
	defer o.mu.Unlock()
	o.iteration++
}

func backendNamesInOrder(config *ControlConfig) []string {
	names := make([]string, 0, len(config.Backends))
	for name := range config.Backends {
		names = append(names, name)
	}
	sort.Strings(names)

	if len(config.Rotation.Order) == 0 {
		return names
	}

	ordered := make([]string, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, name := range config.Rotation.Order {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, ok := config.Backends[name]; !ok {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		ordered = append(ordered, name)
		seen[name] = struct{}{}
	}
	for _, name := range names {
		if _, ok := seen[name]; ok {
			continue
		}
		ordered = append(ordered, name)
	}
	return ordered
}

func rotateCandidates(candidates []backendCandidate, start int) []backendCandidate {
	if len(candidates) == 0 {
		return nil
	}
	start = start % len(candidates)
	out := make([]backendCandidate, 0, len(candidates))
	out = append(out, candidates[start:]...)
	out = append(out, candidates[:start]...)
	return out
}

func earliestTime(current time.Time, candidate time.Time) time.Time {
	if candidate.IsZero() {
		return current
	}
	if current.IsZero() || candidate.Before(current) {
		return candidate
	}
	return current
}

func rotationReason(mode string, attempt int) string {
	if attempt > 0 {
		return "rate_limit"
	}
	switch mode {
	case RotationRoundRobin, RotationTimeSliced:
		return "rotation"
	default:
		return "selection"
	}
}

func promptTokenCount(req BackendRequest) int {
	if req.Context != nil {
		if value, ok := req.Context["prompt_tokens"]; ok {
			switch v := value.(type) {
			case int:
				return v
			case int64:
				return int(v)
			case float64:
				return int(v)
			case string:
				if parsed, err := strconv.Atoi(v); err == nil {
					return parsed
				}
			}
		}
	}
	if req.Prompt == "" {
		return 0
	}
	return conversation.CountTokens(req.Prompt)
}
