package skill

import "sync"

// RuntimeState tracks active skill metadata and tool filtering for a session.
// It implements the SkillConversation interfaces used by skill activation.
type RuntimeState struct {
	mu         sync.RWMutex
	toolFilter []string
	metadata   map[string]any
	inject     func(string)
}

// NewRuntimeState creates a skill runtime state with an optional injector.
// The injector is used to add system messages to the active conversation.
func NewRuntimeState(inject func(string)) *RuntimeState {
	return &RuntimeState{
		metadata: make(map[string]any),
		inject:   inject,
	}
}

// AddSystemMessage injects a system message when an injector is available.
func (s *RuntimeState) AddSystemMessage(content string) {
	if s == nil {
		return
	}
	s.mu.RLock()
	inject := s.inject
	s.mu.RUnlock()
	if inject != nil {
		inject(content)
	}
}

// SetToolFilter stores the active tool allowlist.
func (s *RuntimeState) SetToolFilter(allowedTools []string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if len(allowedTools) == 0 {
		s.toolFilter = nil
		s.mu.Unlock()
		return
	}
	s.toolFilter = append([]string{}, allowedTools...)
	s.mu.Unlock()
}

// ClearToolFilter removes any active tool allowlist.
func (s *RuntimeState) ClearToolFilter() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.toolFilter = nil
	s.mu.Unlock()
}

// ToolFilter returns the currently active tool allowlist (copy).
func (s *RuntimeState) ToolFilter() []string {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.toolFilter) == 0 {
		return nil
	}
	return append([]string{}, s.toolFilter...)
}

// GetMetadata returns stored metadata values.
func (s *RuntimeState) GetMetadata(key string) any {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.metadata[key]
}

// SetMetadata stores metadata values.
func (s *RuntimeState) SetMetadata(key string, value any) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.metadata == nil {
		s.metadata = make(map[string]any)
	}
	s.metadata[key] = value
	s.mu.Unlock()
}

// SetInjector updates the system message injector.
func (s *RuntimeState) SetInjector(inject func(string)) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.inject = inject
	s.mu.Unlock()
}
