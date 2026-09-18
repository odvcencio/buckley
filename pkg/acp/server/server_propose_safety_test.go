package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"m31labs.dev/buckley/pkg/acp/partialresult"
	acppb "m31labs.dev/buckley/pkg/acp/proto"
	"m31labs.dev/buckley/pkg/model"
)

func TestACPProposeEditsResponseErrorReturnsUnacceptedPartialDetail(t *testing.T) {
	const secret = "propose-model-secret"
	provider := &partialServerProvider{
		resp: &model.ChatResponse{
			ID:    "chatcmpl-propose-partial",
			Model: "provider-propose-model",
			Choices: []model.Choice{{
				Message:      model.Message{Role: "assistant", Content: "public proposed edit"},
				FinishReason: "stop",
			}},
			Usage:        model.Usage{PromptTokens: 4, CompletionTokens: 2, TotalTokens: 6},
			UsagePresent: true,
		},
		err: fmt.Errorf("raw model failure: %s", secret),
	}
	srv := newProposeSafetyServer(t, t.TempDir(), provider)

	resp, err := srv.ProposeEdits(context.Background(), proposeSafetyRequest("file:///tmp/propose.go", "old", false, 1))
	if resp != nil {
		t.Fatalf("response = %+v, want no accepted ProposeEdits response", resp)
	}
	detail := requirePartialDetail(t, err, codes.Internal)
	if got := detail.GetPartialResponse().GetContent(); got != "public proposed edit" {
		t.Fatalf("partial draft = %q, want public model draft", got)
	}
	if strings.Contains(status.Convert(err).Message()+detail.String(), secret) {
		t.Fatalf("partial status leaked model secret: %s", detail.String())
	}
	if got := detail.GetTaskResults()[0].GetUsage(); got.GetInputTokens() != 4 || got.GetOutputTokens() != 2 || !got.GetUsageEvidencePresent() {
		t.Fatalf("usage = %+v, want model response usage evidence", got)
	}
}

func TestACPProposeEditsConclusiveStopReturnsAcceptedDraftWithoutAutoApply(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target.txt")
	if err := os.WriteFile(target, []byte("original"), 0o644); err != nil {
		t.Fatalf("write target: %v", err)
	}
	provider := &partialServerProvider{resp: &model.ChatResponse{Choices: []model.Choice{{
		Message:      model.Message{Role: "assistant", Content: "accepted proposed edit"},
		FinishReason: "stop",
	}}}}
	srv := newProposeSafetyServer(t, root, provider)

	resp, err := srv.ProposeEdits(context.Background(), proposeSafetyRequest(target, "original", false, 1))
	if err != nil {
		t.Fatalf("ProposeEdits: %v", err)
	}
	if resp == nil || len(resp.GetEdits()) != 1 || len(resp.GetEdits()[0].GetEdits()) != 1 {
		t.Fatalf("response edits = %+v, want one accepted proposed edit", resp)
	}
	if got := resp.GetEdits()[0].GetEdits()[0].GetNewText(); got != "accepted proposed edit" {
		t.Fatalf("proposed draft = %q, want expected model draft", got)
	}
	data, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatalf("read target: %v", readErr)
	}
	if string(data) != "original" {
		t.Fatalf("target content = %q, want unchanged when Apply=false", string(data))
	}
}

func TestACPProposeEditsNonConclusiveApplyDoesNotWrite(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target.txt")
	if err := os.WriteFile(target, []byte("original"), 0o644); err != nil {
		t.Fatalf("write target: %v", err)
	}
	provider := &partialServerProvider{resp: &model.ChatResponse{Choices: []model.Choice{{
		Message:      model.Message{Role: "assistant", Content: "partial edit"},
		FinishReason: "length",
	}}}}
	srv := newProposeSafetyServer(t, root, provider)

	resp, err := srv.ProposeEdits(context.Background(), proposeSafetyRequest(target, "original", true, 1))
	if resp != nil {
		t.Fatalf("response = %+v, want no accepted response", resp)
	}
	detail := requirePartialDetail(t, err, codes.Internal)
	if detail.GetPartialResponse().GetContent() != "partial edit" {
		t.Fatalf("partial draft = %q", detail.GetPartialResponse().GetContent())
	}
	data, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatalf("read target: %v", readErr)
	}
	if string(data) != "original" {
		t.Fatalf("target content = %q, want unchanged because nonconclusive draft was never applied", string(data))
	}
}

func TestACPProposeEditsLaterSuggestionFailurePreservesFirstUnacceptedCandidate(t *testing.T) {
	const secret = "second-suggestion-secret"
	var calls atomic.Int32
	provider := &partialServerProvider{
		chat: func(context.Context, model.ChatRequest) (*model.ChatResponse, error) {
			if calls.Add(1) == 1 {
				return &model.ChatResponse{Choices: []model.Choice{{
					Message:      model.Message{Role: "assistant", Content: "first candidate"},
					FinishReason: "stop",
				}}}, nil
			}
			return &model.ChatResponse{Choices: []model.Choice{{
				Message:      model.Message{Role: "assistant", Content: "second secret " + secret},
				FinishReason: "stop",
			}}}, fmt.Errorf("second failed: %s", secret)
		},
	}
	srv := newProposeSafetyServer(t, t.TempDir(), provider)

	resp, err := srv.ProposeEdits(context.Background(), proposeSafetyRequest("file:///tmp/propose.go", "old", false, 2))
	if resp != nil {
		t.Fatalf("response = %+v, want no accepted partial suggestions", resp)
	}
	detail := requirePartialDetail(t, err, codes.Internal)
	if got := detail.GetPartialResponse().GetContent(); got != "first candidate" {
		t.Fatalf("partial draft = %q, want first candidate only", got)
	}
	if strings.Contains(detail.String()+status.Convert(err).Message(), secret) {
		t.Fatalf("later failed suggestion leaked secret: %s", detail.String())
	}
	if calls.Load() != 2 {
		t.Fatalf("model calls = %d, want two suggestion attempts", calls.Load())
	}
}

func TestACPProposeEditsApplyFailureIsSafeAndUnaccepted(t *testing.T) {
	const secretPath = "../secret-outside.txt"
	provider := &partialServerProvider{resp: &model.ChatResponse{Choices: []model.Choice{{
		Message:      model.Message{Role: "assistant", Content: "draft edit"},
		FinishReason: "stop",
	}}}}
	srv := newProposeSafetyServer(t, t.TempDir(), provider)

	resp, err := srv.ProposeEdits(context.Background(), proposeSafetyRequest(secretPath, "old", true, 1))
	if resp != nil {
		t.Fatalf("response = %+v, want no applied/accepted response", resp)
	}
	detail := requirePartialDetail(t, err, codes.InvalidArgument)
	if got := status.Convert(err).Message(); got != "edit application failed" {
		t.Fatalf("status message = %q, want generic apply failure", got)
	}
	if detail.GetSafeError() != "edit application failed" || detail.GetReasonCode() != "apply_failed" {
		t.Fatalf("partial detail safe_error=%q reason=%q, want apply failure detail", detail.GetSafeError(), detail.GetReasonCode())
	}
	if detail.GetPartialResponse().GetContent() != "draft edit" {
		t.Fatalf("partial draft = %q", detail.GetPartialResponse().GetContent())
	}
	if strings.Contains(status.Convert(err).Message()+detail.String(), secretPath) {
		t.Fatalf("apply failure leaked path in status/detail: %s", detail.String())
	}
}

func TestACPProposeEditsPartialDetailUsesSharedBounds(t *testing.T) {
	large := strings.Repeat("x", partialresult.MaxDraftBytes+512)
	detail := acpPartialResultFromProposedEdit(&acppb.ProposedEdit{Edits: []*acppb.TextEdit{{NewText: large}}}, nil)
	if detail == nil {
		t.Fatal("detail nil")
	}
	if len(detail.GetPartialResponse().GetContent()) > partialresult.MaxDraftBytes {
		t.Fatalf("partial draft len=%d, want <= %d", len(detail.GetPartialResponse().GetContent()), partialresult.MaxDraftBytes)
	}
	if detail.GetOriginalDraftBytes() != int32(len(large)) || !detail.GetTruncated() {
		t.Fatalf("original=%d truncated=%t, want source length and truncation", detail.GetOriginalDraftBytes(), detail.GetTruncated())
	}
}

func newProposeSafetyServer(t *testing.T, root string, provider *partialServerProvider) *Server {
	t.Helper()
	cfg := configForSafetyServer(root)
	return newPartialServer(t, partialServerOptions{models: newPartialModelManager(t, provider), cfg: cfg, store: nil})
}

func proposeSafetyRequest(uri, content string, apply bool, maxSuggestions int32) *acppb.ProposeEditsRequest {
	return &acppb.ProposeEditsRequest{
		AgentId:        "zed",
		SessionId:      "propose-safety",
		Instruction:    "rewrite",
		MaxSuggestions: maxSuggestions,
		Apply:          apply,
		Context: &acppb.EditorContext{Document: &acppb.DocumentSnapshot{
			Uri:        uri,
			LanguageId: "text",
			Content:    content,
		}},
	}
}
