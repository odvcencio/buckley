package sessionexec

import (
	"context"
	"fmt"
	"time"
)

const (
	DefaultCommandStatusLimit = 50
	MaxCommandStatusLimit     = 100
	MaxRecentCommandStatuses  = 100
	MaxCommandStatusEffects   = 64
	MaxAttentionEffects       = 64
)

type EffectStatus struct {
	SessionID         string      `json:"sessionId"`
	CommandID         string      `json:"commandId"`
	CommandGeneration int         `json:"commandGeneration"`
	EffectID          string      `json:"effectId"`
	Kind              EffectKind  `json:"kind"`
	State             EffectState `json:"state"`
	CreatedAt         time.Time   `json:"createdAt"`
	ExpiresAt         time.Time   `json:"expiresAt"`
	AmbiguousAt       *time.Time  `json:"ambiguousAt,omitempty"`
	EndedAt           *time.Time  `json:"endedAt,omitempty"`
	ResolvedAt        *time.Time  `json:"resolvedAt,omitempty"`
}

type EffectSummary struct {
	Total     int `json:"total"`
	Active    int `json:"active"`
	Ambiguous int `json:"ambiguous"`
	Ended     int `json:"ended"`
	Resolved  int `json:"resolved"`
}

type CommandStatus struct {
	Identity
	Type             string         `json:"type"`
	Lane             Lane           `json:"lane"`
	State            State          `json:"state"`
	Attempt          int            `json:"attempt"`
	TargetCommandID  string         `json:"targetCommandId,omitempty"`
	AcceptedAt       time.Time      `json:"acceptedAt"`
	StartedAt        *time.Time     `json:"startedAt,omitempty"`
	FinishedAt       *time.Time     `json:"finishedAt,omitempty"`
	ErrorCode        string         `json:"errorCode,omitempty"`
	EffectSummary    EffectSummary  `json:"effectSummary"`
	Effects          []EffectStatus `json:"effects,omitempty"`
	EffectsTruncated bool           `json:"effectsTruncated"`
}

type CommandStatusQuery struct {
	SessionID     string  `json:"sessionId"`
	States        []State `json:"states,omitempty"`
	AfterSequence int64   `json:"afterSequence,omitempty"`
	Limit         int     `json:"limit,omitempty"`
}

type CommandStatusPage struct {
	Commands []CommandStatus `json:"commands"`
	Next     int64           `json:"next,omitempty"`
	HasMore  bool            `json:"hasMore"`
}

type ExecutionSnapshot struct {
	SessionID                 string          `json:"sessionId"`
	Initialized               bool            `json:"initialized"`
	ExecutionState            ExecutionState  `json:"executionState"`
	Summary                   Summary         `json:"summary"`
	EffectSummary             EffectSummary   `json:"effectSummary"`
	AttentionEffects          []EffectStatus  `json:"attentionEffects,omitempty"`
	AttentionEffectsTruncated bool            `json:"attentionEffectsTruncated"`
	RecentCommands            []CommandStatus `json:"recentCommands,omitempty"`
	ObservedAt                time.Time       `json:"observedAt"`
}

type MonitorReader interface {
	GetExecutionSnapshot(context.Context, string, int) (ExecutionSnapshot, error)
	ListCommandStatuses(context.Context, CommandStatusQuery) (CommandStatusPage, error)
	GetCommandStatus(context.Context, string, string) (CommandStatus, error)
}

func NormalizeCommandStatusQuery(query CommandStatusQuery) (CommandStatusQuery, error) {
	if err := ValidateSessionID(query.SessionID); err != nil {
		return CommandStatusQuery{}, err
	}
	if query.AfterSequence < 0 || query.AfterSequence > MaxCommandSequence {
		return CommandStatusQuery{}, fmt.Errorf("%w: after sequence out of range", ErrValidation)
	}
	if query.Limit == 0 {
		query.Limit = DefaultCommandStatusLimit
	}
	if query.Limit < 1 || query.Limit > MaxCommandStatusLimit {
		return CommandStatusQuery{}, fmt.Errorf("%w: command status limit out of range", ErrValidation)
	}
	if len(query.States) > 7 {
		return CommandStatusQuery{}, fmt.Errorf("%w: too many command status states", ErrValidation)
	}
	states := make([]State, 0, len(query.States))
	seen := make(map[State]struct{}, len(query.States))
	for _, state := range query.States {
		if !state.Valid() {
			return CommandStatusQuery{}, fmt.Errorf("%w: invalid command status state", ErrValidation)
		}
		if _, ok := seen[state]; ok {
			continue
		}
		seen[state] = struct{}{}
		states = append(states, state)
	}
	query.States = states
	return query, nil
}

func ValidateRecentCommandStatusesLimit(limit int) error {
	if limit < 0 || limit > MaxRecentCommandStatuses {
		return fmt.Errorf("%w: recent command status limit out of range", ErrValidation)
	}
	return nil
}
