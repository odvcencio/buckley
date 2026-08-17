package tui

import (
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"m31labs.dev/buckley/pkg/ui/widgets"
)

const (
	eventWorkBudget            = 32
	eventOverflowMaxCount      = 512
	eventOverflowMaxBytes      = 4 << 20
	eventCoalescedMaxBytes     = 4 << 20
	eventMessageBaseBytes      = 64
	eventDiagnosticReserveSize = eventMessageBaseBytes
	eventRecoveryReserveCount  = 64
	eventRecoveryReserveBytes  = 512 << 10
	eventShutdownReserveCount  = 1
	eventShutdownReserveBytes  = eventMessageBaseBytes
	eventApprovalMaxCount      = 32
	eventApprovalMaxBytes      = 1 << 20
	retainedEstimateLimit      = eventOverflowMaxBytes + eventCoalescedMaxBytes + 1
	retainedEstimateMaxDepth   = 10
	retainedEstimateMaxNodes   = 4096
)

const (
	widgetAppNew uint32 = iota
	widgetAppRunning
	widgetAppStopped
)

const eventOverloadDiagnostic = "Buckley's UI event queue overloaded. Some events were rejected to keep memory bounded; stop or restart this TUI before relying on the transcript."

type deliveryOverloadMsg struct{}

func (deliveryOverloadMsg) isMessage() {}

type deliveryPriority uint8

const (
	deliveryNormal deliveryPriority = iota
	deliveryProtected
	deliveryInteractive
	deliveryApproval
	deliveryShutdown
)

type queuedEvent struct {
	msg      Message
	bytes    int
	priority deliveryPriority
}

type coalescedKind uint8

const (
	coalescedRefresh coalescedKind = iota
	coalescedResize
	coalescedTick
	coalescedActivities
	coalescedSessionNav
	coalescedStatus
	coalescedProcessStatus
	coalescedTokens
	coalescedModel
	coalescedModelVariant
	coalescedMouse
	coalescedKindCount
)

// EventDeliverySnapshot is a point-in-time view of bounded UI event delivery.
type EventDeliverySnapshot struct {
	Closed                    bool
	Overloaded                bool
	FastQueued                int
	OverflowQueued            int
	OverflowBytes             int
	CoalescedQueued           int
	CoalescedBytes            int
	CoalescedReplacements     uint64
	RejectedAfterClose        uint64
	RejectedOverload          uint64
	RejectedProtected         uint64
	RejectedInteractive       uint64
	RejectedApprovals         uint64
	RejectedState             uint64
	EvictedForPriority        uint64
	DiscardedOnClose          uint64
	OverloadTransitions       uint64
	DiagnosticsQueued         uint64
	DiagnosticsDelivered      uint64
	OutstandingApprovals      int
	ApprovalBytes             int
	ApprovalCancellations     int
	ApprovalCancellationBytes int
	ApprovalsResolved         uint64
	ApprovalsCancelled        uint64
	ApprovalCapRejections     uint64
}

type trackedApproval struct {
	bytes int
}

type approvalCancellation struct {
	requestID     string
	reservedBytes int
}

type eventDeliveryQueue struct {
	mu sync.Mutex

	wake chan struct{}

	overflow      []queuedEvent
	overflowBytes int

	coalesced      [coalescedKindCount]queuedEvent
	coalescedSet   [coalescedKindCount]bool
	coalescedBytes int
	coalescedNext  int

	closed              atomic.Bool
	overloaded          bool
	diagnosticScheduled bool

	coalescedReplacements     uint64
	rejectedAfterClose        uint64
	rejectedOverload          uint64
	rejectedProtected         uint64
	rejectedInteractive       uint64
	rejectedApprovals         uint64
	rejectedState             uint64
	evictedForPriority        uint64
	discardedOnClose          uint64
	overloadTransitions       uint64
	diagnosticsQueued         uint64
	diagnosticsDelivered      uint64
	approvals                 map[string]trackedApproval
	approvalBytes             int
	approvalCancellations     []approvalCancellation
	approvalCancellationSet   map[string]struct{}
	approvalCancellationBytes int
	approvalsResolved         uint64
	approvalsCancelled        uint64
	approvalCapRejections     uint64
}

func newEventDeliveryQueue() eventDeliveryQueue {
	return eventDeliveryQueue{
		wake:                    make(chan struct{}, 1),
		approvals:               make(map[string]trackedApproval),
		approvalCancellationSet: make(map[string]struct{}),
	}
}

// Post is safe for concurrent producers and never waits for queue capacity.
func (a *WidgetApp) Post(msg Message) {
	if a == nil || msg == nil {
		return
	}
	if req, ok := msg.(ApprovalRequestMsg); ok {
		a.postApprovalRequest(req)
		return
	}
	if a.delivery.closed.Load() {
		a.recordPostAfterClose()
		return
	}

	switch m := msg.(type) {
	case StreamChunk:
		if a.coalescer != nil {
			m.SessionID = strings.Clone(m.SessionID)
			a.coalescer.AddStream(m.SessionID, m.Generation, m.Text)
		}
		return
	case StreamDone:
		if a.coalescer != nil {
			m.SessionID = strings.Clone(m.SessionID)
			m.FullText = ""
			a.coalescer.FlushAndPostStream(m.SessionID, m.Generation, m)
			return
		}
	}
	a.postEvent(msg)
}

// postCoalescerPublication bypasses StreamDone sequencing because the
// coalescer has already ordered this publication.
func (a *WidgetApp) postCoalescerPublication(msg Message) {
	a.postEvent(msg)
}

func (a *WidgetApp) postEvent(msg Message) bool {
	if a == nil || msg == nil {
		return false
	}
	if a.delivery.closed.Load() {
		a.recordPostAfterClose()
		return false
	}
	preflight := msg
	if result, ok := msg.(ToolResult); ok && result.Err != nil {
		result.Err = retainedToolError{}
		preflight = result
	}
	preparedOK := retainedMessageBytes(preflight) < retainedEstimateLimit
	prepared := msg
	if preparedOK {
		prepared, preparedOK = prepareMessageForRetention(msg)
	}

	q := &a.delivery
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed.Load() {
		q.rejectedAfterClose++
		return false
	}
	if !preparedOK {
		q.enterOverloadLocked()
		q.rejectLocked(retainedEstimateLimit, messageDeliveryPriority(msg))
		return false
	}
	msg = prepared
	if approval, ok := msg.(ApprovalRequestMsg); ok {
		size := retainedMessageBytes(approval)
		_, duplicate := q.approvals[approval.ID]
		_, cancellationDuplicate := q.approvalCancellationSet[approval.ID]
		if duplicate || cancellationDuplicate ||
			len(q.approvals)+len(q.approvalCancellations) >= eventApprovalMaxCount ||
			size > eventApprovalMaxBytes-q.approvalBytes-q.approvalCancellationBytes {
			q.approvalCapRejections++
			q.enterOverloadLocked()
			q.rejectLocked(size, deliveryApproval)
			return false
		}
	}

	if kind, ok := coalescedMessageKind(msg); ok {
		size := retainedMessageBytes(msg)
		if size > eventCoalescedMaxBytes {
			return q.storeCoalescedLocked(kind, msg, size)
		}
		if len(q.overflow) == 0 && !q.coalescedSet[kind] {
			select {
			case a.messages <- msg:
				return true
			default:
			}
		}
		return q.storeCoalescedLocked(kind, msg, size)
	}
	accepted := q.postCriticalLocked(a.messages, msg)
	if accepted {
		if approval, ok := msg.(ApprovalRequestMsg); ok {
			size := retainedMessageBytes(approval)
			q.approvals[approval.ID] = trackedApproval{bytes: size}
			q.approvalBytes += size
		}
	}
	return accepted
}

func (a *WidgetApp) postApprovalRequest(req ApprovalRequestMsg) bool {
	accepted := a.postEvent(req)
	if accepted {
		return true
	}
	if a != nil {
		a.delivery.mu.Lock()
		a.delivery.rejectedApprovals++
		a.delivery.mu.Unlock()
		if a.onApproval != nil {
			a.onApproval(req.ID, false, false)
		}
	}
	return false
}

func (a *WidgetApp) resolveApproval(requestID string, approved, alwaysAllow bool) bool {
	if a == nil {
		return false
	}
	q := &a.delivery
	q.mu.Lock()
	tracked, ok := q.approvals[requestID]
	if !ok {
		q.mu.Unlock()
		return false
	}
	delete(q.approvals, requestID)
	q.approvalBytes -= tracked.bytes
	q.approvalsResolved++
	callback := a.onApproval
	q.mu.Unlock()
	if callback != nil {
		callback(requestID, approved, alwaysAllow)
	}
	return true
}

// CancelApproval releases one outstanding approval without invoking the
// decision callback. The broker that won cancellation already owns waiter
// notification; suppressing the callback prevents duplicate resolution.
// Any retained request message becomes stale and is skipped before display.
func (a *WidgetApp) CancelApproval(requestID string) bool {
	if a == nil || requestID == "" {
		return false
	}
	q := &a.delivery
	q.mu.Lock()
	tracked, ok := q.approvals[requestID]
	if ok {
		delete(q.approvals, requestID)
		q.approvalBytes -= tracked.bytes
		q.approvalsCancelled++
		requestID = strings.Clone(requestID)
		if _, queued := q.approvalCancellationSet[requestID]; !queued {
			q.approvalCancellations = append(q.approvalCancellations, approvalCancellation{
				requestID: requestID, reservedBytes: tracked.bytes,
			})
			q.approvalCancellationSet[requestID] = struct{}{}
			q.approvalCancellationBytes += tracked.bytes
			q.signalLocked()
		}
	}
	q.mu.Unlock()
	return ok
}

func (a *WidgetApp) approvalOutstanding(requestID string) bool {
	if a == nil || requestID == "" {
		return false
	}
	a.delivery.mu.Lock()
	defer a.delivery.mu.Unlock()
	_, ok := a.delivery.approvals[requestID]
	return ok
}

func (a *WidgetApp) recordPostAfterClose() {
	a.delivery.mu.Lock()
	a.delivery.rejectedAfterClose++
	a.delivery.mu.Unlock()
}

func (q *eventDeliveryQueue) postCriticalLocked(fast chan Message, msg Message) bool {
	priority := messageDeliveryPriority(msg)
	msg = normalizeOverflowMessage(msg)
	event := queuedEvent{msg: msg, bytes: retainedMessageBytes(msg), priority: priority}
	if event.bytes > eventOverflowMaxBytes-eventDiagnosticReserveSize {
		q.enterOverloadLocked()
		q.rejectLocked(event.bytes, priority)
		return false
	}
	if q.overloaded && priority == deliveryNormal {
		q.rejectLocked(event.bytes, priority)
		return false
	}

	if len(q.overflow) == 0 {
		select {
		case fast <- msg:
			return true
		default:
		}
	}

	if q.fitsOverflowLocked(event) {
		q.appendOverflowLocked(event)
		return true
	}

	q.enterOverloadLocked()
	if priority >= deliveryProtected {
		for !q.fitsOverflowLocked(event) && q.evictOverflowLocked(priority) {
		}
		if q.fitsOverflowLocked(event) {
			q.appendOverflowLocked(event)
			return true
		}
	}
	q.rejectLocked(event.bytes, priority)
	return false
}

func normalizeOverflowMessage(msg Message) Message {
	if done, ok := msg.(StreamDone); ok {
		done.FullText = ""
		return done
	}
	return msg
}

func (q *eventDeliveryQueue) fitsOverflowLocked(event queuedEvent) bool {
	countLimit := eventOverflowMaxCount
	byteLimit := eventOverflowMaxBytes
	if !q.diagnosticScheduled {
		countLimit--
		byteLimit -= eventDiagnosticReserveSize
	}
	if event.priority <= deliveryProtected {
		countLimit -= eventRecoveryReserveCount
		byteLimit -= eventRecoveryReserveBytes
	}
	if event.priority < deliveryShutdown {
		countLimit -= eventShutdownReserveCount
		byteLimit -= eventShutdownReserveBytes
	}
	return len(q.overflow)+1 <= countLimit && q.overflowBytes+event.bytes <= byteLimit
}

func (q *eventDeliveryQueue) appendOverflowLocked(event queuedEvent) {
	q.overflow = append(q.overflow, event)
	q.overflowBytes += event.bytes
	q.signalLocked()
}

func (q *eventDeliveryQueue) enterOverloadLocked() {
	if !q.overloaded {
		q.overloaded = true
		q.overloadTransitions++
	}
	if q.diagnosticScheduled {
		return
	}
	q.diagnosticScheduled = true
	q.diagnosticsQueued++
	q.appendOverflowLocked(queuedEvent{
		msg:      deliveryOverloadMsg{},
		bytes:    eventDiagnosticReserveSize,
		priority: deliveryApproval,
	})
}

func (q *eventDeliveryQueue) rejectLocked(size int, priority deliveryPriority) {
	q.rejectedOverload++
	if priority >= deliveryProtected {
		q.rejectedProtected++
	}
	if priority >= deliveryInteractive {
		q.rejectedInteractive++
	}
	_ = size
}

func (q *eventDeliveryQueue) evictOverflowLocked(incoming deliveryPriority) bool {
	for i, event := range q.overflow {
		if _, diagnostic := event.msg.(deliveryOverloadMsg); diagnostic {
			continue
		}
		if _, diagnostic := event.msg.(streamOverloadMsg); diagnostic {
			continue
		}
		if _, approval := event.msg.(ApprovalRequestMsg); approval {
			continue
		}
		if incoming == deliveryApproval && event.priority >= deliveryInteractive {
			continue
		}
		if event.priority >= incoming {
			continue
		}
		q.overflowBytes -= event.bytes
		copy(q.overflow[i:], q.overflow[i+1:])
		last := len(q.overflow) - 1
		q.overflow[last] = queuedEvent{}
		q.overflow = q.overflow[:last]
		q.evictedForPriority++
		q.rejectedOverload++
		if event.priority >= deliveryProtected {
			q.rejectedProtected++
		}
		if event.priority >= deliveryInteractive {
			q.rejectedInteractive++
		}
		return true
	}
	return false
}

func (q *eventDeliveryQueue) storeCoalescedLocked(kind coalescedKind, msg Message, size int) bool {
	oldSize := 0
	if q.coalescedSet[kind] {
		oldSize = q.coalesced[kind].bytes
	}
	if size > eventCoalescedMaxBytes || q.coalescedBytes-oldSize+size > eventCoalescedMaxBytes {
		if q.coalescedSet[kind] {
			q.coalescedBytes -= oldSize
			q.coalesced[kind] = queuedEvent{}
			q.coalescedSet[kind] = false
		}
		q.rejectedState++
		q.enterOverloadLocked()
		return false
	}
	if q.coalescedSet[kind] {
		q.coalescedBytes -= oldSize
		q.coalescedReplacements++
	}
	q.coalesced[kind] = queuedEvent{msg: msg, bytes: size}
	q.coalescedSet[kind] = true
	q.coalescedBytes += size
	q.signalLocked()
	return true
}

func (q *eventDeliveryQueue) signalLocked() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (a *WidgetApp) takeOverflowEvent() (Message, bool) {
	q := &a.delivery
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.overflow) == 0 || len(a.messages) != 0 {
		return nil, false
	}
	event := q.overflow[0]
	q.overflow[0] = queuedEvent{}
	q.overflow = q.overflow[1:]
	if len(q.overflow) == 0 {
		q.overflow = nil
	}
	q.overflowBytes -= event.bytes
	return event.msg, true
}

func (a *WidgetApp) takeCoalescedEvent() (Message, bool) {
	q := &a.delivery
	q.mu.Lock()
	defer q.mu.Unlock()
	for i := 0; i < int(coalescedKindCount); i++ {
		idx := (q.coalescedNext + i) % int(coalescedKindCount)
		if !q.coalescedSet[idx] {
			continue
		}
		event := q.coalesced[idx]
		q.coalesced[idx] = queuedEvent{}
		q.coalescedSet[idx] = false
		q.coalescedBytes -= event.bytes
		q.coalescedNext = (idx + 1) % int(coalescedKindCount)
		return event.msg, true
	}
	return nil, false
}

func (a *WidgetApp) takeCriticalEvent() (Message, bool) {
	for {
		if msg, ok := a.takeApprovalCancellation(); ok {
			return msg, true
		}
		select {
		case msg := <-a.messages:
			if approval, ok := msg.(ApprovalRequestMsg); ok && !a.approvalOutstanding(approval.ID) {
				continue
			}
			return msg, true
		default:
		}
		msg, ok := a.takeOverflowEvent()
		if !ok {
			return nil, false
		}
		if approval, approvalMessage := msg.(ApprovalRequestMsg); approvalMessage && !a.approvalOutstanding(approval.ID) {
			continue
		}
		return msg, true
	}
}

func (a *WidgetApp) takeApprovalCancellation() (Message, bool) {
	q := &a.delivery
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.approvalCancellations) == 0 {
		return nil, false
	}
	cancellation := q.approvalCancellations[0]
	q.approvalCancellations[0] = approvalCancellation{}
	q.approvalCancellations = q.approvalCancellations[1:]
	if len(q.approvalCancellations) == 0 {
		q.approvalCancellations = nil
	}
	delete(q.approvalCancellationSet, cancellation.requestID)
	q.approvalCancellationBytes -= cancellation.reservedBytes
	return approvalCancelledMsg{RequestID: cancellation.requestID}, true
}

func (a *WidgetApp) hasCoalescedWork() bool {
	a.delivery.mu.Lock()
	defer a.delivery.mu.Unlock()
	for _, pending := range a.delivery.coalescedSet {
		if pending {
			return true
		}
	}
	return false
}

// drainEventWork applies at most budget messages and always returns control to
// the event-loop select. One slot is reserved for latest-state progress.
func (a *WidgetApp) drainEventWork(budget int) int {
	if budget <= 0 || !a.isRunning() {
		return 0
	}
	processed := 0
	criticalBudget := budget
	if a.hasCoalescedWork() {
		criticalBudget--
	}
	for processed < criticalBudget && a.isRunning() {
		msg, ok := a.takeCriticalEvent()
		if !ok {
			break
		}
		a.processMessage(msg)
		processed++
	}
	if processed < budget && a.isRunning() {
		if msg, ok := a.takeCoalescedEvent(); ok {
			a.processMessage(msg)
			processed++
		}
	}
	a.resignalEventWork()
	return processed
}

func (a *WidgetApp) resignalEventWork() {
	q := &a.delivery
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed.Load() {
		return
	}
	if len(q.approvalCancellations) > 0 {
		q.signalLocked()
		return
	}
	if len(q.overflow) > 0 {
		q.signalLocked()
		return
	}
	for _, pending := range q.coalescedSet {
		if pending {
			q.signalLocked()
			return
		}
	}
}

func (a *WidgetApp) processMessage(msg Message) {
	if a.update(msg) {
		a.dirty = true
	}
}

func (a *WidgetApp) processReadyFrame() bool {
	if a.frameTicker == nil {
		return false
	}
	select {
	case now := <-a.frameTicker.C:
		a.processFrameTick(now)
		return true
	default:
		return false
	}
}

func (a *WidgetApp) processFrameTick(now time.Time) {
	if a.coalescer != nil {
		a.coalescer.Tick()
	}
	if a.updateAnimations(now) {
		a.dirty = true
	}
	if a.dirty {
		a.render()
		a.dirty = false
	}
}

func (a *WidgetApp) closeEventDelivery() {
	if a == nil || !a.delivery.closed.CompareAndSwap(false, true) {
		return
	}
	a.streamMu.Lock()
	clear(a.streamGenerations)
	a.streamGenerationBytes = 0
	a.streamMu.Unlock()
	a.closeModelPickerState()
	a.closeApprovalLayerState()
	q := &a.delivery
	q.mu.Lock()
	cancelledApprovals := make([]string, 0, len(q.approvals))
	for requestID := range q.approvals {
		cancelledApprovals = append(cancelledApprovals, requestID)
		delete(q.approvals, requestID)
	}
	q.approvalBytes = 0
	q.discardedOnClose += uint64(len(q.approvalCancellations))
	for i := range q.approvalCancellations {
		q.approvalCancellations[i] = approvalCancellation{}
	}
	q.approvalCancellations = nil
	clear(q.approvalCancellationSet)
	q.approvalCancellationBytes = 0
	q.approvalsCancelled += uint64(len(cancelledApprovals))
	approvalCallback := a.onApproval
	q.discardedOnClose += uint64(len(q.overflow))
	for i := range q.overflow {
		q.overflow[i] = queuedEvent{}
	}
	q.overflow = nil
	q.overflowBytes = 0
	for i := range q.coalesced {
		if q.coalescedSet[i] {
			q.discardedOnClose++
		}
		q.coalesced[i] = queuedEvent{}
		q.coalescedSet[i] = false
	}
	q.coalescedBytes = 0
	q.mu.Unlock()
	for _, requestID := range cancelledApprovals {
		if approvalCallback != nil {
			approvalCallback(requestID, false, false)
		}
	}

	for {
		select {
		case <-a.messages:
			q.mu.Lock()
			q.discardedOnClose++
			q.mu.Unlock()
		default:
			if a.coalescer != nil {
				a.coalescer.Close()
			}
			return
		}
	}
}

// EventDeliverySnapshot returns bounded-queue state and overload counters.
func (a *WidgetApp) EventDeliverySnapshot() EventDeliverySnapshot {
	if a == nil {
		return EventDeliverySnapshot{Closed: true}
	}
	q := &a.delivery
	q.mu.Lock()
	defer q.mu.Unlock()
	coalescedCount := 0
	for _, pending := range q.coalescedSet {
		if pending {
			coalescedCount++
		}
	}
	return EventDeliverySnapshot{
		Closed:                    q.closed.Load(),
		Overloaded:                q.overloaded,
		FastQueued:                len(a.messages),
		OverflowQueued:            len(q.overflow),
		OverflowBytes:             q.overflowBytes,
		CoalescedQueued:           coalescedCount,
		CoalescedBytes:            q.coalescedBytes,
		CoalescedReplacements:     q.coalescedReplacements,
		RejectedAfterClose:        q.rejectedAfterClose,
		RejectedOverload:          q.rejectedOverload,
		RejectedProtected:         q.rejectedProtected,
		RejectedInteractive:       q.rejectedInteractive,
		RejectedApprovals:         q.rejectedApprovals,
		RejectedState:             q.rejectedState,
		EvictedForPriority:        q.evictedForPriority,
		DiscardedOnClose:          q.discardedOnClose,
		OverloadTransitions:       q.overloadTransitions,
		DiagnosticsQueued:         q.diagnosticsQueued,
		DiagnosticsDelivered:      q.diagnosticsDelivered,
		OutstandingApprovals:      len(q.approvals),
		ApprovalBytes:             q.approvalBytes,
		ApprovalCancellations:     len(q.approvalCancellations),
		ApprovalCancellationBytes: q.approvalCancellationBytes,
		ApprovalsResolved:         q.approvalsResolved,
		ApprovalsCancelled:        q.approvalsCancelled,
		ApprovalCapRejections:     q.approvalCapRejections,
	}
}

func (a *WidgetApp) markOverloadDiagnosticDelivered() {
	a.delivery.mu.Lock()
	a.delivery.diagnosticsDelivered++
	a.delivery.mu.Unlock()
}

func coalescedMessageKind(msg Message) (coalescedKind, bool) {
	switch msg.(type) {
	case RefreshMsg:
		return coalescedRefresh, true
	case ResizeMsg:
		return coalescedResize, true
	case TickMsg:
		return coalescedTick, true
	case SetActivitiesMsg:
		return coalescedActivities, true
	case SessionNavMsg:
		return coalescedSessionNav, true
	case StatusMsg:
		return coalescedStatus, true
	case ProcessStatusMsg:
		return coalescedProcessStatus, true
	case TokensMsg:
		return coalescedTokens, true
	case ModelMsg:
		return coalescedModel, true
	case ModelVariantMsg:
		return coalescedModelVariant, true
	case MouseMsg:
		return coalescedMouse, true
	default:
		return 0, false
	}
}

func messageDeliveryPriority(msg Message) deliveryPriority {
	switch m := msg.(type) {
	case QuitMsg:
		return deliveryShutdown
	case ApprovalRequestMsg:
		return deliveryApproval
	case approvalCancelledMsg:
		return deliveryApproval
	case KeyMsg, PasteMsg, SubmitMsg:
		return deliveryInteractive
	case streamOverloadMsg, streamBoundaryRejectedMsg:
		return deliveryApproval
	case StreamFlush, StreamDone, AddMessageMsg, AppendMsg, ReplaceLastMessageMsg:
		return deliveryProtected
	case ToolResult:
		if m.Err != nil {
			return deliveryProtected
		}
	}
	return deliveryNormal
}

func retainedMessageBytes(msg Message) int {
	size := eventMessageBaseBytes
	addBytes := func(value int) {
		if value < 0 || size > retainedEstimateLimit-value {
			size = retainedEstimateLimit
			return
		}
		size += value
	}
	add := func(values ...string) {
		for _, value := range values {
			addBytes(len(value))
		}
	}
	addDynamic := func(value any) {
		remaining := retainedEstimateLimit - size
		if remaining <= 0 {
			size = retainedEstimateLimit
			return
		}
		addBytes(dynamicRetainedBytes(value, remaining))
	}
	switch m := msg.(type) {
	case StreamChunk:
		add(m.SessionID, m.Text)
	case StreamFlush:
		add(m.SessionID, m.Text)
	case StreamDone:
		add(m.SessionID, m.FullText)
	case PasteMsg:
		add(m.Text)
	case StatusMsg:
		add(m.Text)
	case ProcessStatusMsg:
		add(m.Text)
	case ModelMsg:
		add(m.Name)
	case ModelVariantMsg:
		add(m.Name)
	case AddMessageMsg:
		add(m.Content, m.Source)
	case AppendMsg:
		add(m.Text)
	case ReplaceLastMessageMsg:
		add(m.Content)
	case OverlayMsg:
		add(m.Name)
	case ModeChangeMsg:
		add(m.Mode)
	case SubmitMsg:
		add(m.Text)
	case ToolStart:
		add(m.ToolID, m.ToolName)
		addDynamic(m.Args)
	case ToolResult:
		add(m.ToolID)
		addDynamic(m.Result)
		if m.Err != nil {
			addDynamic(m.Err)
		}
	case ModelPickerMsg:
		addDynamic(m.Items)
	case SetActivitiesMsg:
		addDynamic(m.Records)
	case SessionNavMsg:
		addDynamic(m.Nodes)
	case ApprovalRequestMsg:
		add(m.ID, m.Tool, m.Operation, m.Description, m.Command, m.FilePath)
		addDynamic(m.DiffLines)
	case approvalCancelledMsg:
		add(m.RequestID)
	case deliveryOverloadMsg:
		add(eventOverloadDiagnostic)
	case streamOverloadMsg:
		add(streamOverloadDiagnostic)
	case streamBoundaryRejectedMsg:
		add(streamIncompleteDiagnostic)
	}
	return size
}

type retainedToolError struct{}

func (retainedToolError) Error() string { return "tool result failed" }

func prepareMessageForRetention(msg Message) (Message, bool) {
	clone := strings.Clone
	switch m := msg.(type) {
	case StreamChunk:
		m.SessionID, m.Text = clone(m.SessionID), clone(m.Text)
		return m, true
	case StreamFlush:
		m.SessionID, m.Text = clone(m.SessionID), clone(m.Text)
		return m, true
	case StreamDone:
		m.SessionID, m.FullText = clone(m.SessionID), clone(m.FullText)
		return m, true
	case PasteMsg:
		m.Text = clone(m.Text)
		return m, true
	case StatusMsg:
		m.Text = clone(m.Text)
		return m, true
	case ProcessStatusMsg:
		m.Text = clone(m.Text)
		return m, true
	case ModelMsg:
		m.Name = clone(m.Name)
		return m, true
	case ModelVariantMsg:
		m.Name = clone(m.Name)
		return m, true
	case AddMessageMsg:
		m.Content, m.Source = clone(m.Content), clone(m.Source)
		return m, true
	case AppendMsg:
		m.Text = clone(m.Text)
		return m, true
	case ReplaceLastMessageMsg:
		m.Content = clone(m.Content)
		return m, true
	case OverlayMsg:
		m.Name = clone(m.Name)
		return m, true
	case ModeChangeMsg:
		m.Mode = clone(m.Mode)
		return m, true
	case SubmitMsg:
		m.Text = clone(m.Text)
		return m, true
	case ToolStart:
		m.ToolID, m.ToolName = clone(m.ToolID), clone(m.ToolName)
		args, ok := detachStringBackings(m.Args)
		if !ok {
			return nil, false
		}
		if args != nil {
			m.Args = args.(map[string]any)
		}
		return m, true
	case ToolResult:
		m.ToolID = clone(m.ToolID)
		result, ok := detachStringBackings(m.Result)
		if !ok {
			return nil, false
		}
		m.Result = result
		if m.Err != nil {
			m.Err = retainedToolError{}
		}
		return m, true
	case ModelPickerMsg:
		if m.Action == ModelPickerActionNone || m.Action > ModelPickerActionToggleCurated || len(m.Items) > retainedEstimateMaxNodes {
			return nil, false
		}
		items := make([]widgets.PaletteItem, len(m.Items))
		for i, item := range m.Items {
			data, ok := detachStringBackings(item.Data)
			if !ok {
				return nil, false
			}
			item.ID = clone(item.ID)
			item.Category = clone(item.Category)
			item.Label = clone(item.Label)
			item.Description = clone(item.Description)
			item.Shortcut = clone(item.Shortcut)
			item.Data = data
			items[i] = item
		}
		m.Items = items
		return m, true
	case SetActivitiesMsg:
		if len(m.Records) > retainedEstimateMaxNodes {
			return nil, false
		}
		records := make([]widgets.ActivityRecord, len(m.Records))
		copy(records, m.Records)
		for i := range records {
			records[i].ID = clone(records[i].ID)
			records[i].Kind = clone(records[i].Kind)
			records[i].Title = clone(records[i].Title)
			records[i].Summary = clone(records[i].Summary)
			records[i].Detail = clone(records[i].Detail)
			records[i].Path = clone(records[i].Path)
			records[i].Operation = clone(records[i].Operation)
			records[i].Status = widgets.ActivityStatus(clone(string(records[i].Status)))
		}
		m.Records = records
		return m, true
	case SessionNavMsg:
		if len(m.Nodes) > retainedEstimateMaxNodes {
			return nil, false
		}
		nodes := make([]widgets.SessionNavNode, len(m.Nodes))
		copy(nodes, m.Nodes)
		for i := range nodes {
			nodes[i].ID = clone(nodes[i].ID)
			nodes[i].Label = clone(nodes[i].Label)
			nodes[i].Status = clone(nodes[i].Status)
		}
		m.Nodes = nodes
		return m, true
	case ApprovalRequestMsg:
		if len(m.DiffLines) > retainedEstimateMaxNodes {
			return nil, false
		}
		m.ID = clone(m.ID)
		m.Tool = clone(m.Tool)
		m.Operation = clone(m.Operation)
		m.Description = clone(m.Description)
		m.Command = clone(m.Command)
		m.FilePath = clone(m.FilePath)
		lines := make([]DiffLine, len(m.DiffLines))
		copy(lines, m.DiffLines)
		for i := range lines {
			lines[i].Content = clone(lines[i].Content)
		}
		m.DiffLines = lines
		return m, true
	case approvalCancelledMsg:
		m.RequestID = clone(m.RequestID)
		return m, true
	case deliveryOverloadMsg, streamOverloadMsg, streamBoundaryRejectedMsg,
		KeyMsg, ResizeMsg, TickMsg, QuitMsg, RefreshMsg, TokensMsg, ThinkingMsg, MouseMsg:
		return msg, true
	default:
		if retainedTypeHasReferences(reflect.TypeOf(msg)) {
			return nil, false
		}
		return msg, true
	}
}

type detachVisit struct {
	kind reflect.Kind
	ptr  uintptr
}

type detachState struct {
	seen  map[detachVisit]any
	nodes int
}

func detachStringBackings(value any) (any, bool) {
	state := detachState{seen: make(map[detachVisit]any)}
	return state.clone(value, 0)
}

func (s *detachState) clone(value any, depth int) (any, bool) {
	if value == nil {
		return nil, true
	}
	s.nodes++
	if depth > retainedEstimateMaxDepth || s.nodes > retainedEstimateMaxNodes {
		return nil, false
	}
	switch typed := value.(type) {
	case string:
		if len(typed) > eventOverflowMaxBytes {
			return nil, false
		}
		return strings.Clone(typed), true
	case []byte:
		if len(typed) > eventOverflowMaxBytes {
			return nil, false
		}
		result := make([]byte, len(typed))
		copy(result, typed)
		return result, true
	case []string:
		if len(typed) > retainedEstimateMaxNodes {
			return nil, false
		}
		result := make([]string, len(typed))
		for i, item := range typed {
			result[i] = strings.Clone(item)
		}
		return result, true
	case []any:
		if len(typed) > retainedEstimateMaxNodes {
			return nil, false
		}
		visit := detachVisit{kind: reflect.Slice, ptr: reflect.ValueOf(typed).Pointer()}
		if prior, ok := s.seen[visit]; ok {
			return prior, true
		}
		result := make([]any, len(typed))
		s.seen[visit] = result
		for i, item := range typed {
			cloned, ok := s.clone(item, depth+1)
			if !ok {
				return nil, false
			}
			result[i] = cloned
		}
		return result, true
	case map[string]string:
		if len(typed) > retainedEstimateMaxNodes {
			return nil, false
		}
		result := make(map[string]string, len(typed))
		for key, item := range typed {
			result[strings.Clone(key)] = strings.Clone(item)
		}
		return result, true
	case map[string]any:
		if len(typed) > retainedEstimateMaxNodes {
			return nil, false
		}
		visit := detachVisit{kind: reflect.Map, ptr: reflect.ValueOf(typed).Pointer()}
		if prior, ok := s.seen[visit]; ok {
			return prior, true
		}
		result := make(map[string]any, len(typed))
		s.seen[visit] = result
		for key, item := range typed {
			cloned, ok := s.clone(item, depth+1)
			if !ok {
				return nil, false
			}
			result[strings.Clone(key)] = cloned
		}
		return result, true
	case bool,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64, uintptr,
		float32, float64, complex64, complex128:
		return typed, true
	default:
		return nil, false
	}
}

type retainedVisit struct {
	typ  reflect.Type
	kind reflect.Kind
	ptr  uintptr
}

type retainedSizer struct {
	limit int
	total int
	nodes int
	seen  map[retainedVisit]struct{}
}

func dynamicRetainedBytes(value any, limit int) int {
	if value == nil || limit <= 0 {
		return 0
	}
	sizer := retainedSizer{limit: limit, seen: make(map[retainedVisit]struct{})}
	sizer.walk(reflect.ValueOf(value), 0)
	return sizer.total
}

func (s *retainedSizer) add(size int) {
	if s.total > s.limit {
		return
	}
	if size < 0 || size > s.limit-s.total {
		s.total = s.limit + 1
		return
	}
	s.total += size
}

func (s *retainedSizer) saturate() {
	s.total = s.limit + 1
}

func (s *retainedSizer) walk(value reflect.Value, depth int) {
	if !value.IsValid() || s.total > s.limit {
		return
	}
	s.nodes++
	if depth > retainedEstimateMaxDepth || s.nodes > retainedEstimateMaxNodes {
		s.saturate()
		return
	}

	typ := value.Type()
	switch value.Kind() {
	case reflect.Interface:
		s.add(int(typ.Size()))
		if !value.IsNil() {
			s.walk(value.Elem(), depth+1)
		}
	case reflect.String:
		s.add(int(typ.Size()))
		s.add(value.Len())
	case reflect.Pointer:
		s.add(int(typ.Size()))
		if value.IsNil() || s.seenValue(value) {
			return
		}
		s.walk(value.Elem(), depth+1)
	case reflect.Slice:
		s.add(int(typ.Size()))
		if value.IsNil() || s.seenValue(value) {
			return
		}
		s.addProduct(value.Cap(), int(typ.Elem().Size()))
		if retainedTypeHasReferences(typ.Elem()) {
			if value.Len() > retainedEstimateMaxNodes-s.nodes {
				s.saturate()
				return
			}
			for i := 0; i < value.Len(); i++ {
				s.walk(value.Index(i), depth+1)
			}
		}
	case reflect.Map:
		s.add(int(typ.Size()))
		if value.IsNil() || s.seenValue(value) {
			return
		}
		s.addProduct(value.Len(), int(typ.Key().Size()+typ.Elem().Size())+16)
		if value.Len() > retainedEstimateMaxNodes-s.nodes {
			s.saturate()
			return
		}
		iter := value.MapRange()
		for iter.Next() {
			if retainedTypeHasReferences(typ.Key()) {
				s.walk(iter.Key(), depth+1)
			}
			if retainedTypeHasReferences(typ.Elem()) {
				s.walk(iter.Value(), depth+1)
			}
		}
	case reflect.Struct:
		s.add(int(typ.Size()))
		for i := 0; i < value.NumField(); i++ {
			if retainedTypeHasReferences(typ.Field(i).Type) {
				s.walk(value.Field(i), depth+1)
			}
		}
	case reflect.Array:
		s.add(int(typ.Size()))
		if retainedTypeHasReferences(typ.Elem()) {
			if value.Len() > retainedEstimateMaxNodes-s.nodes {
				s.saturate()
				return
			}
			for i := 0; i < value.Len(); i++ {
				s.walk(value.Index(i), depth+1)
			}
		}
	case reflect.Func, reflect.Chan, reflect.UnsafePointer:
		s.add(int(typ.Size()))
		if !value.IsNil() {
			// Closures, channel buffers, and unsafe pointers can retain arbitrary
			// graphs that reflection cannot safely inspect.
			s.saturate()
		}
	default:
		s.add(int(typ.Size()))
	}
}

func (s *retainedSizer) addProduct(count, size int) {
	if count < 0 || size < 0 || (size != 0 && count > s.limit/size) {
		s.saturate()
		return
	}
	s.add(count * size)
}

func (s *retainedSizer) seenValue(value reflect.Value) bool {
	visit := retainedVisit{typ: value.Type(), kind: value.Kind(), ptr: value.Pointer()}
	if visit.ptr == 0 {
		return false
	}
	if _, ok := s.seen[visit]; ok {
		return true
	}
	s.seen[visit] = struct{}{}
	return false
}

func retainedTypeHasReferences(typ reflect.Type) bool {
	switch typ.Kind() {
	case reflect.Interface, reflect.String, reflect.Pointer, reflect.Slice, reflect.Map, reflect.Func, reflect.Chan, reflect.UnsafePointer:
		return true
	case reflect.Array:
		return retainedTypeHasReferences(typ.Elem())
	case reflect.Struct:
		for i := 0; i < typ.NumField(); i++ {
			if retainedTypeHasReferences(typ.Field(i).Type) {
				return true
			}
		}
	}
	return false
}
