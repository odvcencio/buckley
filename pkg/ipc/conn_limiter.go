package ipc

import "sync"

type connLimiter struct {
	max    int
	mu     sync.Mutex
	active int
}

func newConnLimiter(max int) *connLimiter {
	return &connLimiter{max: max}
}

func (l *connLimiter) Acquire() bool {
	if l == nil || l.max <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.active >= l.max {
		return false
	}
	l.active++
	return true
}

func (l *connLimiter) Release() {
	if l == nil || l.max <= 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.active > 0 {
		l.active--
	}
}
