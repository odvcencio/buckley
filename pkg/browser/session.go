package browser

import "context"

// Runtime manages browser sessions.
type Runtime interface {
	NewSession(ctx context.Context, cfg SessionConfig) (BrowserSession, error)
	Close() error
}

// BrowserSession is the port implemented by browser runtime adapters.
type BrowserSession interface {
	ID() string
	Navigate(ctx context.Context, url string) (*Observation, error)
	Observe(ctx context.Context, opts ObserveOptions) (*Observation, error)
	Act(ctx context.Context, action Action) (*ActionResult, error)
	Stream(ctx context.Context, opts StreamOptions) (<-chan StreamEvent, error)
	Close() error
}
