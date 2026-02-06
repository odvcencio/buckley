package telemetry

import (
	"context"
	"sync"
	"time"
)

const (
	// DefaultPersistBatchSize is the max events to buffer before writing.
	DefaultPersistBatchSize = 100
	// DefaultPersistFlushInterval is how often to flush buffered events.
	DefaultPersistFlushInterval = 500 * time.Millisecond
)

// Persister subscribes to a Hub and durably writes events to a SQLiteStore.
// Events are batched for write efficiency.
type Persister struct {
	store    *SQLiteStore
	streamID string

	eventCh     <-chan Event
	unsubscribe func()

	batch     []Event
	batchMu   sync.Mutex
	flushTick *time.Ticker

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewPersister creates a Persister that subscribes to the Hub and writes
// events to the store under the given streamID (typically a session ID).
func NewPersister(hub *Hub, store *SQLiteStore, streamID string) *Persister {
	ch, unsub := hub.Subscribe()
	ctx, cancel := context.WithCancel(context.Background())

	p := &Persister{
		store:       store,
		streamID:    streamID,
		eventCh:     ch,
		unsubscribe: unsub,
		batch:       make([]Event, 0, DefaultPersistBatchSize),
		flushTick:   time.NewTicker(DefaultPersistFlushInterval),
		ctx:         ctx,
		cancel:      cancel,
	}

	p.wg.Add(1)
	go p.run()

	return p
}

func (p *Persister) run() {
	defer p.wg.Done()

	for {
		select {
		case <-p.ctx.Done():
			// Drain remaining events from channel
			for {
				select {
				case evt, ok := <-p.eventCh:
					if !ok {
						p.flush()
						return
					}
					p.buffer(evt)
				default:
					p.flush()
					return
				}
			}

		case evt, ok := <-p.eventCh:
			if !ok {
				p.flush()
				return
			}
			p.buffer(evt)

		case <-p.flushTick.C:
			p.flush()
		}
	}
}

func (p *Persister) buffer(evt Event) {
	p.batchMu.Lock()
	p.batch = append(p.batch, evt)
	shouldFlush := len(p.batch) >= DefaultPersistBatchSize
	p.batchMu.Unlock()

	if shouldFlush {
		p.flush()
	}
}

func (p *Persister) flush() {
	p.batchMu.Lock()
	if len(p.batch) == 0 {
		p.batchMu.Unlock()
		return
	}
	batch := p.batch
	p.batch = make([]Event, 0, DefaultPersistBatchSize)
	p.batchMu.Unlock()

	// Best-effort write — don't block the subscriber loop
	_ = p.store.Append(context.Background(), p.streamID, batch)
}

// Flush forces an immediate write of buffered events to SQLite.
func (p *Persister) Flush() {
	p.flush()
}

// Stop unsubscribes from the Hub, flushes remaining events, and shuts down.
func (p *Persister) Stop() {
	p.cancel()
	p.wg.Wait()
	p.unsubscribe()
	p.flushTick.Stop()
}
