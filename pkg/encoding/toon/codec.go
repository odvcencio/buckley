package toon

import (
	"encoding/json"
	"fmt"

	"github.com/alpkeskin/gotoon"
)

// Codec wraps gotoon serialization with JSON fallback.
type Codec struct {
	useToon bool
}

// New creates a codec that prefers TOON for compact serialization.
func New(useToon bool) *Codec {
	return &Codec{useToon: useToon}
}

// Marshal encodes v into TOON (or JSON when disabled).
func (c *Codec) Marshal(v any) ([]byte, error) {
	if !c.useToon || v == nil {
		return json.Marshal(v)
	}
	encoded, err := gotoon.Encode(v)
	if err != nil {
		return nil, fmt.Errorf("toon encode: %w", err)
	}
	return []byte(encoded), nil
}

// Unmarshal decodes JSON payloads back into Go values. TOON is designed for
// one-way transmission to models, so we always fall back to standard JSON
// parsing when we need to recover data.
func (c *Codec) Unmarshal(data []byte, v any) error {
	return json.Unmarshal(data, v)
}
