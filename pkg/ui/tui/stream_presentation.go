package tui

import (
	"strings"
	"sync"
	"unicode/utf8"
)

// livePresentation holds bounded, session-scoped source for active bubbles.
// It is independent of the event queue: a replay can use an absolute offset
// and ignore publications that were already represented in the snapshot.
type livePresentation struct {
	mu                        sync.Mutex
	maxBytes, maxCount, bytes int
	retireCount               int
	order                     []uint64
	entries                   map[uint64]*livePresentationEntry
}
type livePresentationEntry struct {
	session, source, content string
	base, offset, revision   uint64
	accepted                 bool
	retire                   bool
	epoch                    uint64
	beforeID                 uint64
}
type livePresentationSnapshot struct {
	session, source, content string
	base, offset, revision   uint64
	accepted                 bool
	epoch                    uint64
	beforeID                 uint64
}

const livePresentationEntryBytes = 256

func newLivePresentation(maxBytes, maxCount int) *livePresentation {
	return &livePresentation{maxBytes: maxBytes, maxCount: maxCount, entries: make(map[uint64]*livePresentationEntry)}
}
func (p *livePresentation) start(session string, id uint64, source string, epoch, beforeID uint64) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	size := livePresentationEntryBytes + len(session) + len(source)
	if id == 0 || p.entries[id] != nil || len(p.entries) >= p.maxCount || size > p.maxBytes-p.bytes {
		return false
	}
	p.entries[id] = &livePresentationEntry{session: strings.Clone(session), source: strings.Clone(source), epoch: epoch, beforeID: beforeID}
	p.order = append(p.order, id)
	p.bytes += size
	return true
}
func (p *livePresentation) append(id uint64, text string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	e := p.entries[id]
	if e == nil {
		return false
	}
	e.offset += uint64(len(text))
	e.revision++
	// At most the global budget can survive, so avoid copying a larger chunk.
	if len(text) > p.maxBytes {
		tail := tailUTF8(text, p.maxBytes)
		e.base += uint64(len(e.content) + len(text) - len(tail))
		p.bytes -= len(e.content)
		e.content = ""
		text = tail
	}
	p.bytes -= len(e.content)
	if e.content == "" {
		e.content = strings.Clone(text)
	} else {
		e.content += text
	}
	p.bytes += len(e.content)
	p.trim()
	return true
}
func (p *livePresentation) replace(id uint64, text string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	e := p.entries[id]
	if e == nil {
		return false
	}
	e.revision++
	e.base = 0
	if len(text) > p.maxBytes {
		n := len(text)
		text = tailUTF8(text, p.maxBytes)
		e.base = uint64(n - len(text))
	}
	p.bytes -= len(e.content)
	e.content = strings.Clone(text)
	p.bytes += len(e.content)
	p.trim()
	return true
}
func (p *livePresentation) trim() {
	for _, id := range p.order {
		if p.bytes <= p.maxBytes {
			return
		}
		e := p.entries[id]
		if e == nil || len(e.content) == 0 {
			continue
		}
		old := len(e.content)
		keep := max(0, old-(p.bytes-p.maxBytes))
		e.content = strings.Clone(tailUTF8(e.content, keep))
		removed := old - len(e.content)
		p.bytes -= removed
		e.base += uint64(removed)
		if removed > 0 {
			e.revision++
		}
	}
}
func tailUTF8(s string, keep int) string {
	if keep >= len(s) {
		return s
	}
	if keep <= 0 {
		return ""
	}
	start := len(s) - keep
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return s[start:]
}
func (p *livePresentation) snapshot(id uint64) (livePresentationSnapshot, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e := p.entries[id]
	if e == nil {
		return livePresentationSnapshot{}, false
	}
	return livePresentationSnapshot{e.session, e.source, e.content, e.base, e.offset, e.revision, e.accepted, e.epoch, e.beforeID}, true
}
func (p *livePresentation) remove(id uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e := p.entries[id]
	if e == nil {
		return
	}
	p.bytes -= livePresentationEntryBytes + len(e.session) + len(e.source) + len(e.content)
	if e.retire {
		p.retireCount--
	}
	delete(p.entries, id)
	for i, v := range p.order {
		if v == id {
			copy(p.order[i:], p.order[i+1:])
			p.order = p.order[:len(p.order)-1]
			break
		}
	}
}

// sessionEntries returns a consistent snapshot in creation order.
func (p *livePresentation) sessionEntries(session string) []livePresentationItem {
	p.mu.Lock()
	defer p.mu.Unlock()
	var result []livePresentationItem
	for _, id := range p.order {
		e := p.entries[id]
		if e != nil && e.session == session && !e.accepted && !e.retire {
			result = append(result, livePresentationItem{id, livePresentationSnapshot{e.session, e.source, e.content, e.base, e.offset, e.revision, e.accepted, e.epoch, e.beforeID}})
		}
	}
	return result
}

type livePresentationItem struct {
	id       uint64
	snapshot livePresentationSnapshot
}

// accept marks a source as represented in the conversation replay.
func (p *livePresentation) accept(id uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if e := p.entries[id]; e != nil {
		e.accepted = true
	}
}

func (p *livePresentation) requestRetire(id uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if e := p.entries[id]; e != nil {
		if !e.retire {
			e.retire = true
			p.retireCount++
		}
	}
}
func (p *livePresentation) retirements() []uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.retireCount == 0 {
		return nil
	}
	var ids []uint64
	for _, id := range p.order {
		if e := p.entries[id]; e != nil && e.retire {
			ids = append(ids, id)
		}
	}
	return ids
}

func (p *livePresentation) clear() {
	p.mu.Lock()
	defer p.mu.Unlock()
	clear(p.entries)
	p.order = nil
	p.bytes = 0
	p.retireCount = 0
}
