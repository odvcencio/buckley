package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/storage"
	"m31labs.dev/buckley/pkg/subagent"
)

func TestAgentRunStrictSpecBeforeProviderInitialization(t *testing.T) {
	t.Setenv(subagent.ChildContractEnv, "")
	previousInit := initDependenciesFn
	t.Cleanup(func() { initDependenciesFn = previousInit })
	called := false
	sentinel := errors.New("provider initialization reached")
	initDependenciesFn = func() (*config.Config, *model.Manager, *storage.Store, error) {
		called = true
		return nil, nil, nil, sentinel
	}
	const base = "version: buckley.agent/v1\nname: worker\nsubagents:\n  - name: edit\n"
	for _, tc := range []struct {
		name, data, want string
		filesystem       bool
	}{
		{name: "unknown subagent intent", data: base + "    task_intent: mutation\n", want: "task_intent"},
		{name: "unknown sandbox field", data: base + "sandbox:\n  netwrok: false\n", want: "netwrok"},
		{name: "extra YAML document", data: base + "---\nname: ignored\n", want: "single YAML document"},
		{name: "filesystem subagent", data: "policies:\n  max_tool_call: 2\n", want: "max_tool_call", filesystem: true},
		{name: "valid control", data: base},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "agent.yaml")
			selector := path
			if tc.filesystem {
				selector = filepath.Join(root, "agent")
				path = filepath.Join(selector, "subagents", "edit", "agent.yaml")
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(selector, "instructions.md"), []byte("Follow the caller task."), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(path, []byte(tc.data), 0600); err != nil {
				t.Fatal(err)
			}
			called = false
			err := runAgentRun([]string{selector, "edit", "Read and summarize the requested source."})
			if tc.want == "" {
				if !called || !errors.Is(err, sentinel) {
					t.Fatalf("valid spec did not reach startup: called=%v err=%v", called, err)
				}
				return
			}
			if called || err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), path) {
				t.Fatalf("invalid spec must fail with source path before provider startup: called=%v err=%v", called, err)
			}
		})
	}
}
