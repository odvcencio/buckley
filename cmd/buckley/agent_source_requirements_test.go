package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/storage"
	"m31labs.dev/buckley/pkg/subagent"
)

func TestAgentSourceRequirements_ParseAndPreview(t *testing.T) {
	t.Setenv(subagent.ChildContractEnv, "")
	path := filepath.Join("..", "..", "templates", "agents", "source-extractor.yaml")
	args := []string{"--dry-run", "--json", "--require-source-text", " alpha ", "--require-source-text", "β", path, "extract", "Summarize source."}
	opts, err := parseAgentRunArgs(args)
	if err != nil || !reflect.DeepEqual(opts.requiredSourceText, []string{" alpha ", "β"}) {
		t.Fatalf("literal flags changed: %+v %v", opts, err)
	}
	var runErr error
	out := captureStdout(t, func() { runErr = runAgentRun(args) })
	if runErr != nil {
		t.Fatal(runErr)
	}
	var snapshot agentRunPreviewSnapshot
	if err := json.Unmarshal([]byte(out), &snapshot); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(snapshot.RequiredSourceText, opts.requiredSourceText) {
		t.Fatalf("preview lost requirements: %+v", snapshot)
	}
	profile, err := loadAgentRunProfile(opts)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(renderAgentRunPreview(opts, profile), `" alpha "`) {
		t.Fatal("text preview lost exact literal")
	}
	for _, bad := range [][]string{{"--require-source-text", ""}, {"--require-source-text", "a", "--require-source-text", "a"}, {"--require-source-text", strings.Repeat("x", 257)}} {
		if _, err := parseAgentRunArgs(append(bad, path, "extract", "task")); err == nil {
			t.Fatalf("invalid flags accepted: %q", bad)
		}
	}
}

func TestAgentSourceRequirements_RequireArtifactBeforeStartup(t *testing.T) {
	t.Setenv(subagent.ChildContractEnv, "")
	path := filepath.Join(t.TempDir(), "agent.yaml")
	if err := os.WriteFile(path, []byte("version: buckley.agent/v1\nname: worker\nsubagents:\n  - name: read\n"), 0600); err != nil {
		t.Fatal(err)
	}
	oldInit := initDependenciesFn
	t.Cleanup(func() { initDependenciesFn = oldInit })
	called := false
	sentinel := errors.New("startup control")
	initDependenciesFn = func() (*config.Config, *model.Manager, *storage.Store, error) {
		called = true
		return nil, nil, nil, sentinel
	}
	err := runAgentRun([]string{"--require-source-text", "alpha", path, "read", "task"})
	if called || err == nil || !strings.Contains(err.Error(), "buckley.artifact/v1") {
		t.Fatalf("unsupported output contract reached startup: called=%v err=%v", called, err)
	}
	err = runAgentRun([]string{path, "read", "task"})
	if !called || !errors.Is(err, sentinel) {
		t.Fatalf("unconstrained control changed: %v", err)
	}
}
