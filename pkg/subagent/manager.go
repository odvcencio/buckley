package subagent

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"

	"m31labs.dev/buckley/pkg/telemetry"
)

const (
	DefaultMaxConcurrent = 4
	maxCapturedOutput    = 256 * 1024
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
	Agent           string
	Spec            string
	Task            string
	TimeoutSeconds  int
}

type Runner interface {
	Run(ctx context.Context, request Request, started func(pid int)) (string, error)
}

type Snapshot struct {
	ID              string    `json:"id"`
	ParentSessionID string    `json:"parent_session_id,omitempty"`
	Agent           string    `json:"agent,omitempty"`
	Spec            string    `json:"spec,omitempty"`
	Task            string    `json:"task,omitempty"`
	State           State     `json:"state"`
	PID             int       `json:"pid,omitempty"`
	StartedAt       time.Time `json:"started_at"`
	FinishedAt      time.Time `json:"finished_at,omitempty"`
	Output          string    `json:"output,omitempty"`
	Error           string    `json:"error,omitempty"`
}

type run struct {
	snapshot Snapshot
	cancel   context.CancelFunc
	done     chan struct{}
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

func (m *Manager) Spawn(agent, spec, task string, timeoutSeconds int) (Snapshot, error) {
	if m == nil || m.runner == nil {
		return Snapshot{}, fmt.Errorf("subagent manager is unavailable")
	}
	task = strings.TrimSpace(task)
	if task == "" {
		return Snapshot{}, fmt.Errorf("subagent task is required")
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
	id := ulid.Make().String()
	ctx, cancel := context.WithCancel(context.Background())
	if timeoutSeconds > 0 {
		ctx, cancel = context.WithTimeout(context.Background(), time.Duration(timeoutSeconds)*time.Second)
	}
	current := &run{
		snapshot: Snapshot{
			ID:              id,
			ParentSessionID: m.parentSession,
			Agent:           strings.TrimSpace(agent),
			Spec:            strings.TrimSpace(spec),
			Task:            boundedTask(task),
			State:           StateRunning,
			StartedAt:       time.Now(),
		},
		cancel: cancel,
		done:   make(chan struct{}),
	}
	m.runs[id] = current
	snapshot := current.snapshot
	m.wg.Add(1)
	m.mu.Unlock()

	m.publish(telemetry.EventSubagentSpawned, snapshot, "")
	go m.run(ctx, current, Request{
		ID:              id,
		ParentSessionID: snapshot.ParentSessionID,
		Agent:           snapshot.Agent,
		Spec:            snapshot.Spec,
		Task:            task,
		TimeoutSeconds:  timeoutSeconds,
	})
	return snapshot, nil
}

func (m *Manager) run(ctx context.Context, current *run, request Request) {
	defer m.wg.Done()
	defer close(current.done)

	output, err := m.runner.Run(ctx, request, func(pid int) {
		m.mu.Lock()
		current.snapshot.PID = pid
		snapshot := current.snapshot
		m.mu.Unlock()
		m.publish(telemetry.EventSubagentState, snapshot, "")
	})

	m.mu.Lock()
	current.snapshot.FinishedAt = time.Now()
	current.snapshot.Output = boundedOutput(output)
	eventType := telemetry.EventSubagentCompleted
	switch {
	case ctx.Err() != nil:
		current.snapshot.State = StateCancelled
		current.snapshot.Error = ctx.Err().Error()
		eventType = telemetry.EventSubagentCancelled
	case err != nil:
		current.snapshot.State = StateFailed
		current.snapshot.Error = err.Error()
		eventType = telemetry.EventSubagentFailed
	default:
		current.snapshot.State = StateCompleted
	}
	snapshot := current.snapshot
	m.mu.Unlock()
	m.publish(eventType, snapshot, snapshot.Error)
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
		"state":             snapshot.State,
		"pid":               snapshot.PID,
		"provider":          "buckley",
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
