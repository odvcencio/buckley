package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/session"
	"github.com/odvcencio/buckley/pkg/storage"
	"github.com/odvcencio/fluffyui/progress"
	"github.com/odvcencio/fluffyui/toast"
)

func loadOrCreateProjectSessions(
	cfg ControllerConfig,
	ctrlCtx context.Context,
	workDir string,
	progressMgr *progress.ProgressManager,
	toastMgr *toast.ToastManager,
) ([]*SessionState, int, error) {
	// Collect all active sessions for this project and load their messages.
	var projectSessions []*SessionState
	allSessions, err := cfg.Store.ListSessions(100)
	if err != nil {
		return nil, 0, fmt.Errorf("list sessions: %w", err)
	}
	for _, s := range allSessions {
		if s.ProjectPath == workDir && s.Status == storage.SessionStatusActive {
			sess, err := newSessionState(ctrlCtx, cfg.Config, cfg.Store, workDir, cfg.Telemetry, cfg.ModelManager, s.ID, true, progressMgr, toastMgr)
			if err != nil {
				return nil, 0, err
			}
			projectSessions = append(projectSessions, sess)
		}
	}

	// Get or create session.
	sessionID := cfg.SessionID
	currentIdx := 0
	if sessionID == "" {
		if len(projectSessions) == 0 {
			baseID := session.DetermineSessionID(workDir)
			timestamp := time.Now().Format("0102-150405") // MMDD-HHMMSS
			sessionID = fmt.Sprintf("%s-%s", baseID, timestamp)

			now := time.Now()
			sess := &storage.Session{
				ID:          sessionID,
				ProjectPath: workDir,
				CreatedAt:   now,
				LastActive:  now,
				Status:      storage.SessionStatusActive,
			}
			if err := cfg.Store.CreateSession(sess); err != nil {
				return nil, 0, fmt.Errorf("create session: %w", err)
			}
			sessState, err := newSessionState(ctrlCtx, cfg.Config, cfg.Store, workDir, cfg.Telemetry, cfg.ModelManager, sessionID, false, progressMgr, toastMgr)
			if err != nil {
				return nil, 0, err
			}
			projectSessions = []*SessionState{sessState}
		}
	} else {
		// Find index of specified session.
		found := false
		for i, s := range projectSessions {
			if s.ID == sessionID {
				currentIdx = i
				found = true
				break
			}
		}
		if !found {
			now := time.Now()
			existing, err := cfg.Store.GetSession(sessionID)
			if err != nil {
				return nil, 0, fmt.Errorf("load session %s: %w", sessionID, err)
			}
			if existing == nil {
				sess := &storage.Session{
					ID:          sessionID,
					ProjectPath: workDir,
					CreatedAt:   now,
					LastActive:  now,
					Status:      storage.SessionStatusActive,
				}
				if err := cfg.Store.CreateSession(sess); err != nil {
					return nil, 0, fmt.Errorf("create session: %w", err)
				}
			} else {
				if strings.TrimSpace(existing.ProjectPath) != workDir {
					if err := cfg.Store.UpdateSessionProjectPath(sessionID, workDir); err != nil {
						return nil, 0, fmt.Errorf("update session project path: %w", err)
					}
				}
				if existing.Status != storage.SessionStatusActive {
					if err := cfg.Store.SetSessionStatus(sessionID, storage.SessionStatusActive); err != nil {
						return nil, 0, fmt.Errorf("activate session %s: %w", sessionID, err)
					}
				}
			}

			sessState, err := newSessionState(ctrlCtx, cfg.Config, cfg.Store, workDir, cfg.Telemetry, cfg.ModelManager, sessionID, true, progressMgr, toastMgr)
			if err != nil {
				return nil, 0, err
			}
			projectSessions = append([]*SessionState{sessState}, projectSessions...)
			currentIdx = 0
		}
	}

	return projectSessions, currentIdx, nil
}
