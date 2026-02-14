## Summary
- [x] Includes tests or rationale for why tests are unnecessary
- [x] References relevant issues/links

### What changed
This branch contains a large release sweep across runtime, tooling, and TUI infrastructure. Key areas:

- Tool runtime and middleware
: Introduces the tool middleware chain and related execution/UX hooks.

- Ralph autonomous execution
: Adds Ralph session lifecycle (run/list/resume/watch/control), backend orchestration, and control-file driven behavior.

- Browser and runtime integrations
: Adds browser runtime support and execution-path integrations for autonomous workflows.

- Machine/runtime and parallel execution
: Adds machine state runtime and concurrency updates for multi-agent flows.

- TUI runtime updates
: Migrates controller behavior to the retained-mode signal UI and wires progress/toast/session behaviors.

- Context/memory handling
: Adds context budgeting and compaction-related behavior across execution paths.

- Stability and race-condition fixes
: Includes follow-up fixes for race conditions and shutdown/channel safety in core runtime paths.

### Testing
Commands used on this branch:

- `./scripts/test.sh`
- `GO_TEST_RACE=1 ./scripts/test.sh`
- `go test -race ./pkg/parallel -run TestFileLockManager_Close_Concurrent -count=1`
- `go test -race ./pkg/orchestrator -run TestWorkflowManager_SendProgress_ConcurrentEnableDisable -count=1`
- `go test -race ./pkg/buckley/ui/tui -count=1`

### Notes for Reviewers
Given PR size, suggested review order:

1. `pkg/tool/*` middleware and execution path
2. `pkg/ralph/*`, `cmd/buckley/ralph*.go`
3. `pkg/machine/*` and `pkg/parallel/*`
4. `pkg/buckley/ui/tui/*`
5. `pkg/execution/*` and context/memory updates

Risk notes:

- Concurrency-sensitive code paths were exercised under `-race`.
- Two prior panic paths were fixed and covered by regression tests:
  - `pkg/parallel/locks.go` (`FileLockManager.Close`)
  - `pkg/orchestrator/workflow.go` (`SendProgress`)

Links:

- ADR index: `docs/architecture/decisions/README.md`
- PR template source: `.github/PULL_REQUEST_TEMPLATE.md`
