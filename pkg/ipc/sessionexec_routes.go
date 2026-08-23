package ipc

import (
	"context"
	"encoding/json"
	stdliberrors "errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"m31labs.dev/buckley/pkg/agentcoord"
	"m31labs.dev/buckley/pkg/runledger"
	"m31labs.dev/buckley/pkg/sessionexec"
	"m31labs.dev/buckley/pkg/storage"
)

const (
	observationRecentCommandLimit = 25
	observationMaxResponseBytes   = 1 << 20
	observationMaxCommandStates   = 7
	observationMaxQueryTokenBytes = 4 << 10
)

var (
	errObservationRequest     = stdliberrors.New("invalid observation request")
	errObservationNotFound    = stdliberrors.New("observation not found")
	errObservationConflict    = stdliberrors.New("observation conflict")
	errObservationUnavailable = stdliberrors.New("observation unavailable")
)

type sessionExecutionMonitorReader = sessionexec.MonitorReader
type routineMonitorReader = agentcoord.MonitorReader

type executionObservationResponse struct {
	Execution sessionexec.ExecutionSnapshot `json:"execution"`
}

type commandObservationsResponse struct {
	Commands []sessionexec.CommandStatus `json:"commands"`
	Next     int64                       `json:"next,omitempty"`
	HasMore  bool                        `json:"hasMore"`
}

type commandObservationResponse struct {
	Command sessionexec.CommandStatus `json:"command"`
}

type routineObservationsResponse struct {
	Routines []agentcoord.RoutineStatus `json:"routines"`
	Next     string                     `json:"next,omitempty"`
	HasMore  bool                       `json:"hasMore"`
}

type routineObservationResponse struct {
	Routine agentcoord.RoutineStatus `json:"routine"`
}

type mailboxObservationsResponse struct {
	Messages []agentcoord.MailboxStatus `json:"messages"`
	Next     int64                      `json:"next,omitempty"`
	HasMore  bool                       `json:"hasMore"`
}

// SetObservationReaders atomically publishes custom observation readers.
// A nil reader resets that capability to its canonical dynamic default: the
// main storage adapter for execution, and the configured durable ledger for
// routines.
func (s *Server) SetObservationReaders(execution sessionexec.MonitorReader, routines agentcoord.MonitorReader) error {
	if s == nil {
		return fmt.Errorf("ipc server unavailable")
	}
	if isTypedNilInterface(execution) {
		return fmt.Errorf("session execution monitor is typed nil")
	}
	if isTypedNilInterface(routines) {
		return fmt.Errorf("routine monitor is typed nil")
	}
	s.observationMu.Lock()
	s.executionMonitor = execution
	s.routineMonitor = routines
	s.observationMu.Unlock()
	return nil
}

func (s *Server) executionObservationReader() sessionexec.MonitorReader {
	if s == nil {
		return nil
	}
	s.observationMu.RLock()
	reader := s.executionMonitor
	s.observationMu.RUnlock()
	if reader != nil {
		if isTypedNilInterface(reader) {
			return nil
		}
		return reader
	}
	if s.store == nil {
		return nil
	}
	return s.store
}

func (s *Server) routineObservationReader() agentcoord.MonitorReader {
	if s == nil {
		return nil
	}
	s.observationMu.RLock()
	reader := s.routineMonitor
	s.observationMu.RUnlock()
	if reader != nil {
		if isTypedNilInterface(reader) {
			return nil
		}
		return reader
	}
	s.headlessMu.RLock()
	ledger := s.durableLedger
	s.headlessMu.RUnlock()
	reader, ok := ledger.(agentcoord.MonitorReader)
	if !ok || isTypedNilInterface(reader) {
		return nil
	}
	return reader
}

func (s *Server) setupSessionExecRoutes(api chi.Router) {
	api.Handle("/sessions/{sessionID}/execution", observationGETHandler(s.handleExecutionObservation, http.MethodGet))
	api.Handle("/sessions/{sessionID}/commands", observationGETHandler(s.handleCommandObservations, http.MethodGet+", "+http.MethodPost))
	api.Handle("/sessions/{sessionID}/commands/{commandID}", observationGETHandler(s.handleCommandObservation, http.MethodGet))
	api.Handle("/sessions/{sessionID}/routines", observationGETHandler(s.handleRoutineObservations, http.MethodGet))
	api.Handle("/sessions/{sessionID}/routines/{runID}", observationGETHandler(s.handleRoutineObservation, http.MethodGet))
	api.Handle("/sessions/{sessionID}/routines/{runID}/mailbox", observationGETHandler(s.handleMailboxObservations, http.MethodGet))
}

func observationGETHandler(handler http.HandlerFunc, allow string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", allow)
			respondError(w, http.StatusMethodNotAllowed, stdliberrors.New("method not allowed"))
			return
		}
		handler.ServeHTTP(w, r)
	})
}

func (s *Server) handleExecutionObservation(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	sessionID := chi.URLParam(r, "sessionID")
	if !s.authorizeObservationSession(w, r, sessionID) {
		return
	}
	if _, err := s.parseObservationRawQuery(r, nil); err != nil {
		writeObservationError(w, err)
		return
	}
	reader := s.executionObservationReader()
	if reader == nil {
		writeObservationError(w, errObservationUnavailable)
		return
	}
	snapshot, err := reader.GetExecutionSnapshot(r.Context(), sessionID, observationRecentCommandLimit)
	if err != nil {
		writeObservationError(w, err)
		return
	}
	if err := validateExecutionObservation(snapshot, sessionID, observationRecentCommandLimit); err != nil {
		writeObservationError(w, err)
		return
	}
	writeObservationJSON(w, executionObservationResponse{Execution: snapshot})
}

func (s *Server) handleCommandObservations(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	sessionID := chi.URLParam(r, "sessionID")
	if !s.authorizeObservationSession(w, r, sessionID) {
		return
	}
	values, err := s.parseObservationRawQuery(r, map[string]struct{}{
		"afterSequence": {}, "limit": {}, "state": {},
	})
	if err != nil {
		writeObservationError(w, err)
		return
	}
	query, err := parseCommandObservationQuery(values, sessionID)
	if err != nil {
		writeObservationError(w, err)
		return
	}
	reader := s.executionObservationReader()
	if reader == nil {
		writeObservationError(w, errObservationUnavailable)
		return
	}
	page, err := reader.ListCommandStatuses(r.Context(), query)
	if err != nil {
		writeObservationError(w, err)
		return
	}
	if err := validateCommandObservationPage(page, query); err != nil {
		writeObservationError(w, err)
		return
	}
	writeObservationJSON(w, commandObservationsResponse{
		Commands: page.Commands,
		Next:     page.Next,
		HasMore:  page.HasMore,
	})
}

func (s *Server) handleCommandObservation(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	sessionID := chi.URLParam(r, "sessionID")
	if !s.authorizeObservationSession(w, r, sessionID) {
		return
	}
	if _, err := s.parseObservationRawQuery(r, nil); err != nil {
		writeObservationError(w, err)
		return
	}
	commandID := chi.URLParam(r, "commandID")
	if err := sessionexec.ValidateCommandID(commandID); err != nil {
		writeObservationError(w, err)
		return
	}
	reader := s.executionObservationReader()
	if reader == nil {
		writeObservationError(w, errObservationUnavailable)
		return
	}
	status, err := reader.GetCommandStatus(r.Context(), sessionID, commandID)
	if err != nil {
		writeObservationError(w, err)
		return
	}
	if err := validateCommandObservation(status, sessionID, commandID); err != nil {
		writeObservationError(w, err)
		return
	}
	writeObservationJSON(w, commandObservationResponse{Command: status})
}

func (s *Server) handleRoutineObservations(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	sessionID := chi.URLParam(r, "sessionID")
	if !s.authorizeObservationSession(w, r, sessionID) {
		return
	}
	values, err := s.parseObservationRawQuery(r, map[string]struct{}{
		"cursor": {}, "limit": {}, "parentRunId": {},
	})
	if err != nil {
		writeObservationError(w, err)
		return
	}
	query, err := parseRoutineObservationQuery(values, sessionID)
	if err != nil {
		writeObservationError(w, err)
		return
	}
	reader := s.routineObservationReader()
	if reader == nil {
		writeObservationError(w, errObservationUnavailable)
		return
	}
	page, err := reader.ListRoutineStatuses(r.Context(), query)
	if err != nil {
		writeObservationError(w, err)
		return
	}
	if err := validateRoutineObservationPage(page, query); err != nil {
		writeObservationError(w, err)
		return
	}
	writeObservationJSON(w, routineObservationsResponse{
		Routines: page.Routines,
		Next:     page.Next,
		HasMore:  page.HasMore,
	})
}

func (s *Server) handleRoutineObservation(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	sessionID := chi.URLParam(r, "sessionID")
	if !s.authorizeObservationSession(w, r, sessionID) {
		return
	}
	if _, err := s.parseObservationRawQuery(r, nil); err != nil {
		writeObservationError(w, err)
		return
	}
	runID := chi.URLParam(r, "runID")
	if err := agentcoord.ValidateMonitorIdentity(sessionID, runID); err != nil {
		writeObservationError(w, err)
		return
	}
	reader := s.routineObservationReader()
	if reader == nil {
		writeObservationError(w, errObservationUnavailable)
		return
	}
	status, err := reader.GetRoutineStatus(r.Context(), sessionID, runID)
	if err != nil {
		writeObservationError(w, err)
		return
	}
	if status.SessionID != sessionID || status.RunID != runID {
		writeObservationError(w, errObservationConflict)
		return
	}
	if err := validateRoutineObservation(status); err != nil {
		writeObservationError(w, errObservationConflict)
		return
	}
	writeObservationJSON(w, routineObservationResponse{Routine: status})
}

func (s *Server) handleMailboxObservations(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	sessionID := chi.URLParam(r, "sessionID")
	if !s.authorizeObservationSession(w, r, sessionID) {
		return
	}
	runID := chi.URLParam(r, "runID")
	if err := agentcoord.ValidateMonitorIdentity(sessionID, runID); err != nil {
		writeObservationError(w, err)
		return
	}
	values, err := s.parseObservationRawQuery(r, map[string]struct{}{
		"afterSequence": {}, "limit": {}, "state": {},
	})
	if err != nil {
		writeObservationError(w, err)
		return
	}
	query, err := parseMailboxObservationQuery(values, sessionID, runID)
	if err != nil {
		writeObservationError(w, err)
		return
	}
	reader := s.routineObservationReader()
	if reader == nil {
		writeObservationError(w, errObservationUnavailable)
		return
	}
	page, err := reader.ListMailboxStatuses(r.Context(), query)
	if err != nil {
		writeObservationError(w, err)
		return
	}
	if err := validateMailboxObservationPage(page, query); err != nil {
		writeObservationError(w, err)
		return
	}
	writeObservationJSON(w, mailboxObservationsResponse{
		Messages: page.Messages,
		Next:     page.Next,
		HasMore:  page.HasMore,
	})
}

func (s *Server) parseObservationRawQuery(r *http.Request, allowed map[string]struct{}) (url.Values, error) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, errObservationRequest
	}
	for key, raw := range values {
		if key == "token" {
			if !isLoopbackBindAddress(s.cfg.BindAddress) || len(raw) != 1 ||
				!observationSafeText(raw[0], observationMaxQueryTokenBytes, true) {
				return nil, errObservationRequest
			}
			continue
		}
		if _, ok := allowed[key]; !ok {
			return nil, errObservationRequest
		}
	}
	return values, nil
}

func (s *Server) authorizeObservationSession(w http.ResponseWriter, r *http.Request, sessionID string) bool {
	principal, ok := requireScope(w, r, storage.TokenScopeViewer)
	if !ok {
		return false
	}
	if err := sessionexec.ValidateSessionID(sessionID); err != nil {
		writeObservationError(w, err)
		return false
	}
	if s == nil || s.store == nil {
		writeObservationError(w, errObservationUnavailable)
		return false
	}
	session, err := s.store.GetSession(sessionID)
	if err != nil {
		writeObservationError(w, err)
		return false
	}
	if session == nil || !principalCanAccessSession(principal, session) {
		writeObservationError(w, errObservationNotFound)
		return false
	}
	return true
}

func parseCommandObservationQuery(values url.Values, sessionID string) (sessionexec.CommandStatusQuery, error) {
	after, err := observationInt64(values, "afterSequence")
	if err != nil {
		return sessionexec.CommandStatusQuery{}, err
	}
	limit, err := observationInt(values, "limit")
	if err != nil {
		return sessionexec.CommandStatusQuery{}, err
	}
	rawStates := values["state"]
	if len(rawStates) > observationMaxCommandStates {
		return sessionexec.CommandStatusQuery{}, errObservationRequest
	}
	states := make([]sessionexec.State, len(rawStates))
	for i, raw := range rawStates {
		state := sessionexec.State(raw)
		if !state.Valid() || raw != strings.ToLower(raw) {
			return sessionexec.CommandStatusQuery{}, errObservationRequest
		}
		states[i] = state
	}
	query := sessionexec.CommandStatusQuery{
		SessionID: sessionID, States: states, AfterSequence: after, Limit: limit,
	}
	query, err = sessionexec.NormalizeCommandStatusQuery(query)
	if err != nil {
		return sessionexec.CommandStatusQuery{}, err
	}
	return query, nil
}

func parseRoutineObservationQuery(values url.Values, sessionID string) (agentcoord.RoutineQuery, error) {
	cursor, present, err := observationScalar(values, "cursor")
	if err != nil {
		return agentcoord.RoutineQuery{}, err
	}
	if present && cursor == "" {
		return agentcoord.RoutineQuery{}, errObservationRequest
	}
	parentRunID, parentPresent, err := observationScalar(values, "parentRunId")
	if err != nil {
		return agentcoord.RoutineQuery{}, err
	}
	if parentPresent && parentRunID == "" {
		return agentcoord.RoutineQuery{}, errObservationRequest
	}
	limit, err := observationInt(values, "limit")
	if err != nil {
		return agentcoord.RoutineQuery{}, err
	}
	query := agentcoord.RoutineQuery{
		SessionID: sessionID, ParentRunID: parentRunID, Before: cursor, Limit: limit,
	}
	query, err = agentcoord.NormalizeRoutineQuery(query)
	if err != nil {
		return agentcoord.RoutineQuery{}, err
	}
	return query, nil
}

func parseMailboxObservationQuery(values url.Values, sessionID, runID string) (agentcoord.MailboxStatusQuery, error) {
	after, err := observationInt64(values, "afterSequence")
	if err != nil {
		return agentcoord.MailboxStatusQuery{}, err
	}
	limit, err := observationInt(values, "limit")
	if err != nil {
		return agentcoord.MailboxStatusQuery{}, err
	}
	rawStates := values["state"]
	if len(rawStates) > agentcoord.MaxMailboxStatusStates {
		return agentcoord.MailboxStatusQuery{}, errObservationRequest
	}
	states := make([]agentcoord.MailboxState, len(rawStates))
	for i, raw := range rawStates {
		state := agentcoord.MailboxState(raw)
		if !state.Valid() || raw != strings.ToLower(raw) {
			return agentcoord.MailboxStatusQuery{}, errObservationRequest
		}
		states[i] = state
	}
	query := agentcoord.MailboxStatusQuery{
		SessionID: sessionID, RunID: runID, States: states, AfterSequence: after, Limit: limit,
	}
	query, err = agentcoord.NormalizeMailboxStatusQuery(query)
	if err != nil {
		return agentcoord.MailboxStatusQuery{}, err
	}
	return query, nil
}

func observationScalar(values url.Values, name string) (string, bool, error) {
	raw, ok := values[name]
	if !ok {
		return "", false, nil
	}
	if len(raw) != 1 {
		return "", false, errObservationRequest
	}
	return raw[0], true, nil
}

func observationInt(values url.Values, name string) (int, error) {
	value, present, err := observationScalar(values, name)
	if err != nil || !present {
		return 0, err
	}
	parsed, err := parseCanonicalObservationInt64(value)
	if err != nil || parsed > int64(^uint(0)>>1) {
		return 0, errObservationRequest
	}
	return int(parsed), nil
}

func observationInt64(values url.Values, name string) (int64, error) {
	value, present, err := observationScalar(values, name)
	if err != nil || !present {
		return 0, err
	}
	return parseCanonicalObservationInt64(value)
}

func parseCanonicalObservationInt64(value string) (int64, error) {
	if value == "" {
		return 0, errObservationRequest
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < 0 || strconv.FormatInt(parsed, 10) != value {
		return 0, errObservationRequest
	}
	return parsed, nil
}

func validateExecutionObservation(snapshot sessionexec.ExecutionSnapshot, sessionID string, recentLimit int) error {
	if snapshot.SessionID != sessionID || snapshot.Summary.SessionID != sessionID || !observationSafeTime(snapshot.ObservedAt) ||
		len(snapshot.RecentCommands) > recentLimit || len(snapshot.AttentionEffects) > sessionexec.MaxAttentionEffects {
		return errObservationConflict
	}
	if snapshot.Initialized {
		state := snapshot.ExecutionState
		if state.SessionID != sessionID || !observationSafeTime(state.UpdatedAt) || state.Generation < 0 ||
			state.Generation > sessionexec.MaxCommandSequence || sessionexec.ValidateExecutionMode(state.Mode, true) != nil {
			return errObservationConflict
		}
		switch state.Mode {
		case sessionexec.ExecutionModeHeadless:
			if state.Generation != 0 || state.ReasonCode != "" {
				return errObservationConflict
			}
		case sessionexec.ExecutionModeDetached, sessionexec.ExecutionModeAdopted:
			if state.Generation < 1 || state.ReasonCode == "" || sessionexec.ValidateErrorCode(state.ReasonCode) != nil {
				return errObservationConflict
			}
		}
	} else {
		if snapshot.ExecutionState != (sessionexec.ExecutionState{}) ||
			snapshot.Summary != (sessionexec.Summary{SessionID: sessionID}) ||
			snapshot.EffectSummary != (sessionexec.EffectSummary{}) || len(snapshot.AttentionEffects) != 0 ||
			snapshot.AttentionEffectsTruncated || len(snapshot.RecentCommands) != 0 {
			return errObservationConflict
		}
		return nil
	}
	if err := validateCommandSummary(snapshot.Summary); err != nil {
		return err
	}
	if err := validateEffectSummary(snapshot.EffectSummary); err != nil {
		return err
	}
	blockingTotal, ok := observationCheckedCountSum(sessionexec.MaxEffectPermitsPerSession,
		snapshot.EffectSummary.Active, snapshot.EffectSummary.Ambiguous)
	if !ok {
		return errObservationConflict
	}
	blockingEffects := int(blockingTotal)
	expectedAttention := blockingEffects
	if expectedAttention > sessionexec.MaxAttentionEffects {
		expectedAttention = sessionexec.MaxAttentionEffects
	}
	if len(snapshot.AttentionEffects) != expectedAttention ||
		snapshot.AttentionEffectsTruncated != (blockingEffects > sessionexec.MaxAttentionEffects) {
		return errObservationConflict
	}
	totalEffects := len(snapshot.AttentionEffects)
	attentionSeen := make(map[string]struct{}, len(snapshot.AttentionEffects))
	var attentionActive, attentionAmbiguous int
	for _, effect := range snapshot.AttentionEffects {
		if effect.SessionID != sessionID || (effect.State != sessionexec.EffectStateActive && effect.State != sessionexec.EffectStateAmbiguous) ||
			validateEffectObservation(effect) != nil {
			return errObservationConflict
		}
		key := observationEffectKey(effect)
		if _, exists := attentionSeen[key]; exists {
			return errObservationConflict
		}
		attentionSeen[key] = struct{}{}
		if effect.State == sessionexec.EffectStateActive {
			attentionActive++
		} else {
			attentionAmbiguous++
		}
	}
	if snapshot.AttentionEffectsTruncated {
		if attentionActive > snapshot.EffectSummary.Active || attentionAmbiguous > snapshot.EffectSummary.Ambiguous ||
			blockingEffects-len(snapshot.AttentionEffects) < 1 {
			return errObservationConflict
		}
	} else if attentionActive != snapshot.EffectSummary.Active || attentionAmbiguous != snapshot.EffectSummary.Ambiguous {
		return errObservationConflict
	}
	var previousSequence int64
	for _, command := range snapshot.RecentCommands {
		if err := validateCommandObservation(command, sessionID, ""); err != nil {
			return err
		}
		if command.Sequence <= previousSequence || command.Sequence > snapshot.Summary.LastSequence {
			return errObservationConflict
		}
		previousSequence = command.Sequence
		totalEffects += len(command.Effects)
	}
	if totalEffects > sessionexec.MaxEffectPermitsPerSession+sessionexec.MaxAttentionEffects {
		return errObservationConflict
	}
	return nil
}

func validateCommandObservationPage(page sessionexec.CommandStatusPage, query sessionexec.CommandStatusQuery) error {
	if len(page.Commands) > query.Limit || page.Next < query.AfterSequence || page.Next > sessionexec.MaxCommandSequence {
		return errObservationConflict
	}
	allowed := make(map[sessionexec.State]struct{}, len(query.States))
	for _, state := range query.States {
		allowed[state] = struct{}{}
	}
	previous := query.AfterSequence
	totalEffects := 0
	for _, command := range page.Commands {
		if err := validateCommandObservation(command, query.SessionID, ""); err != nil {
			return err
		}
		if command.Sequence <= previous {
			return errObservationConflict
		}
		if len(allowed) > 0 {
			if _, ok := allowed[command.State]; !ok {
				return errObservationConflict
			}
		}
		previous = command.Sequence
		totalEffects += len(command.Effects)
	}
	if totalEffects > sessionexec.MaxEffectPermitsPerSession {
		return errObservationConflict
	}
	if len(page.Commands) == 0 {
		if page.Next != query.AfterSequence || page.HasMore {
			return errObservationConflict
		}
	} else if page.Next != previous {
		return errObservationConflict
	}
	if page.HasMore && len(page.Commands) != query.Limit {
		return errObservationConflict
	}
	return nil
}

func validateCommandObservation(status sessionexec.CommandStatus, sessionID, commandID string) error {
	if status.SessionID != sessionID || (commandID != "" && status.CommandID != commandID) ||
		sessionexec.ValidateSessionID(status.SessionID) != nil || sessionexec.ValidateCommandID(status.CommandID) != nil ||
		status.RunID != sessionexec.RunIDForSession(status.SessionID) || status.TaskID != sessionexec.ForegroundTaskID ||
		status.Generation < 0 || status.Generation > sessionexec.MaxCommandAttempts ||
		status.TurnID != sessionexec.TurnID(status.CommandID, status.Generation) ||
		status.Sequence < 1 || status.Sequence > sessionexec.MaxCommandSequence || !observationSafeTime(status.AcceptedAt) ||
		!status.State.Valid() || status.Attempt < 0 || status.Attempt > sessionexec.MaxCommandAttempts ||
		len(status.Effects) > sessionexec.MaxCommandStatusEffects {
		return errObservationConflict
	}
	if status.TargetCommandID != "" && sessionexec.ValidateCommandID(status.TargetCommandID) != nil {
		return errObservationConflict
	}
	lane, err := sessionexec.LaneFor(status.Type)
	if err != nil || lane != status.Lane || status.Type != strings.ToLower(strings.TrimSpace(status.Type)) ||
		sessionexec.ValidateErrorCode(status.ErrorCode) != nil {
		return errObservationConflict
	}
	if status.TargetCommandID != "" && status.Type != "steer" && status.Type != "interrupt" {
		return errObservationConflict
	}
	if (status.Attempt == 0) != (status.StartedAt == nil) {
		return errObservationConflict
	}
	if !observationSafeTimePtr(status.StartedAt) || !observationSafeTimePtr(status.FinishedAt) {
		return errObservationConflict
	}
	if status.StartedAt != nil && status.StartedAt.Before(status.AcceptedAt) {
		return errObservationConflict
	}
	if status.FinishedAt != nil && (status.FinishedAt.Before(status.AcceptedAt) ||
		(status.StartedAt != nil && status.FinishedAt.Before(*status.StartedAt))) {
		return errObservationConflict
	}
	switch status.State {
	case sessionexec.StateAccepted:
		if status.FinishedAt != nil || status.ErrorCode != "" {
			return errObservationConflict
		}
	case sessionexec.StateRunning:
		if status.Attempt < 1 || status.StartedAt == nil || status.FinishedAt != nil || status.ErrorCode != "" {
			return errObservationConflict
		}
	default:
		if status.FinishedAt == nil || (status.State != sessionexec.StateCancelled && status.Attempt < 1) {
			return errObservationConflict
		}
	}
	if err := validateEffectSummary(status.EffectSummary); err != nil {
		return errObservationConflict
	}
	expectedEffects := status.EffectSummary.Total
	if expectedEffects > sessionexec.MaxCommandStatusEffects {
		expectedEffects = sessionexec.MaxCommandStatusEffects
	}
	if len(status.Effects) != expectedEffects ||
		status.EffectsTruncated != (status.EffectSummary.Total > sessionexec.MaxCommandStatusEffects) {
		return errObservationConflict
	}
	effectSeen := make(map[string]struct{}, len(status.Effects))
	var projected sessionexec.EffectSummary
	for _, effect := range status.Effects {
		if effect.SessionID != status.SessionID || effect.CommandID != status.CommandID ||
			effect.CommandGeneration != status.Generation || validateEffectObservation(effect) != nil {
			return errObservationConflict
		}
		key := observationEffectKey(effect)
		if _, exists := effectSeen[key]; exists {
			return errObservationConflict
		}
		effectSeen[key] = struct{}{}
		projected.Total++
		switch effect.State {
		case sessionexec.EffectStateActive:
			projected.Active++
		case sessionexec.EffectStateAmbiguous:
			projected.Ambiguous++
		case sessionexec.EffectStateEnded:
			projected.Ended++
		case sessionexec.EffectStateResolved:
			projected.Resolved++
		}
	}
	if status.EffectsTruncated {
		if projected.Total > status.EffectSummary.Total || projected.Active > status.EffectSummary.Active ||
			projected.Ambiguous > status.EffectSummary.Ambiguous || projected.Ended > status.EffectSummary.Ended ||
			projected.Resolved > status.EffectSummary.Resolved {
			return errObservationConflict
		}
	} else if projected != status.EffectSummary {
		return errObservationConflict
	}
	return nil
}

func validateEffectObservation(effect sessionexec.EffectStatus) error {
	if sessionexec.ValidateSessionID(effect.SessionID) != nil || sessionexec.ValidateCommandID(effect.CommandID) != nil ||
		effect.CommandGeneration < 0 || !observationSafeIdentifier(effect.EffectID, sessionexec.MaxEffectIDBytes) ||
		!observationSafeTime(effect.CreatedAt) || !observationSafeTime(effect.ExpiresAt) ||
		!observationSafeTimePtr(effect.AmbiguousAt) || !observationSafeTimePtr(effect.EndedAt) ||
		!observationSafeTimePtr(effect.ResolvedAt) || effect.ExpiresAt.Before(effect.CreatedAt) {
		return errObservationConflict
	}
	if effect.AmbiguousAt != nil && effect.AmbiguousAt.Before(effect.CreatedAt) ||
		effect.EndedAt != nil && effect.EndedAt.Before(effect.CreatedAt) ||
		effect.ResolvedAt != nil && effect.ResolvedAt.Before(effect.ExpiresAt) {
		return errObservationConflict
	}
	switch effect.Kind {
	case sessionexec.EffectKindModel, sessionexec.EffectKindTool:
	default:
		return errObservationConflict
	}
	switch effect.State {
	case sessionexec.EffectStateActive:
		if effect.AmbiguousAt != nil || effect.EndedAt != nil || effect.ResolvedAt != nil {
			return errObservationConflict
		}
	case sessionexec.EffectStateAmbiguous:
		if effect.AmbiguousAt == nil || effect.EndedAt != nil || effect.ResolvedAt != nil {
			return errObservationConflict
		}
	case sessionexec.EffectStateEnded:
		if effect.EndedAt == nil || effect.ResolvedAt != nil ||
			(effect.AmbiguousAt != nil && effect.EndedAt.Before(*effect.AmbiguousAt)) {
			return errObservationConflict
		}
	case sessionexec.EffectStateResolved:
		if effect.AmbiguousAt == nil || effect.EndedAt != nil || effect.ResolvedAt == nil ||
			effect.ResolvedAt.Before(*effect.AmbiguousAt) {
			return errObservationConflict
		}
	default:
		return errObservationConflict
	}
	return nil
}

func validateCommandSummary(summary sessionexec.Summary) error {
	total, ok := observationCheckedCountSum(sessionexec.MaxCommandSequence, summary.Total)
	if !ok {
		return errObservationConflict
	}
	states, ok := observationCheckedCountSum(sessionexec.MaxCommandSequence,
		summary.Accepted, summary.Running, summary.Succeeded, summary.Failed,
		summary.Blocked, summary.Interrupted, summary.Cancelled)
	if !ok {
		return errObservationConflict
	}
	pending, ok := observationCheckedCountSum(sessionexec.MaxCommandSequence,
		summary.WorkPending, summary.ControlPending)
	if !ok {
		return errObservationConflict
	}
	if states != total || summary.LastSequence != total ||
		summary.LastSequence < 0 || summary.LastSequence > sessionexec.MaxCommandSequence ||
		pending != int64(summary.Accepted) {
		return errObservationConflict
	}
	return nil
}

func validateEffectSummary(summary sessionexec.EffectSummary) error {
	total, ok := observationCheckedCountSum(sessionexec.MaxEffectPermitsPerSession, summary.Total)
	if !ok {
		return errObservationConflict
	}
	states, ok := observationCheckedCountSum(sessionexec.MaxEffectPermitsPerSession,
		summary.Active, summary.Ambiguous, summary.Ended, summary.Resolved)
	if !ok || total != states {
		return errObservationConflict
	}
	return nil
}

func observationCheckedCountSum(max int64, values ...int) (int64, bool) {
	if max < 0 {
		return 0, false
	}
	var total int64
	for _, value := range values {
		if value < 0 {
			return 0, false
		}
		component := int64(value)
		if component > max || total > max-component {
			return 0, false
		}
		total += component
	}
	return total, true
}

func validateRoutineObservationPage(page agentcoord.RoutineStatusPage, query agentcoord.RoutineQuery) error {
	if len(page.Routines) > query.Limit {
		return errObservationConflict
	}
	var previous *agentcoord.RoutineStatus
	seen := make(map[string]struct{}, len(page.Routines))
	for i := range page.Routines {
		status := &page.Routines[i]
		if status.SessionID != query.SessionID || (query.ParentRunID != "" && status.ParentRunID != query.ParentRunID) ||
			!observationRoutineKeyBefore(*status, query.Before) || validateRoutineObservation(*status) != nil {
			return errObservationConflict
		}
		if _, ok := seen[status.RunID]; ok {
			return errObservationConflict
		}
		seen[status.RunID] = struct{}{}
		if previous != nil && (status.StartedAt.After(previous.StartedAt) ||
			(status.StartedAt.Equal(previous.StartedAt) && status.RunID >= previous.RunID)) {
			return errObservationConflict
		}
		previous = status
	}
	if page.HasMore {
		if len(page.Routines) != query.Limit {
			return errObservationConflict
		}
		startedAt, runID, err := agentcoord.DecodeRoutineCursor(page.Next)
		last := page.Routines[len(page.Routines)-1]
		if err != nil || !startedAt.Equal(last.StartedAt) || runID != last.RunID {
			return errObservationConflict
		}
	} else if page.Next != "" {
		return errObservationConflict
	}
	return nil
}

func validateMailboxObservationPage(page agentcoord.MailboxStatusPage, query agentcoord.MailboxStatusQuery) error {
	if len(page.Messages) > query.Limit || page.Next < query.AfterSequence || page.Next > agentcoord.MaxMonitorSequence {
		return errObservationConflict
	}
	allowed := make(map[agentcoord.MailboxState]struct{}, len(query.States))
	for _, state := range query.States {
		allowed[state] = struct{}{}
	}
	previous := query.AfterSequence
	for _, message := range page.Messages {
		if message.SessionID != query.SessionID || message.RunID != query.RunID || message.Sequence <= previous ||
			validateMailboxObservation(message) != nil {
			return errObservationConflict
		}
		if len(allowed) > 0 {
			if _, ok := allowed[message.State]; !ok {
				return errObservationConflict
			}
		}
		previous = message.Sequence
	}
	if len(page.Messages) == 0 {
		if page.Next != query.AfterSequence || page.HasMore {
			return errObservationConflict
		}
	} else if page.Next != previous {
		return errObservationConflict
	}
	if page.HasMore && len(page.Messages) != query.Limit {
		return errObservationConflict
	}
	return nil
}

func observationEffectKey(effect sessionexec.EffectStatus) string {
	return strings.Join([]string{
		effect.CommandID, strconv.Itoa(effect.CommandGeneration), effect.EffectID,
	}, "\x00")
}

func observationRoutineKeyBefore(status agentcoord.RoutineStatus, before string) bool {
	if before == "" {
		return true
	}
	startedAt, runID, err := agentcoord.DecodeRoutineCursor(before)
	if err != nil {
		return false
	}
	return status.StartedAt.Before(startedAt) ||
		(status.StartedAt.Equal(startedAt) && status.RunID < runID)
}

func validateRoutineObservation(status agentcoord.RoutineStatus) error {
	if agentcoord.ValidateRoutineStatus(status) != nil || !observationSafeTime(status.StartedAt) ||
		!observationSafeTimePtr(status.FinishedAt) || !observationSafeTimePtr(status.Attempt.AttachedAt) ||
		!observationSafeTimePtr(status.Attempt.HeartbeatAt) || !observationSafeTimePtr(status.Attempt.LeaseExpiresAt) ||
		!observationSafeTimePtr(status.Attempt.DetachedAt) {
		return errObservationConflict
	}
	return nil
}

func validateMailboxObservation(status agentcoord.MailboxStatus) error {
	if agentcoord.ValidateMailboxStatus(status) != nil || !observationSafeTime(status.CreatedAt) ||
		!observationSafeTimePtr(status.ProcessedAt) || !observationSafeTimePtr(status.DeadLetteredAt) {
		return errObservationConflict
	}
	return nil
}

func observationSafeTime(value time.Time) bool {
	return !value.IsZero() && value.Location() == time.UTC && value.Year() >= 1 && value.Year() <= 9999 &&
		value == value.Round(0)
}

func observationSafeTimePtr(value *time.Time) bool {
	return value == nil || observationSafeTime(*value)
}

func observationSafeIdentifier(value string, maxBytes int) bool {
	return observationSafeText(value, maxBytes, true)
}

func observationSafeText(value string, maxBytes int, required bool) bool {
	if (required && value == "") || len(value) > maxBytes || strings.TrimSpace(value) != value || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func writeObservationJSON(w http.ResponseWriter, payload any) {
	encoded, err := json.Marshal(payload)
	if err != nil || len(encoded)+1 > observationMaxResponseBytes {
		writeObservationError(w, errObservationConflict)
		return
	}
	setJSONResponseHeaders(w)
	_, _ = w.Write(append(encoded, '\n'))
}

func writeObservationError(w http.ResponseWriter, err error) {
	status := observationErrorStatus(err)
	message := "observation failed"
	switch status {
	case http.StatusBadRequest:
		message = "invalid observation request"
	case http.StatusNotFound:
		message = "observation not found"
	case http.StatusConflict:
		message = "observation conflict"
	case http.StatusServiceUnavailable:
		message = "observation unavailable"
	case http.StatusGatewayTimeout:
		message = "observation timed out"
	}
	respondError(w, status, stdliberrors.New(message))
}

func observationErrorStatus(err error) int {
	switch {
	case stdliberrors.Is(err, errObservationRequest), stdliberrors.Is(err, sessionexec.ErrValidation),
		stdliberrors.Is(err, agentcoord.ErrMonitorValidation):
		return http.StatusBadRequest
	case stdliberrors.Is(err, errObservationNotFound), stdliberrors.Is(err, sessionexec.ErrNotFound),
		stdliberrors.Is(err, runledger.ErrNotFound):
		return http.StatusNotFound
	case stdliberrors.Is(err, errObservationConflict), stdliberrors.Is(err, sessionexec.ErrIdempotencyConflict),
		stdliberrors.Is(err, sessionexec.ErrTerminalConflict), stdliberrors.Is(err, sessionexec.ErrTranscriptConflict),
		stdliberrors.Is(err, sessionexec.ErrEffectPermitConflict), stdliberrors.Is(err, agentcoord.ErrMonitorIntegrity),
		stdliberrors.Is(err, agentcoord.ErrMonitorConflict):
		return http.StatusConflict
	case stdliberrors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout
	case stdliberrors.Is(err, errObservationUnavailable), stdliberrors.Is(err, storage.ErrStoreClosed),
		stdliberrors.Is(err, agentcoord.ErrMonitorCapacity), storage.IsSQLiteBusyError(err):
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

var _ sessionexec.MonitorReader = (*storage.Store)(nil)
