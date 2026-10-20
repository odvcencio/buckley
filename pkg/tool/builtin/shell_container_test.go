package builtin

import (
	"bytes"
	"strings"
	"testing"
)

func TestContainerFrames_PreservesStderrAcrossChunkBoundaries(t *testing.T) {
	ready := []byte("\x1etest:ready\x1f")
	done := []byte("\x1etest:done\x1f")
	input := append([]byte("before\n"), ready...)
	input = append(input, []byte("between\x1eunrelated\n")...)
	input = append(input, done...)
	input = append(input, []byte("after\x1e")...)
	for split := 0; split <= len(input); split++ {
		var output bytes.Buffer
		received := 0
		frames := &containerFrames{target: &output, frames: []containerFrame{{text: ready, receive: func() { received++ }}, {text: done, receive: func() { received++ }}}}
		for _, p := range [][]byte{input[:split], input[split:]} {
			if n, err := frames.Write(p); err != nil || n != len(p) {
				t.Fatalf("split=%d write=%d err=%v", split, n, err)
			}
		}
		frames.flush()
		if got := output.String(); got != "before\nbetween\x1eunrelated\nafter\x1e" || received != 2 {
			t.Fatalf("split=%d output=%q frames=%d", split, got, received)
		}
	}
}

func TestContainerFrames_BoundsPendingData(t *testing.T) {
	var output bytes.Buffer
	frame := []byte("\x1eprivate:done\x1f")
	frames := &containerFrames{target: &output, frames: []containerFrame{{text: frame, receive: func() {}}}}
	for _, p := range []string{strings.Repeat("x", 1<<20), "\x1eprivate:", strings.Repeat("y", 1<<20)} {
		if _, err := frames.Write([]byte(p)); err != nil {
			t.Fatal(err)
		}
		if len(frames.pending) >= len(frame) {
			t.Fatalf("unbounded pending data: %d", len(frames.pending))
		}
	}
	frames.flush()
	if output.Len() != (2<<20)+len("\x1eprivate:") {
		t.Fatalf("lost non-frame stderr: %d", output.Len())
	}
}
