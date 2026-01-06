package orchestrator

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/mock/gomock"

	orchestratorMocks "github.com/odvcencio/buckley/pkg/orchestrator/mocks"

	"github.com/odvcencio/buckley/pkg/memory"
	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/storage"
)

type memoryStubProvider struct{}

func (memoryStubProvider) Embed(ctx context.Context, text string) ([]float64, error) {
	text = strings.ToLower(text)
	if strings.Contains(text, "alpha") {
		return []float64{1, 0, 0}, nil
	}
	return []float64{0, 0, 1}, nil
}

func TestMemoryAwareModelClient_InsertsMemories(t *testing.T) {
	store, err := storage.New(filepath.Join(t.TempDir(), "mem.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer store.Close()

	memMgr := memory.NewManager(store, memoryStubProvider{})
	if memMgr == nil {
		t.Fatal("expected memory manager")
	}

	ctx := context.Background()
	if err := store.CreateSession(&storage.Session{
		ID:         "sess-1",
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
		Status:     storage.SessionStatusActive,
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := memMgr.Record(ctx, "sess-1", "summary", "alpha summary from earlier", nil); err != nil {
		t.Fatalf("record memory: %v", err)
	}

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockModel := orchestratorMocks.NewMockModelClient(ctrl)

	mockModel.EXPECT().ChatCompletion(gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, req model.ChatRequest) (*model.ChatResponse, error) {
			found := false
			for _, msg := range req.Messages {
				if msg.Role == "system" {
					if s, ok := msg.Content.(string); ok && strings.Contains(s, "alpha summary") {
						found = true
						break
					}
				}
			}
			if !found {
				t.Fatalf("expected injected memory context, got messages: %+v", req.Messages)
			}
			return &model.ChatResponse{
				Choices: []model.Choice{{
					Message: model.Message{Role: "assistant", Content: "ok"},
				}},
			}, nil
		}).Times(1)

	client := NewMemoryAwareModelClient(mockModel, memMgr, "sess-1", 3, 0)

	req := model.ChatRequest{
		Model: "test",
		Messages: []model.Message{
			{Role: "system", Content: "sys"},
			{Role: "user", Content: "alpha please"},
		},
	}
	if _, err := client.ChatCompletion(ctx, req); err != nil {
		t.Fatalf("chat completion: %v", err)
	}
}
