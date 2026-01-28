// pkg/ralph/control_watcher.go
package ralph

import (
	"crypto/sha256"
	"log"
	"os"
	"sync"
	"time"
)

// ControlWatcher watches a ralph-control.yaml file for changes and hot-reloads.
type ControlWatcher struct {
	path        string
	interval    time.Duration
	current     *ControlConfig
	lastHash    [32]byte
	mu          sync.RWMutex
	stopCh      chan struct{}
	stopOnce    sync.Once
	subscribers []chan *ControlConfig
	subMu       sync.RWMutex
	started     bool
	errorLogger *log.Logger
}

// ControlWatcherOption configures a ControlWatcher.
type ControlWatcherOption func(*ControlWatcher)

// WithErrorLogger sets the logger for control watcher errors.
func WithErrorLogger(logger *log.Logger) ControlWatcherOption {
	return func(w *ControlWatcher) {
		w.errorLogger = logger
	}
}

// NewControlWatcher creates a new ControlWatcher that polls the given path
// at the specified interval. Default interval is 1 second if zero.
func NewControlWatcher(path string, interval time.Duration, opts ...ControlWatcherOption) *ControlWatcher {
	if interval == 0 {
		interval = time.Second
	}
	w := &ControlWatcher{
		path:        path,
		interval:    interval,
		stopCh:      make(chan struct{}),
		subscribers: make([]chan *ControlConfig, 0),
	}
	for _, opt := range opts {
		opt(w)
	}
	return w
}

// Start begins watching the control file. Returns an error if the file
// cannot be read or parsed on startup.
func (w *ControlWatcher) Start() error {
	if w == nil {
		return nil
	}

	// Load initial config
	cfg, hash, err := w.loadAndHash()
	if err != nil {
		return err
	}

	w.mu.Lock()
	w.current = cfg
	w.lastHash = hash
	w.started = true
	w.mu.Unlock()

	// Start polling goroutine
	go w.poll()

	return nil
}

// Stop stops watching the control file. Safe to call multiple times.
func (w *ControlWatcher) Stop() {
	if w == nil {
		return
	}

	w.mu.Lock()
	if !w.started {
		w.mu.Unlock()
		return
	}
	w.started = false
	w.mu.Unlock()

	w.stopOnce.Do(func() {
		close(w.stopCh)
	})
	w.closeSubscribers()
}

// Config returns the current control configuration.
// Returns nil if the watcher has not been started.
func (w *ControlWatcher) Config() *ControlConfig {
	if w == nil {
		return nil
	}

	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.current
}

// Subscribe returns a channel that receives the new configuration
// whenever the control file changes.
func (w *ControlWatcher) Subscribe() <-chan *ControlConfig {
	if w == nil {
		return nil
	}

	ch := make(chan *ControlConfig, 1)

	w.subMu.Lock()
	w.subscribers = append(w.subscribers, ch)
	w.subMu.Unlock()

	return ch
}

// Unsubscribe removes a subscription channel. The channel is not closed;
// the caller is responsible for managing the channel's lifecycle.
func (w *ControlWatcher) Unsubscribe(ch <-chan *ControlConfig) {
	if w == nil {
		return
	}

	w.subMu.Lock()
	defer w.subMu.Unlock()

	for i, sub := range w.subscribers {
		if sub == ch {
			w.subscribers = append(w.subscribers[:i], w.subscribers[i+1:]...)
			return
		}
	}
}

// poll runs the polling loop.
func (w *ControlWatcher) poll() {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-w.stopCh:
			return
		case <-ticker.C:
			w.checkForChanges()
		}
	}
}

// checkForChanges reads the file and updates config if changed.
func (w *ControlWatcher) checkForChanges() {
	cfg, hash, err := w.loadAndHash()
	if err != nil {
		// Log error but keep using the last valid config
		if w.errorLogger != nil {
			w.errorLogger.Printf("control watcher: error loading %s: %v", w.path, err)
		}
		return
	}

	w.mu.Lock()
	if hash == w.lastHash {
		w.mu.Unlock()
		return
	}

	w.current = cfg
	w.lastHash = hash
	w.mu.Unlock()

	// Notify subscribers
	w.notifySubscribers(cfg)
}

// loadAndHash loads the config file and computes its hash.
func (w *ControlWatcher) loadAndHash() (*ControlConfig, [32]byte, error) {
	data, err := os.ReadFile(w.path)
	if err != nil {
		return nil, [32]byte{}, err
	}

	hash := sha256.Sum256(data)

	cfg, err := ParseControlConfig(data)
	if err != nil {
		return nil, [32]byte{}, err
	}

	return cfg, hash, nil
}

// notifySubscribers sends the new config to all subscribers.
func (w *ControlWatcher) notifySubscribers(cfg *ControlConfig) {
	if w == nil {
		return
	}

	w.mu.RLock()
	if !w.started {
		w.mu.RUnlock()
		return
	}
	w.subMu.RLock()
	defer w.subMu.RUnlock()
	defer w.mu.RUnlock()

	for _, ch := range w.subscribers {
		// Non-blocking send to avoid blocking the watcher
		select {
		case ch <- cfg:
		default:
			// Channel full, drop notification
		}
	}
}

func (w *ControlWatcher) closeSubscribers() {
	if w == nil {
		return
	}

	w.subMu.Lock()
	defer w.subMu.Unlock()

	for _, ch := range w.subscribers {
		close(ch)
	}
	w.subscribers = nil
}
