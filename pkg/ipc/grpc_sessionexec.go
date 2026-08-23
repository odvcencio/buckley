package ipc

import (
	"context"
	stdliberrors "errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"m31labs.dev/buckley/pkg/agentcoord"
	ipcpb "m31labs.dev/buckley/pkg/ipc/proto"
	"m31labs.dev/buckley/pkg/runledger"
	"m31labs.dev/buckley/pkg/sessionexec"
	"m31labs.dev/buckley/pkg/storage"
)

func (s *GRPCService) GetSessionExecution(
	ctx context.Context,
	req *connect.Request[ipcpb.GetSessionExecutionRequest],
) (*connect.Response[ipcpb.GetSessionExecutionResponse], error) {
	if req == nil || req.Msg == nil {
		return nil, connectObservationError(errObservationRequest)
	}
	sessionID := req.Msg.SessionId
	if err := s.authorizeGRPCObservationSession(ctx, sessionID); err != nil {
		return nil, err
	}
	reader := s.server.executionObservationReader()
	if reader == nil {
		return nil, connectObservationError(errObservationUnavailable)
	}
	snapshot, err := reader.GetExecutionSnapshot(ctx, sessionID, observationRecentCommandLimit)
	if err != nil {
		return nil, connectObservationError(err)
	}
	if err := validateExecutionObservation(snapshot, sessionID, observationRecentCommandLimit); err != nil {
		return nil, connectObservationError(err)
	}
	return grpcObservationResponse(&ipcpb.GetSessionExecutionResponse{
		Execution: grpcExecutionSnapshot(snapshot),
	})
}

func (s *GRPCService) ListSessionCommands(
	ctx context.Context,
	req *connect.Request[ipcpb.ListSessionCommandsRequest],
) (*connect.Response[ipcpb.ListSessionCommandsResponse], error) {
	if req == nil || req.Msg == nil {
		return nil, connectObservationError(errObservationRequest)
	}
	sessionID := req.Msg.SessionId
	if err := s.authorizeGRPCObservationSession(ctx, sessionID); err != nil {
		return nil, err
	}
	query, err := normalizeGRPCCommandQuery(req.Msg)
	if err != nil {
		return nil, connectObservationError(err)
	}
	reader := s.server.executionObservationReader()
	if reader == nil {
		return nil, connectObservationError(errObservationUnavailable)
	}
	page, err := reader.ListCommandStatuses(ctx, query)
	if err != nil {
		return nil, connectObservationError(err)
	}
	if err := validateCommandObservationPage(page, query); err != nil {
		return nil, connectObservationError(err)
	}
	commands := make([]*ipcpb.SessionCommandStatus, len(page.Commands))
	for i := range page.Commands {
		commands[i] = grpcCommandStatus(page.Commands[i])
	}
	return grpcObservationResponse(&ipcpb.ListSessionCommandsResponse{
		Commands:     commands,
		NextSequence: page.Next,
		HasMore:      page.HasMore,
	})
}

func (s *GRPCService) GetSessionCommand(
	ctx context.Context,
	req *connect.Request[ipcpb.GetSessionCommandRequest],
) (*connect.Response[ipcpb.GetSessionCommandResponse], error) {
	if req == nil || req.Msg == nil {
		return nil, connectObservationError(errObservationRequest)
	}
	sessionID := req.Msg.SessionId
	if err := s.authorizeGRPCObservationSession(ctx, sessionID); err != nil {
		return nil, err
	}
	commandID := req.Msg.CommandId
	if err := sessionexec.ValidateCommandID(commandID); err != nil {
		return nil, connectObservationError(err)
	}
	reader := s.server.executionObservationReader()
	if reader == nil {
		return nil, connectObservationError(errObservationUnavailable)
	}
	status, err := reader.GetCommandStatus(ctx, sessionID, commandID)
	if err != nil {
		return nil, connectObservationError(err)
	}
	if err := validateCommandObservation(status, sessionID, commandID); err != nil {
		return nil, connectObservationError(err)
	}
	return grpcObservationResponse(&ipcpb.GetSessionCommandResponse{
		Command: grpcCommandStatus(status),
	})
}

func (s *GRPCService) ListSessionRoutines(
	ctx context.Context,
	req *connect.Request[ipcpb.ListSessionRoutinesRequest],
) (*connect.Response[ipcpb.ListSessionRoutinesResponse], error) {
	if req == nil || req.Msg == nil {
		return nil, connectObservationError(errObservationRequest)
	}
	sessionID := req.Msg.SessionId
	if err := s.authorizeGRPCObservationSession(ctx, sessionID); err != nil {
		return nil, err
	}
	query, err := normalizeGRPCRoutineQuery(req.Msg)
	if err != nil {
		return nil, connectObservationError(err)
	}
	reader := s.server.routineObservationReader()
	if reader == nil {
		return nil, connectObservationError(errObservationUnavailable)
	}
	page, err := reader.ListRoutineStatuses(ctx, query)
	if err != nil {
		return nil, connectObservationError(err)
	}
	if err := validateRoutineObservationPage(page, query); err != nil {
		return nil, connectObservationError(err)
	}
	routines := make([]*ipcpb.SessionRoutineStatus, len(page.Routines))
	for i := range page.Routines {
		routines[i] = grpcRoutineStatus(page.Routines[i])
	}
	return grpcObservationResponse(&ipcpb.ListSessionRoutinesResponse{
		Routines:   routines,
		NextCursor: page.Next,
		HasMore:    page.HasMore,
	})
}

func (s *GRPCService) GetSessionRoutine(
	ctx context.Context,
	req *connect.Request[ipcpb.GetSessionRoutineRequest],
) (*connect.Response[ipcpb.GetSessionRoutineResponse], error) {
	if req == nil || req.Msg == nil {
		return nil, connectObservationError(errObservationRequest)
	}
	sessionID := req.Msg.SessionId
	if err := s.authorizeGRPCObservationSession(ctx, sessionID); err != nil {
		return nil, err
	}
	runID := req.Msg.RunId
	if err := agentcoord.ValidateMonitorIdentity(sessionID, runID); err != nil {
		return nil, connectObservationError(err)
	}
	reader := s.server.routineObservationReader()
	if reader == nil {
		return nil, connectObservationError(errObservationUnavailable)
	}
	status, err := reader.GetRoutineStatus(ctx, sessionID, runID)
	if err != nil {
		return nil, connectObservationError(err)
	}
	if status.SessionID != sessionID || status.RunID != runID || validateRoutineObservation(status) != nil {
		return nil, connectObservationError(errObservationConflict)
	}
	return grpcObservationResponse(&ipcpb.GetSessionRoutineResponse{
		Routine: grpcRoutineStatus(status),
	})
}

func (s *GRPCService) ListRoutineMailbox(
	ctx context.Context,
	req *connect.Request[ipcpb.ListRoutineMailboxRequest],
) (*connect.Response[ipcpb.ListRoutineMailboxResponse], error) {
	if req == nil || req.Msg == nil {
		return nil, connectObservationError(errObservationRequest)
	}
	sessionID := req.Msg.SessionId
	if err := s.authorizeGRPCObservationSession(ctx, sessionID); err != nil {
		return nil, err
	}
	query, err := normalizeGRPCMailboxQuery(req.Msg)
	if err != nil {
		return nil, connectObservationError(err)
	}
	reader := s.server.routineObservationReader()
	if reader == nil {
		return nil, connectObservationError(errObservationUnavailable)
	}
	page, err := reader.ListMailboxStatuses(ctx, query)
	if err != nil {
		return nil, connectObservationError(err)
	}
	if err := validateMailboxObservationPage(page, query); err != nil {
		return nil, connectObservationError(err)
	}
	messages := make([]*ipcpb.RoutineMailboxStatus, len(page.Messages))
	for i := range page.Messages {
		messages[i] = grpcMailboxStatus(page.Messages[i])
	}
	return grpcObservationResponse(&ipcpb.ListRoutineMailboxResponse{
		Messages:     messages,
		NextSequence: page.Next,
		HasMore:      page.HasMore,
	})
}

func (s *GRPCService) authorizeGRPCObservationSession(ctx context.Context, sessionID string) error {
	if err := requireGRPCScope(ctx, storage.TokenScopeViewer); err != nil {
		return grpcObservationNoStoreError(err)
	}
	if err := sessionexec.ValidateSessionID(sessionID); err != nil {
		return connectObservationError(err)
	}
	if s == nil || s.server == nil || s.server.store == nil {
		return connectObservationError(errObservationUnavailable)
	}
	session, err := s.server.store.GetSession(sessionID)
	if err != nil {
		return connectObservationError(err)
	}
	if session == nil || !principalCanAccessSession(principalFromContext(ctx), session) {
		return connectObservationError(errObservationNotFound)
	}
	return nil
}

func normalizeGRPCCommandQuery(msg *ipcpb.ListSessionCommandsRequest) (sessionexec.CommandStatusQuery, error) {
	if msg.Limit < 0 || len(msg.States) > observationMaxCommandStates {
		return sessionexec.CommandStatusQuery{}, errObservationRequest
	}
	states := make([]sessionexec.State, len(msg.States))
	for i, raw := range msg.States {
		state := sessionexec.State(raw)
		if !state.Valid() || raw != strings.ToLower(raw) {
			return sessionexec.CommandStatusQuery{}, errObservationRequest
		}
		states[i] = state
	}
	return sessionexec.NormalizeCommandStatusQuery(sessionexec.CommandStatusQuery{
		SessionID:     msg.SessionId,
		States:        states,
		AfterSequence: msg.AfterSequence,
		Limit:         int(msg.Limit),
	})
}

func normalizeGRPCRoutineQuery(msg *ipcpb.ListSessionRoutinesRequest) (agentcoord.RoutineQuery, error) {
	if msg.Limit < 0 {
		return agentcoord.RoutineQuery{}, errObservationRequest
	}
	return agentcoord.NormalizeRoutineQuery(agentcoord.RoutineQuery{
		SessionID:   msg.SessionId,
		ParentRunID: msg.ParentRunId,
		Before:      msg.BeforeCursor,
		Limit:       int(msg.Limit),
	})
}

func normalizeGRPCMailboxQuery(msg *ipcpb.ListRoutineMailboxRequest) (agentcoord.MailboxStatusQuery, error) {
	if msg.Limit < 0 || len(msg.States) > agentcoord.MaxMailboxStatusStates {
		return agentcoord.MailboxStatusQuery{}, errObservationRequest
	}
	states := make([]agentcoord.MailboxState, len(msg.States))
	for i, raw := range msg.States {
		state := agentcoord.MailboxState(raw)
		if !state.Valid() || raw != strings.ToLower(raw) {
			return agentcoord.MailboxStatusQuery{}, errObservationRequest
		}
		states[i] = state
	}
	return agentcoord.NormalizeMailboxStatusQuery(agentcoord.MailboxStatusQuery{
		SessionID:     msg.SessionId,
		RunID:         msg.RunId,
		States:        states,
		AfterSequence: msg.AfterSequence,
		Limit:         int(msg.Limit),
	})
}

func connectObservationError(err error) error {
	code := connect.CodeInternal
	message := "observation failed"
	switch {
	case stdliberrors.Is(err, errObservationRequest), stdliberrors.Is(err, sessionexec.ErrValidation),
		stdliberrors.Is(err, agentcoord.ErrMonitorValidation):
		code, message = connect.CodeInvalidArgument, "invalid observation request"
	case stdliberrors.Is(err, errObservationNotFound), stdliberrors.Is(err, sessionexec.ErrNotFound),
		stdliberrors.Is(err, runledger.ErrNotFound):
		code, message = connect.CodeNotFound, "observation not found"
	case stdliberrors.Is(err, agentcoord.ErrMonitorConflict):
		code, message = connect.CodeAborted, "observation changed"
	case stdliberrors.Is(err, errObservationConflict), stdliberrors.Is(err, sessionexec.ErrIdempotencyConflict),
		stdliberrors.Is(err, sessionexec.ErrTerminalConflict), stdliberrors.Is(err, sessionexec.ErrTranscriptConflict),
		stdliberrors.Is(err, sessionexec.ErrEffectPermitConflict), stdliberrors.Is(err, agentcoord.ErrMonitorIntegrity):
		code, message = connect.CodeAborted, "observation conflict"
	case stdliberrors.Is(err, context.DeadlineExceeded):
		code, message = connect.CodeDeadlineExceeded, "observation timed out"
	case stdliberrors.Is(err, context.Canceled):
		code, message = connect.CodeCanceled, "observation cancelled"
	case stdliberrors.Is(err, agentcoord.ErrMonitorCapacity), stdliberrors.Is(err, sessionexec.ErrEffectPermitLimit),
		stdliberrors.Is(err, sessionexec.ErrCancellationLimit):
		code, message = connect.CodeUnavailable, "observation unavailable"
	case stdliberrors.Is(err, errObservationUnavailable), stdliberrors.Is(err, storage.ErrStoreClosed),
		stdliberrors.Is(err, runledger.ErrMonitorUnavailable), storage.IsSQLiteBusyError(err):
		code, message = connect.CodeUnavailable, "observation unavailable"
	}
	return grpcObservationNoStoreError(connect.NewError(code, fmt.Errorf("%s", message)))
}

func grpcObservationResponse[T any](message *T) (*connect.Response[T], error) {
	protoMessage, ok := any(message).(proto.Message)
	if !ok || proto.Size(protoMessage) > observationMaxResponseBytes {
		return nil, connectObservationError(errObservationConflict)
	}
	response := connect.NewResponse(message)
	response.Header().Set("Cache-Control", "no-store")
	return response, nil
}

func grpcObservationNoStoreError(err error) error {
	var connectErr *connect.Error
	if stdliberrors.As(err, &connectErr) {
		connectErr.Meta().Set("Cache-Control", "no-store")
	}
	return err
}

func grpcExecutionSnapshot(snapshot sessionexec.ExecutionSnapshot) *ipcpb.SessionExecutionSnapshot {
	result := &ipcpb.SessionExecutionSnapshot{
		SessionId:                 snapshot.SessionID,
		Initialized:               snapshot.Initialized,
		CommandSummary:            grpcCommandSummary(snapshot.Summary),
		EffectSummary:             grpcEffectSummary(snapshot.EffectSummary),
		AttentionEffectsTruncated: snapshot.AttentionEffectsTruncated,
		ObservedAt:                timestamppb.New(snapshot.ObservedAt),
	}
	if snapshot.Initialized {
		result.State = &ipcpb.SessionExecutionState{
			SessionId:  snapshot.ExecutionState.SessionID,
			Mode:       string(snapshot.ExecutionState.Mode),
			Generation: snapshot.ExecutionState.Generation,
			ReasonCode: snapshot.ExecutionState.ReasonCode,
			UpdatedAt:  timestamppb.New(snapshot.ExecutionState.UpdatedAt),
		}
	}
	result.AttentionEffects = make([]*ipcpb.SessionEffectStatus, len(snapshot.AttentionEffects))
	for i := range snapshot.AttentionEffects {
		result.AttentionEffects[i] = grpcEffectStatus(snapshot.AttentionEffects[i])
	}
	result.RecentCommands = make([]*ipcpb.SessionCommandStatus, len(snapshot.RecentCommands))
	for i := range snapshot.RecentCommands {
		result.RecentCommands[i] = grpcCommandStatus(snapshot.RecentCommands[i])
	}
	return result
}

func grpcCommandSummary(summary sessionexec.Summary) *ipcpb.SessionCommandSummary {
	return &ipcpb.SessionCommandSummary{
		SessionId:      summary.SessionID,
		Total:          int64(summary.Total),
		Accepted:       int64(summary.Accepted),
		Running:        int64(summary.Running),
		Succeeded:      int64(summary.Succeeded),
		Failed:         int64(summary.Failed),
		Blocked:        int64(summary.Blocked),
		Interrupted:    int64(summary.Interrupted),
		Cancelled:      int64(summary.Cancelled),
		WorkPending:    int64(summary.WorkPending),
		ControlPending: int64(summary.ControlPending),
		LastSequence:   summary.LastSequence,
	}
}

func grpcEffectSummary(summary sessionexec.EffectSummary) *ipcpb.SessionEffectSummary {
	return &ipcpb.SessionEffectSummary{
		Total:     int64(summary.Total),
		Active:    int64(summary.Active),
		Ambiguous: int64(summary.Ambiguous),
		Ended:     int64(summary.Ended),
		Resolved:  int64(summary.Resolved),
	}
}

func grpcCommandStatus(status sessionexec.CommandStatus) *ipcpb.SessionCommandStatus {
	result := &ipcpb.SessionCommandStatus{
		SessionId:        status.SessionID,
		RunId:            status.RunID,
		TaskId:           status.TaskID,
		CommandId:        status.CommandID,
		TurnId:           status.TurnID,
		Generation:       int64(status.Generation),
		Sequence:         status.Sequence,
		Type:             status.Type,
		Lane:             string(status.Lane),
		State:            string(status.State),
		Attempt:          int64(status.Attempt),
		TargetCommandId:  status.TargetCommandID,
		AcceptedAt:       timestamppb.New(status.AcceptedAt),
		StartedAt:        grpcOptionalTimestamp(status.StartedAt),
		FinishedAt:       grpcOptionalTimestamp(status.FinishedAt),
		ErrorCode:        status.ErrorCode,
		EffectSummary:    grpcEffectSummary(status.EffectSummary),
		EffectsTruncated: status.EffectsTruncated,
	}
	result.Effects = make([]*ipcpb.SessionEffectStatus, len(status.Effects))
	for i := range status.Effects {
		result.Effects[i] = grpcEffectStatus(status.Effects[i])
	}
	return result
}

func grpcEffectStatus(status sessionexec.EffectStatus) *ipcpb.SessionEffectStatus {
	return &ipcpb.SessionEffectStatus{
		SessionId:         status.SessionID,
		CommandId:         status.CommandID,
		CommandGeneration: int64(status.CommandGeneration),
		EffectId:          status.EffectID,
		Kind:              string(status.Kind),
		State:             string(status.State),
		CreatedAt:         timestamppb.New(status.CreatedAt),
		ExpiresAt:         timestamppb.New(status.ExpiresAt),
		AmbiguousAt:       grpcOptionalTimestamp(status.AmbiguousAt),
		EndedAt:           grpcOptionalTimestamp(status.EndedAt),
		ResolvedAt:        grpcOptionalTimestamp(status.ResolvedAt),
	}
}

func grpcRoutineStatus(status agentcoord.RoutineStatus) *ipcpb.SessionRoutineStatus {
	return &ipcpb.SessionRoutineStatus{
		SessionId:   status.SessionID,
		RunId:       status.RunID,
		ParentRunId: status.ParentRunID,
		TaskId:      status.TaskID,
		AgentId:     status.AgentID,
		ModelId:     status.ModelID,
		ProviderId:  status.ProviderID,
		Backend:     status.Backend,
		State:       string(status.State),
		StartedAt:   timestamppb.New(status.StartedAt),
		FinishedAt:  grpcOptionalTimestamp(status.FinishedAt),
		Attempt: &ipcpb.SessionRoutineAttemptStatus{
			Number:         int64(status.Attempt.Number),
			State:          string(status.Attempt.State),
			AttachedAt:     grpcOptionalTimestamp(status.Attempt.AttachedAt),
			HeartbeatAt:    grpcOptionalTimestamp(status.Attempt.HeartbeatAt),
			LeaseExpiresAt: grpcOptionalTimestamp(status.Attempt.LeaseExpiresAt),
			DetachedAt:     grpcOptionalTimestamp(status.Attempt.DetachedAt),
		},
		Mailbox: &ipcpb.SessionRoutineMailboxSummary{
			Queued:       int64(status.Mailbox.Queued),
			Claimed:      int64(status.Mailbox.Claimed),
			Processed:    int64(status.Mailbox.Processed),
			DeadLetter:   int64(status.Mailbox.DeadLetter),
			LastSequence: status.Mailbox.LastSequence,
		},
	}
}

func grpcMailboxStatus(status agentcoord.MailboxStatus) *ipcpb.RoutineMailboxStatus {
	return &ipcpb.RoutineMailboxStatus{
		SessionId:      status.SessionID,
		RunId:          status.RunID,
		MessageId:      status.MessageID,
		PeerRunId:      status.PeerRunID,
		Kind:           status.Kind,
		Direction:      string(status.Direction),
		State:          string(status.State),
		Sequence:       status.Sequence,
		ByteCount:      status.ByteCount,
		CreatedAt:      timestamppb.New(status.CreatedAt),
		ProcessedAt:    grpcOptionalTimestamp(status.ProcessedAt),
		DeadLetteredAt: grpcOptionalTimestamp(status.DeadLetteredAt),
	}
}

func grpcOptionalTimestamp(value *time.Time) *timestamppb.Timestamp {
	if value == nil {
		return nil
	}
	return timestamppb.New(*value)
}
