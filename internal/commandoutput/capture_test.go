package commandoutput

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCapture_ChunkingAndUTF8(t *testing.T) {
	for _, limit := range []int{1, 2, 3, 24, 25, 128, 2048} {
		for _, input := range []string{"", "short", strings.Repeat("界", 1000) + "final", strings.Repeat("\xff\xfe", 1000) + "final"} {
			t.Run(fmt.Sprintf("limit_%d/bytes_%d", limit, len(input)), func(t *testing.T) {
				var reference string
				for _, chunk := range []int{1, 7, 8192} {
					capture := NewCapture(limit)
					for offset := 0; offset < len(input); offset += chunk {
						part := input[offset:min(offset+chunk, len(input))]
						if n, err := capture.Write([]byte(part)); n != len(part) || err != nil {
							t.Fatalf("write=%d, %v", n, err)
						}
					}
					text, truncated := capture.Finish()
					if len(text) > limit || !utf8.ValidString(text) || capture.TotalBytes() != int64(len(input)) || (len(input) > limit && !truncated) {
						t.Fatalf("limit=%d bytes=%d valid=%t total=%d truncated=%t", limit, len(text), utf8.ValidString(text), capture.TotalBytes(), truncated)
					}
					if len(input) <= limit && utf8.ValidString(input) && (text != input || truncated) {
						t.Fatalf("small output changed: %q", text)
					}
					if chunk == 1 {
						reference = text
					} else if text != reference {
						t.Fatal("output depends on write chunking")
					}
				}
			})
		}
	}
}
