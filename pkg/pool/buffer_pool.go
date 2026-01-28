// Package pool provides memory pooling utilities to reduce GC pressure
// in high-throughput paths.
package pool

import (
	"sync"
)

const (
	// SmallBufferSize is for small operations (1KB)
	SmallBufferSize = 1024
	// MediumBufferSize is for typical operations (8KB)
	MediumBufferSize = 8 * 1024
	// LargeBufferSize is for large operations (64KB)
	LargeBufferSize = 64 * 1024
)

// ByteBufferPool provides reusable byte slices.
// Use this for temporary buffers that don't need specific sizes.
type ByteBufferPool struct {
	pool sync.Pool
}

// NewByteBufferPool creates a new byte buffer pool.
func NewByteBufferPool() *ByteBufferPool {
	return &ByteBufferPool{
		pool: sync.Pool{
			New: func() any {
				// Default to small buffer size
				b := make([]byte, 0, SmallBufferSize)
				return &b
			},
		},
	}
}

// Get retrieves a byte slice from the pool.
// The slice has 0 length but may have non-zero capacity.
func (p *ByteBufferPool) Get() []byte {
	if p == nil {
		return make([]byte, 0, SmallBufferSize)
	}
	bp := p.pool.Get().(*[]byte)
	b := *bp
	b = b[:0] // Reset length but keep capacity
	return b
}

// Put returns a byte slice to the pool.
// The slice should not be used after this call.
func (p *ByteBufferPool) Put(b []byte) {
	if p == nil || cap(b) == 0 {
		return
	}
	// Only pool reasonably-sized buffers to avoid memory bloat
	if cap(b) > LargeBufferSize*2 {
		return
	}
	p.pool.Put(&b)
}

// SizedBufferPool provides buffers categorized by size class.
// This reduces memory waste when the needed size is known upfront.
type SizedBufferPool struct {
	small  sync.Pool // ~1KB buffers
	medium sync.Pool // ~8KB buffers
	large  sync.Pool // ~64KB buffers
}

// NewSizedBufferPool creates a new sized buffer pool.
func NewSizedBufferPool() *SizedBufferPool {
	return &SizedBufferPool{
		small: sync.Pool{
			New: func() any {
				b := make([]byte, 0, SmallBufferSize)
				return &b
			},
		},
		medium: sync.Pool{
			New: func() any {
				b := make([]byte, 0, MediumBufferSize)
				return &b
			},
		},
		large: sync.Pool{
			New: func() any {
				b := make([]byte, 0, LargeBufferSize)
				return &b
			},
		},
	}
}

// Get retrieves a byte slice with at least the requested capacity.
// It returns a slice from the appropriate size class.
func (p *SizedBufferPool) Get(size int) []byte {
	if p == nil {
		return make([]byte, 0, size)
	}

	var bp *[]byte
	switch {
	case size <= SmallBufferSize:
		bp = p.small.Get().(*[]byte)
	case size <= MediumBufferSize:
		bp = p.medium.Get().(*[]byte)
	default:
		bp = p.large.Get().(*[]byte)
	}

	b := *bp
	// Ensure capacity
	if cap(b) < size {
		b = make([]byte, 0, size)
	}
	b = b[:0] // Reset length
	return b
}

// PutSmall returns a small buffer to the pool.
func (p *SizedBufferPool) PutSmall(b []byte) {
	if p == nil || cap(b) == 0 {
		return
	}
	if cap(b) <= SmallBufferSize {
		p.small.Put(&b)
	}
}

// PutMedium returns a medium buffer to the pool.
func (p *SizedBufferPool) PutMedium(b []byte) {
	if p == nil || cap(b) == 0 {
		return
	}
	if cap(b) <= MediumBufferSize {
		p.medium.Put(&b)
	}
}

// PutLarge returns a large buffer to the pool.
func (p *SizedBufferPool) PutLarge(b []byte) {
	if p == nil || cap(b) == 0 {
		return
	}
	if cap(b) <= LargeBufferSize*2 { // Allow some flexibility for large
		p.large.Put(&b)
	}
}

// Put returns a buffer to the appropriate size class pool.
func (p *SizedBufferPool) Put(b []byte) {
	if p == nil || cap(b) == 0 {
		return
	}
	switch {
	case cap(b) <= SmallBufferSize:
		p.small.Put(&b)
	case cap(b) <= MediumBufferSize:
		p.medium.Put(&b)
	default:
		p.large.Put(&b)
	}
}

// Global pools for convenient access
var (
	// DefaultByteBufferPool is the global byte buffer pool
	DefaultByteBufferPool = NewByteBufferPool()
	// DefaultSizedBufferPool is the global sized buffer pool
	DefaultSizedBufferPool = NewSizedBufferPool()
)

// GetBuffer retrieves a buffer from the default byte buffer pool.
func GetBuffer() []byte {
	return DefaultByteBufferPool.Get()
}

// PutBuffer returns a buffer to the default byte buffer pool.
func PutBuffer(b []byte) {
	DefaultByteBufferPool.Put(b)
}

// GetSizedBuffer retrieves a buffer with at least the requested capacity.
func GetSizedBuffer(size int) []byte {
	return DefaultSizedBufferPool.Get(size)
}

// PutSizedBuffer returns a buffer to the appropriate size class pool.
func PutSizedBuffer(b []byte) {
	DefaultSizedBufferPool.Put(b)
}
