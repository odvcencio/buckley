// pkg/ralph/state.go
package ralph

import (
	"fmt"
	"slices"
)

// State represents the current state of a Ralph session.
type State string

const (
	StateInit      State = "init"
	StateRefining  State = "refining"
	StateRunning   State = "running"
	StatePaused    State = "paused"
	StateCompleted State = "completed"
)

// validTransitions defines allowed state transitions.
var validTransitions = map[State][]State{
	StateInit:      {StateRefining, StateRunning},
	StateRefining:  {StateRunning, StateCompleted},
	StateRunning:   {StatePaused, StateCompleted},
	StatePaused:    {StateRunning, StateCompleted},
	StateCompleted: {}, // Terminal state
}

// CanTransitionTo checks if a transition from current to next is valid.
func (s State) CanTransitionTo(next State) bool {
	allowed, ok := validTransitions[s]
	if !ok {
		return false
	}
	return slices.Contains(allowed, next)
}

// String returns the state name.
func (s State) String() string {
	return string(s)
}

// ErrInvalidTransition is returned when a state transition is not allowed.
type ErrInvalidTransition struct {
	From State
	To   State
}

func (e ErrInvalidTransition) Error() string {
	return fmt.Sprintf("invalid state transition: %s -> %s", e.From, e.To)
}
