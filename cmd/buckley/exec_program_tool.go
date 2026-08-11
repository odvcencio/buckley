package main

import (
	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/runledger"
	"m31labs.dev/buckley/pkg/tool/execprogram"
)

// Keep the command-local names while the reusable adapter lives with the
// capability-brokered runtime. Goal mode and interactive code mode now share
// the exact same sandbox, audit, and evidence implementation.
type execProgramTool = execprogram.ProgramTool

func newExecProgramTool(workspaceRoot string, ledger runledger.Store, ev evidence.Store, runID, sessionID string, capabilities []string) (*execProgramTool, error) {
	return execprogram.NewProgramTool(workspaceRoot, ledger, ev, runID, sessionID, capabilities)
}
