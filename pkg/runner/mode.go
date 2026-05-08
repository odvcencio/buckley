package runner

import (
	"github.com/odvcencio/buckley/pkg/types"
)

// RunnerMode identifies the execution mode.
type RunnerMode int

const (
	ModeInteractive RunnerMode = iota // "openclaw"
	ModeOneShot                       // "picoclaw"
	ModeDaemon                        // "channels"
)

var modeNames = [...]string{"interactive", "oneshot", "daemon"}

func (m RunnerMode) String() string {
	if int(m) < len(modeNames) {
		return modeNames[m]
	}
	return "unknown"
}

func parseMode(s string) RunnerMode {
	for i, name := range modeNames {
		if name == s {
			return RunnerMode(i)
		}
	}
	return ModeInteractive
}

// ChannelType identifies how results are delivered.
type ChannelType int

const (
	ChannelLocal ChannelType = iota
	ChannelSSE
	ChannelWebSocket
	ChannelFile
)

var channelNames = [...]string{"local", "sse", "websocket", "file"}

func (c ChannelType) String() string {
	if int(c) < len(channelNames) {
		return channelNames[c]
	}
	return "unknown"
}

// RunnerConfig is fully arbiter-resolved before the runner starts.
type RunnerConfig struct {
	Mode             RunnerMode
	Role             string
	PermissionTier   types.PermissionTier
	MaxTurns         int
	MaxCostUSD       float64
	SessionIsolation bool
	SandboxDefault   types.SandboxLevel
}

// CLIFlags captures command-line inputs for mode resolution.
type CLIFlags struct {
	Mode         string
	Prompt       string
	OutputFormat string
	Environment  string
}
