package builtin

import (
	"testing"
)

func TestNewLimitedBuffer(t *testing.T) {
	buf := newLimitedBuffer(100)
	if buf == nil {
		t.Fatal("newLimitedBuffer returned nil")
	}
	if buf.max != 100 {
		t.Errorf("max = %d, want 100", buf.max)
	}
	if buf.truncated {
		t.Error("new buffer should not be truncated")
	}
	if buf.String() != "" {
		t.Error("new buffer should be empty")
	}
}

func TestLimitedBufferWrite(t *testing.T) {
	tests := []struct {
		name           string
		max            int
		writes         []string
		wantContent    string
		wantTruncated  bool
	}{
		{
			name:          "no limit",
			max:           0,
			writes:        []string{"hello", " world"},
			wantContent:   "hello world",
			wantTruncated: false,
		},
		{
			name:          "negative limit (no limit)",
			max:           -1,
			writes:        []string{"hello", " world"},
			wantContent:   "hello world",
			wantTruncated: false,
		},
		{
			name:          "within limit",
			max:           20,
			writes:        []string{"hello", " world"},
			wantContent:   "hello world",
			wantTruncated: false,
		},
		{
			name:          "exact limit",
			max:           11,
			writes:        []string{"hello", " world"},
			wantContent:   "hello world",
			wantTruncated: false,
		},
		{
			name:          "exceeds limit single write",
			max:           5,
			writes:        []string{"hello world"},
			wantContent:   "hello",
			wantTruncated: true,
		},
		{
			name:          "exceeds limit multiple writes",
			max:           8,
			writes:        []string{"hello", " world"},
			wantContent:   "hello wo",
			wantTruncated: true,
		},
		{
			name:          "zero length write",
			max:           10,
			writes:        []string{"", "hello", ""},
			wantContent:   "hello",
			wantTruncated: false,
		},
		{
			name:          "writes after truncation",
			max:           5,
			writes:        []string{"hello", " world", "!"},
			wantContent:   "hello",
			wantTruncated: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			buf := newLimitedBuffer(tc.max)
			for _, s := range tc.writes {
				n, err := buf.Write([]byte(s))
				if err != nil {
					t.Fatalf("Write failed: %v", err)
				}
				// Write should always report all bytes written (no short writes)
				if n != len(s) {
					t.Errorf("Write returned %d, want %d", n, len(s))
				}
			}
			if got := buf.String(); got != tc.wantContent {
				t.Errorf("String() = %q, want %q", got, tc.wantContent)
			}
			if got := buf.Truncated(); got != tc.wantTruncated {
				t.Errorf("Truncated() = %v, want %v", got, tc.wantTruncated)
			}
		})
	}
}

func TestLimitedBufferNilReceiver(t *testing.T) {
	var buf *limitedBuffer

	// Write should not panic and should report bytes written
	n, err := buf.Write([]byte("test"))
	if err != nil {
		t.Errorf("Write on nil receiver returned error: %v", err)
	}
	if n != 4 {
		t.Errorf("Write on nil receiver returned %d, want 4", n)
	}

	// String should not panic and should return empty
	if s := buf.String(); s != "" {
		t.Errorf("String on nil receiver = %q, want empty", s)
	}

	// Truncated should not panic and should return false
	if buf.Truncated() {
		t.Error("Truncated on nil receiver should return false")
	}
}

func TestLimitedBufferBoundaryConditions(t *testing.T) {
	t.Run("write exactly fills buffer", func(t *testing.T) {
		buf := newLimitedBuffer(5)
		buf.Write([]byte("hello"))
		if buf.Truncated() {
			t.Error("should not be truncated when exactly at limit")
		}
		if buf.String() != "hello" {
			t.Errorf("content = %q, want %q", buf.String(), "hello")
		}
	})

	t.Run("write one over limit", func(t *testing.T) {
		buf := newLimitedBuffer(5)
		buf.Write([]byte("hello!"))
		if !buf.Truncated() {
			t.Error("should be truncated when over limit")
		}
		if buf.String() != "hello" {
			t.Errorf("content = %q, want %q", buf.String(), "hello")
		}
	})

	t.Run("remaining equals zero then write", func(t *testing.T) {
		buf := newLimitedBuffer(5)
		buf.Write([]byte("hello"))
		buf.Write([]byte("world"))
		if !buf.Truncated() {
			t.Error("should be truncated after writing when full")
		}
		if buf.String() != "hello" {
			t.Errorf("content = %q, want %q", buf.String(), "hello")
		}
	})
}
