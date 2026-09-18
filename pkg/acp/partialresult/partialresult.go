package partialresult

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	acppb "m31labs.dev/buckley/pkg/acp/proto"
)

const (
	SchemaVersion = "buckley.acp.partial_result.v1"

	MaxDraftBytes        = 2 * 1024
	MaxStatusBytes       = 4 * 1024
	MaxTaskResults       = 8
	MaxModelIdentities   = 8
	MaxStringBytes       = 512
	MaxReasonCodeBytes   = 128
	MaxSafeErrorBytes    = 512
	statusDetailOverhead = 256
)

var (
	ErrInvalidPartialResult = errors.New("invalid ACP partial result")
	ErrNoPartialResult      = errors.New("ACP partial result unavailable")
)

// IncompleteError carries a validated public partial result from a non-OK ACP
// unary response. It intentionally remains an error so callers do not mistake
// the partial draft for accepted success.
type IncompleteError struct {
	Result *acppb.PartialResult
	Cause  error
}

func (e *IncompleteError) Error() string {
	if e == nil || e.Result == nil {
		return ErrNoPartialResult.Error()
	}
	if msg := strings.TrimSpace(e.Result.SafeError); msg != "" {
		return msg
	}
	return "ACP response incomplete"
}

func (e *IncompleteError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// Build clones and bounds a public PartialResult detail. It does not accept raw
// transcripts, prompts, reasoning, or tool payloads; callers must pass only the
// narrow proto fields defined by acp.proto.
func Build(source *acppb.PartialResult) (*acppb.PartialResult, error) {
	if source == nil {
		return nil, fmt.Errorf("%w: nil detail", ErrInvalidPartialResult)
	}
	out := proto.Clone(source).(*acppb.PartialResult)
	normalize(out)
	fitStatusDetail(codes.Internal, out)
	if err := Validate(out); err != nil {
		return nil, err
	}
	return out, nil
}

// StatusError returns a non-OK gRPC status error with a bounded typed detail.
// If the detail cannot fit in the status budget, lower-priority evidence rows
// are omitted before the public draft is truncated further.
func StatusError(code codes.Code, safeMessage string, source *acppb.PartialResult) error {
	if code == codes.OK {
		code = codes.Internal
	}
	detail, err := Build(source)
	if err != nil {
		return status.Error(code, clampString(safeMessage, MaxSafeErrorBytes))
	}
	if msg := strings.TrimSpace(safeMessage); msg != "" {
		detail.SafeError = clampString(msg, MaxSafeErrorBytes)
	}
	fitStatusDetail(code, detail)
	st := status.New(code, detail.GetSafeError())
	withDetails, err := st.WithDetails(detail)
	if err != nil {
		return st.Err()
	}
	if proto.Size(withDetails.Proto()) > MaxStatusBytes {
		return st.Err()
	}
	return withDetails.Err()
}

// Extract returns the single recognized, validated PartialResult detail from err.
func Extract(err error) (*acppb.PartialResult, bool) {
	st, ok := status.FromError(err)
	if !ok {
		return nil, false
	}
	if proto.Size(st.Proto()) > MaxStatusBytes {
		return nil, false
	}
	var found *acppb.PartialResult
	for _, detail := range st.Details() {
		pr, ok := detail.(*acppb.PartialResult)
		if !ok {
			continue
		}
		if found != nil {
			return nil, false
		}
		if Validate(pr) != nil {
			return nil, false
		}
		found = pr
	}
	if found == nil {
		return nil, false
	}
	out := proto.Clone(found).(*acppb.PartialResult)
	return out, true
}

func Validate(result *acppb.PartialResult) error {
	if result == nil {
		return fmt.Errorf("%w: nil detail", ErrInvalidPartialResult)
	}
	if result.GetSchemaVersion() != SchemaVersion {
		return fmt.Errorf("%w: unsupported schema %q", ErrInvalidPartialResult, result.GetSchemaVersion())
	}
	if !result.GetIncomplete() {
		return fmt.Errorf("%w: incomplete=false", ErrInvalidPartialResult)
	}
	if result.GetOriginalDraftBytes() < 0 || result.GetOmittedTaskResults() < 0 || result.GetOmittedModelIdentities() < 0 {
		return fmt.Errorf("%w: negative counters", ErrInvalidPartialResult)
	}
	if err := validateBoundedString("status", result.GetStatus(), MaxStringBytes); err != nil {
		return err
	}
	if err := validateBoundedString("reason_code", result.GetReasonCode(), MaxReasonCodeBytes); err != nil {
		return err
	}
	if err := validateBoundedString("safe_error", result.GetSafeError(), MaxSafeErrorBytes); err != nil {
		return err
	}
	if msg := result.GetPartialResponse(); msg != nil {
		if err := validateBoundedString("partial_response.role", msg.GetRole(), MaxStringBytes); err != nil {
			return err
		}
		if err := validateBoundedString("partial_response.content", msg.GetContent(), MaxDraftBytes); err != nil {
			return err
		}
	}
	if proto.Size(result) > MaxStatusBytes-statusDetailOverhead {
		return fmt.Errorf("%w: detail exceeds %d bytes", ErrInvalidPartialResult, MaxStatusBytes)
	}
	if len(result.GetTaskResults()) > MaxTaskResults {
		return fmt.Errorf("%w: too many task results", ErrInvalidPartialResult)
	}
	if len(result.GetModelIdentities()) > MaxModelIdentities {
		return fmt.Errorf("%w: too many model identities", ErrInvalidPartialResult)
	}
	for i, task := range result.GetTaskResults() {
		if task == nil {
			return fmt.Errorf("%w: nil task result %d", ErrInvalidPartialResult, i)
		}
		if err := validateTask(task); err != nil {
			return err
		}
	}
	for i, identity := range result.GetModelIdentities() {
		if identity == nil {
			return fmt.Errorf("%w: nil model identity %d", ErrInvalidPartialResult, i)
		}
		if err := validateIdentity(identity); err != nil {
			return err
		}
	}
	return nil
}

func normalize(result *acppb.PartialResult) {
	result.SchemaVersion = SchemaVersion
	result.Incomplete = true
	if strings.TrimSpace(result.Status) == "" {
		result.Status = "incomplete"
	}
	result.Status = clampString(result.Status, MaxStringBytes)
	result.ReasonCode = clampString(result.ReasonCode, MaxReasonCodeBytes)
	if strings.TrimSpace(result.SafeError) == "" {
		result.SafeError = "ACP response incomplete"
	}
	result.SafeError = clampString(result.SafeError, MaxSafeErrorBytes)
	if result.PartialResponse != nil {
		result.PartialResponse.Role = clampString(result.PartialResponse.Role, MaxStringBytes)
		if strings.TrimSpace(result.PartialResponse.Role) == "" {
			result.PartialResponse.Role = "assistant"
		}
		if result.OriginalDraftBytes <= 0 {
			result.OriginalDraftBytes = int32(len(result.PartialResponse.Content))
		}
		content, truncated := clampUTF8(result.PartialResponse.Content, MaxDraftBytes)
		result.PartialResponse.Content = content
		result.Truncated = result.Truncated || truncated
	}
	if len(result.TaskResults) > MaxTaskResults {
		result.OmittedTaskResults += int32(len(result.TaskResults) - MaxTaskResults)
		result.TaskResults = result.TaskResults[:MaxTaskResults]
		result.Truncated = true
	}
	if len(result.ModelIdentities) > MaxModelIdentities {
		result.OmittedModelIdentities += int32(len(result.ModelIdentities) - MaxModelIdentities)
		result.ModelIdentities = result.ModelIdentities[:MaxModelIdentities]
		result.Truncated = true
	}
	for _, task := range result.TaskResults {
		normalizeTask(task)
	}
	for _, identity := range result.ModelIdentities {
		normalizeIdentity(identity)
	}
}

func normalizeTask(task *acppb.PartialTaskResult) {
	if task == nil {
		return
	}
	task.TaskId = clampString(task.TaskId, MaxStringBytes)
	task.Status = clampString(task.Status, MaxStringBytes)
	task.Summary = clampString(task.Summary, MaxStringBytes)
	task.Error = clampString(task.Error, MaxStringBytes)
	task.FinishReason = clampString(task.FinishReason, MaxStringBytes)
}

func normalizeIdentity(identity *acppb.PartialModelIdentity) {
	if identity == nil {
		return
	}
	identity.RequestedModel = clampString(identity.RequestedModel, MaxStringBytes)
	identity.SelectedModel = clampString(identity.SelectedModel, MaxStringBytes)
	identity.ProviderId = clampString(identity.ProviderId, MaxStringBytes)
	identity.ResponseModel = clampString(identity.ResponseModel, MaxStringBytes)
	identity.ResponseId = clampString(identity.ResponseId, MaxStringBytes)
}

func validateTask(task *acppb.PartialTaskResult) error {
	for name, value := range map[string]string{
		"task_id":       task.GetTaskId(),
		"task_status":   task.GetStatus(),
		"summary":       task.GetSummary(),
		"task_error":    task.GetError(),
		"finish_reason": task.GetFinishReason(),
	} {
		if err := validateBoundedString(name, value, MaxStringBytes); err != nil {
			return err
		}
	}
	if task.GetToolCallCount() < 0 || task.GetCommandCount() < 0 {
		return fmt.Errorf("%w: negative task counters", ErrInvalidPartialResult)
	}
	if usage := task.GetUsage(); usage != nil {
		if usage.GetInputTokens() < 0 ||
			usage.GetOutputTokens() < 0 ||
			usage.GetReasoningTokens() < 0 ||
			usage.GetCachedInputTokens() < 0 ||
			usage.GetUnclassifiedTokens() < 0 ||
			usage.GetReportedTotalTokens() < 0 ||
			usage.GetReportedCacheWriteTokens() < 0 {
			return fmt.Errorf("%w: negative usage counters", ErrInvalidPartialResult)
		}
		if usage.ReportedReasoningTokens != nil && usage.GetReportedReasoningTokens() < 0 {
			return fmt.Errorf("%w: negative reported reasoning tokens", ErrInvalidPartialResult)
		}
		if usage.ReportedCachedInputTokens != nil && usage.GetReportedCachedInputTokens() < 0 {
			return fmt.Errorf("%w: negative reported cached input tokens", ErrInvalidPartialResult)
		}
	}
	return nil
}

func validateIdentity(identity *acppb.PartialModelIdentity) error {
	for name, value := range map[string]string{
		"requested_model": identity.GetRequestedModel(),
		"selected_model":  identity.GetSelectedModel(),
		"provider_id":     identity.GetProviderId(),
		"response_model":  identity.GetResponseModel(),
		"response_id":     identity.GetResponseId(),
	} {
		if err := validateBoundedString(name, value, MaxStringBytes); err != nil {
			return err
		}
	}
	return nil
}

func validateBoundedString(name, value string, max int) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("%w: invalid UTF-8 in %s", ErrInvalidPartialResult, name)
	}
	if len(value) > max {
		return fmt.Errorf("%w: %s exceeds %d bytes", ErrInvalidPartialResult, name, max)
	}
	return nil
}

func fitStatusDetail(code codes.Code, detail *acppb.PartialResult) {
	for proto.Size(statusWithDetail(code, detail)) > MaxStatusBytes {
		switch {
		case len(detail.ModelIdentities) > 0:
			detail.OmittedModelIdentities++
			detail.ModelIdentities = detail.ModelIdentities[:len(detail.ModelIdentities)-1]
			detail.Truncated = true
		case len(detail.TaskResults) > 0:
			detail.OmittedTaskResults++
			detail.TaskResults = detail.TaskResults[:len(detail.TaskResults)-1]
			detail.Truncated = true
		case detail.PartialResponse != nil && len(detail.PartialResponse.Content) > 0:
			next := len(detail.PartialResponse.Content) / 2
			if next < 1 {
				next = 0
			}
			detail.PartialResponse.Content, _ = clampUTF8(detail.PartialResponse.Content, next)
			detail.Truncated = true
		default:
			return
		}
	}
}

func statusWithDetail(code codes.Code, detail *acppb.PartialResult) proto.Message {
	st := status.New(code, detail.GetSafeError())
	withDetails, err := st.WithDetails(detail)
	if err != nil {
		return st.Proto()
	}
	return withDetails.Proto()
}

func clampString(value string, max int) string {
	out, _ := clampUTF8(value, max)
	return out
}

func clampUTF8(value string, max int) (string, bool) {
	if max <= 0 {
		return "", value != ""
	}
	if len(value) <= max && utf8.ValidString(value) {
		return value, false
	}
	out := value
	if len(out) > max {
		out = out[:max]
	}
	for !utf8.ValidString(out) && len(out) > 0 {
		out = out[:len(out)-1]
	}
	return out, out != value
}
