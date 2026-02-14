package tui

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/conversation"
	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/skill"
	"github.com/odvcencio/buckley/pkg/storage"
	"github.com/odvcencio/buckley/pkg/telemetry"
	"github.com/odvcencio/buckley/pkg/tool/builtin"
	"github.com/odvcencio/fluffyui/progress"
	"github.com/odvcencio/fluffyui/toast"
)

func newSessionState(ctx context.Context, cfg *config.Config, store *storage.Store, workDir string, hub *telemetry.Hub, modelMgr *model.Manager, sessionID string, loadMessages bool, progressMgr *progress.ProgressManager, toastMgr *toast.ToastManager) (*SessionState, error) {
	sess := &SessionState{
		ID:           sessionID,
		Conversation: conversation.New(sessionID),
	}

	if loadMessages && store != nil {
		if msgs, err := store.GetMessages(sessionID, 1000, 0); err == nil {
			for _, msg := range msgs {
				content := conversation.MaterializeContent(msg.ContentJSON, msg.Content)
				switch msg.Role {
				case "user":
					if parts, ok := content.([]model.ContentPart); ok {
						sess.Conversation.AddUserMessageParts(parts)
						continue
					}
					if text, ok := content.(string); ok {
						sess.Conversation.AddUserMessage(text)
					}
				case "assistant":
					if parts, ok := content.([]model.ContentPart); ok {
						sess.Conversation.AddAssistantMessageParts(parts, msg.Reasoning)
						continue
					}
					if text, ok := content.(string); ok {
						sess.Conversation.AddAssistantMessageWithReasoning(text, msg.Reasoning)
					}
				}
			}
		}
	}

	skills := skill.NewRegistry()
	if err := skills.LoadAll(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to load skills: %v\n", err)
	}

	skillState := skill.NewRuntimeState(sess.Conversation.AddSystemMessage)
	registry := buildRegistry(ctx, cfg, store, workDir, hub, sessionID, progressMgr, toastMgr)
	registry.Register(&builtin.SkillActivationTool{
		Registry:     skills,
		Conversation: skillState,
	})
	createTool := &builtin.CreateSkillTool{Registry: skills}
	if strings.TrimSpace(workDir) != "" {
		createTool.SetWorkDir(workDir)
	}
	registry.Register(createTool)

	sess.ToolRegistry = registry
	sess.SkillRegistry = skills
	sess.SkillState = skillState
	compactor := conversation.NewCompactionManager(modelMgr, cfg)
	compactor.SetConversation(sess.Conversation)
	if store != nil {
		compactor.SetOnComplete(func(_ *conversation.CompactionResult) {
			if err := sess.Conversation.SaveAllMessages(store); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to persist compaction: %v\n", err)
			}
		})
	}
	sess.Compactor = compactor
	if sess.ToolRegistry != nil {
		sess.ToolRegistry.SetCompactionManager(compactor)
	}

	return sess, nil
}
