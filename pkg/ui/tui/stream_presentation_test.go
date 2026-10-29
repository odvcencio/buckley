package tui

import (
	"strings"
	"testing"
	"unsafe"
)

func (p *livePresentation) retainedBytes() int { p.mu.Lock(); defer p.mu.Unlock(); return p.bytes }

func TestLivePresentation_ReattachTracksByteOffset(t *testing.T) {
	p := newLivePresentation(4096, 8)
	if !p.start("session", 11, "assistant", 0, 0) {
		t.Fatal("start rejected")
	}
	if !p.append(11, "a界") {
		t.Fatal("append rejected")
	}
	first, ok := p.snapshot(11)
	if !ok || first.content != "a界" || first.offset != 4 {
		t.Fatalf("first = %+v, %v", first, ok)
	}
	if !p.append(11, "b") {
		t.Fatal("second append rejected")
	}
	second, _ := p.snapshot(11)
	if second.content != "a界b" || second.offset != 5 {
		t.Fatalf("second = %+v", second)
	}
	if first.offset >= second.offset {
		t.Fatal("replayed snapshot would mask later bytes")
	}
}

func TestLivePresentation_BoundedTailAndMetadata(t *testing.T) {
	p := newLivePresentation(576, 2)
	if !p.start("s", 1, "assistant", 0, 0) || !p.start("s", 2, "thinking", 0, 0) {
		t.Fatal("start rejected")
	}
	if p.start("s", 3, "assistant", 0, 0) {
		t.Fatal("metadata count cap not enforced")
	}
	if !p.append(1, strings.Repeat("界", 80)) {
		t.Fatal("append rejected")
	}
	snap, _ := p.snapshot(1)
	if !strings.HasSuffix(snap.content, "界") || snap.base == 0 || snap.offset != 240 {
		t.Fatalf("snapshot = %+v", snap)
	}
	if p.retainedBytes() > 576 {
		t.Fatal("byte budget exceeded")
	}
	p.remove(1)
	if _, ok := p.snapshot(1); ok {
		t.Fatal("removed target retained")
	}
	if !p.start("s", 3, "assistant", 0, 0) {
		t.Fatal("removed metadata was not reclaimed")
	}
}

func TestLivePresentation_EmptyCorrectionAndSessionIsolation(t *testing.T) {
	p := newLivePresentation(4096, 8)
	p.start("a", 1, "assistant", 0, 0)
	p.start("b", 2, "assistant", 0, 0)
	p.append(1, "draft")
	p.append(2, "other")
	if !p.replace(1, "") {
		t.Fatal("replace rejected")
	}
	a, _ := p.snapshot(1)
	b, _ := p.snapshot(2)
	if a.content != "" || a.base != 0 || a.offset != 5 || a.revision != 2 || b.content != "other" {
		t.Fatalf("a=%+v b=%+v", a, b)
	}
}

func TestLivePresentation_DoesNotRetainOversizedBacking(t *testing.T) {
	p := newLivePresentation(2000, 2)
	p.start("a", 1, "assistant", 0, 0)
	p.start("b", 2, "assistant", 0, 0)
	p.append(1, strings.Repeat("a", 1400))
	large := strings.Repeat("b", 1<<20)
	chunk := large[:1024]
	p.append(2, chunk)
	snap, ok := p.snapshot(2)
	if !ok || snap.content != chunk {
		t.Fatalf("snapshot missing")
	}
	if unsafe.StringData(snap.content) == unsafe.StringData(chunk) {
		t.Fatal("retained oversized backing")
	}
	if p.retainedBytes() > 2000 {
		t.Fatal("budget exceeded")
	}
}
