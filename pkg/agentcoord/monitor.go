package agentcoord

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	DefaultRoutineStatusLimit       = 50
	MaxRoutineStatusLimit           = 100
	DefaultMailboxStatusLimit       = 25
	MaxMailboxStatusLimit           = 100
	MaxMailboxStatusStates          = 4
	MaxRoutineCursorBytes           = 512
	MaxMonitorIdentifierBytes       = 256
	MaxMonitorKindBytes             = 256
	MaxMonitorMailboxBytes          = 8 * 1024 * 1024
	MaxMonitorSequence        int64 = 1_000_000_000_000

	routineCursorVersion = "v1"
)

var (
	ErrMonitorValidation = errors.New("agentcoord: invalid monitor request")
	ErrMonitorIntegrity  = errors.New("agentcoord: monitor integrity violation")
	ErrMonitorCapacity   = errors.New("agentcoord: monitor materialization capacity exceeded")
	ErrMonitorConflict   = errors.New("agentcoord: monitor observation changed")
)

type AttemptState string

const (
	AttemptNone     AttemptState = "none"
	AttemptAttached AttemptState = "attached"
	AttemptExpired  AttemptState = "expired"
	AttemptDetached AttemptState = "detached"
)

func (s AttemptState) Valid() bool {
	switch s {
	case AttemptNone, AttemptAttached, AttemptExpired, AttemptDetached:
		return true
	default:
		return false
	}
}

type AttemptStatus struct {
	Number         int          `json:"number"`
	State          AttemptState `json:"state"`
	AttachedAt     *time.Time   `json:"attachedAt,omitempty"`
	HeartbeatAt    *time.Time   `json:"heartbeatAt,omitempty"`
	LeaseExpiresAt *time.Time   `json:"leaseExpiresAt,omitempty"`
	DetachedAt     *time.Time   `json:"detachedAt,omitempty"`
}

type MailboxSummary struct {
	Queued       int   `json:"queued"`
	Claimed      int   `json:"claimed"`
	Processed    int   `json:"processed"`
	DeadLetter   int   `json:"deadLetter"`
	LastSequence int64 `json:"lastSequence"`
}

type RoutineStatus struct {
	SessionID   string         `json:"sessionId"`
	RunID       string         `json:"runId"`
	ParentRunID string         `json:"parentRunId,omitempty"`
	TaskID      string         `json:"taskId,omitempty"`
	AgentID     string         `json:"agentId,omitempty"`
	ModelID     string         `json:"modelId,omitempty"`
	ProviderID  string         `json:"providerId,omitempty"`
	Backend     string         `json:"backend,omitempty"`
	State       RunState       `json:"state"`
	StartedAt   time.Time      `json:"startedAt"`
	FinishedAt  *time.Time     `json:"finishedAt,omitempty"`
	Attempt     AttemptStatus  `json:"attempt"`
	Mailbox     MailboxSummary `json:"mailbox"`
}

type RoutineQuery struct {
	SessionID   string `json:"sessionId"`
	ParentRunID string `json:"parentRunId,omitempty"`
	Before      string `json:"before,omitempty"`
	Limit       int    `json:"limit,omitempty"`
}

type RoutineStatusPage struct {
	Routines []RoutineStatus `json:"routines"`
	Next     string          `json:"next,omitempty"`
	HasMore  bool            `json:"hasMore"`
}

type MailboxState string

const (
	MailboxQueued     MailboxState = MessageQueued
	MailboxClaimed    MailboxState = MessageClaimed
	MailboxProcessed  MailboxState = MessageProcessed
	MailboxDeadLetter MailboxState = MessageDeadLetter
)

func (s MailboxState) Valid() bool {
	switch s {
	case MailboxQueued, MailboxClaimed, MailboxProcessed, MailboxDeadLetter:
		return true
	default:
		return false
	}
}

type MailboxDirection string

const (
	MailboxFromParent   MailboxDirection = "from_parent"
	MailboxFromChild    MailboxDirection = "from_child"
	MailboxFromOperator MailboxDirection = "from_operator"
)

func (d MailboxDirection) Valid() bool {
	switch d {
	case MailboxFromParent, MailboxFromChild, MailboxFromOperator:
		return true
	default:
		return false
	}
}

type MailboxStatus struct {
	SessionID      string           `json:"sessionId"`
	RunID          string           `json:"runId"`
	MessageID      string           `json:"messageId"`
	PeerRunID      string           `json:"peerRunId,omitempty"`
	Kind           string           `json:"kind"`
	Direction      MailboxDirection `json:"direction"`
	State          MailboxState     `json:"state"`
	Sequence       int64            `json:"sequence"`
	ByteCount      int64            `json:"byteCount"`
	CreatedAt      time.Time        `json:"createdAt"`
	ProcessedAt    *time.Time       `json:"processedAt,omitempty"`
	DeadLetteredAt *time.Time       `json:"deadLetteredAt,omitempty"`
}

type MailboxStatusQuery struct {
	SessionID     string         `json:"sessionId"`
	RunID         string         `json:"runId"`
	States        []MailboxState `json:"states,omitempty"`
	AfterSequence int64          `json:"afterSequence,omitempty"`
	Limit         int            `json:"limit,omitempty"`
}

type MailboxStatusPage struct {
	Messages []MailboxStatus `json:"messages"`
	Next     int64           `json:"next,omitempty"`
	HasMore  bool            `json:"hasMore"`
}

type MonitorReader interface {
	GetRoutineStatus(context.Context, string, string) (RoutineStatus, error)
	ListRoutineStatuses(context.Context, RoutineQuery) (RoutineStatusPage, error)
	ListMailboxStatuses(context.Context, MailboxStatusQuery) (MailboxStatusPage, error)
}

func ValidateMonitorIdentity(sessionID, runID string) error {
	if err := ValidateMonitorIdentifier("session id", sessionID, true); err != nil {
		return err
	}
	return ValidateMonitorIdentifier("run id", runID, true)
}

func NormalizeRoutineQuery(query RoutineQuery) (RoutineQuery, error) {
	if err := ValidateMonitorIdentifier("session id", query.SessionID, true); err != nil {
		return RoutineQuery{}, err
	}
	if err := ValidateMonitorIdentifier("parent run id", query.ParentRunID, false); err != nil {
		return RoutineQuery{}, err
	}
	if query.Before != "" {
		if _, _, err := DecodeRoutineCursor(query.Before); err != nil {
			return RoutineQuery{}, err
		}
	}
	if query.Limit == 0 {
		query.Limit = DefaultRoutineStatusLimit
	}
	if query.Limit < 1 || query.Limit > MaxRoutineStatusLimit {
		return RoutineQuery{}, fmt.Errorf("%w: routine limit out of range", ErrMonitorValidation)
	}
	return query, nil
}

func NormalizeMailboxStatusQuery(query MailboxStatusQuery) (MailboxStatusQuery, error) {
	if err := ValidateMonitorIdentifier("session id", query.SessionID, true); err != nil {
		return MailboxStatusQuery{}, err
	}
	if err := ValidateMonitorIdentifier("run id", query.RunID, true); err != nil {
		return MailboxStatusQuery{}, err
	}
	if query.AfterSequence < 0 || query.AfterSequence > MaxMonitorSequence {
		return MailboxStatusQuery{}, fmt.Errorf("%w: mailbox sequence out of range", ErrMonitorValidation)
	}
	if query.Limit == 0 {
		query.Limit = DefaultMailboxStatusLimit
	}
	if query.Limit < 1 || query.Limit > MaxMailboxStatusLimit {
		return MailboxStatusQuery{}, fmt.Errorf("%w: mailbox limit out of range", ErrMonitorValidation)
	}
	if len(query.States) > MaxMailboxStatusStates {
		return MailboxStatusQuery{}, fmt.Errorf("%w: too many mailbox states", ErrMonitorValidation)
	}
	states := make([]MailboxState, 0, len(query.States))
	seen := make(map[MailboxState]struct{}, len(query.States))
	for _, state := range query.States {
		if !state.Valid() {
			return MailboxStatusQuery{}, fmt.Errorf("%w: invalid mailbox state", ErrMonitorValidation)
		}
		if _, exists := seen[state]; exists {
			continue
		}
		seen[state] = struct{}{}
		states = append(states, state)
	}
	query.States = states
	return query, nil
}

func EncodeRoutineCursor(startedAt time.Time, runID string) (string, error) {
	if startedAt.IsZero() {
		return "", fmt.Errorf("%w: cursor timestamp is required", ErrMonitorValidation)
	}
	if err := ValidateMonitorIdentifier("run id", runID, true); err != nil {
		return "", err
	}
	payload := strings.Join([]string{routineCursorVersion, startedAt.UTC().Format(time.RFC3339Nano), runID}, "\x00")
	encoded := base64.RawURLEncoding.EncodeToString([]byte(payload))
	if len(encoded) > MaxRoutineCursorBytes {
		return "", fmt.Errorf("%w: cursor exceeds %d bytes", ErrMonitorValidation, MaxRoutineCursorBytes)
	}
	return encoded, nil
}

func DecodeRoutineCursor(cursor string) (time.Time, string, error) {
	if cursor == "" || len(cursor) > MaxRoutineCursorBytes || strings.TrimSpace(cursor) != cursor {
		return time.Time{}, "", fmt.Errorf("%w: malformed routine cursor", ErrMonitorValidation)
	}
	payload, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil || base64.RawURLEncoding.EncodeToString(payload) != cursor {
		return time.Time{}, "", fmt.Errorf("%w: malformed routine cursor", ErrMonitorValidation)
	}
	parts := strings.Split(string(payload), "\x00")
	if len(parts) != 3 || parts[0] != routineCursorVersion {
		return time.Time{}, "", fmt.Errorf("%w: unsupported routine cursor", ErrMonitorValidation)
	}
	startedAt, err := time.Parse(time.RFC3339Nano, parts[1])
	if err != nil || startedAt.Location() != time.UTC || startedAt.Format(time.RFC3339Nano) != parts[1] {
		return time.Time{}, "", fmt.Errorf("%w: malformed routine cursor timestamp", ErrMonitorValidation)
	}
	if err := ValidateMonitorIdentifier("run id", parts[2], true); err != nil {
		return time.Time{}, "", err
	}
	return startedAt, parts[2], nil
}

func ValidateRoutineStatus(status RoutineStatus) error {
	for name, value := range map[string]string{
		"session id": status.SessionID, "run id": status.RunID,
		"parent run id": status.ParentRunID, "task id": status.TaskID,
		"agent id": status.AgentID, "model id": status.ModelID,
		"provider id": status.ProviderID, "backend id": status.Backend,
	} {
		required := name == "session id" || name == "run id"
		if err := ValidateMonitorIdentifier(name, value, required); err != nil {
			return fmt.Errorf("%w: %v", ErrMonitorIntegrity, err)
		}
	}
	if status.StartedAt.IsZero() {
		return fmt.Errorf("%w: routine start timestamp is required", ErrMonitorIntegrity)
	}
	switch status.State {
	case RunQueued, RunRunning, RunResumable:
		if status.FinishedAt != nil {
			return fmt.Errorf("%w: nonterminal routine has a finish timestamp", ErrMonitorIntegrity)
		}
	case RunCompleted, RunFailed, RunCancelled, RunBlocked:
		if status.FinishedAt == nil || status.FinishedAt.Before(status.StartedAt) {
			return fmt.Errorf("%w: terminal routine finish timestamp is invalid", ErrMonitorIntegrity)
		}
	default:
		return fmt.Errorf("%w: unknown routine state", ErrMonitorIntegrity)
	}
	if err := ValidateAttemptStatus(status.Attempt); err != nil {
		return err
	}
	if err := ValidateMailboxSummary(status.Mailbox); err != nil {
		return err
	}
	return nil
}

func ValidateAttemptStatus(status AttemptStatus) error {
	if !status.State.Valid() || status.Number < 0 {
		return fmt.Errorf("%w: invalid attempt status", ErrMonitorIntegrity)
	}
	if status.State == AttemptNone {
		if status.Number != 0 || status.AttachedAt != nil || status.HeartbeatAt != nil ||
			status.LeaseExpiresAt != nil || status.DetachedAt != nil {
			return fmt.Errorf("%w: empty attempt has lifecycle data", ErrMonitorIntegrity)
		}
		return nil
	}
	if status.Number < 1 || status.AttachedAt == nil || status.HeartbeatAt == nil || status.LeaseExpiresAt == nil ||
		status.HeartbeatAt.Before(*status.AttachedAt) || status.LeaseExpiresAt.Before(*status.HeartbeatAt) {
		return fmt.Errorf("%w: attempt chronology is invalid", ErrMonitorIntegrity)
	}
	if status.State == AttemptDetached {
		if status.DetachedAt == nil || status.DetachedAt.Before(*status.AttachedAt) || status.DetachedAt.Before(*status.HeartbeatAt) {
			return fmt.Errorf("%w: detached attempt timestamp is invalid", ErrMonitorIntegrity)
		}
	} else if status.DetachedAt != nil {
		return fmt.Errorf("%w: nondetached attempt has a detached timestamp", ErrMonitorIntegrity)
	}
	return nil
}

func ValidateMailboxSummary(summary MailboxSummary) error {
	if summary.LastSequence < 0 || summary.LastSequence > MaxMonitorSequence {
		return fmt.Errorf("%w: mailbox summary sequence is invalid", ErrMonitorIntegrity)
	}
	total, ok := monitorCheckedCountSum(MaxMonitorSequence,
		summary.Queued, summary.Claimed, summary.Processed, summary.DeadLetter)
	if !ok {
		return fmt.Errorf("%w: mailbox summary count is invalid", ErrMonitorIntegrity)
	}
	if total != summary.LastSequence {
		return fmt.Errorf("%w: mailbox sequence history is incomplete", ErrMonitorIntegrity)
	}
	return nil
}

func monitorCheckedCountSum(max int64, values ...int) (int64, bool) {
	if max < 0 {
		return 0, false
	}
	var total int64
	for _, value := range values {
		if value < 0 {
			return 0, false
		}
		component := int64(value)
		if component > max || total > max-component {
			return 0, false
		}
		total += component
	}
	return total, true
}

func ValidateMailboxStatus(status MailboxStatus) error {
	for name, value := range map[string]string{
		"session id": status.SessionID, "run id": status.RunID,
		"message id": status.MessageID, "peer run id": status.PeerRunID,
	} {
		required := name != "peer run id" || status.Direction != MailboxFromOperator
		if err := ValidateMonitorIdentifier(name, value, required); err != nil {
			return fmt.Errorf("%w: %v", ErrMonitorIntegrity, err)
		}
	}
	if status.Kind == "" || len(status.Kind) > MaxMonitorKindBytes || !monitorSafeText(status.Kind) || strings.TrimSpace(status.Kind) != status.Kind {
		return fmt.Errorf("%w: mailbox kind is invalid", ErrMonitorIntegrity)
	}
	if !status.Direction.Valid() || !status.State.Valid() || status.Sequence < 1 || status.Sequence > MaxMonitorSequence ||
		status.ByteCount < 0 || status.ByteCount > MaxMonitorMailboxBytes || status.CreatedAt.IsZero() {
		return fmt.Errorf("%w: mailbox projection is invalid", ErrMonitorIntegrity)
	}
	if status.Direction == MailboxFromOperator {
		if status.PeerRunID != "" {
			return fmt.Errorf("%w: operator mailbox entry has a peer run", ErrMonitorIntegrity)
		}
	} else if status.PeerRunID == status.RunID {
		return fmt.Errorf("%w: mailbox peer equals target run", ErrMonitorIntegrity)
	}
	switch status.State {
	case MailboxProcessed:
		if status.ProcessedAt == nil || status.ProcessedAt.Before(status.CreatedAt) || status.DeadLetteredAt != nil {
			return fmt.Errorf("%w: processed mailbox timestamp is invalid", ErrMonitorIntegrity)
		}
	case MailboxDeadLetter:
		if status.DeadLetteredAt == nil || status.DeadLetteredAt.Before(status.CreatedAt) || status.ProcessedAt != nil {
			return fmt.Errorf("%w: dead-letter mailbox timestamp is invalid", ErrMonitorIntegrity)
		}
	default:
		if status.ProcessedAt != nil || status.DeadLetteredAt != nil {
			return fmt.Errorf("%w: live mailbox entry has a terminal timestamp", ErrMonitorIntegrity)
		}
	}
	return nil
}

func ValidateMonitorIdentifier(name, value string, required bool) error {
	if required && value == "" {
		return fmt.Errorf("%w: %s is required", ErrMonitorValidation, name)
	}
	if value == "" {
		return nil
	}
	if strings.TrimSpace(value) != value || len(value) > MaxMonitorIdentifierBytes || !monitorSafeText(value) {
		return fmt.Errorf("%w: %s is malformed", ErrMonitorValidation, name)
	}
	return nil
}

func monitorSafeText(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
