package dockersandbox

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/odvcencio/buckley/pkg/config"
)

type mockCall struct {
	name string
	args []string
}

type mockRunner struct {
	calls    []mockCall
	handlers map[string]func(args []string) (string, string, error)
}

func newMockRunner() *mockRunner {
	return &mockRunner{
		handlers: make(map[string]func([]string) (string, string, error)),
	}
}

func (m *mockRunner) run(_ context.Context, name string, args ...string) (string, string, error) {
	m.calls = append(m.calls, mockCall{name: name, args: args})
	key := name
	if len(args) > 0 {
		key = name + " " + args[0]
	}
	if handler, ok := m.handlers[key]; ok {
		return handler(args)
	}
	return "", "", nil
}

func (m *mockRunner) onDocker(subcommand string, handler func(args []string) (string, string, error)) {
	m.handlers["docker "+subcommand] = handler
}

func TestDockerSandbox_Ready(t *testing.T) {
	runner := newMockRunner()
	s := New(config.DockerSandboxConfig{}, WithCommandRunner(runner.run))

	err := s.Ready(context.Background())
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(runner.calls))
	}
	if runner.calls[0].name != "docker" || runner.calls[0].args[0] != "info" {
		t.Errorf("expected docker info, got %s %v", runner.calls[0].name, runner.calls[0].args)
	}
}

func TestDockerSandbox_Ready_Error(t *testing.T) {
	runner := newMockRunner()
	runner.onDocker("info", func([]string) (string, string, error) {
		return "", "Cannot connect to the Docker daemon", fmt.Errorf("exit status 1")
	})

	s := New(config.DockerSandboxConfig{}, WithCommandRunner(runner.run))
	err := s.Ready(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "docker not available") {
		t.Errorf("expected 'docker not available', got: %v", err)
	}
}

func TestDockerSandbox_Execute(t *testing.T) {
	runner := newMockRunner()
	runner.onDocker("create", func([]string) (string, string, error) {
		return "abc123\n", "", nil
	})
	runner.onDocker("start", func([]string) (string, string, error) {
		return "", "", nil
	})
	runner.onDocker("exec", func(args []string) (string, string, error) {
		for i, a := range args {
			if a == "-c" && i+1 < len(args) {
				return fmt.Sprintf("ran: %s", args[i+1]), "", nil
			}
		}
		return "", "", nil
	})

	cfg := config.DockerSandboxConfig{
		Image:          "ubuntu:24.04",
		WorkspaceMount: "/workspace",
	}
	s := New(cfg, WithCommandRunner(runner.run), WithWorkspacePath("/tmp/project"))

	result, err := s.Execute(context.Background(), Request{
		Command:  "echo hello",
		ToolName: "run_shell",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", result.ExitCode)
	}
	if !strings.Contains(result.Stdout, "ran: echo hello") {
		t.Errorf("expected stdout to contain command, got: %s", result.Stdout)
	}
}

func TestDockerSandbox_Execute_NonZeroExit(t *testing.T) {
	runner := newMockRunner()
	runner.onDocker("create", func([]string) (string, string, error) {
		return "abc123\n", "", nil
	})
	runner.onDocker("start", func([]string) (string, string, error) {
		return "", "", nil
	})
	runner.onDocker("exec", func([]string) (string, string, error) {
		return "", "command not found", fmt.Errorf("exit status 1")
	})

	cfg := config.DockerSandboxConfig{
		Image:          "ubuntu:24.04",
		WorkspaceMount: "/workspace",
	}
	s := New(cfg, WithCommandRunner(runner.run), WithWorkspacePath("/tmp/project"))

	_, err := s.Execute(context.Background(), Request{
		Command:  "nonexistent",
		ToolName: "run_shell",
	})
	// With mock runner, non-exec.ExitError errors are returned as docker exec errors
	if err == nil {
		t.Fatal("expected error for non-exec.ExitError")
	}
}

func TestDockerSandbox_Close(t *testing.T) {
	runner := newMockRunner()
	runner.onDocker("create", func([]string) (string, string, error) {
		return "abc123\n", "", nil
	})
	runner.onDocker("start", func([]string) (string, string, error) {
		return "", "", nil
	})
	runner.onDocker("exec", func([]string) (string, string, error) {
		return "ok", "", nil
	})
	runner.onDocker("rm", func([]string) (string, string, error) {
		return "", "", nil
	})

	cfg := config.DockerSandboxConfig{
		Image:          "ubuntu:24.04",
		WorkspaceMount: "/workspace",
	}
	s := New(cfg, WithCommandRunner(runner.run), WithWorkspacePath("/tmp/project"))

	_, err := s.Execute(context.Background(), Request{Command: "true"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := s.Close(); err != nil {
		t.Fatalf("close error: %v", err)
	}

	var found bool
	for _, call := range runner.calls {
		if call.name == "docker" && len(call.args) >= 2 && call.args[0] == "rm" && call.args[1] == "-f" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected docker rm -f to be called on Close")
	}

	_, err = s.Execute(context.Background(), Request{Command: "echo fail"})
	if err == nil {
		t.Error("expected error when executing after close")
	}
}

func TestDockerSandbox_ContainerReuse(t *testing.T) {
	runner := newMockRunner()
	createCount := 0
	runner.onDocker("create", func([]string) (string, string, error) {
		createCount++
		return "abc123\n", "", nil
	})
	runner.onDocker("start", func([]string) (string, string, error) {
		return "", "", nil
	})
	runner.onDocker("inspect", func([]string) (string, string, error) {
		return "true\n", "", nil
	})
	runner.onDocker("exec", func([]string) (string, string, error) {
		return "ok", "", nil
	})

	cfg := config.DockerSandboxConfig{
		Image:            "ubuntu:24.04",
		KeepAlive:        true,
		KeepAliveTimeout: time.Minute,
	}
	s := New(cfg, WithCommandRunner(runner.run))

	for i := 0; i < 3; i++ {
		_, err := s.Execute(context.Background(), Request{Command: "true"})
		if err != nil {
			t.Fatalf("iteration %d: unexpected error: %v", i, err)
		}
	}

	if createCount != 1 {
		t.Errorf("expected 1 docker create call (container reuse), got %d", createCount)
	}
}
