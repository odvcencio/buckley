package skill

import "sync"

// RuntimeState tracks active skill metadata and tool filtering for a session.
// It implements the SkillConversation interfaces used by skill activation.
type RuntimeState struct {
	mu            sync.RWMutex
	toolFilter    []string
	toolFilterSet bool
	toolCeiling   []string
	metadata      map[string]any
	inject        func(string)
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
	s.toolFilter = append([]string{}, allowedTools...)
	s.toolFilterSet = true
	s.mu.Unlock()
}

// ClearToolFilter removes any active tool allowlist.
func (s *RuntimeState) ClearToolFilter() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.toolFilter = nil
	s.toolFilterSet = false
	s.mu.Unlock()
}

// SetToolCeiling limits which tools active skills may expose. Nil leaves the
// caller unrestricted; an empty slice permits no tools. Clearing a skill's
// filter does not clear this caller-owned limit.
func (s *RuntimeState) SetToolCeiling(allowedTools []string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if allowedTools == nil {
		s.toolCeiling = nil
	} else {
		s.toolCeiling = append([]string{}, allowedTools...)
	}
}

// ToolFilter returns a copy of the effective allowlist within the caller's ceiling.
func (s *RuntimeState) ToolFilter() []string {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.toolFilterSet {
		if s.toolCeiling == nil {
			return nil
		}
		return append([]string{}, s.toolCeiling...)
	}
	if s.toolCeiling == nil {
		return append([]string{}, s.toolFilter...)
	}
	allowed := make(map[string]struct{}, len(s.toolCeiling))
	for _, name := range s.toolCeiling {
		allowed[name] = struct{}{}
	}
	tools := make([]string, 0, len(s.toolFilter))
	for _, name := range s.toolFilter {
		if _, ok := allowed[name]; ok {
			tools = append(tools, name)
		}
	}
	return tools
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
