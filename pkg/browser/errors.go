package browser

import "errors"

var (
	ErrUnavailable    = errors.New("browser runtime unavailable")
	ErrNotImplemented = errors.New("browser runtime not implemented")
	ErrSessionClosed  = errors.New("browser session closed")
	ErrStaleState     = errors.New("stale state version")
)
