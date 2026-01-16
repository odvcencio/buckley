package agentserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	acppb "github.com/odvcencio/buckley/pkg/acp/proto"
	"github.com/odvcencio/buckley/pkg/ui/buckley/viewmodel"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// acpClient is the subset of ACP client methods we need.
type acpClient interface {
	StreamInlineCompletions(ctx context.Context, in *acppb.InlineCompletionRequest, opts ...grpc.CallOption) (acppb.AgentCommunication_StreamInlineCompletionsClient, error)
	ProposeEdits(ctx context.Context, in *acppb.ProposeEditsRequest, opts ...grpc.CallOption) (*acppb.ProposeEditsResponse, error)
	ApplyEdits(ctx context.Context, in *acppb.ApplyEditsRequest, opts ...grpc.CallOption) (*acppb.ApplyEditsResponse, error)
	UpdateEditorState(ctx context.Context, in *acppb.UpdateEditorStateRequest, opts ...grpc.CallOption) (*acppb.UpdateEditorStateResponse, error)
}

// Server provides a Zed-friendly agent server shim over HTTP.
type Server struct {
	client acpClient
	view   ViewProvider
	mux    *http.ServeMux
}

// ViewProvider supplies renderer-friendly session view snapshots.
type ViewProvider interface {
	BuildSessionState(ctx context.Context, sessionID string) (*viewmodel.SessionState, error)
}

type Option func(*Server)

// WithViewProvider adds a view provider for richer editor status surfaces.
func WithViewProvider(v ViewProvider) Option {
	return func(s *Server) {
		s.view = v
	}
}

// New constructs a new Server.
func New(client acpClient, opts ...Option) *Server {
	s := &Server{
		client: client,
		mux:    http.NewServeMux(),
	}
	for _, opt := range opts {
		opt(s)
	}
	s.routes()
	return s
}

// Router returns the HTTP handler.
func (s *Server) Router() http.Handler {
	return s.mux
}

type position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

type rangeJSON struct {
	Start position `json:"start"`
	End   position `json:"end"`
}

type document struct {
	URI        string     `json:"uri"`
	LanguageID string     `json:"language_id"`
	Content    string     `json:"content"`
	Selection  *rangeJSON `json:"selection,omitempty"`
}

type inlineRequest struct {
	AgentID   string   `json:"agent_id"`
	SessionID string   `json:"session_id"`
	Prompt    string   `json:"prompt"`
	Document  document `json:"document"`
}

type proposeRequest struct {
	AgentID        string   `json:"agent_id"`
	SessionID      string   `json:"session_id"`
	Instruction    string   `json:"instruction"`
	MaxSuggestions int      `json:"max_suggestions"`
	Document       document `json:"document"`
}

type textEdit struct {
	URI     string     `json:"uri"`
	Range   *rangeJSON `json:"range,omitempty"`
	NewText string     `json:"new_text"`
}

type applyRequest struct {
	AgentID   string     `json:"agent_id"`
	SessionID string     `json:"session_id"`
	Title     string     `json:"title,omitempty"`
	DryRun    bool       `json:"dry_run"`
	Edits     []textEdit `json:"edits"`
}

type statusRequest struct {
	AgentID   string   `json:"agent_id"`
	SessionID string   `json:"session_id"`
	Document  document `json:"document"`
}

type inlineResponse struct {
	Text         string `json:"text"`
	FinishReason string `json:"finish_reason,omitempty"`
}

type proposeResponse struct {
	Suggestions []*acppb.ProposedEdit `json:"suggestions"`
}

func (s *Server) routes() {
	s.mux.HandleFunc("/inline_complete", s.handleInline)
	s.mux.HandleFunc("/propose_edits", s.handlePropose)
	s.mux.HandleFunc("/apply_edits", s.handleApply)
	s.mux.HandleFunc("/status", s.handleStatus)
	s.mux.HandleFunc("/view_state", s.handleViewState)
}

func (s *Server) handleInline(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req inlineRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("decode: %v", err), http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	ctx = withAgentMetadata(ctx, req.AgentID)

	editorCtx, err := toEditorContext(req.Document)
	if err != nil {
		http.Error(w, fmt.Sprintf("invalid document: %v", err), http.StatusBadRequest)
		return
	}
	icReq := &acppb.InlineCompletionRequest{
		AgentId:   req.AgentID,
		SessionId: req.SessionID,
		Context:   editorCtx,
		Prompt:    req.Prompt,
	}

	stream, err := s.client.StreamInlineCompletions(ctx, icReq)
	if err != nil {
		http.Error(w, fmt.Sprintf("inline: %v", err), http.StatusInternalServerError)
		return
	}

	var text, finish string
	var recvErr error
	for {
		ev, err := stream.Recv()
		if err != nil {
			recvErr = err
			break
		}
		if ev.GetText() != "" {
			text += ev.GetText()
		}
		if ev.GetFinishReason() != "" {
			finish = ev.GetFinishReason()
		}
	}

	if recvErr != nil && recvErr != io.EOF {
		http.Error(w, fmt.Sprintf("inline stream: %v", recvErr), http.StatusInternalServerError)
		return
	}
	writeJSON(w, inlineResponse{Text: text, FinishReason: finish})
}

func (s *Server) handlePropose(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req proposeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("decode: %v", err), http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	ctx = withAgentMetadata(ctx, req.AgentID)

	editorCtx, err := toEditorContext(req.Document)
	if err != nil {
		http.Error(w, fmt.Sprintf("invalid document: %v", err), http.StatusBadRequest)
		return
	}
	maxSuggestions, err := intToNonNegativeInt32(req.MaxSuggestions)
	if err != nil {
		http.Error(w, fmt.Sprintf("invalid max_suggestions: %v", err), http.StatusBadRequest)
		return
	}
	prReq := &acppb.ProposeEditsRequest{
		AgentId:        req.AgentID,
		SessionId:      req.SessionID,
		Instruction:    req.Instruction,
		Context:        editorCtx,
		MaxSuggestions: maxSuggestions,
	}

	resp, err := s.client.ProposeEdits(ctx, prReq)
	if err != nil {
		http.Error(w, fmt.Sprintf("propose: %v", err), http.StatusInternalServerError)
		return
	}

	writeJSON(w, proposeResponse{Suggestions: resp.GetEdits()})
}

func (s *Server) handleApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req applyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("decode: %v", err), http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	ctx = withAgentMetadata(ctx, req.AgentID)

	edits, err := toPBEdits(req.Edits)
	if err != nil {
		http.Error(w, fmt.Sprintf("invalid edits: %v", err), http.StatusBadRequest)
		return
	}
	apReq := &acppb.ApplyEditsRequest{
		AgentId:   req.AgentID,
		SessionId: req.SessionID,
		Edits:     edits,
		Title:     req.Title,
		DryRun:    req.DryRun,
	}

	resp, err := s.client.ApplyEdits(ctx, apReq)
	if err != nil {
		http.Error(w, fmt.Sprintf("apply: %v", err), http.StatusInternalServerError)
		return
	}

	writeJSON(w, resp)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req statusRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("decode: %v", err), http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	ctx = withAgentMetadata(ctx, req.AgentID)

	editorCtx, err := toEditorContext(req.Document)
	if err != nil {
		http.Error(w, fmt.Sprintf("invalid document: %v", err), http.StatusBadRequest)
		return
	}
	statusReq := &acppb.UpdateEditorStateRequest{
		AgentId:   req.AgentID,
		SessionId: req.SessionID,
		Context:   editorCtx,
	}
	resp, err := s.client.UpdateEditorState(ctx, statusReq)
	if err != nil {
		http.Error(w, fmt.Sprintf("status: %v", err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, resp)
}

func toEditorContext(doc document) (*acppb.EditorContext, error) {
	var sel *acppb.Range
	if doc.Selection != nil {
		startLine, err := intToNonNegativeInt32(doc.Selection.Start.Line)
		if err != nil {
			return nil, fmt.Errorf("selection.start.line: %w", err)
		}
		startChar, err := intToNonNegativeInt32(doc.Selection.Start.Character)
		if err != nil {
			return nil, fmt.Errorf("selection.start.character: %w", err)
		}
		endLine, err := intToNonNegativeInt32(doc.Selection.End.Line)
		if err != nil {
			return nil, fmt.Errorf("selection.end.line: %w", err)
		}
		endChar, err := intToNonNegativeInt32(doc.Selection.End.Character)
		if err != nil {
			return nil, fmt.Errorf("selection.end.character: %w", err)
		}
		sel = &acppb.Range{
			Start: &acppb.Position{Line: startLine, Character: startChar},
			End:   &acppb.Position{Line: endLine, Character: endChar},
		}
	}

	return &acppb.EditorContext{
		Document: &acppb.DocumentSnapshot{
			Uri:        doc.URI,
			LanguageId: doc.LanguageID,
			Content:    doc.Content,
			Selection:  sel,
		},
	}, nil
}

func toPBEdits(edits []textEdit) ([]*acppb.TextEdit, error) {
	out := make([]*acppb.TextEdit, 0, len(edits))
	for _, e := range edits {
		var rng *acppb.Range
		if e.Range != nil {
			startLine, err := intToNonNegativeInt32(e.Range.Start.Line)
			if err != nil {
				return nil, fmt.Errorf("range.start.line: %w", err)
			}
			startChar, err := intToNonNegativeInt32(e.Range.Start.Character)
			if err != nil {
				return nil, fmt.Errorf("range.start.character: %w", err)
			}
			endLine, err := intToNonNegativeInt32(e.Range.End.Line)
			if err != nil {
				return nil, fmt.Errorf("range.end.line: %w", err)
			}
			endChar, err := intToNonNegativeInt32(e.Range.End.Character)
			if err != nil {
				return nil, fmt.Errorf("range.end.character: %w", err)
			}
			rng = &acppb.Range{
				Start: &acppb.Position{Line: startLine, Character: startChar},
				End:   &acppb.Position{Line: endLine, Character: endChar},
			}
		}
		out = append(out, &acppb.TextEdit{
			Uri:     e.URI,
			Range:   rng,
			NewText: e.NewText,
		})
	}
	return out, nil
}

func intToNonNegativeInt32(value int) (int32, error) {
	if value < 0 {
		return 0, fmt.Errorf("must be >= 0")
	}
	if value > math.MaxInt32 {
		return 0, fmt.Errorf("must be <= %d", math.MaxInt32)
	}
	return int32(value), nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func withAgentMetadata(ctx context.Context, agentID string) context.Context {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return ctx
	}
	return metadata.AppendToOutgoingContext(ctx, "x-buckley-agent-id", agentID)
}

// handleViewState returns a renderer-friendly view snapshot for the given session.
// This powers editor-side status bars and prevents "dead end" waiting states by surfacing pause/awaiting info.
func (s *Server) handleViewState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.view == nil {
		http.Error(w, "view provider unavailable", http.StatusServiceUnavailable)
		return
	}
	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		http.Error(w, "session_id required", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	state, err := s.view.BuildSessionState(ctx, sessionID)
	if err != nil {
		http.Error(w, fmt.Sprintf("view: %v", err), http.StatusInternalServerError)
		return
	}
	if state == nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	writeJSON(w, state)
}
