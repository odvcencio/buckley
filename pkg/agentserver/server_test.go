package agentserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"m31labs.dev/buckley/pkg/acp/partialresult"
	acppb "m31labs.dev/buckley/pkg/acp/proto"
	"m31labs.dev/buckley/pkg/ui/viewmodel"
)

const inlinePartialTextMaxBytesForTest = 2048

type inlineErrorResponseForTest struct {
	Error        string `json:"error"`
	Incomplete   bool   `json:"incomplete"`
	Accepted     *bool  `json:"accepted"`
	Text         string `json:"text"`
	FinishReason string `json:"finish_reason"`
}

type proposeErrorResponseForTest struct {
	Error       string                `json:"error"`
	Incomplete  bool                  `json:"incomplete"`
	Accepted    *bool                 `json:"accepted"`
	Text        string                `json:"text"`
	Suggestions []*acppb.ProposedEdit `json:"suggestions"`
}

type downstreamErrorResponseForTest struct {
	Error      string `json:"error"`
	Incomplete bool   `json:"incomplete"`
	Accepted   *bool  `json:"accepted"`
	Applied    *bool  `json:"applied"`
}

// --- Fake/Stub implementations ---

type fakeInlineStream struct {
	ctx    context.Context
	events []*acppb.InlineCompletionEvent
	err    error
	idx    int
}

func (f *fakeInlineStream) Recv() (*acppb.InlineCompletionEvent, error) {
	if f.idx < len(f.events) {
		ev := f.events[f.idx]
		f.idx++
		return ev, nil
	}
	if f.err != nil {
		return nil, f.err
	}
	return nil, io.EOF
}

func (f *fakeInlineStream) Header() (metadata.MD, error) { return nil, nil }
func (f *fakeInlineStream) Trailer() metadata.MD         { return nil }
func (f *fakeInlineStream) CloseSend() error             { return nil }
func (f *fakeInlineStream) Context() context.Context {
	if f.ctx != nil {
		return f.ctx
	}
	return context.Background()
}
func (f *fakeInlineStream) SendMsg(any) error { return nil }
func (f *fakeInlineStream) RecvMsg(any) error { return nil }

// mockACPClient is a more flexible mock than stubACPClient.
type mockACPClient struct {
	stream         acppb.AgentCommunication_StreamInlineCompletionsClient
	streamErr      error
	proposeResp    *acppb.ProposeEditsResponse
	proposeErr     error
	applyResp      *acppb.ApplyEditsResponse
	applyErr       error
	applyNilResp   bool
	statusResp     *acppb.UpdateEditorStateResponse
	statusErr      error
	statusNilResp  bool
	lastProposeReq *acppb.ProposeEditsRequest
	lastApplyReq   *acppb.ApplyEditsRequest
	lastStatusReq  *acppb.UpdateEditorStateRequest
	lastInlineReq  *acppb.InlineCompletionRequest
}

func (m *mockACPClient) StreamInlineCompletions(_ context.Context, req *acppb.InlineCompletionRequest, _ ...grpc.CallOption) (acppb.AgentCommunication_StreamInlineCompletionsClient, error) {
	m.lastInlineReq = req
	return m.stream, m.streamErr
}

func (m *mockACPClient) ProposeEdits(_ context.Context, req *acppb.ProposeEditsRequest, _ ...grpc.CallOption) (*acppb.ProposeEditsResponse, error) {
	m.lastProposeReq = req
	if m.proposeErr != nil {
		return nil, m.proposeErr
	}
	if m.proposeResp != nil {
		return m.proposeResp, nil
	}
	return &acppb.ProposeEditsResponse{}, nil
}

func (m *mockACPClient) ApplyEdits(_ context.Context, req *acppb.ApplyEditsRequest, _ ...grpc.CallOption) (*acppb.ApplyEditsResponse, error) {
	m.lastApplyReq = req
	if m.applyErr != nil {
		return nil, m.applyErr
	}
	if m.applyNilResp {
		return nil, nil
	}
	if m.applyResp != nil {
		return m.applyResp, nil
	}
	return &acppb.ApplyEditsResponse{}, nil
}

func (m *mockACPClient) UpdateEditorState(_ context.Context, req *acppb.UpdateEditorStateRequest, _ ...grpc.CallOption) (*acppb.UpdateEditorStateResponse, error) {
	m.lastStatusReq = req
	if m.statusErr != nil {
		return nil, m.statusErr
	}
	if m.statusNilResp {
		return nil, nil
	}
	if m.statusResp != nil {
		return m.statusResp, nil
	}
	return &acppb.UpdateEditorStateResponse{}, nil
}

// stubACPClient is kept for backward compatibility with existing tests.
type stubACPClient struct {
	stream acppb.AgentCommunication_StreamInlineCompletionsClient
	err    error
}

func (s stubACPClient) StreamInlineCompletions(context.Context, *acppb.InlineCompletionRequest, ...grpc.CallOption) (acppb.AgentCommunication_StreamInlineCompletionsClient, error) {
	return s.stream, s.err
}
func (s stubACPClient) ProposeEdits(context.Context, *acppb.ProposeEditsRequest, ...grpc.CallOption) (*acppb.ProposeEditsResponse, error) {
	return &acppb.ProposeEditsResponse{}, nil
}
func (s stubACPClient) ApplyEdits(context.Context, *acppb.ApplyEditsRequest, ...grpc.CallOption) (*acppb.ApplyEditsResponse, error) {
	return &acppb.ApplyEditsResponse{}, nil
}
func (s stubACPClient) UpdateEditorState(context.Context, *acppb.UpdateEditorStateRequest, ...grpc.CallOption) (*acppb.UpdateEditorStateResponse, error) {
	return &acppb.UpdateEditorStateResponse{}, nil
}

// mockViewProvider implements ViewProvider for testing.
type mockViewProvider struct {
	state *viewmodel.SessionState
	err   error
}

func (m *mockViewProvider) BuildSessionState(_ context.Context, _ string) (*viewmodel.SessionState, error) {
	return m.state, m.err
}

// --- Constructor and Option tests ---

func TestNew(t *testing.T) {
	client := &mockACPClient{}
	srv := New(client)
	if srv == nil {
		t.Fatal("expected non-nil server")
	}
	if srv.client != client {
		t.Error("client not set correctly")
	}
	if srv.mux == nil {
		t.Error("mux should be initialized")
	}
	if srv.view != nil {
		t.Error("view should be nil by default")
	}
}

func TestNewWithViewProvider(t *testing.T) {
	client := &mockACPClient{}
	vp := &mockViewProvider{}
	srv := New(client, WithViewProvider(vp))
	if srv.view != vp {
		t.Error("view provider not set correctly")
	}
}

func TestRouter(t *testing.T) {
	srv := New(&mockACPClient{})
	router := srv.Router()
	if router == nil {
		t.Fatal("Router() returned nil")
	}
	if router != srv.mux {
		t.Error("Router() should return the mux")
	}
}

// --- Inline completion handler tests ---

func TestInlineCompletionReturns200OnEOFTermination(t *testing.T) {
	stream := &fakeInlineStream{
		events: []*acppb.InlineCompletionEvent{
			{Text: "hello "},
			{Text: "world", FinishReason: "stop"},
		},
	}
	srv := New(stubACPClient{stream: stream})

	reqBody := []byte(`{"agent_id":"a1","session_id":"s1","prompt":"p","document":{"uri":"file:///tmp/main.go","language_id":"go","content":"package main\n"}}`)
	req := httptest.NewRequest(http.MethodPost, "/inline_complete", bytes.NewReader(reqBody))
	rec := httptest.NewRecorder()

	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want %d body=%q", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := rec.Body.String(); !bytes.Contains([]byte(got), []byte(`"text":"hello world"`)) {
		t.Fatalf("unexpected body: %q", got)
	}
	if got := rec.Body.String(); !bytes.Contains([]byte(got), []byte(`"finish_reason":"stop"`)) {
		t.Fatalf("unexpected body: %q", got)
	}
}

func TestInlineCompletionReturns500OnStreamError(t *testing.T) {
	stream := &fakeInlineStream{
		events: []*acppb.InlineCompletionEvent{
			{Text: "partial"},
		},
		err: errors.New("boom"),
	}
	srv := New(stubACPClient{stream: stream})

	reqBody := []byte(`{"agent_id":"a1","session_id":"s1","prompt":"p","document":{"uri":"file:///tmp/main.go","language_id":"go","content":"package main\n"}}`)
	req := httptest.NewRequest(http.MethodPost, "/inline_complete", bytes.NewReader(reqBody))
	rec := httptest.NewRecorder()

	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want %d body=%q", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}
}

func TestInlineCompletionStreamErrorRetainsBoundedUnacceptedPartial(t *testing.T) {
	const secret = "provider-secret-terminal"
	stream := &fakeInlineStream{
		events: []*acppb.InlineCompletionEvent{
			{Text: "public "},
			{Text: "prefix", FinishReason: "length"},
		},
		err: errors.New("boom " + secret),
	}
	srv := New(stubACPClient{stream: stream})

	rec := serveInlineCompletion(t, srv)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want %d body=%q", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}
	var body inlineErrorResponseForTest
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v; body=%q", err, rec.Body.String())
	}
	if body.Error != "inline completion incomplete" || !body.Incomplete || body.Accepted == nil || *body.Accepted {
		t.Fatalf("body = %+v, want incomplete accepted=false", body)
	}
	if body.Text != "public prefix" || body.FinishReason != "length" {
		t.Fatalf("body text/finish = %q/%q, want retained public prefix", body.Text, body.FinishReason)
	}
	if strings.Contains(rec.Body.String(), secret) || strings.Count(rec.Body.String(), "public prefix") != 1 {
		t.Fatalf("body leaked secret or duplicated partial: %q", rec.Body.String())
	}
}

func TestInlineCompletionInitialStreamFailureIsSafeAndEmpty(t *testing.T) {
	const secret = "provider-secret-initial"
	srv := New(stubACPClient{err: errors.New("dial failed " + secret)})

	rec := serveInlineCompletion(t, srv)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want %d body=%q", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}
	var body inlineErrorResponseForTest
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v; body=%q", err, rec.Body.String())
	}
	if body.Text != "" || body.FinishReason != "" || !body.Incomplete || body.Accepted == nil || *body.Accepted {
		t.Fatalf("body = %+v, want safe empty incomplete response", body)
	}
	if strings.Contains(rec.Body.String(), secret) || strings.Contains(rec.Body.String(), "dial failed") {
		t.Fatalf("body leaked raw initial error: %q", rec.Body.String())
	}
}

func TestInlineCompletionSuccessOmitsIncompleteLabels(t *testing.T) {
	stream := &fakeInlineStream{
		events: []*acppb.InlineCompletionEvent{
			{Text: "hello "},
			{Text: "world", FinishReason: "stop"},
		},
	}
	srv := New(stubACPClient{stream: stream})

	rec := serveInlineCompletion(t, srv)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want %d body=%q", rec.Code, http.StatusOK, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v; body=%q", err, rec.Body.String())
	}
	if body["text"] != "hello world" || body["finish_reason"] != "stop" {
		t.Fatalf("body = %+v, want normal inline success", body)
	}
	if _, ok := body["incomplete"]; ok {
		t.Fatalf("success body should omit incomplete label: %+v", body)
	}
	if _, ok := body["accepted"]; ok {
		t.Fatalf("success body should omit accepted label: %+v", body)
	}
}

func TestInlineCompletionStreamErrorBoundsUTF8Partial(t *testing.T) {
	chunk := strings.Repeat("🙂", inlinePartialTextMaxBytesForTest)
	stream := &fakeInlineStream{
		events: []*acppb.InlineCompletionEvent{{Text: chunk}},
		err:    errors.New("terminal failure"),
	}
	srv := New(stubACPClient{stream: stream})

	rec := serveInlineCompletion(t, srv)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want %d body=%q", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}
	var body inlineErrorResponseForTest
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v; body=%q", err, rec.Body.String())
	}
	if len(body.Text) > inlinePartialTextMaxBytesForTest {
		t.Fatalf("partial bytes=%d want <= %d", len(body.Text), inlinePartialTextMaxBytesForTest)
	}
	if strings.ContainsRune(body.Text, '\uFFFD') {
		t.Fatalf("partial should remain valid UTF-8 without replacement rune: %q", body.Text)
	}
}

func TestInlineCompletionEOFWithNonConclusiveFinishIsSafeIncomplete(t *testing.T) {
	stream := &fakeInlineStream{
		events: []*acppb.InlineCompletionEvent{
			{Text: "public "},
			{Text: "prefix", FinishReason: "length"},
		},
		err: io.EOF,
	}
	srv := New(stubACPClient{stream: stream})

	rec := serveInlineCompletion(t, srv)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want %d body=%q", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}
	var body inlineErrorResponseForTest
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v; body=%q", err, rec.Body.String())
	}
	if body.Error != "inline completion incomplete" || !body.Incomplete || body.Accepted == nil || *body.Accepted {
		t.Fatalf("body = %+v, want incomplete accepted=false", body)
	}
	if body.Text != "public prefix" || body.FinishReason != "length" {
		t.Fatalf("body text/finish = %q/%q, want retained public prefix and nonconclusive finish", body.Text, body.FinishReason)
	}
	if strings.Count(rec.Body.String(), "public prefix") != 1 {
		t.Fatalf("body duplicated partial: %q", rec.Body.String())
	}
}

func TestInlineCompletionEOFWithUnknownFinishIsSafeIncomplete(t *testing.T) {
	stream := &fakeInlineStream{
		events: []*acppb.InlineCompletionEvent{
			{Text: "public prefix", FinishReason: "mystery"},
		},
		err: io.EOF,
	}
	srv := New(stubACPClient{stream: stream})

	rec := serveInlineCompletion(t, srv)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want %d body=%q", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}
	var body inlineErrorResponseForTest
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v; body=%q", err, rec.Body.String())
	}
	if body.Text != "public prefix" || body.FinishReason != "mystery" || body.Accepted == nil || *body.Accepted {
		t.Fatalf("body = %+v, want bounded unaccepted partial with original finish", body)
	}
}

func TestInlineCompletionMethodNotAllowed(t *testing.T) {
	srv := New(&mockACPClient{})
	req := httptest.NewRequest(http.MethodGet, "/inline_complete", nil)
	rec := httptest.NewRecorder()

	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestInlineCompletionBadJSON(t *testing.T) {
	srv := New(&mockACPClient{})
	req := httptest.NewRequest(http.MethodPost, "/inline_complete", strings.NewReader("{invalid"))
	rec := httptest.NewRecorder()

	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestInlineCompletionClientError(t *testing.T) {
	client := &mockACPClient{streamErr: errors.New("connection failed")}
	srv := New(client)

	reqBody := []byte(`{"agent_id":"a1","session_id":"s1","prompt":"p","document":{"uri":"file:///x","language_id":"go","content":""}}`)
	req := httptest.NewRequest(http.MethodPost, "/inline_complete", bytes.NewReader(reqBody))
	rec := httptest.NewRecorder()

	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want %d", rec.Code, http.StatusInternalServerError)
	}
}

func TestInlineCompletionInvalidSelection(t *testing.T) {
	srv := New(&mockACPClient{})
	// Negative line number should cause validation error
	reqBody := []byte(`{"agent_id":"a1","session_id":"s1","prompt":"p","document":{"uri":"file:///x","language_id":"go","content":"","selection":{"start":{"line":-1,"character":0},"end":{"line":0,"character":0}}}}`)
	req := httptest.NewRequest(http.MethodPost, "/inline_complete", bytes.NewReader(reqBody))
	rec := httptest.NewRecorder()

	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want %d body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestInlineCompletionWithSelection(t *testing.T) {
	stream := &fakeInlineStream{
		events: []*acppb.InlineCompletionEvent{{Text: "done", FinishReason: "stop"}},
	}
	client := &mockACPClient{stream: stream}
	srv := New(client)

	reqBody := []byte(`{"agent_id":"a1","session_id":"s1","prompt":"p","document":{"uri":"file:///x","language_id":"go","content":"abc","selection":{"start":{"line":0,"character":0},"end":{"line":0,"character":3}}}}`)
	req := httptest.NewRequest(http.MethodPost, "/inline_complete", bytes.NewReader(reqBody))
	rec := httptest.NewRecorder()

	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want %d", rec.Code, http.StatusOK)
	}
	// Verify request was passed correctly
	if client.lastInlineReq.Context.Document.Selection == nil {
		t.Error("expected selection to be set")
	}
}

func serveInlineCompletion(t *testing.T, srv *Server) *httptest.ResponseRecorder {
	t.Helper()
	reqBody := []byte(`{"agent_id":"a1","session_id":"s1","prompt":"p","document":{"uri":"file:///tmp/main.go","language_id":"go","content":"package main\n"}}`)
	req := httptest.NewRequest(http.MethodPost, "/inline_complete", bytes.NewReader(reqBody))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	return rec
}

// --- Propose edits handler tests ---

func TestProposeEditsSuccess(t *testing.T) {
	client := &mockACPClient{
		proposeResp: &acppb.ProposeEditsResponse{
			Edits: []*acppb.ProposedEdit{
				{Title: "Fix bug", Summary: "desc"},
			},
		},
	}
	srv := New(client)

	reqBody := []byte(`{"agent_id":"a1","session_id":"s1","instruction":"fix this","max_suggestions":3,"document":{"uri":"file:///x","language_id":"go","content":"package main"}}`)
	req := httptest.NewRequest(http.MethodPost, "/propose_edits", bytes.NewReader(reqBody))
	rec := httptest.NewRecorder()

	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	// Verify response contains suggestions
	if !strings.Contains(rec.Body.String(), "Fix bug") {
		t.Errorf("response should contain suggestion: %s", rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if _, ok := body["incomplete"]; ok {
		t.Fatalf("success body should omit incomplete label: %+v", body)
	}
	if _, ok := body["accepted"]; ok {
		t.Fatalf("success body should omit accepted label: %+v", body)
	}
	// Verify request params
	if client.lastProposeReq.MaxSuggestions != 3 {
		t.Errorf("max_suggestions=%d want 3", client.lastProposeReq.MaxSuggestions)
	}
	if client.lastProposeReq.Apply {
		t.Errorf("propose request should not auto-apply edits")
	}
}

func TestProposeEditsMethodNotAllowed(t *testing.T) {
	srv := New(&mockACPClient{})
	req := httptest.NewRequest(http.MethodGet, "/propose_edits", nil)
	rec := httptest.NewRecorder()

	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestProposeEditsBadJSON(t *testing.T) {
	srv := New(&mockACPClient{})
	req := httptest.NewRequest(http.MethodPost, "/propose_edits", strings.NewReader("not json"))
	rec := httptest.NewRecorder()

	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestProposeEditsInvalidDocument(t *testing.T) {
	srv := New(&mockACPClient{})
	// Negative character in selection
	reqBody := []byte(`{"agent_id":"a1","session_id":"s1","instruction":"fix","max_suggestions":1,"document":{"uri":"file:///x","language_id":"go","content":"","selection":{"start":{"line":0,"character":-5},"end":{"line":0,"character":0}}}}`)
	req := httptest.NewRequest(http.MethodPost, "/propose_edits", bytes.NewReader(reqBody))
	rec := httptest.NewRecorder()

	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestProposeEditsNegativeMaxSuggestions(t *testing.T) {
	srv := New(&mockACPClient{})
	reqBody := []byte(`{"agent_id":"a1","session_id":"s1","instruction":"fix","max_suggestions":-1,"document":{"uri":"file:///x","language_id":"go","content":""}}`)
	req := httptest.NewRequest(http.MethodPost, "/propose_edits", bytes.NewReader(reqBody))
	rec := httptest.NewRecorder()

	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want %d body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestProposeEditsClientError(t *testing.T) {
	client := &mockACPClient{proposeErr: errors.New("rpc failed provider-secret-status")}
	srv := New(client)

	reqBody := []byte(`{"agent_id":"a1","session_id":"s1","instruction":"fix","max_suggestions":1,"document":{"uri":"file:///x","language_id":"go","content":""}}`)
	req := httptest.NewRequest(http.MethodPost, "/propose_edits", bytes.NewReader(reqBody))
	rec := httptest.NewRecorder()

	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want %d", rec.Code, http.StatusInternalServerError)
	}
	var body proposeErrorResponseForTest
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v body=%s", err, rec.Body.String())
	}
	if body.Error != "propose edits incomplete" || !body.Incomplete || body.Accepted == nil || *body.Accepted {
		t.Fatalf("body = %+v, want incomplete accepted=false", body)
	}
	if body.Text != "" {
		t.Fatalf("ordinary error should not invent draft text: %q", body.Text)
	}
	if len(body.Suggestions) != 0 {
		t.Fatalf("ordinary error should not return accepted suggestions: %+v", body.Suggestions)
	}
	if strings.Contains(rec.Body.String(), "provider-secret-status") || strings.Contains(rec.Body.String(), "rpc failed") {
		t.Fatalf("raw propose error leaked in body: %s", rec.Body.String())
	}
}

func TestProposeEditsTypedPartialReturnsBoundedUnacceptedDraft(t *testing.T) {
	publicDraft := strings.Repeat("go✓", 900)
	client := &mockACPClient{
		proposeErr: partialresult.StatusError(codes.Internal, "safe incomplete", &acppb.PartialResult{
			ReasonCode:      "provider_error",
			PartialResponse: &acppb.Message{Role: "assistant", Content: publicDraft},
		}),
	}
	srv := New(client)

	reqBody := []byte(`{"agent_id":"a1","session_id":"s1","instruction":"fix","max_suggestions":1,"document":{"uri":"file:///x","language_id":"go","content":""}}`)
	req := httptest.NewRequest(http.MethodPost, "/propose_edits", bytes.NewReader(reqBody))
	rec := httptest.NewRecorder()

	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want %d body=%s", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}
	var body proposeErrorResponseForTest
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v body=%s", err, rec.Body.String())
	}
	if body.Error != "propose edits incomplete" || !body.Incomplete || body.Accepted == nil || *body.Accepted {
		t.Fatalf("body = %+v, want incomplete accepted=false", body)
	}
	if body.Text == "" || len(body.Text) > inlinePartialTextMaxBytesForTest || !strings.HasPrefix(publicDraft, body.Text) {
		t.Fatalf("text length=%d prefix=%t want bounded public draft", len(body.Text), strings.HasPrefix(publicDraft, body.Text))
	}
	if len(body.Suggestions) != 0 {
		t.Fatalf("partial error should not return accepted suggestions: %+v", body.Suggestions)
	}
	if !client.lastProposeReq.GetApply() {
		return
	}
	t.Fatalf("propose request should not auto-apply edits")
}

// --- Apply edits handler tests ---

func TestApplyEditsSuccess(t *testing.T) {
	client := &mockACPClient{
		applyResp: &acppb.ApplyEditsResponse{Applied: true},
	}
	srv := New(client)

	reqBody := []byte(`{"agent_id":"a1","session_id":"s1","title":"My Edit","dry_run":false,"edits":[{"uri":"file:///x","new_text":"new content"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/apply_edits", bytes.NewReader(reqBody))
	rec := httptest.NewRecorder()

	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	// Verify request was passed correctly
	if client.lastApplyReq.Title != "My Edit" {
		t.Errorf("title=%q want 'My Edit'", client.lastApplyReq.Title)
	}
	if client.lastApplyReq.DryRun {
		t.Error("dry_run should be false")
	}
	if len(client.lastApplyReq.Edits) != 1 {
		t.Errorf("edits count=%d want 1", len(client.lastApplyReq.Edits))
	}
}

func TestApplyEditsWithRange(t *testing.T) {
	client := &mockACPClient{
		applyResp: &acppb.ApplyEditsResponse{Applied: true},
	}
	srv := New(client)

	reqBody := []byte(`{"agent_id":"a1","session_id":"s1","edits":[{"uri":"file:///x","range":{"start":{"line":0,"character":0},"end":{"line":0,"character":5}},"new_text":"hello"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/apply_edits", bytes.NewReader(reqBody))
	rec := httptest.NewRecorder()

	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want %d", rec.Code, http.StatusOK)
	}
	if client.lastApplyReq.Edits[0].Range == nil {
		t.Error("expected range to be set")
	}
	if client.lastApplyReq.Edits[0].Range.Start.Line != 0 {
		t.Error("range.start.line should be 0")
	}
}

func TestApplyEditsMethodNotAllowed(t *testing.T) {
	srv := New(&mockACPClient{})
	req := httptest.NewRequest(http.MethodGet, "/apply_edits", nil)
	rec := httptest.NewRecorder()

	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestApplyEditsBadJSON(t *testing.T) {
	srv := New(&mockACPClient{})
	req := httptest.NewRequest(http.MethodPost, "/apply_edits", strings.NewReader("{{"))
	rec := httptest.NewRecorder()

	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestApplyEditsInvalidRange(t *testing.T) {
	srv := New(&mockACPClient{})
	// Negative line in range
	reqBody := []byte(`{"agent_id":"a1","session_id":"s1","edits":[{"uri":"file:///x","range":{"start":{"line":-1,"character":0},"end":{"line":0,"character":0}},"new_text":"x"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/apply_edits", bytes.NewReader(reqBody))
	rec := httptest.NewRecorder()

	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestApplyEditsClientError(t *testing.T) {
	client := &mockACPClient{applyErr: errors.New("apply failed provider-secret-path-/tmp/hidden")}
	srv := New(client)

	reqBody := []byte(`{"agent_id":"a1","session_id":"s1","edits":[{"uri":"file:///x","new_text":"y"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/apply_edits", bytes.NewReader(reqBody))
	rec := httptest.NewRecorder()

	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want %d", rec.Code, http.StatusInternalServerError)
	}
	var body downstreamErrorResponseForTest
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v body=%s", err, rec.Body.String())
	}
	if body.Error != "apply edits incomplete" || !body.Incomplete {
		t.Fatalf("body = %+v, want generic incomplete apply error", body)
	}
	if body.Accepted != nil || body.Applied != nil {
		t.Fatalf("apply error should not claim accepted/applied state: %+v", body)
	}
	if strings.Contains(rec.Body.String(), "provider-secret-path") || strings.Contains(rec.Body.String(), "apply failed") {
		t.Fatalf("raw apply error leaked in body: %s", rec.Body.String())
	}
}

func TestApplyEditsNilResponseIsSafeIncompleteJSON(t *testing.T) {
	client := &mockACPClient{applyNilResp: true}
	srv := New(client)

	reqBody := []byte(`{"agent_id":"a1","session_id":"s1","dry_run":true,"edits":[{"uri":"file:///x","new_text":"y"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/apply_edits", bytes.NewReader(reqBody))
	rec := httptest.NewRecorder()

	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want %d body=%s", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}
	var body downstreamErrorResponseForTest
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v body=%s", err, rec.Body.String())
	}
	if body.Error != "apply edits incomplete" || !body.Incomplete || body.Accepted != nil || body.Applied != nil {
		t.Fatalf("body = %+v, want generic incomplete apply nil-response error", body)
	}
	if !client.lastApplyReq.GetDryRun() {
		t.Fatal("dry_run should still be passed through")
	}
}

func TestApplyEditsDryRun(t *testing.T) {
	client := &mockACPClient{}
	srv := New(client)

	reqBody := []byte(`{"agent_id":"a1","session_id":"s1","dry_run":true,"edits":[{"uri":"file:///x","new_text":"test"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/apply_edits", bytes.NewReader(reqBody))
	rec := httptest.NewRecorder()

	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want %d", rec.Code, http.StatusOK)
	}
	if !client.lastApplyReq.DryRun {
		t.Error("dry_run should be true")
	}
}

// --- Status handler tests ---

func TestStatusSuccess(t *testing.T) {
	client := &mockACPClient{
		statusResp: &acppb.UpdateEditorStateResponse{},
	}
	srv := New(client)

	reqBody := []byte(`{"agent_id":"a1","session_id":"s1","document":{"uri":"file:///x","language_id":"go","content":"package main"}}`)
	req := httptest.NewRequest(http.MethodPost, "/status", bytes.NewReader(reqBody))
	rec := httptest.NewRecorder()

	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if client.lastStatusReq.AgentId != "a1" {
		t.Errorf("agent_id=%q want 'a1'", client.lastStatusReq.AgentId)
	}
}

func TestStatusMethodNotAllowed(t *testing.T) {
	srv := New(&mockACPClient{})
	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	rec := httptest.NewRecorder()

	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestStatusBadJSON(t *testing.T) {
	srv := New(&mockACPClient{})
	req := httptest.NewRequest(http.MethodPost, "/status", strings.NewReader("bad"))
	rec := httptest.NewRecorder()

	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestStatusInvalidDocument(t *testing.T) {
	srv := New(&mockACPClient{})
	// Invalid selection end line
	reqBody := []byte(`{"agent_id":"a1","session_id":"s1","document":{"uri":"file:///x","language_id":"go","content":"","selection":{"start":{"line":0,"character":0},"end":{"line":-1,"character":0}}}}`)
	req := httptest.NewRequest(http.MethodPost, "/status", bytes.NewReader(reqBody))
	rec := httptest.NewRecorder()

	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestStatusClientError(t *testing.T) {
	client := &mockACPClient{statusErr: errors.New("status failed provider-secret-status")}
	srv := New(client)

	reqBody := []byte(`{"agent_id":"a1","session_id":"s1","document":{"uri":"file:///x","language_id":"go","content":""}}`)
	req := httptest.NewRequest(http.MethodPost, "/status", bytes.NewReader(reqBody))
	rec := httptest.NewRecorder()

	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want %d", rec.Code, http.StatusInternalServerError)
	}
	var body downstreamErrorResponseForTest
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v body=%s", err, rec.Body.String())
	}
	if body.Error != "editor status incomplete" || !body.Incomplete {
		t.Fatalf("body = %+v, want generic incomplete status error", body)
	}
	if body.Accepted != nil || body.Applied != nil {
		t.Fatalf("status error should not claim accepted/applied state: %+v", body)
	}
	if strings.Contains(rec.Body.String(), "provider-secret-status") || strings.Contains(rec.Body.String(), "status failed") {
		t.Fatalf("raw status error leaked in body: %s", rec.Body.String())
	}
}

func TestStatusNilResponseIsSafeIncompleteJSON(t *testing.T) {
	client := &mockACPClient{statusNilResp: true}
	srv := New(client)

	reqBody := []byte(`{"agent_id":"a1","session_id":"s1","document":{"uri":"file:///x","language_id":"go","content":""}}`)
	req := httptest.NewRequest(http.MethodPost, "/status", bytes.NewReader(reqBody))
	rec := httptest.NewRecorder()

	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want %d body=%s", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}
	var body downstreamErrorResponseForTest
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v body=%s", err, rec.Body.String())
	}
	if body.Error != "editor status incomplete" || !body.Incomplete || body.Accepted != nil || body.Applied != nil {
		t.Fatalf("body = %+v, want generic incomplete status nil-response error", body)
	}
}

// --- View state handler tests ---

func TestViewStateSuccess(t *testing.T) {
	vp := &mockViewProvider{
		state: &viewmodel.SessionState{
			ID:    "s1",
			Title: "Test Session",
		},
	}
	srv := New(&mockACPClient{}, WithViewProvider(vp))

	req := httptest.NewRequest(http.MethodGet, "/view_state?session_id=s1", nil)
	rec := httptest.NewRecorder()

	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Test Session") {
		t.Errorf("response should contain title: %s", rec.Body.String())
	}
}

func TestViewStateMethodNotAllowed(t *testing.T) {
	srv := New(&mockACPClient{}, WithViewProvider(&mockViewProvider{}))
	req := httptest.NewRequest(http.MethodPost, "/view_state?session_id=s1", nil)
	rec := httptest.NewRecorder()

	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestViewStateNoProvider(t *testing.T) {
	srv := New(&mockACPClient{}) // no view provider
	req := httptest.NewRequest(http.MethodGet, "/view_state?session_id=s1", nil)
	rec := httptest.NewRecorder()

	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestViewStateMissingSessionID(t *testing.T) {
	srv := New(&mockACPClient{}, WithViewProvider(&mockViewProvider{}))
	req := httptest.NewRequest(http.MethodGet, "/view_state", nil)
	rec := httptest.NewRecorder()

	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestViewStateProviderError(t *testing.T) {
	vp := &mockViewProvider{err: errors.New("provider failed provider-secret-view /tmp/private")}
	srv := New(&mockACPClient{}, WithViewProvider(vp))

	req := httptest.NewRequest(http.MethodGet, "/view_state?session_id=s1", nil)
	rec := httptest.NewRecorder()

	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want %d", rec.Code, http.StatusInternalServerError)
	}
	var body downstreamErrorResponseForTest
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v body=%s", err, rec.Body.String())
	}
	if body.Error != "view state unavailable" || !body.Incomplete {
		t.Fatalf("body = %+v, want generic incomplete view error", body)
	}
	if body.Accepted != nil || body.Applied != nil {
		t.Fatalf("view error should not claim accepted/applied state: %+v", body)
	}
	if strings.Contains(rec.Body.String(), "provider-secret-view") || strings.Contains(rec.Body.String(), "provider failed") || strings.Contains(rec.Body.String(), "/tmp/private") {
		t.Fatalf("raw view provider error leaked in body: %s", rec.Body.String())
	}
}

func TestViewStateSessionNotFound(t *testing.T) {
	vp := &mockViewProvider{state: nil} // nil state = not found
	srv := New(&mockACPClient{}, WithViewProvider(vp))

	req := httptest.NewRequest(http.MethodGet, "/view_state?session_id=nonexistent", nil)
	rec := httptest.NewRecorder()

	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d want %d", rec.Code, http.StatusNotFound)
	}
}

// --- Helper function tests ---

func TestIntToNonNegativeInt32(t *testing.T) {
	tests := []struct {
		name    string
		input   int
		want    int32
		wantErr bool
	}{
		{"zero", 0, 0, false},
		{"positive", 100, 100, false},
		{"max int32", math.MaxInt32, math.MaxInt32, false},
		{"negative", -1, 0, true},
		{"large negative", -1000, 0, true},
		{"overflow", math.MaxInt32 + 1, 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := intToNonNegativeInt32(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("intToNonNegativeInt32(%d) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("intToNonNegativeInt32(%d) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

func TestToEditorContext(t *testing.T) {
	tests := []struct {
		name    string
		doc     document
		wantErr bool
	}{
		{
			name: "basic document",
			doc: document{
				URI:        "file:///test.go",
				LanguageID: "go",
				Content:    "package main",
			},
			wantErr: false,
		},
		{
			name: "with selection",
			doc: document{
				URI:        "file:///test.go",
				LanguageID: "go",
				Content:    "package main\nfunc main() {}",
				Selection: &rangeJSON{
					Start: position{Line: 0, Character: 0},
					End:   position{Line: 0, Character: 7},
				},
			},
			wantErr: false,
		},
		{
			name: "negative start line",
			doc: document{
				URI:        "file:///test.go",
				LanguageID: "go",
				Content:    "",
				Selection: &rangeJSON{
					Start: position{Line: -1, Character: 0},
					End:   position{Line: 0, Character: 0},
				},
			},
			wantErr: true,
		},
		{
			name: "negative start character",
			doc: document{
				URI:        "file:///test.go",
				LanguageID: "go",
				Content:    "",
				Selection: &rangeJSON{
					Start: position{Line: 0, Character: -1},
					End:   position{Line: 0, Character: 0},
				},
			},
			wantErr: true,
		},
		{
			name: "negative end line",
			doc: document{
				URI:        "file:///test.go",
				LanguageID: "go",
				Content:    "",
				Selection: &rangeJSON{
					Start: position{Line: 0, Character: 0},
					End:   position{Line: -1, Character: 0},
				},
			},
			wantErr: true,
		},
		{
			name: "negative end character",
			doc: document{
				URI:        "file:///test.go",
				LanguageID: "go",
				Content:    "",
				Selection: &rangeJSON{
					Start: position{Line: 0, Character: 0},
					End:   position{Line: 0, Character: -1},
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, err := toEditorContext(tt.doc)
			if (err != nil) != tt.wantErr {
				t.Errorf("toEditorContext() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if ctx == nil {
					t.Error("expected non-nil context")
					return
				}
				if ctx.Document.Uri != tt.doc.URI {
					t.Errorf("URI = %q, want %q", ctx.Document.Uri, tt.doc.URI)
				}
				if ctx.Document.LanguageId != tt.doc.LanguageID {
					t.Errorf("LanguageId = %q, want %q", ctx.Document.LanguageId, tt.doc.LanguageID)
				}
				if ctx.Document.Content != tt.doc.Content {
					t.Errorf("Content = %q, want %q", ctx.Document.Content, tt.doc.Content)
				}
				if tt.doc.Selection != nil && ctx.Document.Selection == nil {
					t.Error("expected Selection to be set")
				}
			}
		})
	}
}

func TestToPBEdits(t *testing.T) {
	tests := []struct {
		name    string
		edits   []textEdit
		wantLen int
		wantErr bool
	}{
		{
			name:    "empty",
			edits:   []textEdit{},
			wantLen: 0,
			wantErr: false,
		},
		{
			name: "single edit no range",
			edits: []textEdit{
				{URI: "file:///x", NewText: "new"},
			},
			wantLen: 1,
			wantErr: false,
		},
		{
			name: "single edit with range",
			edits: []textEdit{
				{
					URI:     "file:///x",
					NewText: "new",
					Range: &rangeJSON{
						Start: position{Line: 0, Character: 0},
						End:   position{Line: 0, Character: 3},
					},
				},
			},
			wantLen: 1,
			wantErr: false,
		},
		{
			name: "multiple edits",
			edits: []textEdit{
				{URI: "file:///x", NewText: "a"},
				{URI: "file:///y", NewText: "b"},
			},
			wantLen: 2,
			wantErr: false,
		},
		{
			name: "invalid range start line",
			edits: []textEdit{
				{
					URI:     "file:///x",
					NewText: "new",
					Range: &rangeJSON{
						Start: position{Line: -1, Character: 0},
						End:   position{Line: 0, Character: 0},
					},
				},
			},
			wantErr: true,
		},
		{
			name: "invalid range start character",
			edits: []textEdit{
				{
					URI:     "file:///x",
					NewText: "new",
					Range: &rangeJSON{
						Start: position{Line: 0, Character: -1},
						End:   position{Line: 0, Character: 0},
					},
				},
			},
			wantErr: true,
		},
		{
			name: "invalid range end line",
			edits: []textEdit{
				{
					URI:     "file:///x",
					NewText: "new",
					Range: &rangeJSON{
						Start: position{Line: 0, Character: 0},
						End:   position{Line: -1, Character: 0},
					},
				},
			},
			wantErr: true,
		},
		{
			name: "invalid range end character",
			edits: []textEdit{
				{
					URI:     "file:///x",
					NewText: "new",
					Range: &rangeJSON{
						Start: position{Line: 0, Character: 0},
						End:   position{Line: 0, Character: -1},
					},
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := toPBEdits(tt.edits)
			if (err != nil) != tt.wantErr {
				t.Errorf("toPBEdits() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && len(result) != tt.wantLen {
				t.Errorf("len(result) = %d, want %d", len(result), tt.wantLen)
			}
		})
	}
}

func TestWithAgentMetadata(t *testing.T) {
	tests := []struct {
		name         string
		agentID      string
		expectHeader bool
	}{
		{"empty agent id", "", false},
		{"whitespace only", "   ", false},
		{"valid agent id", "agent-123", true},
		{"agent id with spaces", "  agent-456  ", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := withAgentMetadata(context.Background(), tt.agentID)
			md, ok := metadata.FromOutgoingContext(ctx)
			if tt.expectHeader {
				if !ok {
					t.Error("expected metadata in context")
					return
				}
				vals := md.Get("x-buckley-agent-id")
				if len(vals) != 1 {
					t.Errorf("expected 1 value, got %d", len(vals))
					return
				}
				// The value should be trimmed
				if vals[0] != strings.TrimSpace(tt.agentID) {
					t.Errorf("value = %q, want %q", vals[0], strings.TrimSpace(tt.agentID))
				}
			} else {
				if ok && len(md.Get("x-buckley-agent-id")) > 0 {
					t.Error("did not expect metadata")
				}
			}
		})
	}
}

func TestWriteJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	data := map[string]string{"key": "value"}
	writeJSON(rec, data)

	if rec.Header().Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type = %q, want 'application/json'", rec.Header().Get("Content-Type"))
	}

	var result map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if result["key"] != "value" {
		t.Errorf("key = %q, want 'value'", result["key"])
	}
}

// --- Edge case and integration tests ---

func TestRoutesRegistered(t *testing.T) {
	srv := New(&mockACPClient{})
	router := srv.Router()

	// Test that all expected routes are registered (will return 405 for wrong method)
	routes := []string{"/inline_complete", "/propose_edits", "/apply_edits", "/status", "/view_state"}
	for _, route := range routes {
		req := httptest.NewRequest(http.MethodOptions, route, nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		// 405 means route exists but method not allowed; 404 means route not found
		if rec.Code == http.StatusNotFound {
			t.Errorf("route %s not found", route)
		}
	}
}

func TestEmptyStreamResponse(t *testing.T) {
	stream := &fakeInlineStream{
		events: []*acppb.InlineCompletionEvent{},
	}
	srv := New(stubACPClient{stream: stream})

	reqBody := []byte(`{"agent_id":"a1","session_id":"s1","prompt":"p","document":{"uri":"file:///x","language_id":"go","content":""}}`)
	req := httptest.NewRequest(http.MethodPost, "/inline_complete", bytes.NewReader(reqBody))
	rec := httptest.NewRecorder()

	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want %d body=%q", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}
	var body inlineErrorResponseForTest
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v; body=%q", err, rec.Body.String())
	}
	if body.Text != "" || body.FinishReason != "" || !body.Incomplete || body.Accepted == nil || *body.Accepted {
		t.Fatalf("body = %+v, want safe empty incomplete response", body)
	}
}

func TestMultipleEditsValidation(t *testing.T) {
	client := &mockACPClient{}
	srv := New(client)

	// First edit valid, second invalid
	reqBody := []byte(`{"agent_id":"a1","session_id":"s1","edits":[{"uri":"file:///x","new_text":"ok"},{"uri":"file:///y","range":{"start":{"line":-1,"character":0},"end":{"line":0,"character":0}},"new_text":"bad"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/apply_edits", bytes.NewReader(reqBody))
	rec := httptest.NewRecorder()

	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want %d", rec.Code, http.StatusBadRequest)
	}
}
