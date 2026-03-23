package graft

import (
	"testing"
)

func TestNewCoordinator(t *testing.T) {
	runner := NewRunner(WithBinary("echo"))
	coord := NewCoordinator(runner, "cedar")

	if coord.AgentName() != "cedar" {
		t.Errorf("agent name = %q, want %q", coord.AgentName(), "cedar")
	}
	if coord.runner != runner {
		t.Error("runner not set correctly")
	}
}

func TestAgent_JSONTags(t *testing.T) {
	// Verify the Agent struct compiles and fields are accessible.
	a := Agent{
		Name:      "birch",
		Workspace: "/home/draco/work/buckley",
		Host:      "draco-desktop",
	}
	if a.Name != "birch" {
		t.Errorf("Name = %q, want %q", a.Name, "birch")
	}
	if a.Workspace != "/home/draco/work/buckley" {
		t.Errorf("Workspace = %q, want expected", a.Workspace)
	}
	if a.Host != "draco-desktop" {
		t.Errorf("Host = %q, want %q", a.Host, "draco-desktop")
	}
}

func TestNewVCS(t *testing.T) {
	runner := NewRunner(WithBinary("echo"))
	vcs := NewVCS(runner)

	if vcs.runner != runner {
		t.Error("runner not set correctly")
	}
}

func TestVCS_Add_EmptyFiles(t *testing.T) {
	runner := NewRunner(WithBinary("echo"))
	vcs := NewVCS(runner)

	// Adding zero files should be a no-op.
	err := vcs.Add(t.Context(), /* no files */)
	if err != nil {
		t.Fatalf("unexpected error adding zero files: %v", err)
	}
}

func TestNewClient_Available(t *testing.T) {
	// NewClient should degrade gracefully if graft isn't installed.
	// We can't guarantee graft is installed in CI, so just verify
	// that the client is always constructed without panic.
	client := NewClient(t.TempDir(), "oak")

	if client.VCS == nil {
		t.Fatal("VCS is nil")
	}
	if client.Coordination == nil {
		t.Fatal("Coordination is nil")
	}
	if client.Coordination.AgentName() != "oak" {
		t.Errorf("agent name = %q, want %q", client.Coordination.AgentName(), "oak")
	}

	// Available() should return a boolean without panic.
	_ = client.Available()
}
