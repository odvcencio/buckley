package widgets

import "github.com/odvcencio/fluffyui/state"

// Search highlights matching text.
func (m *ChatMessages) Search(query string) {
	m.setSearchQuery(query)
}

// NextMatch moves to the next search match.
func (m *ChatMessages) NextMatch() {
	m.buffer.NextMatch()
	m.syncListOffset()
	m.updateSearchMatches()
	m.requestInvalidate()
}

// PrevMatch moves to the previous search match.
func (m *ChatMessages) PrevMatch() {
	m.buffer.PrevMatch()
	m.syncListOffset()
	m.updateSearchMatches()
	m.requestInvalidate()
}

// SearchMatches returns current and total matches.
func (m *ChatMessages) SearchMatches() (current, total int) {
	return m.buffer.SearchMatches()
}

// ClearSearch clears search highlighting.
func (m *ChatMessages) ClearSearch() {
	m.setSearchQuery("")
}

func (m *ChatMessages) setSearchQuery(query string) {
	if m == nil {
		return
	}
	if writable, ok := m.searchQuerySig.(state.Writable[string]); ok && writable != nil {
		writable.Set(query)
	}
	m.applySearchQuery(query, true)
}

func (m *ChatMessages) applySearchQuery(query string, force bool) {
	if m == nil || m.buffer == nil {
		return
	}
	if !force && m.searchQuery == query {
		return
	}
	m.searchQuery = query
	m.buffer.Search(query)
	m.updateSearchMatches()
	m.syncListOffset()
	m.requestInvalidate()
}

func (m *ChatMessages) updateSearchMatches() {
	if m == nil || m.searchMatchesSig == nil || m.buffer == nil {
		return
	}
	current, total := m.buffer.SearchMatches()
	m.searchMatchesSig.Set(SearchMatchState{Current: current, Total: total})
}
