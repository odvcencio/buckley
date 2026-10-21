package mcp

import (
	"errors"
	"fmt"
	"io"
	"sync/atomic"
)

// ErrMessageTooLarge reports an incoming JSON message rejected before decoding.
var ErrMessageTooLarge = errors.New("mcp: incoming message exceeds byte limit")

// messageLimitReader counts JSON value boundaries without buffering or decoding
// their contents. The SDK remains responsible for JSON-RPC validation. Tracking
// quoted strings and nesting also bounds multiline JSON and legacy batches.
// Inter-message whitespace has the same bound so padding cannot grow buffers.
type messageLimitReader struct {
	source                   io.Reader
	limit                    int
	bytes, whitespace, depth int
	started, quoted, escaped bool
	exceeded                 atomic.Bool
}

func (r *messageLimitReader) Read(p []byte) (int, error) {
	if r.exceeded.Load() {
		return 0, r.limitError()
	}
	p = p[:min(len(p), r.limit-r.bytes+1)]
	n, err := r.source.Read(p)
	for i, ch := range p[:n] {
		if !r.started {
			if ch == ' ' || ch == '\t' || ch == '\r' || ch == '\n' {
				r.whitespace++
				if r.whitespace > r.limit {
					r.exceeded.Store(true)
					return i, r.limitError()
				}
				continue
			}
			r.started, r.whitespace = true, 0
		}
		r.bytes++
		if r.bytes > r.limit {
			r.exceeded.Store(true)
			return i, r.limitError()
		}
		if r.quoted {
			if r.escaped {
				r.escaped = false
			} else if ch == '\\' {
				r.escaped = true
			} else if ch == '"' {
				r.quoted = false
				if r.depth == 0 {
					r.started, r.bytes = false, 0
				}
			}
			continue
		}
		switch ch {
		case '"':
			r.quoted = true
		case '{', '[':
			r.depth++
		case '}', ']':
			if r.depth > 0 {
				r.depth--
			}
			if r.depth == 0 {
				r.started, r.bytes = false, 0
			}
		}
	}
	return n, err
}

func (r *messageLimitReader) limitError() error {
	return fmt.Errorf("%w (%d bytes)", ErrMessageTooLarge, r.limit)
}
