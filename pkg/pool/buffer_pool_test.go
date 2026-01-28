package pool

import (
	"testing"
)

func TestByteBufferPool_Basic(t *testing.T) {
	pool := NewByteBufferPool()

	// Get a buffer
	b1 := pool.Get()
	if len(b1) != 0 {
		t.Errorf("expected empty buffer, got len=%d", len(b1))
	}
	if cap(b1) < SmallBufferSize {
		t.Errorf("expected capacity at least %d, got %d", SmallBufferSize, cap(b1))
	}

	// Use the buffer
	b1 = append(b1, []byte("hello world")...)
	if string(b1) != "hello world" {
		t.Errorf("expected 'hello world', got '%s'", string(b1))
	}

	// Put it back
	pool.Put(b1)

	// Get another buffer - should be from pool
	b2 := pool.Get()
	if len(b2) != 0 {
		t.Errorf("expected empty buffer after Get, got len=%d", len(b2))
	}
	// b2 might be b1 reset, but we can't guarantee it's the same pointer
	// Just verify it works
	b2 = append(b2, []byte("test")...)
	if string(b2) != "test" {
		t.Errorf("expected 'test', got '%s'", string(b2))
	}
}

func TestByteBufferPool_NilPool(t *testing.T) {
	var pool *ByteBufferPool

	// Should not panic
	b := pool.Get()
	if len(b) != 0 {
		t.Errorf("expected empty buffer, got len=%d", len(b))
	}

	// Should not panic
	pool.Put(b)
}

func TestSizedBufferPool_Basic(t *testing.T) {
	pool := NewSizedBufferPool()

	tests := []struct {
		name     string
		size     int
		minCap   int
		poolType string
	}{
		{"small", 100, 100, "small"},
		{"medium", 4 * 1024, 4 * 1024, "medium"},
		{"large", 32 * 1024, 32 * 1024, "large"},
		{"xlarge", 128 * 1024, 128 * 1024, "large"}, // Exceeds large, new allocation
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := pool.Get(tt.size)
			if len(b) != 0 {
				t.Errorf("expected empty buffer, got len=%d", len(b))
			}
			if cap(b) < tt.minCap {
				t.Errorf("expected capacity at least %d, got %d", tt.minCap, cap(b))
			}

			// Use and return
			b = append(b, []byte("data")...)
			pool.Put(b)
		})
	}
}

func TestSizedBufferPool_PutMethods(t *testing.T) {
	pool := NewSizedBufferPool()

	// Small buffer
	small := make([]byte, 0, SmallBufferSize)
	pool.PutSmall(small)

	// Medium buffer
	medium := make([]byte, 0, MediumBufferSize)
	pool.PutMedium(medium)

	// Large buffer
	large := make([]byte, 0, LargeBufferSize)
	pool.PutLarge(large)

	// Very large buffer - should not be pooled
	xlarge := make([]byte, 0, LargeBufferSize*3)
	pool.PutLarge(xlarge) // Should be dropped
}

func TestSizedBufferPool_NilPool(t *testing.T) {
	var pool *SizedBufferPool

	// Should not panic
	b := pool.Get(100)
	if len(b) != 0 {
		t.Errorf("expected empty buffer, got len=%d", len(b))
	}

	// Should not panic
	pool.Put(b)
	pool.PutSmall(b)
	pool.PutMedium(b)
	pool.PutLarge(b)
}

func TestGlobalFunctions(t *testing.T) {
	// Test that global functions work
	b1 := GetBuffer()
	if len(b1) != 0 {
		t.Errorf("expected empty buffer, got len=%d", len(b1))
	}

	b1 = append(b1, []byte("test data")...)
	PutBuffer(b1)

	// Test sized buffer
	b2 := GetSizedBuffer(500)
	if cap(b2) < 500 {
		t.Errorf("expected capacity at least 500, got %d", cap(b2))
	}
	PutSizedBuffer(b2)
}

func BenchmarkByteBufferPool(b *testing.B) {
	pool := NewByteBufferPool()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf := pool.Get()
		buf = append(buf, []byte("benchmark data")...)
		pool.Put(buf)
	}
}

func BenchmarkByteBufferPool_NoPool(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf := make([]byte, 0, SmallBufferSize)
		buf = append(buf, []byte("benchmark data")...)
		_ = buf
	}
}

func BenchmarkSizedBufferPool(b *testing.B) {
	pool := NewSizedBufferPool()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf := pool.Get(1024)
		buf = append(buf, []byte("benchmark data")...)
		pool.Put(buf)
	}
}
