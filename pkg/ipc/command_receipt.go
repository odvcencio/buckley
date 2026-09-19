package ipc

import (
	"context"
	stdliberrors "errors"
	"fmt"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"m31labs.dev/buckley/pkg/ipc/command"
	ipcpb "m31labs.dev/buckley/pkg/ipc/proto"
	"m31labs.dev/buckley/pkg/sessionexec"
	"m31labs.dev/buckley/pkg/storage"
)

type receiptCommandAcceptor interface {
	AcceptCommand(context.Context, command.SessionCommand) (sessionexec.Receipt, error)
}

type commandDispatchMode uint8

const (
	commandDispatchGateway commandDispatchMode = iota
	commandDispatchRegistry
	commandDispatchRegistryThenGateway
)

type commandDispatchOutcome struct {
	Receipt       sessionexec.Receipt
	Durable       bool
	Authoritative bool
	UsedRegistry  bool
}

type authoritativeCommandError struct {
	err error
}

func (e *authoritativeCommandError) Error() string {
	if e == nil || e.err == nil {
		return "command acceptance failed"
	}
	return e.err.Error()
}

func (e *authoritativeCommandError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func commandAuthoritativeError(err error) error {
	if err == nil {
		return nil
	}
	return &authoritativeCommandError{err: err}
}

func isAuthoritativeCommandError(err error) bool {
	var target *authoritativeCommandError
	return stdliberrors.As(err, &target)
}

func (s *Server) dispatchCommandWithReceipt(
	ctx context.Context,
	cmd *command.SessionCommand,
	mode commandDispatchMode,
) (commandDispatchOutcome, error) {
	if cmd == nil {
		return commandDispatchOutcome{}, commandAuthoritativeError(fmt.Errorf("%w: command is required", sessionexec.ErrValidation))
	}
	cmd.EnsureID()
	if err := sessionexec.ValidateCommandID(cmd.ID); err != nil {
		return commandDispatchOutcome{}, commandAuthoritativeError(err)
	}

	registry := s.getHeadlessRegistry()
	if registry != nil {
		if acceptor, ok := registry.(receiptCommandAcceptor); ok && !isTypedNilInterface(acceptor) {
			receipt, err := acceptor.AcceptCommand(ctx, *cmd)
			if err != nil {
				return commandDispatchOutcome{Authoritative: true}, commandAuthoritativeError(err)
			}
			durable, err := validateAcceptedCommandReceipt(receipt, cmd.SessionID, cmd.ID)
			if err != nil {
				return commandDispatchOutcome{Authoritative: true}, commandAuthoritativeError(err)
			}
			return commandDispatchOutcome{
				Receipt: receipt, Durable: durable, Authoritative: true, UsedRegistry: true,
			}, nil
		}
	}

	if mode == commandDispatchRegistry || mode == commandDispatchRegistryThenGateway {
		if registry == nil {
			if mode == commandDispatchRegistry {
				return commandDispatchOutcome{}, fmt.Errorf("headless sessions not enabled")
			}
		} else if err := registry.DispatchCommand(*cmd); err == nil {
			return commandDispatchOutcome{UsedRegistry: true}, nil
		} else if mode == commandDispatchRegistry {
			return commandDispatchOutcome{}, err
		}
	}

	if mode == commandDispatchGateway || mode == commandDispatchRegistryThenGateway {
		if s == nil || s.commandGW == nil {
			return commandDispatchOutcome{}, fmt.Errorf("no command handler available")
		}
		if err := s.commandGW.Dispatch(*cmd); err != nil {
			return commandDispatchOutcome{}, err
		}
		return commandDispatchOutcome{}, nil
	}

	return commandDispatchOutcome{}, fmt.Errorf("no command handler available")
}

func validateAcceptedCommandReceipt(receipt sessionexec.Receipt, sessionID, commandID string) (bool, error) {
	if receipt.SessionID != sessionID || receipt.CommandID != commandID {
		return false, fmt.Errorf("command receipt identity mismatch")
	}
	if isSyntheticCommandReceipt(receipt) {
		return false, nil
	}
	if sessionexec.ValidateSessionID(receipt.SessionID) != nil || sessionexec.ValidateCommandID(receipt.CommandID) != nil ||
		receipt.RunID != sessionexec.RunIDForSession(receipt.SessionID) || receipt.TaskID != sessionexec.ForegroundTaskID ||
		receipt.Generation < 0 || receipt.Generation > sessionexec.MaxCommandAttempts ||
		receipt.TurnID != sessionexec.TurnID(receipt.CommandID, receipt.Generation) ||
		receipt.Sequence < 1 || receipt.Sequence > sessionexec.MaxCommandSequence ||
		receipt.Attempt < 0 || receipt.Attempt > sessionexec.MaxCommandAttempts ||
		!receipt.State.Valid() || !observationSafeTime(receipt.AcceptedAt) ||
		!observationSafeTimePtr(receipt.StartedAt) || !observationSafeTimePtr(receipt.FinishedAt) ||
		sessionexec.ValidateErrorCode(receipt.ErrorCode) != nil {
		return false, fmt.Errorf("command receipt is invalid")
	}
	if receipt.Lane != sessionexec.LaneWork && receipt.Lane != sessionexec.LaneControl {
		return false, fmt.Errorf("command receipt lane is invalid")
	}
	if receipt.TargetCommandID != "" && sessionexec.ValidateCommandID(receipt.TargetCommandID) != nil {
		return false, fmt.Errorf("command receipt target is invalid")
	}
	if (receipt.Attempt == 0) != (receipt.StartedAt == nil) {
		return false, fmt.Errorf("command receipt attempt is invalid")
	}
	if receipt.StartedAt != nil && receipt.StartedAt.Before(receipt.AcceptedAt) {
		return false, fmt.Errorf("command receipt start precedes acceptance")
	}
	if receipt.FinishedAt != nil && (receipt.FinishedAt.Before(receipt.AcceptedAt) ||
		(receipt.StartedAt != nil && receipt.FinishedAt.Before(*receipt.StartedAt))) {
		return false, fmt.Errorf("command receipt finish precedes start")
	}
	switch receipt.State {
	case sessionexec.StateAccepted:
		if receipt.FinishedAt != nil || receipt.ErrorCode != "" {
			return false, fmt.Errorf("accepted command receipt has terminal fields")
		}
	case sessionexec.StateRunning:
		if receipt.Attempt < 1 || receipt.StartedAt == nil || receipt.FinishedAt != nil || receipt.ErrorCode != "" {
			return false, fmt.Errorf("running command receipt fields are invalid")
		}
	default:
		if receipt.FinishedAt == nil || (receipt.State != sessionexec.StateCancelled && receipt.Attempt < 1) {
			return false, fmt.Errorf("terminal command receipt fields are invalid")
		}
	}
	return true, nil
}

func isSyntheticCommandReceipt(receipt sessionexec.Receipt) bool {
	return receipt.RunID == "" && receipt.TaskID == "" && receipt.TurnID == "" &&
		receipt.Generation == 0 && receipt.Sequence == 0 && receipt.Lane == "" &&
		receipt.State == sessionexec.StateAccepted && !receipt.Duplicate && receipt.Attempt == 0 &&
		receipt.TargetCommandID == "" && receipt.AcceptedAt.IsZero() && receipt.StartedAt == nil &&
		receipt.FinishedAt == nil && receipt.ErrorCode == "" && receipt.Error == ""
}

type commandReceiptJSON struct {
	SessionID       string     `json:"sessionId"`
	RunID           string     `json:"runId"`
	TaskID          string     `json:"taskId"`
	CommandID       string     `json:"commandId"`
	TurnID          string     `json:"turnId"`
	Generation      int        `json:"generation"`
	Sequence        int64      `json:"sequence"`
	Lane            string     `json:"lane"`
	State           string     `json:"state"`
	Duplicate       bool       `json:"duplicate"`
	Attempt         int        `json:"attempt"`
	TargetCommandID string     `json:"targetCommandId,omitempty"`
	AcceptedAt      time.Time  `json:"acceptedAt"`
	StartedAt       *time.Time `json:"startedAt,omitempty"`
	FinishedAt      *time.Time `json:"finishedAt,omitempty"`
	ErrorCode       string     `json:"errorCode,omitempty"`
}

func commandReceiptForJSON(receipt sessionexec.Receipt) *commandReceiptJSON {
	return &commandReceiptJSON{
		SessionID: receipt.SessionID, RunID: receipt.RunID, TaskID: receipt.TaskID,
		CommandID: receipt.CommandID, TurnID: receipt.TurnID, Generation: receipt.Generation,
		Sequence: receipt.Sequence, Lane: string(receipt.Lane), State: string(receipt.State),
		Duplicate: receipt.Duplicate, Attempt: receipt.Attempt, TargetCommandID: receipt.TargetCommandID,
		AcceptedAt: receipt.AcceptedAt, StartedAt: receipt.StartedAt, FinishedAt: receipt.FinishedAt,
		ErrorCode: receipt.ErrorCode,
	}
}

func commandReceiptForProto(receipt sessionexec.Receipt) *ipcpb.CommandReceipt {
	result := &ipcpb.CommandReceipt{
		SessionId: receipt.SessionID, RunId: receipt.RunID, TaskId: receipt.TaskID,
		CommandId: receipt.CommandID, TurnId: receipt.TurnID, Generation: int64(receipt.Generation),
		Sequence: receipt.Sequence, Lane: string(receipt.Lane), State: string(receipt.State),
		Duplicate: receipt.Duplicate, Attempt: int64(receipt.Attempt), TargetCommandId: receipt.TargetCommandID,
		AcceptedAt: timestamppb.New(receipt.AcceptedAt), ErrorCode: receipt.ErrorCode,
	}
	if receipt.StartedAt != nil {
		result.StartedAt = timestamppb.New(*receipt.StartedAt)
	}
	if receipt.FinishedAt != nil {
		result.FinishedAt = timestamppb.New(*receipt.FinishedAt)
	}
	return result
}

func commandAcceptanceHTTPError(err error) (int, error) {
	switch {
	case stdliberrors.Is(err, sessionexec.ErrValidation):
		return http.StatusBadRequest, fmt.Errorf("invalid command")
	case stdliberrors.Is(err, sessionexec.ErrNotFound):
		return http.StatusNotFound, fmt.Errorf("session not found")
	case stdliberrors.Is(err, sessionexec.ErrIdempotencyConflict):
		return http.StatusConflict, fmt.Errorf("command id conflict")
	case stdliberrors.Is(err, sessionexec.ErrSessionQuiesced):
		return http.StatusConflict, fmt.Errorf("session execution unavailable")
	case stdliberrors.Is(err, sessionexec.ErrCancellationLimit):
		return http.StatusTooManyRequests, fmt.Errorf("command cancellation limit reached")
	case stdliberrors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, fmt.Errorf("command acceptance timed out")
	case stdliberrors.Is(err, context.Canceled):
		return http.StatusRequestTimeout, fmt.Errorf("command acceptance cancelled")
	case stdliberrors.Is(err, storage.ErrStoreClosed), storage.IsSQLiteBusyError(err):
		return http.StatusServiceUnavailable, fmt.Errorf("command acceptance unavailable")
	default:
		return http.StatusInternalServerError, fmt.Errorf("command acceptance failed")
	}
}

func commandAcceptanceConnectError(err error) error {
	code := connect.CodeInternal
	message := "command acceptance failed"
	switch {
	case stdliberrors.Is(err, sessionexec.ErrValidation):
		code, message = connect.CodeInvalidArgument, "invalid command"
	case stdliberrors.Is(err, sessionexec.ErrNotFound):
		code, message = connect.CodeNotFound, "session not found"
	case stdliberrors.Is(err, sessionexec.ErrIdempotencyConflict):
		code, message = connect.CodeAlreadyExists, "command id conflict"
	case stdliberrors.Is(err, sessionexec.ErrSessionQuiesced):
		code, message = connect.CodeFailedPrecondition, "session execution unavailable"
	case stdliberrors.Is(err, sessionexec.ErrCancellationLimit):
		code, message = connect.CodeResourceExhausted, "command cancellation limit reached"
	case stdliberrors.Is(err, context.DeadlineExceeded):
		code, message = connect.CodeDeadlineExceeded, "command acceptance timed out"
	case stdliberrors.Is(err, context.Canceled):
		code, message = connect.CodeCanceled, "command acceptance cancelled"
	case stdliberrors.Is(err, storage.ErrStoreClosed), storage.IsSQLiteBusyError(err):
		code, message = connect.CodeUnavailable, "command acceptance unavailable"
	}
	return connect.NewError(code, fmt.Errorf("%s", message))
}

func commandTargetAvailable(s *Server) bool {
	if s == nil {
		return false
	}
	if registry := s.getHeadlessRegistry(); registry != nil {
		if acceptor, ok := registry.(receiptCommandAcceptor); ok && !isTypedNilInterface(acceptor) {
			return true
		}
	}
	return s.commandGW != nil
}
