package tui

// isCurrentSession reports whether sess is the actively displayed session.
func (c *Controller) isCurrentSession(sess *SessionState) bool {
	if c == nil || sess == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.currentSession < 0 || c.currentSession >= len(c.sessions) {
		return false
	}
	return c.sessions[c.currentSession] == sess
}

// runIfCurrentSession executes fn only when sess is the active session.
func (c *Controller) runIfCurrentSession(sess *SessionState, fn func()) bool {
	if fn == nil || !c.isCurrentSession(sess) {
		return false
	}
	fn()
	return true
}
