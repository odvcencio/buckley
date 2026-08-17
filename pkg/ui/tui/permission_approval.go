package tui

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"

	"m31labs.dev/buckley/pkg/tool"
)

type approvalWaitResult struct {
	response tool.PermissionApprovalResponse
	err      error
}

type pendingTUIApproval struct {
	wait  chan approvalWaitResult
	scope string
}

// tuiApprovalRouter owns the process-wide approval ID namespace used by one
// controller. Widget callbacks resolve exactly one broker by ID rather than
// scanning sessions and accepting the first coincidental match.
type tuiApprovalRouter struct {
	mu     sync.Mutex
	routes map[string]*tuiApprovalBroker
}

func newTUIApprovalRouter() *tuiApprovalRouter {
	return &tuiApprovalRouter{routes: make(map[string]*tuiApprovalBroker)}
}

func (r *tuiApprovalRouter) register(id string, broker *tuiApprovalBroker) bool {
	if r == nil || id == "" || broker == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.routes[id]; exists {
		return false
	}
	r.routes[id] = broker
	return true
}

func (r *tuiApprovalRouter) unregister(id string, broker *tuiApprovalBroker) bool {
	if r == nil || id == "" || broker == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.routes[id] != broker {
		return false
	}
	delete(r.routes, id)
	return true
}

func (r *tuiApprovalRouter) resolve(id string, approved, alwaysAllow bool) bool {
	if r == nil || id == "" {
		return false
	}
	r.mu.Lock()
	broker := r.routes[id]
	if broker != nil {
		delete(r.routes, id)
	}
	r.mu.Unlock()
	return broker != nil && broker.deliver(id, approved, alwaysAllow)
}

func (r *tuiApprovalRouter) routeCount() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.routes)
}

// tuiApprovalBroker is the session-local bridge between tool middleware and
// the UI event queue. Tool workers wait on the broker; the UI goroutine only
// renders the modal and invokes the callback, so approval never blocks input
// or rendering.
type tuiApprovalBroker struct {
	mu         sync.Mutex
	app        *WidgetApp
	router     *tuiApprovalRouter
	sessionID  string
	sequence   uint64
	closed     bool
	pending    map[string]pendingTUIApproval
	remembered map[string]struct{}
}

func newTUIApprovalBroker(sessionID string, router *tuiApprovalRouter) *tuiApprovalBroker {
	if router == nil {
		router = newTUIApprovalRouter()
	}
	return &tuiApprovalBroker{
		sessionID:  strings.TrimSpace(sessionID),
		router:     router,
		pending:    make(map[string]pendingTUIApproval),
		remembered: make(map[string]struct{}),
	}
}

func (b *tuiApprovalBroker) bindApp(app *WidgetApp) {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.app = app
	b.mu.Unlock()
}

// Request implements tool.PermissionApprovalHandler.
func (b *tuiApprovalBroker) Request(ctx context.Context, req tool.PermissionApprovalRequest) (tool.PermissionApprovalResponse, error) {
	if b == nil {
		return tool.PermissionApprovalResponse{}, fmt.Errorf("approval broker unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return tool.PermissionApprovalResponse{}, fmt.Errorf("approval UI is shut down")
	}
	if req.Scope != "" {
		if _, ok := b.remembered[req.Scope]; ok {
			b.mu.Unlock()
			return tool.PermissionApprovalResponse{Approved: true, AlwaysAllow: true}, nil
		}
	}
	var id string
	for {
		id = b.nextIDLocked(req)
		if _, exists := b.pending[id]; exists {
			continue
		}
		if b.router.register(id, b) {
			break
		}
	}
	wait := make(chan approvalWaitResult, 1)
	b.pending[id] = pendingTUIApproval{wait: wait, scope: req.Scope}
	app := b.app
	b.mu.Unlock()

	if app == nil {
		b.rejectPending(id, fmt.Errorf("approval UI is unavailable"))
		return tool.PermissionApprovalResponse{}, fmt.Errorf("approval UI is unavailable")
	}

	message := approvalRequestMessage(id, req)
	if !app.RequestApproval(message) {
		b.rejectPending(id, fmt.Errorf("approval request rejected by UI"))
		return tool.PermissionApprovalResponse{}, fmt.Errorf("approval request rejected by UI")
	}

	select {
	case result := <-wait:
		return result.response, result.err
	case <-ctx.Done():
		if b.cancelPending(id) {
			return tool.PermissionApprovalResponse{}, ctx.Err()
		}
		// A decision removed the pending entry concurrently with cancellation.
		// Its buffered result owns the outcome and must be consumed.
		result := <-wait
		return result.response, result.err
	}
}

func (b *tuiApprovalBroker) nextIDLocked(req tool.PermissionApprovalRequest) string {
	b.sequence++
	hash := sha256.New()
	var size [8]byte
	for _, field := range []string{
		"tui-approval-v1",
		b.sessionID,
		strings.TrimSpace(req.ID),
		strings.TrimSpace(req.Tool),
		req.Scope,
		fmt.Sprintf("%d", b.sequence),
	} {
		binary.BigEndian.PutUint64(size[:], uint64(len(field)))
		_, _ = hash.Write(size[:])
		_, _ = hash.Write([]byte(field))
	}
	return "approval:v1:" + hex.EncodeToString(hash.Sum(nil))
}

func (b *tuiApprovalBroker) resolve(id string, approved, alwaysAllow bool) bool {
	if b == nil || b.router == nil {
		return false
	}
	return b.router.resolve(id, approved, alwaysAllow)
}

func (b *tuiApprovalBroker) deliver(id string, approved, alwaysAllow bool) bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	pending, ok := b.pending[id]
	if !ok {
		b.mu.Unlock()
		return false
	}
	delete(b.pending, id)
	if approved && alwaysAllow && pending.scope != "" {
		b.remembered[pending.scope] = struct{}{}
	}
	b.mu.Unlock()
	pending.wait <- approvalWaitResult{
		response: tool.PermissionApprovalResponse{Approved: approved, AlwaysAllow: alwaysAllow},
	}
	return true
}

func (b *tuiApprovalBroker) rejectPending(id string, err error) bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	pending, ok := b.pending[id]
	if ok {
		delete(b.pending, id)
	}
	b.mu.Unlock()
	if !ok {
		return false
	}
	b.router.unregister(id, b)
	pending.wait <- approvalWaitResult{err: err}
	return true
}

func (b *tuiApprovalBroker) cancelPending(id string) bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	_, ok := b.pending[id]
	if ok {
		delete(b.pending, id)
	}
	app := b.app
	b.mu.Unlock()
	if !ok {
		return false
	}
	b.router.unregister(id, b)
	if app != nil {
		app.CancelApproval(id)
	}
	return true
}

func (b *tuiApprovalBroker) close() {
	if b == nil {
		return
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	type closedApproval struct {
		id      string
		pending pendingTUIApproval
	}
	pending := make([]closedApproval, 0, len(b.pending))
	for id, approval := range b.pending {
		delete(b.pending, id)
		pending = append(pending, closedApproval{id: id, pending: approval})
	}
	app := b.app
	b.mu.Unlock()
	for _, approval := range pending {
		b.router.unregister(approval.id, b)
		if app != nil {
			app.CancelApproval(approval.id)
		}
		approval.pending.wait <- approvalWaitResult{err: fmt.Errorf("approval UI is shut down")}
	}
}

func (b *tuiApprovalBroker) pendingCount() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.pending)
}

func approvalRequestMessage(id string, req tool.PermissionApprovalRequest) ApprovalRequestMsg {
	toolName := strings.TrimSpace(req.Tool)
	if toolName == "" {
		toolName = strings.TrimSpace(req.Permission.Tool)
	}
	arg := strings.TrimSpace(req.Permission.Arg)
	message := ApprovalRequestMsg{
		ID:          id,
		Tool:        toolName,
		Operation:   approvalOperation(req.Permission.Category, toolName),
		Description: approvalDescription(req),
	}
	if toolName == "run_shell" || toolName == "run_code" {
		message.Command = arg
	} else {
		message.FilePath = arg
	}
	return message
}

func approvalOperation(category, toolName string) string {
	category = strings.TrimSpace(category)
	if category == "" {
		category = "permission"
	}
	if toolName == "run_shell" || toolName == "run_code" {
		return category + ":execute"
	}
	if category == "file_read" {
		return category + ":read"
	}
	return category + ":write"
}

func approvalDescription(req tool.PermissionApprovalRequest) string {
	layer := strings.TrimSpace(req.Decision.Layer)
	if layer == "" {
		layer = "policy"
	}
	pattern := strings.TrimSpace(req.Decision.Rule.ArgPattern)
	if pattern == "" {
		pattern = "all matching arguments"
	}
	return fmt.Sprintf("Allowed only after approval. Rule %s matches %s. Always allow is limited to this tool/category/rule scope.", layer, pattern)
}
