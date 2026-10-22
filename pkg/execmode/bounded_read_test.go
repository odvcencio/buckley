package execmode

import (
	"errors"
	"io"
	"math"
	"strings"
	"testing"
	"testing/iotest"
)

func TestReadBoundedFile_SizeChanges(t *testing.T) {
	for _, tc := range []struct {
		name string
		hint int64
		size int
	}{
		{"empty", 0, 0},
		{"stable", 7, 7},
		{"shrunk", 30, 7},
		{"grown", 1, 20},
		{"empty_grown", 0, 40},
		{"past_eof", -10, 0},
		{"exact_limit", 31, 32},
		{"over_limit", 32, 40},
		{"huge_hint", math.MaxInt64, 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := strings.Repeat("x", tc.size)
			result, err := readBoundedFile(strings.NewReader(source), tc.hint, 32)
			if err != nil || string(result) != source[:min(tc.size, 32)] {
				t.Fatalf("bytes=%d err=%v", len(result), err)
			}
		})
	}
}

func TestReadBoundedFile_ReadError(t *testing.T) {
	for _, hint := range []int64{0, 20} {
		reader := io.MultiReader(strings.NewReader("abc"), iotest.ErrReader(io.ErrClosedPipe))
		if _, err := readBoundedFile(reader, hint, 32); !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("hint=%d error=%v", hint, err)
		}
	}
}
