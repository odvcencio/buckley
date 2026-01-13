// pkg/ralph/session.go
package ralph

import (
	"sync"
	"time"
)

// SessionConfig configures a new Ralph session.
type SessionConfig struct {
	SessionID     string
	Prompt        string
	PromptFile    string
	Sandbox       string
	Timeout       time.Duration
	MaxIterations int
	NoRefine      bool
}

// Session manages a Ralph autonomous execution session.
type Session struct {
	mu sync.RWMutex

	ID            string
	Prompt        string
	PromptFile    string
	Sandbox       string
	Timeout       time.Duration
	MaxIterations int
	NoRefine      bool

	state         State
	iteration     int
	startTime     time.Time
	totalTokens   int
	totalCost     float64
	filesModified []string
}

// NewSession creates a new Ralph session.
func NewSession(cfg SessionConfig) *Session {
	return &Session{
		ID:            cfg.SessionID,
		Prompt:        cfg.Prompt,
		PromptFile:    cfg.PromptFile,
		Sandbox:       cfg.Sandbox,
		Timeout:       cfg.Timeout,
		MaxIterations: cfg.MaxIterations,
		NoRefine:      cfg.NoRefine,
		state:         StateInit,
	}
}

// State returns the current session state.
func (s *Session) State() State {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state
}

// TransitionTo attempts to transition to a new state.
func (s *Session) TransitionTo(next State) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.state.CanTransitionTo(next) {
		return ErrInvalidTransition{From: s.state, To: next}
	}
	s.state = next
	return nil
}

// Iteration returns the current iteration number.
func (s *Session) Iteration() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.iteration
}

// IncrementIteration increments and returns the new iteration number.
func (s *Session) IncrementIteration() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.iteration++
	return s.iteration
}

// Stats returns current session statistics.
func (s *Session) Stats() SessionStats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return SessionStats{
		Iteration:     s.iteration,
		TotalTokens:   s.totalTokens,
		TotalCost:     s.totalCost,
		FilesModified: len(s.filesModified),
		Elapsed:       time.Since(s.startTime),
	}
}

// SessionStats contains session metrics.
type SessionStats struct {
	Iteration     int
	TotalTokens   int
	TotalCost     float64
	FilesModified int
	Elapsed       time.Duration
}

// Start marks the session as started.
func (s *Session) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.startTime = time.Now()
}

// AddTokens adds to the token count.
func (s *Session) AddTokens(tokens int, cost float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.totalTokens += tokens
	s.totalCost += cost
}

// AddModifiedFile records a file modification.
func (s *Session) AddModifiedFile(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.filesModified = append(s.filesModified, path)
}

// GetPrompt returns the current prompt.
func (s *Session) GetPrompt() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Prompt
}

// SetPrompt updates the prompt.
func (s *Session) SetPrompt(prompt string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Prompt = prompt
}
