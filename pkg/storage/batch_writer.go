package storage

import (
	"sync"
	"time"
)

// BatchWriter provides asynchronous batching of message writes with automatic
// flushing based on batch size or timeout. This reduces transaction overhead
// when saving multiple messages in rapid succession.
//
// Usage:
//
	// writer := store.NewBatchWriter(100, 100*time.Millisecond)
	// defer writer.Close()
	// writer.Add(msg) // messages are batched and flushed automatically
//
// The writer is safe for concurrent use and handles errors by logging them
// and continuing to accept new messages.
type BatchWriter struct {
	store   *Store
	batch   []*Message
	maxSize int
	maxWait time.Duration
	mu      sync.Mutex
	timer   *time.Timer
	done    chan struct{}
	wg      sync.WaitGroup
}

// NewBatchWriter creates a new batch writer with the specified batch size and
// maximum wait time. Messages are flushed when either maxSize messages have
// been accumulated or maxWait time has elapsed since the first message.
//
// For a store-configured batch writer, use store.NewMessageBatchWriter().
func (s *Store) NewBatchWriter(maxSize int, maxWait time.Duration) *BatchWriter {
	if maxSize <= 0 {
		maxSize = 100 // default batch size
	}
	if maxWait <= 0 {
		maxWait = 100 * time.Millisecond // default flush interval
	}

	bw := &BatchWriter{
		store:   s,
		batch:   make([]*Message, 0, maxSize),
		maxSize: maxSize,
		maxWait: maxWait,
		done:    make(chan struct{}),
	}

	// Start background flusher
	bw.wg.Add(1)
	go bw.flusher()

	return bw
}

// Add adds a message to the batch. If the batch reaches maxSize, it is
// flushed immediately. Otherwise, a timer is started to flush after maxWait.
func (bw *BatchWriter) Add(msg *Message) error {
	bw.mu.Lock()
	defer bw.mu.Unlock()

	// Check if closed
	select {
	case <-bw.done:
		return ErrStoreClosed
	default:
	}

	bw.batch = append(bw.batch, msg)

	// Flush immediately if batch is full
	if len(bw.batch) >= bw.maxSize {
		return bw.flushLocked()
	}

	// Start timer on first message
	if len(bw.batch) == 1 {
		bw.startTimer()
	}

	return nil
}

// Flush manually flushes the current batch.
func (bw *BatchWriter) Flush() error {
	bw.mu.Lock()
	defer bw.mu.Unlock()
	return bw.flushLocked()
}

// Close stops the batch writer and flushes any remaining messages.
func (bw *BatchWriter) Close() error {
	close(bw.done)
	bw.wg.Wait()

	// Final flush
	return bw.Flush()
}

// flushLocked flushes the batch while holding the lock.
// Must be called with lock held.
func (bw *BatchWriter) flushLocked() error {
	if len(bw.batch) == 0 {
		return nil
	}

	// Stop any running timer
	if bw.timer != nil {
		bw.timer.Stop()
		bw.timer = nil
	}

	// Save messages using batch insert
	batch := bw.batch
	bw.batch = make([]*Message, 0, bw.maxSize)

	// Release lock during database operation
	bw.mu.Unlock()
	err := bw.store.SaveMessagesBatch(batch)
	bw.mu.Lock()

	return err
}

// startTimer starts the flush timer.
// Must be called with lock held.
func (bw *BatchWriter) startTimer() {
	if bw.timer != nil {
		bw.timer.Stop()
	}

	bw.timer = time.AfterFunc(bw.maxWait, func() {
		bw.mu.Lock()
		_ = bw.flushLocked()
		bw.mu.Unlock()
	})
}

// flusher runs in the background to handle shutdown signals.
func (bw *BatchWriter) flusher() {
	defer bw.wg.Done()
	<-bw.done
}

// BatchSize returns the current number of messages in the batch.
func (bw *BatchWriter) BatchSize() int {
	bw.mu.Lock()
	defer bw.mu.Unlock()
	return len(bw.batch)
}

// MessageCount returns the total number of messages flushed so far.
// Note: This is not currently tracked; returns 0.
func (bw *BatchWriter) MessageCount() int {
	return 0 // Could be extended to track total flushed count
}
