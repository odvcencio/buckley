package github

import (
	"errors"
	"strings"
	"testing"

	"go.uber.org/mock/gomock"
)

func TestEnsureAuthenticatedUsesTimeoutAndCachesState(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRunner := NewMockcommandRunner(ctrl)
	mockRunner.EXPECT().Run(gomock.Any(), "gh", "auth", "status").Return([]byte("ok"), nil).Times(1)

	cli := NewCLI()
	cli.runner = mockRunner

	if err := cli.EnsureAuthenticated(); err != nil {
		t.Fatalf("EnsureAuthenticated() error = %v", err)
	}
	if err := cli.EnsureAuthenticated(); err != nil {
		t.Fatalf("EnsureAuthenticated() second call error = %v", err)
	}
}

func TestCreatePRParsesLastLine(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRunner := NewMockcommandRunner(ctrl)
	mockRunner.EXPECT().Run(gomock.Any(), "gh", gomock.Any()).DoAndReturn(
		func(_ any, name string, args ...string) ([]byte, error) {
			cmd := strings.Join(append([]string{name}, args...), " ")
			if !strings.Contains(cmd, "pr create") {
				t.Fatalf("unexpected command args: %s", cmd)
			}
			return []byte("processing...\nhttps://example.com/pr/123\n"), nil
		})

	cli := NewCLI()
	cli.runner = mockRunner
	cli.authenticated = true

	url, err := cli.CreatePR("title", "body", "")
	if err != nil {
		t.Fatalf("CreatePR() error = %v", err)
	}
	if want := "https://example.com/pr/123"; url != want {
		t.Fatalf("CreatePR() url = %s, want %s", url, want)
	}
}

func TestListIssuesParsesJSON(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	payload := `[{"number":1,"title":"Bug","state":"open","assignees":["alice"],"url":"https://example.com"}]`
	mockRunner := NewMockcommandRunner(ctrl)
	mockRunner.EXPECT().Run(gomock.Any(), "gh", gomock.Any()).Return([]byte(payload), nil)

	cli := NewCLI()
	cli.runner = mockRunner
	cli.authenticated = true

	issues, err := cli.ListIssues(map[string]string{"state": "open"})
	if err != nil {
		t.Fatalf("ListIssues() error = %v", err)
	}
	if len(issues) != 1 || issues[0].Title != "Bug" {
		t.Fatalf("ListIssues() returned %+v, want Bug issue", issues)
	}
}

func TestMergePRErrorPropagation(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRunner := NewMockcommandRunner(ctrl)
	mockRunner.EXPECT().Run(gomock.Any(), "gh", gomock.Any()).Return(nil, errors.New("boom"))

	cli := NewCLI()
	cli.runner = mockRunner
	cli.authenticated = true

	err := cli.MergePR(42, "squash")
	if err == nil || !strings.Contains(err.Error(), "failed to merge PR") {
		t.Fatalf("MergePR() error = %v, want wrapped failure", err)
	}
}
