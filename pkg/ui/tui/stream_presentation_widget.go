package tui

import "strings"

type presentationUpdate struct{ id uint64 }

func (presentationUpdate) isMessage() {}

type presentationBinding struct {
	revision      uint64
	seen, evicted bool
}

func (a *WidgetApp) beginAssistantPresentation(session string) uint64 {
	a.streamMu.Lock()
	generation, ok := a.streamGenerationLocked(session)
	if ok {
		ok = a.live.start(session, generation, "assistant", a.presentationEpoch.Load(), 0)
		if ok {
			a.targetedStreams.Store(generation, struct{}{})
		} else {
			delete(a.streamGenerations, session)
			a.streamGenerationBytes -= len(session)
		}
	}
	a.streamMu.Unlock()
	if !ok {
		a.postEvent(streamOverloadMsg{})
		return 0
	}
	a.Post(presentationUpdate{id: generation})
	return generation
}
func (a *WidgetApp) beginReasoningPresentation(session string, beforeID uint64) uint64 {
	id := a.streamSequence.Add(1)
	if !a.live.start(session, id, "thinking", a.presentationEpoch.Load(), beforeID) {
		a.postEvent(streamOverloadMsg{})
		return 0
	}
	return id
}
func (a *WidgetApp) updatePresentation(id uint64, content string) {
	if a.live.replace(id, content) {
		a.Post(presentationUpdate{id: id})
	}
}
func (a *WidgetApp) bindPresentationSession(session string) {
	a.presentationSession = session
	clear(a.presentationBindings)
	for _, item := range a.live.sessionEntries(session) {
		a.renderPresentation(item.id)
	}
}
func (a *WidgetApp) renderPresentation(id uint64) bool {
	snap, ok := a.live.snapshot(id)
	if !ok || snap.session != a.presentationSession {
		return false
	}
	binding := a.presentationBindings[id]
	if snap.accepted && !binding.seen && snap.epoch != a.presentationEpoch.Load() {
		return false
	}
	if binding.evicted {
		return false
	}
	content := snap.content
	if snap.base > 0 {
		content = "[Display shortened]\n" + content
	}
	if !binding.seen {
		if snap.beforeID == 0 || !a.chatView.AddTargetMessageBefore(id, snap.beforeID, content, snap.source) {
			a.chatView.AddTargetMessage(id, content, snap.source)
		}
		binding.seen = true
	} else if snap.revision != binding.revision {
		if !a.chatView.ReplaceTargetText(id, content) {
			binding.evicted = true
		}
	}
	binding.revision = snap.revision
	a.presentationBindings[id] = binding
	a.updateScrollStatus()
	return true
}
func (a *WidgetApp) presentationTargeted(id uint64) bool {
	_, ok := a.targetedStreams.Load(id)
	return ok
}
func (a *WidgetApp) finishPresentation(id uint64) {
	a.live.remove(id)
	a.targetedStreams.Delete(id)
}
func normalizeLiveReasoning(s string) string { return "Thinking\n\n" + strings.TrimSpace(s) }

func (a *WidgetApp) retirePresentation(id uint64) {
	if id != 0 {
		a.live.requestRetire(id)
	}
}
func (a *WidgetApp) sweepPresentations() bool {
	changed := false
	for _, id := range a.live.retirements() {
		if a.renderPresentation(id) {
			changed = true
		}
		a.chatView.EndTarget(id)
		delete(a.presentationBindings, id)
		a.finishPresentation(id)
	}
	return changed
}
