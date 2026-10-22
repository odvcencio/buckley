package execmode

import (
	"bytes"
	"testing"
)

func TestLimitedWriterTruncation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		writes []int
	}{
		{"empty", []int{0}},
		{"exact", []int{maxOutputBytes, 0}},
		{"split_exact", []int{maxOutputBytes - 1, 1, 0}},
		{"overflow", []int{maxOutputBytes + 1}},
		{"after_full", []int{maxOutputBytes, 0, 1, 0}},
		{"repeated", []int{maxOutputBytes / 2, maxOutputBytes / 2, 0, 3}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			writer := &limitedWriter{buf: &buf}
			total := 0
			for _, size := range tc.writes {
				payload := bytes.Repeat([]byte("x"), size)
				n, err := writer.Write(payload)
				total += size
				if err != nil || n != size {
					t.Fatalf("write=%d err=%v want=%d", n, err, size)
				}
				if buf.Len() != min(total, maxOutputBytes) || writer.truncated != (total > maxOutputBytes) {
					t.Fatalf("captured=%d truncated=%v after %d bytes", buf.Len(), writer.truncated, total)
				}
			}
		})
	}
}

func TestLimitedWriter_CustomLimitNotifiesOnce(t *testing.T) {
	var buffer bytes.Buffer
	notifications := 0
	writer := &limitedWriter{buf: &buffer, limit: MaxSourceBytes, onTruncate: func() { notifications++ }}
	if n, err := writer.Write(bytes.Repeat([]byte("x"), MaxSourceBytes)); err != nil || n != MaxSourceBytes || notifications != 0 {
		t.Fatalf("exact write=%d notifications=%d err=%v", n, notifications, err)
	}
	for _, data := range [][]byte{nil, []byte("overflow"), []byte("again")} {
		if n, err := writer.Write(data); err != nil || n != len(data) {
			t.Fatalf("overflow write=%d err=%v", n, err)
		}
	}
	if buffer.Len() != MaxSourceBytes || !writer.truncated || notifications != 1 {
		t.Fatalf("bytes=%d truncated=%v notifications=%d", buffer.Len(), writer.truncated, notifications)
	}
}
