package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
	"unsafe"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
	"m31labs.dev/buckley/pkg/acp/partialresult"
	acppb "m31labs.dev/buckley/pkg/acp/proto"
	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/coordination/coordinator"
	"m31labs.dev/buckley/pkg/coordination/events"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/rlm"
	"m31labs.dev/buckley/pkg/storage"
	"m31labs.dev/buckley/pkg/transparency"
)

func TestACPServerSendMessageDirectModelErrorReturnsPartialDetail(t *testing.T) {
	const secret = "provider secret sentinel"
	provider := &partialServerProvider{
		resp: &model.ChatResponse{
			ID:    "chatcmpl-direct-partial",
			Model: "provider-direct-model",
			Choices: []model.Choice{{
				Message:      model.Message{Role: "assistant", Content: "public direct draft"},
				FinishReason: "length",
			}},
			Usage:        model.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
			UsagePresent: true,
		},
		err: fmt.Errorf("raw provider failure: %s", secret),
	}
	srv := newPartialServer(t, partialServerOptions{models: newPartialModelManager(t, provider), cfg: nil, store: nil})
	client, cleanup := newPartialBufconnClient(t, srv)
	defer cleanup()

	_, err := client.SendMessage(context.Background(), &acppb.SendMessageRequest{
		AgentId: "zed",
		Message: &acppb.Message{Role: "user", Content: "question"},
	})
	detail := requirePartialDetail(t, err, codes.Internal)
	if provider.calls.Load() != 1 {
		t.Fatalf("provider calls = %d, want one", provider.calls.Load())
	}
	if detail.GetSchemaVersion() != partialresult.SchemaVersion || !detail.GetIncomplete() || detail.GetReasonCode() != "error" {
		t.Fatalf("detail = %+v, want incomplete error schema", detail)
	}
	if got := detail.GetPartialResponse().GetContent(); got != "public direct draft" {
		t.Fatalf("partial response = %q, want labeled public draft", got)
	}
	if strings.Contains(detail.String(), secret) {
		t.Fatalf("partial detail leaked provider secret: %s", detail.String())
	}
	if got := detail.GetTaskResults()[0].GetUsage(); got.GetInputTokens() != 10 || got.GetOutputTokens() != 5 || got.GetReportedTotalTokens() != 15 || !got.GetUsageEvidencePresent() {
		t.Fatalf("usage detail = %+v, want response usage", got)
	}
	if got := detail.GetModelIdentities()[0]; got.GetRequestedModel() != "openai/gpt-5.4" || got.GetProviderId() != "openai" || got.GetResponseId() != "chatcmpl-direct-partial" {
		t.Fatalf("identity = %+v, want routed direct identity", got)
	}
}

func TestACPServerSendMessageDirectModelUsesCallerContext(t *testing.T) {
	started := make(chan struct{})
	provider := &partialServerProvider{
		chat: func(ctx context.Context, _ model.ChatRequest) (*model.ChatResponse, error) {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	srv := newPartialServer(t, partialServerOptions{models: newPartialModelManager(t, provider), cfg: nil, store: nil})
	client, cleanup := newPartialBufconnClient(t, srv)
	defer cleanup()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := client.SendMessage(ctx, &acppb.SendMessageRequest{
			AgentId: "zed",
			Message: &acppb.Message{Role: "user", Content: "question"},
		})
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("provider was not called")
	}
	cancel()
	select {
	case err := <-done:
		if status.Code(err) != codes.Canceled {
			t.Fatalf("SendMessage error = %v, want caller cancellation", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SendMessage did not return after caller cancellation")
	}
	if provider.calls.Load() != 1 {
		t.Fatalf("provider calls = %d, want one canceled execution", provider.calls.Load())
	}
}

func TestACPServerStreamTaskDirectModelReturnsRealCompletionEvent(t *testing.T) {
	provider := &partialServerProvider{
		resp: &model.ChatResponse{
			Choices: []model.Choice{{
				Message: model.Message{Role: "assistant", Content: "direct stream answer"},
			}},
		},
	}
	srv := newPartialServer(t, partialServerOptions{models: newPartialModelManager(t, provider), cfg: nil, store: nil})
	client, cleanup := newPartialBufconnClient(t, srv)
	defer cleanup()

	stream, err := client.StreamTask(context.Background(), &acppb.TaskStreamRequest{AgentId: "zed", TaskId: "direct-stream", Query: "answer"})
	if err != nil {
		t.Fatalf("StreamTask: %v", err)
	}
	event, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv completion event: %v", err)
	}
	if event.GetStatus() != "completed" || event.GetMessage() != "direct stream answer" {
		t.Fatalf("event = %+v, want real completed model output", event)
	}
	if event.GetMessage() == "Processing task..." || event.GetMessage() == "Done." {
		t.Fatalf("event = %+v, want no fabricated progress/done event", event)
	}
	if _, err := stream.Recv(); err != io.EOF {
		t.Fatalf("second Recv = %v, want EOF", err)
	}
	if provider.calls.Load() != 1 {
		t.Fatalf("provider calls = %d, want one direct model request", provider.calls.Load())
	}
}

func TestACPServerStreamTaskDirectModelErrorEmitsBoundedPartialDetail(t *testing.T) {
	const secret = "stream provider secret sentinel"
	large := strings.Repeat("π", partialresult.MaxDraftBytes)
	provider := &partialServerProvider{
		resp: &model.ChatResponse{
			ID:    "chatcmpl-direct-stream-partial",
			Model: "provider-stream-partial-model",
			Choices: []model.Choice{{
				Message:      model.Message{Role: "assistant", Content: "public " + large},
				FinishReason: "length",
			}},
			Usage:        model.Usage{PromptTokens: 21, CompletionTokens: 7, TotalTokens: 28},
			UsagePresent: true,
		},
		err: fmt.Errorf("raw stream provider failure: %s", secret),
	}
	srv := newPartialServer(t, partialServerOptions{models: newPartialModelManager(t, provider), cfg: nil, store: nil})
	client, cleanup := newPartialBufconnClient(t, srv)
	defer cleanup()

	stream, err := client.StreamTask(context.Background(), &acppb.TaskStreamRequest{AgentId: "zed", TaskId: "direct-stream-partial", Query: "answer"})
	if err != nil {
		t.Fatalf("StreamTask: %v", err)
	}
	event, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv incomplete event: %v", err)
	}
	if event.GetStatus() != "incomplete" || !strings.Contains(event.GetMessage(), "public ") {
		t.Fatalf("event = %+v, want labeled incomplete draft", event)
	}
	if len(event.GetMessage()) > partialresult.MaxDraftBytes || !utf8.ValidString(event.GetMessage()) {
		t.Fatalf("event message len=%d utf8=%t, want shared draft bound", len(event.GetMessage()), utf8.ValidString(event.GetMessage()))
	}
	_, err = stream.Recv()
	detail := requirePartialDetail(t, err, codes.Internal)
	if proto.Size(status.Convert(err).Proto()) > partialresult.MaxStatusBytes {
		t.Fatalf("status proto size = %d, want <= %d", proto.Size(status.Convert(err).Proto()), partialresult.MaxStatusBytes)
	}
	if strings.Contains(detail.String(), secret) {
		t.Fatalf("terminal detail leaked provider secret: %s", detail.String())
	}
	if got := detail.GetTaskResults()[0].GetUsage(); got.GetInputTokens() != 21 || got.GetOutputTokens() != 7 || got.GetReportedTotalTokens() != 28 {
		t.Fatalf("usage detail = %+v, want response usage", got)
	}
	if provider.calls.Load() != 1 {
		t.Fatalf("provider calls = %d, want no fallback replay", provider.calls.Load())
	}
}

func TestACPServerStreamTaskModelsUnavailableFailsPreconditionNoFakeDone(t *testing.T) {
	srv := newPartialServer(t, partialServerOptions{models: nil, cfg: nil, store: nil})
	client, cleanup := newPartialBufconnClient(t, srv)
	defer cleanup()

	stream, err := client.StreamTask(context.Background(), &acppb.TaskStreamRequest{AgentId: "zed", TaskId: "no-models", Query: "answer"})
	if err != nil {
		t.Fatalf("StreamTask: %v", err)
	}
	_, err = stream.Recv()
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("Recv error = %v, want FailedPrecondition before any fake events", err)
	}
}

func TestACPServerSendMessageClassicPlanErrorUsesSafeStatus(t *testing.T) {
	const secret = "classic plan secret sentinel"
	provider := &partialServerProvider{err: fmt.Errorf("planner failed: %s", secret)}
	srv := newClassicPartialServer(t, provider)
	client, cleanup := newPartialBufconnClient(t, srv)
	defer cleanup()

	_, err := client.SendMessage(context.Background(), &acppb.SendMessageRequest{
		AgentId: "zed",
		Message: &acppb.Message{Role: "user", Content: "classic plan"},
	})
	if status.Code(err) != codes.Internal {
		t.Fatalf("SendMessage error = %v, want Internal", err)
	}
	if got := status.Convert(err).Message(); got != "execution incomplete" || strings.Contains(got, secret) {
		t.Fatalf("status message = %q, want generic execution error without secret", got)
	}
	if len(status.Convert(err).Details()) != 0 {
		t.Fatalf("details = %+v, want no accepted partial detail for classic plan failure", status.Convert(err).Details())
	}
	if provider.calls.Load() != 1 {
		t.Fatalf("provider calls = %d, want one planner attempt", provider.calls.Load())
	}
}

func TestACPServerStreamTaskClassicPlanErrorSendsGenericIncomplete(t *testing.T) {
	const secret = "classic stream secret sentinel"
	provider := &partialServerProvider{err: fmt.Errorf("planner failed: %s", secret)}
	srv := newClassicPartialServer(t, provider)
	client, cleanup := newPartialBufconnClient(t, srv)
	defer cleanup()

	stream, err := client.StreamTask(context.Background(), &acppb.TaskStreamRequest{AgentId: "zed", TaskId: "classic-stream", Query: "classic plan"})
	if err != nil {
		t.Fatalf("StreamTask: %v", err)
	}
	event, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv planning event: %v", err)
	}
	if event.GetMessage() != "Planning task…" || event.GetStatus() != "" {
		t.Fatalf("first event = %+v, want existing progress event", event)
	}
	event, err = stream.Recv()
	if err != nil {
		t.Fatalf("Recv incomplete event: %v", err)
	}
	if event.GetStatus() != "incomplete" || event.GetMessage() != "Task incomplete (not accepted)" {
		t.Fatalf("incomplete event = %+v, want generic incomplete marker", event)
	}
	if strings.Contains(event.String(), secret) {
		t.Fatalf("incomplete event leaked secret: %s", event.String())
	}
	_, err = stream.Recv()
	if status.Code(err) != codes.Internal {
		t.Fatalf("terminal error = %v, want Internal", err)
	}
	if got := status.Convert(err).Message(); got != "execution incomplete" || strings.Contains(got, secret) {
		t.Fatalf("terminal status = %q, want generic error without secret", got)
	}
	if len(status.Convert(err).Details()) != 0 {
		t.Fatalf("terminal details = %+v, want no classic partial detail", status.Convert(err).Details())
	}
	if provider.calls.Load() != 1 {
		t.Fatalf("provider calls = %d, want one planner attempt", provider.calls.Load())
	}
}

func TestACPServerSendMessageRLMErrorReturnsPartialDetail(t *testing.T) {
	const secret = "private reasoning sentinel"
	var calls atomic.Int32
	srv := newRLMPartialServer(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, fmt.Sprintf(`{
			"id":"chatcmpl-rlm-partial","model":"provider-rlm-model",
			"choices":[{"index":0,"message":{"role":"assistant","content":"public rlm draft","reasoning":"%s"},"finish_reason":"length"}],
			"usage":{"prompt_tokens":12,"completion_tokens":6,"total_tokens":18}
		}`, secret))
	})
	client, cleanup := newPartialBufconnClient(t, srv)
	defer cleanup()

	_, err := client.SendMessage(context.Background(), &acppb.SendMessageRequest{
		AgentId: "zed",
		Message: &acppb.Message{Role: "user", Content: "run coordinated partial"},
	})
	detail := requirePartialDetail(t, err, codes.Internal)
	if calls.Load() != 1 {
		t.Fatalf("model calls = %d, want one RLM execution", calls.Load())
	}
	if got := detail.GetPartialResponse().GetContent(); !strings.Contains(got, "public rlm draft") {
		t.Fatalf("partial response = %q, want RLM public draft", got)
	}
	if strings.Contains(detail.String(), secret) {
		t.Fatalf("partial detail leaked private sentinel: %s", detail.String())
	}
	if len(detail.GetTaskResults()) != 0 {
		t.Fatalf("task results = %+v, want none without delegation", detail.GetTaskResults())
	}
}

func TestACPServerLegacyRLMModeSelectsCoordinatedRuntime(t *testing.T) {
	var calls atomic.Int32
	srv := newRLMPartialServer(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id":"chatcmpl-coordinated","model":"gpt-5.4",
			"choices":[{"index":0,"message":{"role":"assistant","content":"coordinated result"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
		}`)
	})

	response, err := srv.SendMessage(context.Background(), &acppb.SendMessageRequest{
		AgentId: "zed",
		Message: &acppb.Message{Role: "user", Content: "run coordinated execution"},
	})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if response.GetResponse().GetContent() != "coordinated result" {
		t.Fatalf("response = %+v, want coordinated result", response)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("model calls = %d, want 1 coordinated runtime call", got)
	}
}

func TestACPServerStreamTaskRLMErrorEmitsIncompleteThenTerminalDetail(t *testing.T) {
	var calls atomic.Int32
	srv := newRLMPartialServer(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id":"chatcmpl-stream-partial","model":"provider-stream-model",
			"choices":[{"index":0,"message":{"role":"assistant","content":"stream public draft"},"finish_reason":"length"}],
			"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}
		}`)
	})
	client, cleanup := newPartialBufconnClient(t, srv)
	defer cleanup()

	stream, err := client.StreamTask(context.Background(), &acppb.TaskStreamRequest{AgentId: "zed", TaskId: "task-stream", Query: "stream partial"})
	if err != nil {
		t.Fatalf("StreamTask: %v", err)
	}
	event, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv incomplete event: %v", err)
	}
	if event.GetStatus() != "incomplete" || !strings.Contains(event.GetMessage(), "stream public draft") {
		t.Fatalf("event = %+v, want incomplete public draft event", event)
	}
	_, err = stream.Recv()
	detail := requirePartialDetail(t, err, codes.Internal)
	if !strings.Contains(detail.GetPartialResponse().GetContent(), "stream public draft") {
		t.Fatalf("terminal detail = %+v, want same partial draft", detail)
	}
	if calls.Load() != 1 {
		t.Fatalf("model calls = %d, want no fallback replay", calls.Load())
	}
}

func TestACPServerRLMInitFailureHasNoPartialDetailOrFallbackReplay(t *testing.T) {
	provider := &partialServerProvider{resp: nil, err: errors.New("should not be called")}
	cfg := config.DefaultConfig()
	cfg.Execution.Mode = config.ExecutionModeRLM
	store := newPartialStore(t)
	srv := newPartialServer(t, partialServerOptions{models: newPartialModelManagerWithoutInitialize(t, provider), cfg: cfg, store: store})
	client, cleanup := newPartialBufconnClient(t, srv)
	defer cleanup()

	_, err := client.SendMessage(context.Background(), &acppb.SendMessageRequest{
		AgentId: "zed",
		Message: &acppb.Message{Role: "user", Content: "question"},
	})
	if status.Code(err) != codes.Internal {
		t.Fatalf("SendMessage error = %v, want internal runtime unavailable", err)
	}
	if details := status.Convert(err).Details(); len(details) != 0 {
		t.Fatalf("status details = %+v, want no partial detail for init failure", details)
	}
	if provider.calls.Load() != 0 {
		t.Fatalf("provider calls = %d, want no fallback replay after RLM init failure", provider.calls.Load())
	}
}

func TestACPPartialProjectionBoundsAndOmitsPrivateToolData(t *testing.T) {
	const secret = "private tool raw sentinel"
	large := strings.Repeat("x", partialresult.MaxDraftBytes+4096)
	answer := &rlm.Answer{
		Content: large,
		TaskResults: []rlm.BatchResult{{
			TaskID:  "task-private",
			Summary: strings.Repeat("summary", 400),
			Error:   "raw provider failure " + secret,
			Usage:   transparency.TokenUsage{Input: 1, Output: 2, ReportedTotal: 3, UsageEvidencePresent: true},
			ToolCalls: []rlm.SubAgentToolCall{{
				Name: "read_file",
				Data: map[string]any{"secret": secret},
			}},
			ExecutionEvidence: []model.CommandExecutionEvidence{{Command: "secret command"}},
			ModelExecutions: []model.ExecutionIdentity{{
				RequestedModel: "openai/gpt-5.4",
				SelectedModel:  "openai/gpt-5.4",
				ProviderID:     "openai",
				ResponseModel:  "provider-model",
				ResponseID:     "resp-id",
			}},
		}},
	}

	detail := acpPartialResultFromAnswer(answer, errors.New("terminal secret "+secret))
	if detail == nil {
		t.Fatal("detail nil, want retained partial")
	}
	if protoSize := protoSizeForTest(detail); protoSize > partialresult.MaxStatusBytes {
		t.Fatalf("detail size = %d, want <= %d", protoSize, partialresult.MaxStatusBytes)
	}
	if !detail.GetTruncated() || detail.GetOriginalDraftBytes() <= partialresult.MaxDraftBytes {
		t.Fatalf("detail truncation = %t original=%d, want bounded draft metadata", detail.GetTruncated(), detail.GetOriginalDraftBytes())
	}
	encoded := detail.String()
	if strings.Contains(encoded, secret) || strings.Contains(encoded, "secret command") {
		t.Fatalf("partial detail leaked private tool/raw data: %s", encoded)
	}
	row := detail.GetTaskResults()[0]
	if row.GetError() != "task failed" || row.GetToolCallCount() != 1 || row.GetCommandCount() != 1 {
		t.Fatalf("task row = %+v, want generic error and evidence counts", row)
	}
}

func TestACPPartialProjectionRecordsOriginalDraftBytes(t *testing.T) {
	rawDraft := strings.Repeat("é", partialresult.MaxDraftBytes)
	resp := &model.ChatResponse{
		Choices: []model.Choice{{
			Message: model.Message{Role: "assistant", Content: rawDraft},
		}},
	}
	detail := acpPartialResultFromModelResponse(resp, errors.New("terminal"))
	if detail == nil || detail.GetPartialResponse() == nil {
		t.Fatal("detail missing partial response")
	}
	if got, want := detail.GetOriginalDraftBytes(), int32(len(rawDraft)); got != want {
		t.Fatalf("original_draft_bytes = %d, want raw draft length %d", got, want)
	}
	if len(detail.GetPartialResponse().GetContent()) > partialresult.MaxDraftBytes || !utf8.ValidString(detail.GetPartialResponse().GetContent()) {
		t.Fatalf("partial content len=%d utf8=%t, want shared bounded raw draft", len(detail.GetPartialResponse().GetContent()), utf8.ValidString(detail.GetPartialResponse().GetContent()))
	}
}

type partialServerOptions struct {
	models *model.Manager
	cfg    *config.Config
	store  *storage.Store
}

func newPartialServer(t *testing.T, opts partialServerOptions) *Server {
	t.Helper()
	eventStore := events.NewInMemoryStore()
	coord, err := coordinator.NewCoordinator(coordinator.DefaultConfig(), eventStore)
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	srv, err := NewServer(coord, opts.models, opts.cfg, opts.store)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return srv
}

func newPartialBufconnClient(t *testing.T, srv *Server) (acppb.AgentCommunicationClient, func()) {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	grpcServer := grpc.NewServer()
	acppb.RegisterAgentCommunicationServer(grpcServer, srv)
	go func() { _ = grpcServer.Serve(listener) }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := grpc.DialContext(ctx, "bufnet", grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
		return listener.Dial()
	}), grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock())
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	cleanup := func() {
		_ = conn.Close()
		grpcServer.Stop()
		_ = listener.Close()
	}
	return acppb.NewAgentCommunicationClient(conn), cleanup
}

func newRLMPartialServer(t *testing.T, handler http.HandlerFunc) *Server {
	t.Helper()
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	cfg := config.DefaultConfig()
	cfg.Worktrees.RootPath = t.TempDir()
	cfg.Artifacts.PlanningDir = filepath.Join(cfg.Worktrees.RootPath, "plans")
	cfg.Providers.OpenAI.Enabled = true
	cfg.Providers.OpenAI.APIKey = "test-key"
	cfg.Providers.OpenAI.BaseURL = httpServer.URL
	cfg.Models.DefaultProvider = "openai"
	cfg.Models.Execution = "openai/gpt-5.4"
	cfg.Execution.Mode = config.ExecutionModeRLM
	cfg.RLM.Coordinator.Model = "openai/gpt-5.4"
	cfg.RLM.Coordinator.MaxIterations = 1
	cfg.RLM.Coordinator.MaxTokensBudget = 1_000
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := mgr.Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	return newPartialServer(t, partialServerOptions{models: mgr, cfg: cfg, store: newPartialStore(t)})
}

func newClassicPartialServer(t *testing.T, provider *partialServerProvider) *Server {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Execution.Mode = config.ExecutionModeClassic
	cfg.Worktrees.RootPath = t.TempDir()
	cfg.Artifacts.PlanningDir = filepath.Join(cfg.Worktrees.RootPath, "plans")
	return newPartialServer(t, partialServerOptions{models: newPartialModelManager(t, provider), cfg: cfg, store: newPartialStore(t)})
}

func newPartialStore(t *testing.T) *storage.Store {
	t.Helper()
	store, err := storage.New(filepath.Join(t.TempDir(), "buckley.db"))
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func newPartialModelManager(t *testing.T, provider *partialServerProvider) *model.Manager {
	t.Helper()
	mgr := newPartialModelManagerWithoutInitialize(t, provider)
	if err := mgr.Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	return mgr
}

func newPartialModelManagerWithoutInitialize(t *testing.T, provider *partialServerProvider) *model.Manager {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Providers.OpenAI.Enabled = true
	cfg.Providers.OpenAI.APIKey = "test-key"
	cfg.Models.DefaultProvider = "openai"
	cfg.Models.Execution = "openai/gpt-5.4"
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	replacePartialProvider(t, mgr, "openai", provider)
	return mgr
}

func replacePartialProvider(t *testing.T, mgr *model.Manager, id string, provider model.Provider) {
	t.Helper()
	value := reflect.ValueOf(mgr).Elem().FieldByName("providers")
	reflect.NewAt(value.Type(), unsafe.Pointer(value.UnsafeAddr())).Elem().SetMapIndex(reflect.ValueOf(id), reflect.ValueOf(provider))
}

type partialServerProvider struct {
	chat   func(context.Context, model.ChatRequest) (*model.ChatResponse, error)
	stream func(context.Context, model.ChatRequest) (<-chan model.StreamChunk, <-chan error)
	resp   *model.ChatResponse
	err    error
	calls  atomic.Int32
}

func (p *partialServerProvider) ID() string { return "openai" }

func (p *partialServerProvider) FetchCatalog() (*model.ModelCatalog, error) {
	return &model.ModelCatalog{Data: []model.ModelInfo{{ID: "openai/gpt-5.4", Name: "test model", ContextLength: 128000}}}, nil
}

func (p *partialServerProvider) GetModelInfo(modelID string) (*model.ModelInfo, error) {
	return &model.ModelInfo{ID: modelID, ContextLength: 128000}, nil
}

func (p *partialServerProvider) ChatCompletion(ctx context.Context, req model.ChatRequest) (*model.ChatResponse, error) {
	p.calls.Add(1)
	if p.chat != nil {
		return p.chat(ctx, req)
	}
	return p.resp, p.err
}

func (p *partialServerProvider) ChatCompletionStream(ctx context.Context, req model.ChatRequest) (<-chan model.StreamChunk, <-chan error) {
	p.calls.Add(1)
	if p.stream != nil {
		return p.stream(ctx, req)
	}
	chunks := make(chan model.StreamChunk)
	errs := make(chan error)
	close(chunks)
	close(errs)
	return chunks, errs
}

func requirePartialDetail(t *testing.T, err error, code codes.Code) *acppb.PartialResult {
	t.Helper()
	if status.Code(err) != code {
		t.Fatalf("error = %v, code=%v want %v", err, status.Code(err), code)
	}
	if partial, ok := partialresult.Extract(err); ok {
		return partial
	}
	t.Fatalf("error details = %+v, want PartialResult", status.Convert(err).Details())
	return nil
}

func protoSizeForTest(detail *acppb.PartialResult) int {
	return proto.Size(detail)
}
