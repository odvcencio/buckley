package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"m31labs.dev/buckley/pkg/acp"
	"m31labs.dev/buckley/pkg/conversation"
	"m31labs.dev/buckley/pkg/storage"
)

const acpSessionPrefix = "acp-"

type acpApprovalStoreKey struct{}

func acpWorkspace(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("ACP workspace must be absolute")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("ACP workspace must be a directory")
	}
	return resolved, nil
}

func createACPSession(store *storage.Store, session *acp.AgentSession, defaultDir string) error {
	dir := session.WorkingDirectory
	if dir == "" {
		dir = defaultDir
	}
	workspace, err := acpWorkspace(dir)
	if err != nil {
		return err
	}
	session.ID = acpSessionPrefix + session.ID
	session.WorkingDirectory = workspace
	return store.CreateSession(&storage.Session{
		ID: session.ID, ProjectPath: workspace,
		CreatedAt: session.CreatedAt, LastActive: session.CreatedAt,
	})
}

func loadACPSession(ctx context.Context, store *storage.Store, params acp.LoadSessionParams, stream acp.StreamFunc) (*acp.AgentSession, error) {
	if !strings.HasPrefix(params.SessionID, acpSessionPrefix) {
		return nil, nil
	}
	saved, err := store.GetSession(params.SessionID)
	if err != nil || saved == nil {
		return nil, err
	}
	workspace, err := acpWorkspace(params.Cwd)
	if err != nil || workspace != saved.ProjectPath || saved.Principal != "" {
		return nil, nil
	}
	conv := conversation.New(saved.ID)
	if err := conv.LoadFromStorage(store); err != nil {
		return nil, err
	}
	for _, msg := range conv.Messages {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		text := conversation.GetContentAsString(msg.Content)
		switch msg.Role {
		case "user":
			err = stream(acp.SessionUpdate{SessionUpdate: acp.SessionUpdateUserMessageChunk, Content: acp.NewTextContent(text)})
		case "assistant":
			if text != "" {
				err = stream(acp.NewAgentMessageChunk(text))
			}
			for _, call := range msg.ToolCalls {
				if err != nil {
					break
				}
				err = stream(acp.SessionUpdate{SessionUpdate: acp.SessionUpdateToolCall, ToolCallID: call.ID,
					Title: call.Function.Name, Status: acp.ToolCallStatusPending})
			}
		case "tool":
			status := acp.ToolCallStatusCompleted
			if strings.HasPrefix(text, "Error:") || strings.HasPrefix(text, "Permission denied") || strings.HasPrefix(text, "No confirmed result") {
				status = acp.ToolCallStatusFailed
			}
			err = stream(acp.SessionUpdate{SessionUpdate: acp.SessionUpdateToolCallUpdate, ToolCallID: msg.ToolCallID,
				Status: status, RawOutput: text})
		}
		if err != nil {
			return nil, err
		}
	}
	session := &acp.AgentSession{ID: saved.ID, CreatedAt: saved.CreatedAt, WorkingDirectory: workspace, McpServers: params.McpServers}
	if saved.Model != "" {
		session.Mode = acpModePrefix + saved.Model
	}
	return session, nil
}

func persistACPTurn(store *storage.Store, conv *conversation.Conversation, start int) error {
	messages := make([]*storage.Message, 0, len(conv.Messages)-start)
	for _, msg := range conv.Messages[start:] {
		row, err := conversation.StorageMessage(conv.SessionID, msg)
		if err != nil {
			return err
		}
		messages = append(messages, &row)
	}
	return store.SaveMessagesBatch(messages)
}
