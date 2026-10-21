package mcp

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

func TestMessageLimitReader_BoundariesAndChunking(t *testing.T) {
	value := `{"text":"escaped \\\" quote, {braces} and [brackets] 界","items":[{},[]]}`
	multiline := "{\n\"items\": [\n{},\n{}\n]\n}"
	if !json.Valid([]byte(value)) || !json.Valid([]byte(multiline)) {
		t.Fatal("invalid JSON fixture")
	}
	for _, tc := range []struct {
		name, input string
		limit       int
		rejected    bool
	}{
		{"exact", value, len(value), false},
		{"oversized", value, len(value) - 1, true},
		{"multiple messages", strings.Repeat(value+"\r\n", 100), len(value), false},
		{"multiline", multiline, len(multiline), false},
		{"multiline oversized", multiline, len(multiline) - 1, true},
		{"legacy batch", "[" + value + "," + value + "]", 2*len(value) + 3, false},
		{"unfinished string", `{"text":"` + strings.Repeat("x", 128), 64, true},
		{"whitespace", strings.Repeat(" \r\n\t", 100), 64, true},
		{"empty", "", 64, false},
	} {
		for _, chunk := range []string{"full", "one byte", "half"} {
			t.Run(tc.name+"/"+chunk, func(t *testing.T) {
				var source io.Reader = strings.NewReader(tc.input)
				if chunk == "one byte" {
					source = iotest.OneByteReader(source)
				}
				if chunk == "half" {
					source = iotest.HalfReader(source)
				}
				reader := &messageLimitReader{source: source, limit: tc.limit}
				data, err := io.ReadAll(reader)
				if errors.Is(err, ErrMessageTooLarge) != tc.rejected {
					t.Fatalf("bytes=%d error=%v rejected=%t", len(data), err, tc.rejected)
				}
				if !tc.rejected && (err != nil || string(data) != tc.input) {
					t.Fatalf("stream changed: bytes=%d error=%v", len(data), err)
				}
				if tc.rejected {
					if n, err := reader.Read(make([]byte, 8)); n != 0 || !errors.Is(err, ErrMessageTooLarge) {
						t.Fatal("reader resumed after rejection")
					}
				}
			})
		}
	}
}
