package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/acp"
	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model"
)

type acpCapturedStreamRequest struct {
	MaxTokens  int
	ToolCount  int
	ToolChoice string
}

type acpCancelOnUnwrapError struct {
	cancel context.CancelFunc
}

func (e *acpCancelOnUnwrapError) Error() string { return "cancel during error classification" }

func (e *acpCancelOnUnwrapError) Unwrap() error {
	if e != nil && e.cancel != nil {
		e.cancel()
	}
	return io.ErrUnexpectedEOF
}

type acpStreamRequestCapture struct {
	mu       sync.Mutex
	requests []acpCapturedStreamRequest
}

func (c *acpStreamRequestCapture) add(t *testing.T, r *http.Request) {
	t.Helper()
	var wire struct {
		MaxTokens  int              `json:"max_tokens"`
		Tools      []map[string]any `json:"tools"`
		ToolChoice string           `json:"tool_choice"`
	}
	if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
		t.Errorf("decode streamed request: %v", err)
		return
	}
	c.mu.Lock()
	c.requests = append(c.requests, acpCapturedStreamRequest{
		MaxTokens:  wire.MaxTokens,
		ToolCount:  len(wire.Tools),
		ToolChoice: wire.ToolChoice,
	})
	c.mu.Unlock()
}

func (c *acpStreamRequestCapture) snapshot() []acpCapturedStreamRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]acpCapturedStreamRequest(nil), c.requests...)
}

func newACPHTTPStreamManager(t *testing.T, handler func(http.ResponseWriter, *http.Request, int32)) (*model.Manager, *int32, *acpStreamRequestCapture) {
	t.Helper()
	var requests int32
	capture := &acpStreamRequestCapture{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ordinal := atomic.AddInt32(&requests, 1)
		capture.add(t, r)
		handler(w, r, ordinal)
	}))
	t.Cleanup(server.Close)

	cfg := configForACPParticleTerminalTest(server.URL)
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return mgr, &requests, capture
}

func configForACPParticleTerminalTest(baseURL string) *config.Config {
	cfg := config.DefaultConfig()
	cfg.Providers.OpenAI.Enabled = true
	cfg.Providers.OpenAI.APIKey = "test-key"
	cfg.Providers.OpenAI.BaseURL = baseURL
	cfg.Models.DefaultProvider = "openai"
	return cfg
}

func writeACPDone(w io.Writer) {
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func writeACPUsageEvent(t *testing.T, w io.Writer, usage model.Usage) {
	t.Helper()
	payload, err := json.Marshal(model.StreamChunk{
		Choices: []model.StreamChoice{},
		Usage:   &usage,
	})
	if err != nil {
		t.Fatalf("marshal usage event: %v", err)
	}
	if _, err := io.WriteString(w, "data: "+string(payload)+"\n\n"); err != nil {
		t.Fatalf("write usage event: %v", err)
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func TestDrainACPStreamTurn_CommitsSemanticFinishWithoutTrailer(t *testing.T) {
	finish := "stop"
	chunks := make(chan model.StreamChunk, 1)
	chunks <- model.StreamChunk{Choices: []model.StreamChoice{{
		Delta:        model.MessageDelta{Content: "answer"},
		FinishReason: &finish,
	}}}
	// Deliberately never close either channel. A semantic finish must not wait
	// for an optional usage/error trailer that the provider may leave open.
	errs := make(chan error)

	turn, err := drainACPStreamTurn(context.Background(), model.ChatRequest{Model: "glm-5.3-flash"}, chunks, errs, nil, false)
	if err != nil {
		t.Fatalf("drainACPStreamTurn: %v", err)
	}
	if got := model.ExtractTextContentOrEmpty(turn.Message.Content); got != "answer" {
		t.Fatalf("content = %q, want answer", got)
	}
	if turn.Usage != nil {
		t.Fatalf("usage = %+v, want unavailable usage", turn.Usage)
	}
}

func TestDrainACPStreamTurn_AttachesBufferedUsageTrailer(t *testing.T) {
	finish := "stop"
	usage := &model.Usage{PromptTokens: 7, CompletionTokens: 3, TotalTokens: 10}
	chunks := make(chan model.StreamChunk, 2)
	chunks <- model.StreamChunk{Choices: []model.StreamChoice{{
		Delta:        model.MessageDelta{Content: "answer"},
		FinishReason: &finish,
	}}}
	chunks <- model.StreamChunk{Usage: usage}

	turn, err := drainACPStreamTurn(context.Background(), model.ChatRequest{Model: "glm-5.3-flash"}, chunks, make(chan error), nil, false)
	if err != nil {
		t.Fatalf("drainACPStreamTurn: %v", err)
	}
	if turn.Usage == nil || *turn.Usage != *usage {
		t.Fatalf("usage = %+v, want %+v", turn.Usage, usage)
	}
}

func TestDrainACPStreamTurn_WaitsForDelayedRequiredUsageTrailer(t *testing.T) {
	finish := "stop"
	usage := &model.Usage{PromptTokens: 11, CompletionTokens: 4, TotalTokens: 15}
	chunks := make(chan model.StreamChunk, 2)
	chunks <- model.StreamChunk{Choices: []model.StreamChoice{{
		Delta:        model.MessageDelta{Content: "answer"},
		FinishReason: &finish,
	}}}
	errs := make(chan error)

	var once sync.Once
	callback := func(acp.SessionUpdate) error {
		once.Do(func() {
			go func() {
				time.Sleep(10 * time.Millisecond)
				chunks <- model.StreamChunk{Usage: usage}
				close(chunks)
				close(errs)
			}()
		})
		return nil
	}

	turn, err := drainACPStreamTurn(context.Background(), model.ChatRequest{
		Model:         "glm-5.3-flash",
		StreamOptions: &model.StreamOptions{IncludeUsage: true},
	}, chunks, errs, callback, false)
	if err != nil {
		t.Fatalf("drainACPStreamTurn: %v", err)
	}
	if turn.Usage == nil || *turn.Usage != *usage {
		t.Fatalf("usage = %+v, want delayed required usage %+v", turn.Usage, usage)
	}
}

func TestDrainACPStreamTurn_RequiredUsageTrailerGraceIsBounded(t *testing.T) {
	finish := "stop"
	chunks := make(chan model.StreamChunk, 1)
	chunks <- model.StreamChunk{Choices: []model.StreamChoice{{
		Delta:        model.MessageDelta{Content: "answer"},
		FinishReason: &finish,
	}}}
	errs := make(chan error)

	type result struct {
		turn acpStreamTurn
		err  error
	}
	resultCh := make(chan result, 1)
	go func() {
		turn, err := drainACPStreamTurn(context.Background(), model.ChatRequest{
			Model:         "glm-5.3-flash",
			StreamOptions: &model.StreamOptions{IncludeUsage: true},
		}, chunks, errs, nil, false)
		resultCh <- result{turn: turn, err: err}
	}()

	select {
	case got := <-resultCh:
		if got.err != nil {
			t.Fatalf("drainACPStreamTurn: %v", got.err)
		}
		if !acpStreamTurnHasMaterial(got.turn) || got.turn.Usage != nil {
			t.Fatalf("turn = %+v, want material with unavailable usage", got.turn)
		}
	case <-time.After(acpRequiredUsageTrailerGrace + time.Second):
		t.Fatalf("required usage grace exceeded bounded return")
	}
}

func TestDrainACPStreamTurn_AcceptsUsageTrailerErrorByStructuredCode(t *testing.T) {
	finish := "stop"
	chunks := make(chan model.StreamChunk, 1)
	chunks <- model.StreamChunk{Choices: []model.StreamChoice{{
		Delta:        model.MessageDelta{Content: "answer"},
		FinishReason: &finish,
	}}}
	errs := make(chan error, 1)
	trailerErr := &model.APIError{
		StatusCode: 200,
		Code:       acpUsageTrackingUnavailableCode,
		Message:    "opaque provider trailer failure",
	}
	callback := func(acp.SessionUpdate) error {
		errs <- trailerErr
		return nil
	}

	turn, err := drainACPStreamTurn(context.Background(), model.ChatRequest{Model: "glm-5.3-flash"}, chunks, errs, callback, false)
	if err != nil {
		t.Fatalf("drainACPStreamTurn: %v, want accepted completion", err)
	}
	if got := model.ExtractTextContentOrEmpty(turn.Message.Content); got != "answer" {
		t.Fatalf("content = %q, want answer", got)
	}
}

func TestDrainACPStreamTurn_CanceledContextWithReadyStopChunkFails(t *testing.T) {
	for i := 0; i < 200; i++ {
		finish := "stop"
		chunks := make(chan model.StreamChunk, 1)
		chunks <- model.StreamChunk{Choices: []model.StreamChoice{{
			Delta:        model.MessageDelta{Content: "partial"},
			FinishReason: &finish,
		}}}
		errs := make(chan error)

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		turn, err := drainACPStreamTurn(ctx, model.ChatRequest{Model: "glm-5.3-flash"}, chunks, errs, nil, false)
		if err == nil {
			t.Fatalf("iteration %d: drainACPStreamTurn = nil error, want context.Canceled", i)
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("iteration %d: err = %v, want context.Canceled", i, err)
		}
		// The outer select may legally pick cancellation before the already
		// buffered chunk, so only inspect the partial turn when material exists.
		if acpStreamTurnHasMaterial(turn) {
			var partial *partialStreamTurnError
			if !errors.As(err, &partial) {
				t.Fatalf("iteration %d: err = %v, want *partialStreamTurnError", i, err)
			}
			if got := model.ExtractTextContentOrEmpty(turn.Message.Content); got != "partial" {
				t.Fatalf("iteration %d: content = %q, want partial", i, got)
			}
		}
	}
}

func TestDrainACPStreamTurn_CancelDuringPostFinishDrainReturnsPartial(t *testing.T) {
	finish := "stop"
	chunks := make(chan model.StreamChunk, 2)
	chunks <- model.StreamChunk{Choices: []model.StreamChoice{{
		Delta:        model.MessageDelta{Content: "answer"},
		FinishReason: &finish,
	}}}
	chunks <- model.StreamChunk{Choices: []model.StreamChoice{{
		Delta: model.MessageDelta{Content: " tail"},
	}}}
	errs := make(chan error)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls atomic.Int32
	callback := func(acp.SessionUpdate) error {
		if calls.Add(1) == 2 {
			cancel()
		}
		return nil
	}

	turn, err := drainACPStreamTurn(ctx, model.ChatRequest{Model: "glm-5.3-flash"}, chunks, errs, callback, false)
	if err == nil {
		t.Fatalf("drainACPStreamTurn = nil error, want context.Canceled")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	var partial *partialStreamTurnError
	if !errors.As(err, &partial) {
		t.Fatalf("err = %v, want *partialStreamTurnError", err)
	}
	if got := model.ExtractTextContentOrEmpty(turn.Message.Content); got != "answer tail" {
		t.Fatalf("content = %q, want %q", got, "answer tail")
	}
}

func TestDrainACPStreamTurn_PostFinishDrainIsBounded(t *testing.T) {
	finish := "stop"
	chunks := make(chan model.StreamChunk, 1+acpPostFinishDrainLimit+200)
	chunks <- model.StreamChunk{Choices: []model.StreamChoice{{
		Delta:        model.MessageDelta{Content: "answer"},
		FinishReason: &finish,
	}}}
	for i := 0; i < acpPostFinishDrainLimit+200; i++ {
		chunks <- model.StreamChunk{Choices: []model.StreamChoice{{
			Delta: model.MessageDelta{Content: "x"},
		}}}
	}
	errs := make(chan error)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls atomic.Int32
	callback := func(acp.SessionUpdate) error {
		calls.Add(1)
		return nil
	}

	type acpDrainResult struct {
		turn  acpStreamTurn
		err   error
		calls int32
	}
	resCh := make(chan acpDrainResult, 1)
	go func() {
		turn, err := drainACPStreamTurn(ctx, model.ChatRequest{Model: "glm-5.3-flash"}, chunks, errs, callback, false)
		resCh <- acpDrainResult{turn: turn, err: err, calls: calls.Load()}
	}()

	select {
	case res := <-resCh:
		if res.err != nil {
			t.Fatalf("drainACPStreamTurn: %v", res.err)
		}
		if !acpStreamTurnHasMaterial(res.turn) {
			t.Fatalf("turn = %+v, want material", res.turn)
		}
		if res.calls > int32(1+acpPostFinishDrainLimit) {
			t.Fatalf("callbacks = %d, want <= %d", res.calls, 1+acpPostFinishDrainLimit)
		}
	case <-time.After(time.Second):
		t.Fatalf("drainACPStreamTurn did not return within 1s")
	}
}

func TestACPAcceptsUsageTrailerError_RequiresFinalStopFinish(t *testing.T) {
	req := model.ChatRequest{
		Model:    "test-model",
		Messages: []model.Message{{Role: "user", Content: "hello"}},
	}
	trailerErr := &model.APIError{
		Code:    acpUsageTrackingUnavailableCode,
		Message: "opaque trailer failure",
	}
	tests := []struct {
		name   string
		reason string
		want   bool
	}{
		{name: "stop", reason: "stop", want: true},
		{name: "stop padded and upper", reason: " STOP ", want: true},
		{name: "empty", reason: "", want: false},
		{name: "tool_calls", reason: "tool_calls", want: false},
		{name: "function_call", reason: "function_call", want: false},
		{name: "length", reason: "length", want: false},
		{name: "max_tokens", reason: "max_tokens", want: false},
		{name: "max_output_tokens", reason: "max_output_tokens", want: false},
		{name: "content_filter", reason: "content_filter", want: false},
		{name: "cancelled", reason: "cancelled", want: false},
		{name: "canceled", reason: "canceled", want: false},
		{name: "error", reason: "error", want: false},
		{name: "provider_error", reason: "provider_error", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			turn := acpStreamTurn{
				FinishReason: tt.reason,
				Message:      model.Message{Role: "assistant", Content: "answer text"},
			}
			if got := acpAcceptsUsageTrailerError(req, turn, trailerErr, true, 1); got != tt.want {
				t.Fatalf("acpAcceptsUsageTrailerError(reason=%q) = %v, want %v", tt.reason, got, tt.want)
			}
		})
	}
}

func TestACPAcceptsUsageTrailerError_RequiresOrderedExclusiveTrailer(t *testing.T) {
	req := model.ChatRequest{
		Model:    "test-model",
		Messages: []model.Message{{Role: "user", Content: "hello"}},
	}
	turn := acpStreamTurn{
		FinishReason: "stop",
		Message:      model.Message{Role: "assistant", Content: "answer text"},
	}
	usageErr := &model.APIError{
		Code:    acpUsageTrackingUnavailableCode,
		Message: "opaque trailer failure",
	}
	unrelatedErr := errors.New("unrelated provider failure")

	tests := []struct {
		name                string
		err                 error
		observedAfterFinish bool
		terminalErrorCount  int
		want                bool
	}{
		{
			name:                "single usage error after finish",
			err:                 usageErr,
			observedAfterFinish: true,
			terminalErrorCount:  1,
			want:                true,
		},
		{
			name:                "usage error before finish",
			err:                 usageErr,
			observedAfterFinish: false,
			terminalErrorCount:  1,
		},
		{
			name:                "multiple terminal errors",
			err:                 usageErr,
			observedAfterFinish: true,
			terminalErrorCount:  2,
		},
		{
			name:                "joined usage and unrelated leaves",
			err:                 errors.Join(usageErr, unrelatedErr),
			observedAfterFinish: true,
			terminalErrorCount:  1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := acpAcceptsUsageTrailerError(req, turn, tt.err, tt.observedAfterFinish, tt.terminalErrorCount); got != tt.want {
				t.Fatalf("acpAcceptsUsageTrailerError() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDrainACPStreamTurn_UsageErrorInStopChunkIsNotTrailer(t *testing.T) {
	finish := "stop"
	chunks := make(chan model.StreamChunk, 1)
	chunks <- model.StreamChunk{
		Error: &model.ErrorDetail{
			Code:    acpUsageTrackingUnavailableCode,
			Message: "usage trailer arrived in the stop chunk",
		},
		Choices: []model.StreamChoice{{
			Delta:        model.MessageDelta{Content: "answer"},
			FinishReason: &finish,
		}},
	}
	close(chunks)
	errs := make(chan error)
	close(errs)

	turn, err := drainACPStreamTurn(context.Background(), model.ChatRequest{
		Model:    "glm-5.3-flash",
		Messages: []model.Message{{Role: "user", Content: "answer"}},
	}, chunks, errs, nil, false)
	if err == nil {
		t.Fatalf("drainACPStreamTurn = nil error, want same-chunk usage error")
	}
	var partial *partialStreamTurnError
	if !errors.As(err, &partial) {
		t.Fatalf("err = %v, want *partialStreamTurnError", err)
	}
	if got := model.ExtractTextContentOrEmpty(turn.Message.Content); got != "answer" {
		t.Fatalf("content = %q, want answer", got)
	}
}

func TestDrainACPStreamTurn_PostFinishDrainPrioritizesReadyErrorAtLimit(t *testing.T) {
	for i := 0; i < 64; i++ {
		finish := "stop"
		chunks := make(chan model.StreamChunk, 1+acpPostFinishDrainLimit)
		chunks <- model.StreamChunk{Choices: []model.StreamChoice{{
			Delta:        model.MessageDelta{Content: "answer"},
			FinishReason: &finish,
		}}}
		for j := 0; j < acpPostFinishDrainLimit; j++ {
			chunks <- model.StreamChunk{Choices: []model.StreamChoice{{
				Delta: model.MessageDelta{Content: "x"},
			}}}
		}

		providerErr := errors.New("provider terminal failure")
		errs := make(chan error, 1)
		calls := 0
		callback := func(acp.SessionUpdate) error {
			calls++
			if calls == acpPostFinishDrainLimit {
				errs <- providerErr
			}
			return nil
		}

		turn, err := drainACPStreamTurn(context.Background(), model.ChatRequest{
			Model:    "glm-5.3-flash",
			Messages: []model.Message{{Role: "user", Content: "answer"}},
		}, chunks, errs, callback, false)
		if !errors.Is(err, providerErr) {
			t.Fatalf("iteration %d: err = %v, want provider terminal failure", i, err)
		}
		if !acpStreamTurnHasMaterial(turn) {
			t.Fatalf("iteration %d: turn = %+v, want material", i, turn)
		}
	}
}

func TestStreamACPTurnWithDelivery_ConclusiveUsageTrailerErrorIsAcceptedOnce(t *testing.T) {
	mgr, requests, _ := newACPHTTPStreamManager(t, func(w http.ResponseWriter, r *http.Request, ordinal int32) {
		if ordinal != 1 {
			t.Fatalf("unexpected retry attempt %d", ordinal)
		}
		acpSSEChunk(t, w, "answer", "", "stop")
		_, _ = io.WriteString(w, "data: {\"error\":{\"message\":\"trailer unavailable\",\"code\":\"usage_tracking_unavailable\"}}\n\n")
	})
	collector := &collectingStream{}
	turn, err := streamACPTurnWithDelivery(context.Background(), mgr, model.ChatRequest{
		Model:    "gpt-4o",
		Messages: []model.Message{{Role: "user", Content: "answer"}},
	}, collector.fn, false)
	if err != nil {
		t.Fatalf("streamACPTurnWithDelivery: %v, want accepted completion", err)
	}
	if got := atomic.LoadInt32(requests); got != 1 {
		t.Fatalf("provider requests = %d, want exactly one", got)
	}
	if got := strings.Join(collector.messageChunks(), ""); got != "answer" {
		t.Fatalf("delivered content = %q, want answer exactly once", got)
	}
	if turn.Usage != nil {
		t.Fatalf("usage = %+v, want unavailable usage", turn.Usage)
	}
	response := buildACPStreamChatResponse(model.ChatRequest{
		Model:    "gpt-4o",
		Messages: []model.Message{{Role: "user", Content: "answer"}},
	}, turn)
	if response.UsagePresent || !response.Usage.Estimated || response.Usage.TotalTokens <= 0 {
		t.Fatalf("response usage = %+v (present=%t), want marked local estimate", response.Usage, response.UsagePresent)
	}
}

func TestIsUsageTrackingUnavailableError_PrefersStructuredCode(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "structured code",
			err:  &model.APIError{Code: acpUsageTrackingUnavailableCode, Message: "unrelated wording"},
			want: true,
		},
		{
			name: "different structured code wins over text",
			err:  &model.APIError{Code: "provider_failure", Message: "usage tracking unavailable"},
			want: false,
		},
		{
			name: "narrow unstructured compatibility fallback",
			err:  &model.APIError{Message: "usage tracking unavailable"},
			want: true,
		},
		{
			name: "plain error is not classified",
			err:  errors.New("usage tracking unavailable"),
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isUsageTrackingUnavailableError(tt.err); got != tt.want {
				t.Fatalf("isUsageTrackingUnavailableError() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestStreamACPTurnWithDelivery_SafeRetryBuffersFirstAttempt(t *testing.T) {
	mgr, requests, capture := newACPHTTPStreamManager(t, func(w http.ResponseWriter, r *http.Request, ordinal int32) {
		switch ordinal {
		case 1:
			acpSSEChunk(t, w, "first attempt", "", "")
			return
		case 2:
			acpSSEChunk(t, w, "retry answer", "", "stop")
			writeACPDone(w)
		}
	})

	collector := &collectingStream{}
	turn, err := streamACPTurnWithDelivery(context.Background(), mgr, model.ChatRequest{
		Model:     "gpt-4o",
		MaxTokens: 777,
		Messages:  []model.Message{{Role: "user", Content: "answer"}},
	}, collector.fn, false)
	if err != nil {
		t.Fatalf("streamACPTurnWithDelivery: %v", err)
	}
	if got := model.ExtractTextContentOrEmpty(turn.Message.Content); got != "retry answer" {
		t.Fatalf("content = %q, want retry answer", got)
	}
	if got := atomic.LoadInt32(requests); got != 2 {
		t.Fatalf("provider requests = %d, want exactly one retry", got)
	}
	if got := collector.messageChunks(); len(got) != 1 || got[0] != "retry answer" {
		t.Fatalf("delivered message chunks = %#v, want retry answer only", got)
	}
	if len(turn.Attempts) == 0 || model.ExtractTextContentOrEmpty(turn.Attempts[0].turn.Message.Content) != "first attempt" {
		t.Fatalf("first-attempt evidence = %#v, want preserved first attempt", turn.Attempts)
	}
	for i, request := range capture.snapshot() {
		if request.MaxTokens != 777 {
			t.Fatalf("request %d max_tokens = %d, want 777", i+1, request.MaxTokens)
		}
	}
}

func TestStreamACPTurnWithDelivery_SafeRetryAggregatesUsageAndEvidence(t *testing.T) {
	firstUsage := model.Usage{
		PromptTokens:     11,
		CompletionTokens: 3,
		TotalTokens:      14,
		PromptTokensDetails: &model.PromptTokensDetails{
			CachedTokens: 5,
		},
		CacheWriteTokens: 7,
	}
	secondUsage := model.Usage{
		PromptTokens:     13,
		CompletionTokens: 4,
		TotalTokens:      17,
		CompletionTokenDetails: &model.CompletionTokenDetails{
			ReasoningTokens: 2,
		},
	}
	wantAggregate := model.AddUsage(firstUsage, secondUsage)

	mgr, requests, _ := newACPHTTPStreamManager(t, func(w http.ResponseWriter, r *http.Request, ordinal int32) {
		switch ordinal {
		case 1:
			acpSSEChunk(t, w, "first attempt", "", "")
			writeACPUsageEvent(t, w, firstUsage)
			return
		case 2:
			writeACPUsageEvent(t, w, secondUsage)
			acpSSEChunk(t, w, "retry answer", "", "stop")
			writeACPDone(w)
		default:
			t.Fatalf("unexpected retry attempt %d", ordinal)
		}
	})

	turn, err := streamACPTurnWithDelivery(context.Background(), mgr, model.ChatRequest{
		Model:    "gpt-4o",
		Messages: []model.Message{{Role: "user", Content: "answer"}},
	}, nil, false)
	if err != nil {
		t.Fatalf("streamACPTurnWithDelivery: %v", err)
	}
	if got := atomic.LoadInt32(requests); got != 2 {
		t.Fatalf("provider requests = %d, want exactly two", got)
	}
	if got := model.ExtractTextContentOrEmpty(turn.Message.Content); got != "retry answer" {
		t.Fatalf("content = %q, want retry answer", got)
	}
	if turn.Usage == nil || !reflect.DeepEqual(*turn.Usage, wantAggregate) {
		t.Fatalf("aggregate usage = %+v, want %+v", turn.Usage, wantAggregate)
	}
	if len(turn.Attempts) != 2 {
		t.Fatalf("attempt evidence count = %d, want 2", len(turn.Attempts))
	}
	if !errors.Is(turn.Attempts[0].err, io.ErrUnexpectedEOF) {
		t.Fatalf("first attempt error = %v, want io.ErrUnexpectedEOF", turn.Attempts[0].err)
	}
	if turn.Attempts[1].err != nil {
		t.Fatalf("second attempt error = %v, want nil", turn.Attempts[1].err)
	}
	if turn.Attempts[0].turn.Usage == nil || !reflect.DeepEqual(*turn.Attempts[0].turn.Usage, firstUsage) {
		t.Fatalf("first attempt usage = %+v, want %+v", turn.Attempts[0].turn.Usage, firstUsage)
	}
	if turn.Attempts[1].turn.Usage == nil || !reflect.DeepEqual(*turn.Attempts[1].turn.Usage, secondUsage) {
		t.Fatalf("second attempt usage = %+v, want %+v", turn.Attempts[1].turn.Usage, secondUsage)
	}

	response := buildACPStreamChatResponse(model.ChatRequest{Model: "gpt-4o"}, turn)
	if !response.UsagePresent || !reflect.DeepEqual(response.Usage, wantAggregate) {
		t.Fatalf("response usage = %+v (present=%t), want %+v", response.Usage, response.UsagePresent, wantAggregate)
	}
	if len(response.AttemptEvidence) != 2 {
		t.Fatalf("public attempt evidence count = %d, want 2", len(response.AttemptEvidence))
	}
	if !reflect.DeepEqual(response.AttemptEvidence[0].Usage, firstUsage) {
		t.Fatalf("first public attempt usage = %+v, want %+v", response.AttemptEvidence[0].Usage, firstUsage)
	}
	if !reflect.DeepEqual(response.AttemptEvidence[1].Usage, secondUsage) {
		t.Fatalf("second public attempt usage = %+v, want %+v", response.AttemptEvidence[1].Usage, secondUsage)
	}
	if !response.AttemptEvidence[0].UsagePresent || !response.AttemptEvidence[0].Incomplete {
		t.Fatalf("first public attempt evidence = %+v, want present and incomplete", response.AttemptEvidence[0])
	}
	if !response.AttemptEvidence[1].UsagePresent || response.AttemptEvidence[1].Incomplete {
		t.Fatalf("second public attempt evidence = %+v, want present and complete", response.AttemptEvidence[1])
	}
	if response.AttemptEvidence[0].FinishReason != "" || response.AttemptEvidence[1].FinishReason != "stop" {
		t.Fatalf("finish reasons = %q, %q; want empty, stop", response.AttemptEvidence[0].FinishReason, response.AttemptEvidence[1].FinishReason)
	}
	if response.ExecutionIdentity == nil {
		t.Fatal("response execution identity missing")
	}
	if response.ExecutionIdentity.ProviderID != "openai" || response.ExecutionIdentity.ResponseID != "chatcmpl-1" || response.ExecutionIdentity.ResponseModel != "gpt-4o" {
		t.Fatalf("response execution identity = %+v", response.ExecutionIdentity)
	}
	for i, attempt := range response.AttemptEvidence {
		if attempt.ExecutionIdentity == nil {
			t.Fatalf("attempt %d execution identity missing", i)
		}
		if attempt.ExecutionIdentity.ProviderID != "openai" || attempt.ExecutionIdentity.ResponseID != "chatcmpl-1" || attempt.ExecutionIdentity.ResponseModel != "gpt-4o" {
			t.Fatalf("attempt %d execution identity = %+v", i, attempt.ExecutionIdentity)
		}
	}

	wire, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	if strings.Contains(string(wire), "first attempt") {
		t.Fatalf("response leaked first-attempt content: %s", wire)
	}
	if !strings.Contains(string(wire), "retry answer") {
		t.Fatalf("response omitted final content: %s", wire)
	}
}

func TestStreamACPTurnWithDelivery_SafeRetryFailureCombinesDiagnostics(t *testing.T) {
	mgr, requests, capture := newACPHTTPStreamManager(t, func(w http.ResponseWriter, r *http.Request, ordinal int32) {
		content := "first attempt"
		message := "first trailer"
		if ordinal == 2 {
			content = "second attempt"
			message = "second trailer"
		}
		acpSSEChunk(t, w, content, "", "")
		_, _ = io.WriteString(w, "data: {\"error\":{\"message\":\""+message+"\",\"code\":\"usage_tracking_unavailable\"}}\n\n")
	})

	turn, err := streamACPTurnWithDelivery(context.Background(), mgr, model.ChatRequest{
		Model:     "gpt-4o",
		MaxTokens: 888,
		Messages:  []model.Message{{Role: "user", Content: "answer"}},
	}, nil, false)
	if err == nil {
		t.Fatal("streamACPTurnWithDelivery succeeded after both attempts failed")
	}
	if got := atomic.LoadInt32(requests); got != 2 {
		t.Fatalf("provider requests = %d, want exactly two total attempts", got)
	}
	var partial *partialStreamTurnError
	if !errors.As(err, &partial) {
		t.Fatalf("error = %v, want partialStreamTurnError", err)
	}
	if !strings.Contains(err.Error(), "first trailer") || !strings.Contains(err.Error(), "second trailer") {
		t.Fatalf("combined error = %v, want both attempt diagnostics", err)
	}
	if len(partial.attempts) != 2 {
		t.Fatalf("attempt evidence count = %d, want 2", len(partial.attempts))
	}
	if got := model.ExtractTextContentOrEmpty(turn.Message.Content); got != "second attempt" {
		t.Fatalf("returned turn content = %q, want second attempt evidence", got)
	}
	if got := model.ExtractTextContentOrEmpty(partial.attempts[0].turn.Message.Content); got != "first attempt" {
		t.Fatalf("first evidence content = %q, want first attempt", got)
	}
	if got := model.ExtractTextContentOrEmpty(partial.attempts[1].turn.Message.Content); got != "second attempt" {
		t.Fatalf("second evidence content = %q, want second attempt", got)
	}
	if got := len(capture.snapshot()); got != 2 {
		t.Fatalf("captured requests = %d, want 2", got)
	}
}

func TestStreamACPTurnWithDelivery_DoesNotRetryToolRisk(t *testing.T) {
	mgr, requests, _ := newACPHTTPStreamManager(t, func(w http.ResponseWriter, r *http.Request, ordinal int32) {
		if ordinal == 1 {
			acpSSEChunk(t, w, "toolful partial", "", "")
			_, _ = io.WriteString(w, "data: {broken-json}\n\n")
			return
		}
		acpSSEChunk(t, w, "unsafe replay", "", "stop")
		writeACPDone(w)
	})
	collector := &collectingStream{}
	_, err := streamACPTurnWithDelivery(context.Background(), mgr, model.ChatRequest{
		Model:    "gpt-4o",
		Messages: []model.Message{{Role: "user", Content: "answer"}},
		Tools:    []map[string]any{{"type": "function", "function": map[string]any{"name": "edit_file"}}},
	}, collector.fn, false)
	if err == nil {
		t.Fatal("tool-enabled stream unexpectedly retried/succeeded")
	}
	if got := atomic.LoadInt32(requests); got != 1 {
		t.Fatalf("provider requests = %d, want no retry", got)
	}
	if got := strings.Join(collector.messageChunks(), ""); got != "toolful partial" {
		t.Fatalf("delivered toolful partial = %q, want original partial", got)
	}
}

func TestStreamACPTurnWithDelivery_DoesNotRetryObservedToolDelta(t *testing.T) {
	mgr, requests, _ := newACPHTTPStreamManager(t, func(w http.ResponseWriter, r *http.Request, ordinal int32) {
		if ordinal != 1 {
			acpSSEChunk(t, w, "unsafe replay", "", "stop")
			writeACPDone(w)
			return
		}
		acpSSEJSON(t, w, map[string]any{
			"id": "chatcmpl-tool",
			"choices": []any{map[string]any{
				"index": 0,
				"delta": map[string]any{
					"tool_calls": []any{map[string]any{
						"index": 0,
						"id":    "call-1",
						"type":  "function",
						"function": map[string]any{
							"name":      "read_file",
							"arguments": `{"path":"x"}`,
						},
					}},
				},
				"finish_reason": nil,
			}},
		})
		_, _ = io.WriteString(w, "data: {broken-json}\n\n")
	})
	turn, err := streamACPTurnWithDelivery(context.Background(), mgr, model.ChatRequest{
		Model:    "gpt-4o",
		Messages: []model.Message{{Role: "user", Content: "read"}},
	}, nil, false)
	if err == nil {
		t.Fatal("observed tool delta unexpectedly retried/succeeded")
	}
	if got := atomic.LoadInt32(requests); got != 1 {
		t.Fatalf("provider requests = %d, want no retry", got)
	}
	if len(turn.Message.ToolCalls) != 1 {
		t.Fatalf("tool calls = %#v, want preserved observed tool call", turn.Message.ToolCalls)
	}
}

func TestDrainACPStreamTurn_UnrelatedTerminalErrorRemainsAnError(t *testing.T) {
	finish := "stop"
	providerErr := errors.New("unrelated trailer failure")
	chunks := make(chan model.StreamChunk, 1)
	chunks <- model.StreamChunk{Choices: []model.StreamChoice{{
		Delta:        model.MessageDelta{Content: "answer"},
		FinishReason: &finish,
	}}}
	errs := make(chan error, 1)
	errs <- providerErr

	turn, err := drainACPStreamTurn(context.Background(), model.ChatRequest{Model: "glm-5.3-flash"}, chunks, errs, nil, false)
	if !errors.Is(err, providerErr) {
		t.Fatalf("error = %v, want provider error", err)
	}
	var partial *partialStreamTurnError
	if !errors.As(err, &partial) {
		t.Fatalf("error = %v, want partialStreamTurnError", err)
	}
	if got := model.ExtractTextContentOrEmpty(turn.Message.Content); got != "answer" {
		t.Fatalf("content = %q, want preserved answer", got)
	}
}

func TestACPStreamRetryCandidate_RejectsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if acpStreamRetryCandidate(ctx, acpStreamTurn{}, context.Canceled) {
		t.Fatal("cancellation was classified as a retry candidate")
	}
}

func TestACPStreamRetryCandidate_RechecksCancellationAfterClassification(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	err := &acpCancelOnUnwrapError{cancel: cancel}
	if acpStreamRetryCandidate(ctx, acpStreamTurn{}, err) {
		t.Fatal("candidate retried after classification canceled its context")
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("context error = %v, want context.Canceled", ctx.Err())
	}
}

func TestACPStreamRetryCandidate_PartialRequiresSafeCause(t *testing.T) {
	usageErr := &model.APIError{
		Code:    acpUsageTrackingUnavailableCode,
		Message: "usage tracking unavailable",
	}
	tests := []struct {
		name  string
		cause error
		want  bool
	}{
		{name: "partial provider failure api error", cause: &model.APIError{Message: "upstream provider failure"}, want: false},
		{name: "partial arbitrary plain error", cause: errors.New("connection reset by peer"), want: false},
		{name: "partial usage tracking unavailable", cause: usageErr, want: true},
		{name: "partial unexpected eof", cause: io.ErrUnexpectedEOF, want: true},
		{name: "wrapped unexpected eof", cause: fmt.Errorf("stream ended: %w", io.ErrUnexpectedEOF), want: true},
		{name: "joined eof and usage error", cause: errors.Join(io.ErrUnexpectedEOF, usageErr), want: true},
		{name: "joined eof and unrelated error", cause: errors.Join(io.ErrUnexpectedEOF, errors.New("provider failed")), want: false},
		{name: "joined usage and unrelated error", cause: errors.Join(usageErr, errors.New("provider failed")), want: false},
	}
	for _, tt := range tests {
		for _, wrapped := range []bool{false, true} {
			name := "bare"
			if wrapped {
				name = "partial"
			}
			t.Run(tt.name+"/"+name, func(t *testing.T) {
				err := tt.cause
				if wrapped {
					err = &partialStreamTurnError{cause: err}
				}
				if got := acpStreamRetryCandidate(context.Background(), acpStreamTurn{}, err); got != tt.want {
					t.Fatalf("acpStreamRetryCandidate(cause=%v) = %v, want %v", tt.cause, got, tt.want)
				}
			})
		}
	}
}

type acpNonComparableLeafError struct {
	payloads []string
}

func (e *acpNonComparableLeafError) Error() string { return "non-comparable leaf failure" }

func TestACPStreamRetryCandidate_NonComparableLeafIsNotRetryable(t *testing.T) {
	nonComparable := &acpNonComparableLeafError{payloads: []string{"chunk"}}
	tests := []struct {
		name  string
		cause error
	}{
		{name: "bare non-comparable leaf", cause: nonComparable},
		{name: "joined eof and non-comparable leaf", cause: errors.Join(io.ErrUnexpectedEOF, nonComparable)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if acpStreamRetryCandidate(context.Background(), acpStreamTurn{}, tt.cause) {
				t.Fatalf("acpStreamRetryCandidate(cause=%v) = true, want false", tt.cause)
			}
		})
	}
}

func TestDrainACPStreamTurn_ObservedToolDeltaInAnyChoiceForbidsRetry(t *testing.T) {
	chunks := make(chan model.StreamChunk, 1)
	chunks <- model.StreamChunk{Choices: []model.StreamChoice{
		{Index: 0, Delta: model.MessageDelta{Content: "partial answer"}},
		{Index: 1, Delta: model.MessageDelta{ToolCalls: []model.ToolCallDelta{{
			Index: 1,
			ID:    "call-choice-1",
			Type:  "function",
			Function: &model.FunctionCallDelta{
				Name:      "read_file",
				Arguments: `{}`,
			},
		}}}},
	}}
	close(chunks)
	errs := make(chan error, 1)
	errs <- io.ErrUnexpectedEOF
	close(errs)

	turn, err := drainACPStreamTurn(context.Background(), model.ChatRequest{Model: "glm-5.3-flash"}, chunks, errs, nil, false)
	if err == nil {
		t.Fatal("drain unexpectedly succeeded")
	}
	if !turn.ObservedToolDelta {
		t.Fatal("tool delta from non-first choice was not retained")
	}
	if len(turn.Message.ToolCalls) != 0 {
		t.Fatalf("first-choice accumulated tool calls = %+v, want none", turn.Message.ToolCalls)
	}
	if acpStreamRetryCandidate(context.Background(), turn, err) {
		t.Fatal("non-first-choice tool delta was classified as safe to replay")
	}
}

func TestACPStreamRetryAllowed_RejectsToolBearingRequests(t *testing.T) {
	tests := []model.ChatRequest{
		{Tools: []map[string]any{{"type": "function"}}},
		{ToolChoice: "required"},
		{Messages: []model.Message{{Role: "tool", Content: "result"}}},
		{Messages: []model.Message{{Role: "assistant", ToolCalls: []model.ToolCall{{ID: "call-1"}}}}},
	}
	for i, req := range tests {
		if acpStreamRetryAllowed(req) {
			t.Fatalf("request %d was retryable despite tool risk: %+v", i, req)
		}
	}
	if !acpStreamRetryAllowed(model.ChatRequest{ToolChoice: "none"}) {
		t.Fatal("tool_choice=none should remain a no-tools request")
	}
}

func TestACPStreamRetryDiagnosticsDoNotIncludePromptText(t *testing.T) {
	partial := &partialStreamTurnError{
		cause: errors.New("first provider failure"),
		attempts: []acpStreamAttemptEvidence{
			{turn: acpStreamTurn{Message: model.Message{Content: "secret prompt-shaped output"}}, err: errors.New("first provider failure")},
			{turn: acpStreamTurn{Message: model.Message{Content: "second output"}}, err: errors.New("second provider failure")},
		},
	}
	message := partial.Error()
	if !strings.Contains(message, "attempt 1") || !strings.Contains(message, "attempt 2") {
		t.Fatalf("diagnostics = %q, want both attempts", message)
	}
	if strings.Contains(message, "secret prompt-shaped output") {
		t.Fatalf("diagnostics leaked stream content: %q", message)
	}
}

func TestACPHandlerPromptErrorWithNilStreamDoesNotPanic(t *testing.T) {
	t.Parallel()

	cfg, mgr := newACPPartialConsumerManager(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"error\":{\"message\":\"provider failed before any draft\",\"type\":\"server_error\",\"code\":\"provider_failed\"}}\n\n")
	})

	handler, cleanup := makePromptHandler(cfg, mgr, nil, nil, t.TempDir(), func(string, ...interface{}) {}, nil, nil)
	t.Cleanup(cleanup)

	var handlerErr error
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("prompt handler panicked with nil stream: %v", r)
			}
		}()
		_, handlerErr = handler(context.Background(), &acp.AgentSession{ID: "acp-nil-stream-error", Mode: acpModePrefix + "acp-test/no-tools-model"}, []acp.ContentBlock{acp.NewTextContent("produce a partial answer")}, nil)
	}()

	if handlerErr == nil {
		t.Fatalf("handler error = nil, want projected provider error")
	}
	var projected *acpProjectedError
	if !errors.As(handlerErr, &projected) {
		t.Fatalf("handler error = %v (%T), want *acpProjectedError", handlerErr, handlerErr)
	}
	if strings.TrimSpace(projected.Error()) == "" {
		t.Fatalf("projected error message is empty, want preserved provider error projection")
	}
}
