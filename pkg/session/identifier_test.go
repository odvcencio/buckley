package session

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"go.uber.org/mock/gomock"
)

func TestDetermineSessionIDUsesGitMetadata(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRunner := NewMockgitCommandRunner(ctrl)

	// Set up expectations for the two git commands
	mockRunner.EXPECT().Run(gomock.Any(), "/workspace", "rev-parse", "--show-toplevel").
		DoAndReturn(func(ctx context.Context, dir string, args ...string) ([]byte, error) {
			if _, ok := ctx.Deadline(); !ok {
				t.Fatal("expected context with deadline")
			}
			return []byte("/tmp/my-repo\n"), nil
		})

	mockRunner.EXPECT().Run(gomock.Any(), "/workspace", "rev-parse", "--abbrev-ref", "HEAD").
		DoAndReturn(func(ctx context.Context, dir string, args ...string) ([]byte, error) {
			if _, ok := ctx.Deadline(); !ok {
				t.Fatal("expected context with deadline")
			}
			return []byte("feature/docs\n"), nil
		})

	det := newGitDetector()
	det.runner = mockRunner
	restore := setGitDetector(det)
	defer restore()

	id := DetermineSessionID("/workspace")
	if want := "my-repo-feature/docs"; id != want {
		t.Fatalf("DetermineSessionID() = %s, want %s", id, want)
	}

	// Second call should hit cache (no additional git commands)
	_ = DetermineSessionID("/workspace")
}

func TestDetermineSessionIDFallsBackWhenGitUnavailable(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRunner := NewMockgitCommandRunner(ctrl)
	mockRunner.EXPECT().Run(gomock.Any(), "/home/user/project", "rev-parse", "--show-toplevel").
		Return(nil, errors.New("not a git repo"))

	det := newGitDetector()
	det.runner = mockRunner
	restore := setGitDetector(det)
	defer restore()

	id := DetermineSessionID("/home/user/project")
	if !strings.HasPrefix(id, "project-") {
		t.Fatalf("expected fallback session id, got %s", id)
	}
}

func TestGitDetectorTimeoutHonorsContext(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRunner := NewMockgitCommandRunner(ctrl)
	mockRunner.EXPECT().Run(gomock.Any(), "/repo", "rev-parse", "--show-toplevel").
		DoAndReturn(func(ctx context.Context, dir string, args ...string) ([]byte, error) {
			// Simulate latency and check if context times out
			select {
			case <-time.After(100 * time.Millisecond):
				return nil, context.DeadlineExceeded
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		})

	det := newGitDetector()
	det.runner = mockRunner
	det.timeout = 10 * time.Millisecond
	restore := setGitDetector(det)
	defer restore()

	info := getGitMetadata("/repo")
	if info.valid {
		t.Fatalf("expected invalid metadata on timeout")
	}
}
