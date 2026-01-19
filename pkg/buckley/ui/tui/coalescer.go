package tui

import (
	"strings"
	"sync"
	"time"
)

// Coalescer buffers streaming chunks and flushes them on frame boundaries.
// This prevents excessive renders during high token throughput.
type Coalescer struct {
	mu       sync.Mutex
	buffers  map[string]*strings.Builder
	lastAdd  map[string]time.Time
	maxChars int           // Flush if buffer exceeds this
	maxWait  time.Duration // Max time before forced flush
	post     func(Message) // Message posting function
}

// CoalescerConfig configures the coalescer behavior.
type CoalescerConfig struct {
	MaxChars int           // Default: 128 chars
	MaxWait  time.Duration // Default: 16ms (~60 FPS)
}

// DefaultCoalescerConfig returns sensible defaults.
func DefaultCoalescerConfig() CoalescerConfig {
	return CoalescerConfig{
		MaxChars: 128,
		MaxWait:  16 * time.Millisecond,
	}
}

// NewCoalescer creates a streaming coalescer.
func NewCoalescer(cfg CoalescerConfig, post func(Message)) *Coalescer {
	if cfg.MaxChars == 0 {
		cfg.MaxChars = 128
	}
	if cfg.MaxWait == 0 {
		cfg.MaxWait = 16 * time.Millisecond
	}
	return &Coalescer{
		buffers:  make(map[string]*strings.Builder),
		lastAdd:  make(map[string]time.Time),
		maxChars: cfg.MaxChars,
		maxWait:  cfg.MaxWait,
		post:     post,
	}
}

// Add queues a chunk for a session.
// May trigger immediate flush if buffer is full.
func (c *Coalescer) Add(sessionID, text string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	buf := c.buffers[sessionID]
	if buf == nil {
		buf = &strings.Builder{}
		c.buffers[sessionID] = buf
		c.lastAdd[sessionID] = time.Now()
	}

	buf.WriteString(text)

	// Flush immediately if buffer is large
	if buf.Len() >= c.maxChars {
		c.flushLocked(sessionID)
	}
}

// Tick is called on frame boundaries to flush pending content.
func (c *Coalescer) Tick() {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	for sessionID, lastTime := range c.lastAdd {
		buf := c.buffers[sessionID]
		if buf == nil || buf.Len() == 0 {
			continue
		}

		// Flush if content has been waiting too long
		if now.Sub(lastTime) >= c.maxWait {
			c.flushLocked(sessionID)
		}
	}
}

// FlushAll forces all buffers to flush immediately.
func (c *Coalescer) FlushAll() {
	c.mu.Lock()
	defer c.mu.Unlock()

	for sessionID := range c.buffers {
		c.flushLocked(sessionID)
	}
}

// Flush forces a specific session to flush.
func (c *Coalescer) Flush(sessionID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.flushLocked(sessionID)
}

// flushLocked sends buffered content as a StreamFlush message.
// Must be called with lock held.
func (c *Coalescer) flushLocked(sessionID string) {
	buf := c.buffers[sessionID]
	if buf == nil || buf.Len() == 0 {
		return
	}

	text := buf.String()
	buf.Reset()
	c.lastAdd[sessionID] = time.Now()

	if c.post != nil {
		c.post(StreamFlush{SessionID: sessionID, Text: text})
	}
}

// Clear removes a session's buffer (call on stream end).
func (c *Coalescer) Clear(sessionID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.buffers, sessionID)
	delete(c.lastAdd, sessionID)
}

// HasPending returns true if any session has buffered content.
func (c *Coalescer) HasPending() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, buf := range c.buffers {
		if buf.Len() > 0 {
			return true
		}
	}
	return false
}
