package graft

import (
	"os/exec"
)

// Client provides access to all graft operations.
// If the graft binary is not found, the client degrades gracefully:
// Available() returns false, and all operations return nil/zero values.
type Client struct {
	VCS          *VCS
	Coordination *Coordinator
	runner       *Runner
	available    bool
}

// NewClient creates a graft Client for the given working directory and agent name.
// Auto-detects the graft binary. If not found, all operations become no-ops.
func NewClient(workDir string, agentName string) *Client {
	_, err := exec.LookPath("graft")
	if err != nil {
		return &Client{
			available:    false,
			VCS:          &VCS{runner: &Runner{}},
			Coordination: &Coordinator{agent: agentName},
		}
	}

	runner := NewRunner(WithWorkDir(workDir))
	return &Client{
		runner:       runner,
		available:    true,
		VCS:          NewVCS(runner),
		Coordination: NewCoordinator(runner, agentName),
	}
}

// Available reports whether the graft binary was found.
func (c *Client) Available() bool {
	return c.available
}
