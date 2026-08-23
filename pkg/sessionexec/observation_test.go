package sessionexec

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestNormalizeCommandStatusQuery_DefaultsAndDeduplicatesWithoutMutatingInput(t *testing.T) {
	states := []State{StateRunning, StateSucceeded, StateRunning, StateFailed, StateSucceeded}
	query := CommandStatusQuery{
		SessionID: "session-01",
		States:    states,
	}
	original := append([]State(nil), states...)

	normalized, err := NormalizeCommandStatusQuery(query)
	if err != nil {
		t.Fatal(err)
	}
	if normalized.Limit != DefaultCommandStatusLimit {
		t.Fatalf("default limit = %d, want %d", normalized.Limit, DefaultCommandStatusLimit)
	}
	wantStates := []State{StateRunning, StateSucceeded, StateFailed}
	if !reflect.DeepEqual(normalized.States, wantStates) {
		t.Fatalf("normalized states = %#v, want %#v", normalized.States, wantStates)
	}
	if !reflect.DeepEqual(query.States, original) {
		t.Fatalf("input states mutated: %#v, want %#v", query.States, original)
	}

	normalized.States[0] = StateCancelled
	if query.States[0] != StateRunning {
		t.Fatalf("normalized states alias input states: %#v", query.States)
	}
}

func TestValidateEffectPermit_EnforcesTerminalChronology(t *testing.T) {
	created := time.Unix(1_700_000_000, 0).UTC()
	expires := created.Add(20 * time.Second)
	ambiguous := created.Add(5 * time.Second)
	ended := created.Add(10 * time.Second)
	resolved := expires
	base := EffectPermit{
		EffectRequest: EffectRequest{
			Lease: LeaseRef{
				SessionID: "session-01", CommandID: "command-01", Generation: 0,
				Owner: "worker-01", LeaseGeneration: 1, ExpiresAt: expires,
			},
			EffectID: "effect-01", Kind: EffectKindTool,
		},
		ExpiresAt: expires,
		CreatedAt: created,
	}
	tests := []struct {
		name    string
		permit  EffectPermit
		wantErr bool
	}{
		{name: "valid ambiguous then ended", permit: func() EffectPermit {
			permit := base
			permit.State = EffectStateEnded
			permit.AmbiguousAt = &ambiguous
			permit.EndedAt = &ended
			return permit
		}()},
		{name: "ended before ambiguity", wantErr: true, permit: func() EffectPermit {
			permit := base
			lateAmbiguity := created.Add(15 * time.Second)
			permit.State = EffectStateEnded
			permit.AmbiguousAt = &lateAmbiguity
			permit.EndedAt = &ended
			return permit
		}()},
		{name: "resolved before expiry", wantErr: true, permit: func() EffectPermit {
			permit := base
			permit.State = EffectStateResolved
			permit.AmbiguousAt = &ambiguous
			permit.ResolvedAt = &ended
			permit.ResolvedBy = "operator-01"
			permit.ResolutionReason = "confirmed"
			return permit
		}()},
		{name: "resolved at expiry", permit: func() EffectPermit {
			permit := base
			permit.State = EffectStateResolved
			permit.AmbiguousAt = &ambiguous
			permit.ResolvedAt = &resolved
			permit.ResolvedBy = "operator-01"
			permit.ResolutionReason = "confirmed"
			return permit
		}()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateEffectPermit(test.permit)
			if test.wantErr {
				if !errors.Is(err, ErrValidation) {
					t.Fatalf("error = %v, want ErrValidation", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNormalizeCommandStatusQuery_LimitBounds(t *testing.T) {
	tests := []struct {
		name    string
		limit   int
		want    int
		wantErr bool
	}{
		{name: "default", limit: 0, want: DefaultCommandStatusLimit},
		{name: "minimum", limit: 1, want: 1},
		{name: "maximum", limit: MaxCommandStatusLimit, want: MaxCommandStatusLimit},
		{name: "below minimum", limit: -1, wantErr: true},
		{name: "above maximum", limit: MaxCommandStatusLimit + 1, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeCommandStatusQuery(CommandStatusQuery{SessionID: "session-01", Limit: tt.limit})
			if tt.wantErr {
				if !errors.Is(err, ErrValidation) {
					t.Fatalf("error = %v, want ErrValidation", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Limit != tt.want {
				t.Fatalf("limit = %d, want %d", got.Limit, tt.want)
			}
		})
	}
}

func TestNormalizeCommandStatusQuery_AfterSequenceBounds(t *testing.T) {
	tests := []struct {
		name    string
		after   int64
		wantErr bool
	}{
		{name: "zero", after: 0},
		{name: "maximum", after: MaxCommandSequence},
		{name: "negative", after: -1, wantErr: true},
		{name: "above maximum", after: MaxCommandSequence + 1, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeCommandStatusQuery(CommandStatusQuery{SessionID: "session-01", AfterSequence: tt.after})
			if tt.wantErr {
				if !errors.Is(err, ErrValidation) {
					t.Fatalf("error = %v, want ErrValidation", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.AfterSequence != tt.after {
				t.Fatalf("after sequence = %d, want %d", got.AfterSequence, tt.after)
			}
		})
	}
}

func TestNormalizeCommandStatusQuery_RejectsInvalidIDsStatesAndStateCount(t *testing.T) {
	tooManyStates := make([]State, 8)
	for i := range tooManyStates {
		tooManyStates[i] = StateAccepted
	}
	tests := []struct {
		name  string
		query CommandStatusQuery
	}{
		{name: "missing session ID", query: CommandStatusQuery{}},
		{name: "malformed session ID", query: CommandStatusQuery{SessionID: " session-01"}},
		{name: "oversized session ID", query: CommandStatusQuery{SessionID: strings.Repeat("s", MaxSessionIDBytes+1)}},
		{name: "invalid state", query: CommandStatusQuery{SessionID: "session-01", States: []State{"unknown"}}},
		{name: "too many states", query: CommandStatusQuery{SessionID: "session-01", States: tooManyStates}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NormalizeCommandStatusQuery(tt.query); !errors.Is(err, ErrValidation) {
				t.Fatalf("error = %v, want ErrValidation", err)
			}
		})
	}
}

func TestNormalizeCommandStatusQuery_AllowsMaximumStateCount(t *testing.T) {
	states := []State{
		StateAccepted,
		StateRunning,
		StateSucceeded,
		StateFailed,
		StateBlocked,
		StateInterrupted,
		StateCancelled,
	}

	normalized, err := NormalizeCommandStatusQuery(CommandStatusQuery{SessionID: "session-01", States: states})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(normalized.States, states) {
		t.Fatalf("normalized states = %#v, want %#v", normalized.States, states)
	}
}

func TestValidateRecentCommandStatusesLimit_Bounds(t *testing.T) {
	tests := []struct {
		name    string
		limit   int
		wantErr bool
	}{
		{name: "zero", limit: 0},
		{name: "positive", limit: 1},
		{name: "maximum", limit: MaxRecentCommandStatuses},
		{name: "negative", limit: -1, wantErr: true},
		{name: "above maximum", limit: MaxRecentCommandStatuses + 1, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateRecentCommandStatusesLimit(tt.limit)
			if tt.wantErr {
				if !errors.Is(err, ErrValidation) {
					t.Fatalf("error = %v, want ErrValidation", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("error = %v, want nil", err)
			}
		})
	}
}
