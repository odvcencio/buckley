package partialresult

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
	acppb "m31labs.dev/buckley/pkg/acp/proto"
)

func TestStatusErrorExtractsBoundedPartialResult(t *testing.T) {
	longDraft := strings.Repeat("界", MaxDraftBytes)
	err := StatusError(codes.Internal, "incomplete response", &acppb.PartialResult{
		ReasonCode: "provider_error",
		SafeError:  "raw secret should be replaced",
		PartialResponse: &acppb.Message{
			Role:    "assistant",
			Content: longDraft,
		},
		TaskResults: []*acppb.PartialTaskResult{{
			TaskId:       "task-1",
			Status:       "incomplete",
			Summary:      strings.Repeat("summary ", 200),
			Error:        "safe task error",
			FinishReason: "length",
			Usage: &acppb.PartialUsage{
				InputTokens:               100,
				OutputTokens:              50,
				ReportedTotalTokens:       150,
				UsageEvidencePresent:      true,
				UsageEvidenceMissing:      true,
				ReportedReasoningTokens:   ptrInt64(20),
				ReportedCachedInputTokens: ptrInt64(30),
			},
			ToolCallCount: 2,
			CommandCount:  1,
		}},
		ModelIdentities: []*acppb.PartialModelIdentity{{
			RequestedModel:              "alias",
			SelectedModel:               "provider/model",
			ProviderId:                  "openai",
			ResponseModel:               "gpt-4o",
			ResponseId:                  "chatcmpl-1",
			ExecutionIdentityConflicted: true,
		}},
	})
	if err == nil {
		t.Fatal("StatusError returned nil")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.Internal {
		t.Fatalf("status = %v, %t; want internal status", st.Code(), ok)
	}
	if size := proto.Size(st.Proto()); size > MaxStatusBytes {
		t.Fatalf("status proto size = %d, want <= %d", size, MaxStatusBytes)
	}
	got, ok := Extract(err)
	if !ok {
		t.Fatalf("Extract() ok=false")
	}
	if got.GetSchemaVersion() != SchemaVersion || !got.GetIncomplete() || got.GetStatus() != "incomplete" {
		t.Fatalf("partial result header = %+v", got)
	}
	if got.GetSafeError() != "incomplete response" {
		t.Fatalf("safe_error = %q", got.GetSafeError())
	}
	if len(got.GetPartialResponse().GetContent()) > MaxDraftBytes || !got.GetTruncated() {
		t.Fatalf("draft length/truncated = %d/%t", len(got.GetPartialResponse().GetContent()), got.GetTruncated())
	}
	if strings.Contains(protojsonString(t, got), "raw secret") {
		t.Fatalf("raw safe_error input leaked after replacement: %s", protojsonString(t, got))
	}
	got.TaskResults[0].Summary = "mutated"
	again, ok := Extract(err)
	if !ok || again.GetTaskResults()[0].GetSummary() == "mutated" {
		t.Fatalf("Extract result aliases status detail: %+v", again)
	}
}

func TestExtractRejectsUnknownOrInvalidPartialResultDetails(t *testing.T) {
	unknown := status.Error(codes.Internal, "plain")
	if got, ok := Extract(unknown); ok || got != nil {
		t.Fatalf("Extract plain = %+v, %t; want none", got, ok)
	}

	st := status.New(codes.Internal, "bad detail")
	withDetails, err := st.WithDetails(&acppb.PartialResult{SchemaVersion: "other", Incomplete: true})
	if err != nil {
		t.Fatalf("WithDetails: %v", err)
	}
	if got, ok := Extract(withDetails.Err()); ok || got != nil {
		t.Fatalf("Extract invalid schema = %+v, %t; want none", got, ok)
	}

	withDetails, err = st.WithDetails(&acppb.PartialResult{SchemaVersion: SchemaVersion, Incomplete: false})
	if err != nil {
		t.Fatalf("WithDetails incomplete=false: %v", err)
	}
	if got, ok := Extract(withDetails.Err()); ok || got != nil {
		t.Fatalf("Extract incomplete=false = %+v, %t; want none", got, ok)
	}

	oversize := &acppb.PartialResult{
		SchemaVersion: SchemaVersion,
		Incomplete:    true,
		Status:        "incomplete",
		SafeError:     "safe",
		PartialResponse: &acppb.Message{
			Role:    "assistant",
			Content: strings.Repeat("x", MaxStatusBytes),
		},
	}
	if err := Validate(oversize); err == nil {
		t.Fatalf("Validate oversize = nil, want rejection")
	}
}

func TestStatusErrorRejectsOKCode(t *testing.T) {
	err := StatusError(codes.OK, "should not be OK", &acppb.PartialResult{
		PartialResponse: &acppb.Message{Role: "assistant", Content: "draft"},
	})
	if err == nil {
		t.Fatal("StatusError(codes.OK) = nil, want non-OK error")
	}
	if st, ok := status.FromError(err); !ok || st.Code() == codes.OK {
		t.Fatalf("status = %v, %t; want non-OK", st.Code(), ok)
	}
}

func TestBuildFitsLargeProjectableSourceAndPreservesOriginalLength(t *testing.T) {
	source := &acppb.PartialResult{
		PartialResponse: &acppb.Message{
			Role:    "assistant",
			Content: strings.Repeat("draft-", 2000),
		},
		OriginalDraftBytes: 12345,
		TaskResults:        make([]*acppb.PartialTaskResult, MaxTaskResults+20),
		ModelIdentities:    make([]*acppb.PartialModelIdentity, MaxModelIdentities+20),
	}
	for i := range source.TaskResults {
		source.TaskResults[i] = &acppb.PartialTaskResult{
			TaskId:  strings.Repeat("task", 200),
			Status:  strings.Repeat("status", 200),
			Summary: strings.Repeat("summary", 200),
			Error:   strings.Repeat("error", 200),
		}
	}
	for i := range source.ModelIdentities {
		source.ModelIdentities[i] = &acppb.PartialModelIdentity{
			RequestedModel: strings.Repeat("requested", 200),
			SelectedModel:  strings.Repeat("selected", 200),
			ProviderId:     strings.Repeat("provider", 200),
			ResponseModel:  strings.Repeat("response-model", 200),
			ResponseId:     strings.Repeat("response-id", 200),
		}
	}

	got, err := Build(source)
	if err != nil {
		t.Fatalf("Build large projectable source: %v", err)
	}
	if size := proto.Size(statusWithDetail(codes.Internal, got)); size > MaxStatusBytes {
		t.Fatalf("built status size = %d, want <= %d", size, MaxStatusBytes)
	}
	if got.GetOriginalDraftBytes() != 12345 {
		t.Fatalf("original_draft_bytes = %d, want preserved supplied 12345", got.GetOriginalDraftBytes())
	}
	if !got.GetTruncated() || got.GetOmittedTaskResults() == 0 || got.GetOmittedModelIdentities() == 0 {
		t.Fatalf("truncation metadata = truncated:%t omitted tasks:%d identities:%d", got.GetTruncated(), got.GetOmittedTaskResults(), got.GetOmittedModelIdentities())
	}
}

func TestValidateRejectsHostileInboundFields(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*acppb.PartialResult)
	}{
		{"draft over cap", func(r *acppb.PartialResult) { r.PartialResponse.Content = strings.Repeat("x", MaxDraftBytes+1) }},
		{"invalid utf8 draft", func(r *acppb.PartialResult) { r.PartialResponse.Content = string([]byte{0xff}) }},
		{"reason over cap", func(r *acppb.PartialResult) { r.ReasonCode = strings.Repeat("x", MaxReasonCodeBytes+1) }},
		{"safe error over cap", func(r *acppb.PartialResult) { r.SafeError = strings.Repeat("x", MaxSafeErrorBytes+1) }},
		{"nil task row", func(r *acppb.PartialResult) { r.TaskResults = []*acppb.PartialTaskResult{nil} }},
		{"task string over cap", func(r *acppb.PartialResult) { r.TaskResults[0].Summary = strings.Repeat("x", MaxStringBytes+1) }},
		{"negative task counter", func(r *acppb.PartialResult) { r.TaskResults[0].ToolCallCount = -1 }},
		{"negative usage", func(r *acppb.PartialResult) { r.TaskResults[0].Usage.InputTokens = -1 }},
		{"negative pointer usage", func(r *acppb.PartialResult) { r.TaskResults[0].Usage.ReportedReasoningTokens = ptrInt64(-1) }},
		{"nil identity row", func(r *acppb.PartialResult) { r.ModelIdentities = []*acppb.PartialModelIdentity{nil} }},
		{"identity string over cap", func(r *acppb.PartialResult) { r.ModelIdentities[0].ResponseId = strings.Repeat("x", MaxStringBytes+1) }},
		{"negative omitted counter", func(r *acppb.PartialResult) { r.OmittedTaskResults = -1 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := validPartialResult()
			tt.mutate(result)
			if err := Validate(result); err == nil {
				t.Fatalf("Validate() = nil, want rejection for %s", tt.name)
			}
		})
	}
}

func TestExtractRejectsOversizeAndMultiplePartialDetails(t *testing.T) {
	first, err := Build(&acppb.PartialResult{
		PartialResponse: &acppb.Message{Role: "assistant", Content: "first"},
	})
	if err != nil {
		t.Fatalf("Build first: %v", err)
	}
	second, err := Build(&acppb.PartialResult{
		PartialResponse: &acppb.Message{Role: "assistant", Content: "second"},
	})
	if err != nil {
		t.Fatalf("Build second: %v", err)
	}
	multi, err := status.New(codes.Internal, "multiple").WithDetails(first, second)
	if err != nil {
		t.Fatalf("WithDetails multiple: %v", err)
	}
	if got, ok := Extract(multi.Err()); ok || got != nil {
		t.Fatalf("Extract multiple partial details = %+v, %t; want reject", got, ok)
	}

	oversize, err := status.New(codes.Internal, strings.Repeat("x", MaxStatusBytes)).WithDetails(first)
	if err != nil {
		t.Fatalf("WithDetails oversize status: %v", err)
	}
	if got, ok := Extract(oversize.Err()); ok || got != nil {
		t.Fatalf("Extract oversize status = %+v, %t; want reject", got, ok)
	}
}

func TestBufconnUnaryClientReceivesTypedPartialResultDetail(t *testing.T) {
	listener := bufconn.Listen(1024 * 1024)
	grpcServer := grpc.NewServer()
	acppb.RegisterAgentCommunicationServer(grpcServer, partialResultTestServer{})
	go func() {
		_ = grpcServer.Serve(listener)
	}()
	t.Cleanup(func() {
		grpcServer.Stop()
		_ = listener.Close()
	})

	ctx := context.Background()
	conn, err := grpc.DialContext(ctx, "bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	resp, err := acppb.NewAgentCommunicationClient(conn).SendMessage(ctx, &acppb.SendMessageRequest{
		Message: &acppb.Message{Role: "user", Content: "hello"},
	})
	if err == nil || resp != nil {
		t.Fatalf("SendMessage response/error = %+v/%v, want nil response and non-OK error", resp, err)
	}
	got, ok := Extract(err)
	if !ok {
		t.Fatalf("Extract() ok=false from generated gRPC client error: %v", err)
	}
	if got.GetPartialResponse().GetContent() != "public bufconn draft" || got.GetReasonCode() != "provider_error" {
		t.Fatalf("partial result = %+v", got)
	}
}

type partialResultTestServer struct {
	acppb.UnimplementedAgentCommunicationServer
}

func (partialResultTestServer) SendMessage(context.Context, *acppb.SendMessageRequest) (*acppb.SendMessageResponse, error) {
	return nil, StatusError(codes.Internal, "ACP response incomplete", &acppb.PartialResult{
		ReasonCode:      "provider_error",
		PartialResponse: &acppb.Message{Role: "assistant", Content: "public bufconn draft"},
	})
}

func TestIncompleteErrorUnwrapsCause(t *testing.T) {
	cause := status.Error(codes.DeadlineExceeded, "deadline")
	result := &acppb.PartialResult{SchemaVersion: SchemaVersion, Incomplete: true, SafeError: "safe incomplete"}
	err := &IncompleteError{Result: result, Cause: cause}
	if !errors.Is(err, cause) {
		t.Fatalf("errors.Is did not preserve cause")
	}
	if err.Error() != "safe incomplete" {
		t.Fatalf("Error() = %q", err.Error())
	}
}

func ptrInt64(v int64) *int64 { return &v }

func validPartialResult() *acppb.PartialResult {
	return &acppb.PartialResult{
		SchemaVersion: SchemaVersion,
		Incomplete:    true,
		Status:        "incomplete",
		ReasonCode:    "provider_error",
		SafeError:     "safe incomplete",
		PartialResponse: &acppb.Message{
			Role:    "assistant",
			Content: "public draft",
		},
		TaskResults: []*acppb.PartialTaskResult{{
			TaskId:       "task-1",
			Status:       "incomplete",
			Summary:      "public task summary",
			Error:        "safe task error",
			FinishReason: "length",
			Usage: &acppb.PartialUsage{
				InputTokens:               1,
				OutputTokens:              2,
				ReportedTotalTokens:       3,
				UsageEvidencePresent:      true,
				ReportedReasoningTokens:   ptrInt64(1),
				ReportedCachedInputTokens: ptrInt64(1),
			},
			ToolCallCount: 1,
			CommandCount:  1,
		}},
		ModelIdentities: []*acppb.PartialModelIdentity{{
			RequestedModel:              "alias",
			SelectedModel:               "selected",
			ProviderId:                  "provider",
			ResponseModel:               "model",
			ResponseId:                  "response-id",
			ExecutionIdentityConflicted: true,
		}},
	}
}

func protojsonString(t *testing.T, msg proto.Message) string {
	t.Helper()
	return fmt.Sprint(msg)
}
