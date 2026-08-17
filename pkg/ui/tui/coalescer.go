package tui

import (
	"crypto/sha256"
	"strings"
	"sync"
	"time"
)

const (
	coalescerPendingMaxCount        = 256
	coalescerPendingMaxBytes        = 1 << 20
	coalescerBoundaryMarkerMaxCount = 16
	coalescerBoundaryReserve        = coalescerBoundaryMarkerMaxCount + 1
	coalescerBoundaryByteReserve    = 64 << 10
	coalescerBufferMaxCount         = 256
	coalescerBufferMaxBytes         = 1 << 20
)

const (
	streamOverloadDiagnostic   = "Buckley's stream renderer overloaded. Buffered stream updates were rejected to keep memory bounded; the visible response may be incomplete."
	streamIncompleteDiagnostic = "A streamed response ended while the renderer was overloaded. Its visible content may be incomplete."
)

type streamOverloadMsg struct{}

func (streamOverloadMsg) isMessage() {}

type streamBoundaryRejectedMsg struct {
	Fingerprint [16]byte
	Generation  uint64
}

func (streamBoundaryRejectedMsg) isMessage() {}

type streamKey struct {
	sessionID  string
	generation uint64
}

type streamBoundaryKey struct {
	fingerprint [16]byte
	generation  uint64
}

func streamFingerprint(sessionID string) [16]byte {
	sum := sha256.Sum256([]byte(sessionID))
	var fingerprint [16]byte
	copy(fingerprint[:], sum[:len(fingerprint)])
	return fingerprint
}

type coalescerPublication struct {
	msg      Message
	bytes    int
	boundary bool
}

// CoalescerSnapshot exposes bounded publication state for diagnostics/tests.
type CoalescerSnapshot struct {
	Closed              bool
	Publishing          bool
	Overloaded          bool
	BufferedStreams     int
	BufferBytes         int
	PendingCount        int
	PendingBytes        int
	InFlightCount       int
	InFlightBytes       int
	Rejected            uint64
	RejectedBoundaries  uint64
	Evicted             uint64
	DiagnosticsQueued   uint64
	DiagnosticsRejected uint64
	BoundarySignals     uint64
	RejectedBuffers     uint64
}

// Coalescer buffers streaming chunks and flushes them on frame boundaries.
// Detachment and publication order are serialized under mu, while callbacks
// always run after unlocking it.
type Coalescer struct {
	mu          sync.Mutex
	buffers     map[streamKey]*strings.Builder
	lastAdd     map[streamKey]time.Time
	bufferBytes int

	pending         []coalescerPublication
	pendingBytes    int
	inFlightCount   int
	inFlightBytes   int
	publishing      bool
	closed          bool
	overloaded      bool
	diagnosticSet   bool
	boundaryMarkers map[streamBoundaryKey]struct{}

	rejected            uint64
	rejectedBoundaries  uint64
	evicted             uint64
	diagnosticsQueued   uint64
	diagnosticsRejected uint64
	boundarySignals     uint64
	rejectedBuffers     uint64

	maxChars int
	maxWait  time.Duration
	post     func(Message)
}

// CoalescerConfig configures the coalescer behavior.
type CoalescerConfig struct {
	MaxChars int
	MaxWait  time.Duration
}

// DefaultCoalescerConfig returns sensible defaults.
func DefaultCoalescerConfig() CoalescerConfig {
	return CoalescerConfig{MaxChars: 128, MaxWait: 16 * time.Millisecond}
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
		buffers:         make(map[streamKey]*strings.Builder),
		lastAdd:         make(map[streamKey]time.Time),
		boundaryMarkers: make(map[streamBoundaryKey]struct{}),
		maxChars:        cfg.MaxChars,
		maxWait:         cfg.MaxWait,
		post:            post,
	}
}

// Add queues a chunk in the legacy generation-zero stream.
func (c *Coalescer) Add(sessionID, text string) {
	c.AddStream(sessionID, 0, text)
}

// AddStream queues a chunk for one concrete stream generation.
func (c *Coalescer) AddStream(sessionID string, generation uint64, text string) {
	if text == "" {
		return
	}
	if len(sessionID) > coalescerPendingMaxBytes || len(text) > coalescerPendingMaxBytes-len(sessionID) {
		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			return
		}
		c.rejected++
		c.rejectedBuffers++
		startPublisher := c.enterOverloadLocked()
		c.mu.Unlock()
		c.drainPublications(startPublisher)
		return
	}
	sessionID = strings.Clone(sessionID)
	text = strings.Clone(text)
	key := streamKey{sessionID: sessionID, generation: generation}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	startPublisher := false
	buf := c.buffers[key]
	if buf == nil {
		if len(text) >= c.maxChars {
			startPublisher = c.enqueuePublicationLocked(StreamFlush{
				SessionID: key.sessionID, Generation: key.generation, Text: text,
			}, false)
			c.mu.Unlock()
			c.drainPublications(startPublisher)
			return
		}
		if len(c.buffers) >= coalescerBufferMaxCount || c.bufferBytes+len(key.sessionID)+len(text) > coalescerBufferMaxBytes {
			c.rejectedBuffers++
			startPublisher = c.enterOverloadLocked()
			c.mu.Unlock()
			c.drainPublications(startPublisher)
			return
		}
		buf = &strings.Builder{}
		c.buffers[key] = buf
		c.lastAdd[key] = time.Now()
		c.bufferBytes += len(key.sessionID)
	}

	if buf.Len()+len(text) >= c.maxChars || c.bufferBytes-buf.Cap()+buf.Len()+len(text) > coalescerBufferMaxBytes {
		existing := strings.Clone(buf.String())
		c.bufferBytes -= len(key.sessionID) + buf.Cap()
		delete(c.buffers, key)
		delete(c.lastAdd, key)
		startPublisher = c.enqueuePublicationLocked(StreamFlush{
			SessionID: key.sessionID, Generation: key.generation, Text: existing + text,
		}, false)
		c.mu.Unlock()
		c.drainPublications(startPublisher)
		return
	}

	oldCap := buf.Cap()
	buf.WriteString(text)
	c.bufferBytes += buf.Cap() - oldCap
	if c.bufferBytes > coalescerBufferMaxBytes {
		if flush, ok := c.detachLocked(key); ok {
			startPublisher = c.enqueuePublicationLocked(flush, false)
		}
	}
	c.mu.Unlock()
	c.drainPublications(startPublisher)
}

// Tick flushes content that has waited for a frame boundary.
func (c *Coalescer) Tick() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	startPublisher := false
	now := time.Now()
	for key, lastTime := range c.lastAdd {
		buf := c.buffers[key]
		if buf == nil || buf.Len() == 0 || now.Sub(lastTime) < c.maxWait {
			continue
		}
		if flush, ok := c.detachLocked(key); ok {
			if c.enqueuePublicationLocked(flush, false) {
				startPublisher = true
			}
		}
	}
	c.mu.Unlock()
	c.drainPublications(startPublisher)
}

// FlushAll forces all buffers to flush immediately.
func (c *Coalescer) FlushAll() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	startPublisher := false
	for key := range c.buffers {
		if flush, ok := c.detachLocked(key); ok {
			if c.enqueuePublicationLocked(flush, false) {
				startPublisher = true
			}
		}
	}
	c.mu.Unlock()
	c.drainPublications(startPublisher)
}

// Flush forces every generation for a session to flush.
func (c *Coalescer) Flush(sessionID string) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	startPublisher := false
	for key := range c.buffers {
		if key.sessionID != sessionID {
			continue
		}
		if flush, ok := c.detachLocked(key); ok {
			if c.enqueuePublicationLocked(flush, false) {
				startPublisher = true
			}
		}
	}
	c.mu.Unlock()
	c.drainPublications(startPublisher)
}

// FlushAndPost publishes a legacy generation-zero flush and boundary.
func (c *Coalescer) FlushAndPost(sessionID string, after Message) {
	c.FlushAndPostStream(sessionID, 0, after)
}

// FlushAndPostStream atomically sequences a generation's final flush before
// its boundary. A publisher already in flight drains both in FIFO order.
func (c *Coalescer) FlushAndPostStream(sessionID string, generation uint64, after Message) {
	key := streamKey{sessionID: sessionID, generation: generation}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	startPublisher := false
	if flush, ok := c.detachLocked(key); ok {
		startPublisher = c.enqueuePublicationLocked(flush, false)
	}
	if c.enqueuePublicationLocked(after, true) {
		startPublisher = true
	}
	c.mu.Unlock()
	c.drainPublications(startPublisher)
}

func (c *Coalescer) detachLocked(key streamKey) (StreamFlush, bool) {
	buf := c.buffers[key]
	if buf == nil || buf.Len() == 0 {
		return StreamFlush{}, false
	}
	text := strings.Clone(buf.String())
	c.bufferBytes -= len(key.sessionID) + buf.Cap()
	delete(c.buffers, key)
	delete(c.lastAdd, key)
	return StreamFlush{SessionID: key.sessionID, Generation: key.generation, Text: text}, true
}

func (c *Coalescer) enqueuePublicationLocked(msg Message, boundary bool) bool {
	if msg == nil {
		return false
	}
	msg = normalizeOverflowMessage(msg)
	event := coalescerPublication{msg: msg, bytes: retainedMessageBytes(msg), boundary: boundary}
	if !boundary {
		if event.bytes > coalescerPendingMaxBytes-coalescerBoundaryByteReserve || !c.fitsPublicationLocked(event, true) {
			c.rejected++
			return c.enterOverloadLocked()
		}
		return c.appendPublicationLocked(event)
	}

	if c.fitsPublicationLocked(event, true) {
		return c.appendPublicationLocked(event)
	}
	c.rejected++
	c.rejectedBoundaries++
	startPublisher := c.enterOverloadLocked()
	for !c.diagnosticSet && c.evictPublicationLocked(true) {
		if c.enterOverloadLocked() {
			startPublisher = true
		}
	}
	if c.enqueueBoundaryRejectionLocked(msg) {
		startPublisher = true
	}
	return startPublisher
}

func (c *Coalescer) fitsPublicationLocked(event coalescerPublication, reserveBoundary bool) bool {
	countLimit := coalescerPendingMaxCount
	byteLimit := coalescerPendingMaxBytes
	if reserveBoundary {
		countLimit -= coalescerBoundaryReserve
		byteLimit -= coalescerBoundaryByteReserve
	}
	count := len(c.pending) + c.inFlightCount + 1
	bytes := c.pendingBytes + c.inFlightBytes + event.bytes
	return count <= countLimit && bytes <= byteLimit
}

func (c *Coalescer) appendPublicationLocked(event coalescerPublication) bool {
	c.pending = append(c.pending, event)
	c.pendingBytes += event.bytes
	if c.publishing {
		return false
	}
	c.publishing = true
	return true
}

func (c *Coalescer) enterOverloadLocked() bool {
	c.overloaded = true
	if c.diagnosticSet {
		return false
	}
	event := coalescerPublication{msg: streamOverloadMsg{}, bytes: retainedMessageBytes(streamOverloadMsg{})}
	if !c.fitsPublicationLocked(event, false) {
		c.diagnosticsRejected++
		return false
	}
	c.diagnosticSet = true
	c.diagnosticsQueued++
	return c.appendPublicationLocked(event)
}

func (c *Coalescer) enqueueBoundaryRejectionLocked(rejected Message) bool {
	done, ok := rejected.(StreamDone)
	if !ok {
		return false
	}
	key := streamBoundaryKey{fingerprint: streamFingerprint(done.SessionID), generation: done.Generation}
	if _, exists := c.boundaryMarkers[key]; exists || len(c.boundaryMarkers) >= coalescerBoundaryMarkerMaxCount {
		return false
	}
	marker := streamBoundaryRejectedMsg{Fingerprint: key.fingerprint, Generation: key.generation}
	event := coalescerPublication{
		msg:      marker,
		bytes:    retainedMessageBytes(marker),
		boundary: true,
	}
	for !c.fitsPublicationLocked(event, false) && c.evictPublicationLocked(true) {
	}
	if !c.fitsPublicationLocked(event, false) {
		return false
	}
	c.boundaryMarkers[key] = struct{}{}
	c.boundarySignals++
	return c.appendPublicationLocked(event)
}

func (c *Coalescer) evictPublicationLocked(allowBoundary bool) bool {
	for i, event := range c.pending {
		if event.boundary {
			continue
		}
		if _, diagnostic := event.msg.(streamOverloadMsg); diagnostic {
			continue
		}
		c.pendingBytes -= event.bytes
		copy(c.pending[i:], c.pending[i+1:])
		last := len(c.pending) - 1
		c.pending[last] = coalescerPublication{}
		c.pending = c.pending[:last]
		c.rejected++
		c.evicted++
		return true
	}
	if !allowBoundary {
		return false
	}
	for i, event := range c.pending {
		if !event.boundary {
			continue
		}
		if _, rejection := event.msg.(streamBoundaryRejectedMsg); rejection {
			continue
		}
		c.pendingBytes -= event.bytes
		copy(c.pending[i:], c.pending[i+1:])
		last := len(c.pending) - 1
		c.pending[last] = coalescerPublication{}
		c.pending = c.pending[:last]
		c.rejected++
		c.rejectedBoundaries++
		c.evicted++
		return true
	}
	return false
}

func (c *Coalescer) drainPublications(start bool) {
	if !start {
		return
	}
	for {
		c.mu.Lock()
		if len(c.pending) == 0 {
			c.publishing = false
			c.mu.Unlock()
			return
		}
		event := c.pending[0]
		c.pending[0] = coalescerPublication{}
		c.pending = c.pending[1:]
		if len(c.pending) == 0 {
			c.pending = nil
		}
		c.pendingBytes -= event.bytes
		c.inFlightCount = 1
		c.inFlightBytes = event.bytes
		post := c.post
		c.mu.Unlock()

		if post != nil {
			post(event.msg)
		}

		c.mu.Lock()
		c.inFlightCount = 0
		c.inFlightBytes = 0
		if marker, ok := event.msg.(streamBoundaryRejectedMsg); ok {
			delete(c.boundaryMarkers, streamBoundaryKey{fingerprint: marker.Fingerprint, generation: marker.Generation})
		}
		c.mu.Unlock()
	}
}

// Clear removes every buffered generation for a session.
func (c *Coalescer) Clear(sessionID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key := range c.buffers {
		if key.sessionID == sessionID {
			c.bufferBytes -= len(key.sessionID) + c.buffers[key].Cap()
			delete(c.buffers, key)
			delete(c.lastAdd, key)
		}
	}
}

// ClearStream removes only the completed generation's buffer.
func (c *Coalescer) ClearStream(sessionID string, generation uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := streamKey{sessionID: sessionID, generation: generation}
	if buf := c.buffers[key]; buf != nil {
		c.bufferBytes -= len(key.sessionID) + buf.Cap()
	}
	delete(c.buffers, key)
	delete(c.lastAdd, key)
}

// ClearRejectedStream releases buffered text for the rejected terminal
// generation without disturbing a newer generation that reused the session ID.
func (c *Coalescer) ClearRejectedStream(fingerprint [16]byte, generation uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, buf := range c.buffers {
		if key.generation != generation || streamFingerprint(key.sessionID) != fingerprint {
			continue
		}
		c.bufferBytes -= len(key.sessionID) + buf.Cap()
		delete(c.buffers, key)
		delete(c.lastAdd, key)
	}
}

// Close releases buffered and queued payloads and rejects future chunks.
func (c *Coalescer) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	clear(c.buffers)
	clear(c.lastAdd)
	c.bufferBytes = 0
	for i := range c.pending {
		c.pending[i] = coalescerPublication{}
	}
	c.pending = nil
	c.pendingBytes = 0
	clear(c.boundaryMarkers)
}

// HasPending reports whether any generation has buffered text.
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

// HasPendingStream reports whether one generation has buffered text.
func (c *Coalescer) HasPendingStream(sessionID string, generation uint64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	buf := c.buffers[streamKey{sessionID: sessionID, generation: generation}]
	return buf != nil && buf.Len() > 0
}

// Snapshot returns bounded publication and overload state.
func (c *Coalescer) Snapshot() CoalescerSnapshot {
	if c == nil {
		return CoalescerSnapshot{Closed: true}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	buffered := 0
	for _, buf := range c.buffers {
		if buf.Len() > 0 {
			buffered++
		}
	}
	return CoalescerSnapshot{
		Closed:              c.closed,
		Publishing:          c.publishing,
		Overloaded:          c.overloaded,
		BufferedStreams:     buffered,
		BufferBytes:         c.bufferBytes,
		PendingCount:        len(c.pending),
		PendingBytes:        c.pendingBytes,
		InFlightCount:       c.inFlightCount,
		InFlightBytes:       c.inFlightBytes,
		Rejected:            c.rejected,
		RejectedBoundaries:  c.rejectedBoundaries,
		Evicted:             c.evicted,
		DiagnosticsQueued:   c.diagnosticsQueued,
		DiagnosticsRejected: c.diagnosticsRejected,
		BoundarySignals:     c.boundarySignals,
		RejectedBuffers:     c.rejectedBuffers,
	}
}
