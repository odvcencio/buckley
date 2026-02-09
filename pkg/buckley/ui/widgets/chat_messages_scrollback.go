package widgets

// GetContent returns all text content from the chat view (last N lines).
func (m *ChatMessages) GetContent(limit int) []string {
	if m == nil || m.buffer == nil {
		return nil
	}
	return m.buffer.GetAllContent(limit)
}

// ScrollUp scrolls up by n lines.
func (m *ChatMessages) ScrollUp(n int) {
	if m == nil || m.buffer == nil {
		return
	}
	m.buffer.ScrollUp(n)
	m.syncListOffset()
	m.notifyScroll()
}

// ScrollDown scrolls down by n lines.
func (m *ChatMessages) ScrollDown(n int) {
	if m == nil || m.buffer == nil {
		return
	}
	m.buffer.ScrollDown(n)
	m.syncListOffset()
	m.notifyScroll()
}

// PageUp scrolls up by one page.
func (m *ChatMessages) PageUp() {
	if m == nil || m.buffer == nil {
		return
	}
	m.buffer.PageUp()
	m.syncListOffset()
	m.notifyScroll()
}

// PageDown scrolls down by one page.
func (m *ChatMessages) PageDown() {
	if m == nil || m.buffer == nil {
		return
	}
	m.buffer.PageDown()
	m.syncListOffset()
	m.notifyScroll()
}

// ScrollToTop scrolls to the beginning.
func (m *ChatMessages) ScrollToTop() {
	if m == nil || m.buffer == nil {
		return
	}
	m.buffer.ScrollToTop()
	m.syncListOffset()
	m.notifyScroll()
}

// ScrollToBottom scrolls to the end.
func (m *ChatMessages) ScrollToBottom() {
	if m == nil || m.buffer == nil {
		return
	}
	m.buffer.ScrollToBottom()
	m.syncListOffset()
	m.notifyScroll()
}

// ScrollPosition returns scroll position info.
func (m *ChatMessages) ScrollPosition() (top, total, viewHeight int) {
	if m == nil || m.buffer == nil {
		return 0, 0, 0
	}
	return m.buffer.ScrollPosition()
}

func (m *ChatMessages) notifyScroll() {
	if m.onScrollChange != nil {
		top, total, viewH := m.buffer.ScrollPosition()
		m.onScrollChange(top, total, viewH)
	}
}

func (m *ChatMessages) syncListOffset() {
	if m.scrollView == nil || m.buffer == nil {
		return
	}
	top, _, _ := m.buffer.ScrollPosition()
	if top < 0 {
		top = 0
	}
	m.scrollView.ScrollTo(0, top)
	m.notifyScroll()
}

func (m *ChatMessages) ScrollBy(dx, dy int) {
	if dy < 0 {
		m.ScrollUp(-dy)
		return
	}
	if dy > 0 {
		m.ScrollDown(dy)
	}
}

func (m *ChatMessages) ScrollTo(x, y int) {
	top, _, _ := m.ScrollPosition()
	if y < top {
		m.ScrollUp(top - y)
		return
	}
	if y > top {
		m.ScrollDown(y - top)
	}
}

func (m *ChatMessages) PageBy(pages int) {
	if pages < 0 {
		for i := 0; i < -pages; i++ {
			m.PageUp()
		}
		return
	}
	for i := 0; i < pages; i++ {
		m.PageDown()
	}
}

func (m *ChatMessages) ScrollToStart() {
	m.ScrollToTop()
}

func (m *ChatMessages) ScrollToEnd() {
	m.ScrollToBottom()
}
