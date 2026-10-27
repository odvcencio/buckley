package agentloop

// repeatWindow retains exact counts for only the most recent observations.
// Entries are shared by the map and ring, so forgetting a failed check cannot
// let an older observation decrement a later generation of the same key.
type repeatWindow struct {
	limit   int
	counts  map[string]*repeatCount
	history []*repeatCount
	next    int
}

type repeatCount struct {
	key   string
	count int
}

func newRepeatWindow(limit int) repeatWindow {
	return repeatWindow{limit: limit, counts: make(map[string]*repeatCount)}
}

func (w *repeatWindow) observe(key string) int {
	if len(w.history) == w.limit {
		old := w.history[w.next]
		old.count--
		if old.count == 0 && w.counts[old.key] == old {
			delete(w.counts, old.key)
		}
	}
	entry := w.counts[key]
	if entry == nil {
		entry = &repeatCount{key: key}
		w.counts[key] = entry
	}
	entry.count++
	if len(w.history) < w.limit {
		w.history = append(w.history, entry)
	} else {
		w.history[w.next] = entry
		w.next = (w.next + 1) % w.limit
	}
	return entry.count
}

func (w *repeatWindow) forget(key string) {
	delete(w.counts, key)
}

func (w *repeatWindow) reset() {
	clear(w.counts)
	clear(w.history)
	w.history = w.history[:0]
	w.next = 0
}
