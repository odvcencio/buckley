# Mission Control (GoSX UI)

Mission Control is Buckley’s local browser UI for monitoring and steering
sessions. It presents the live transcript, workflow controls, approvals,
session telemetry, and the shared agent/subagent tree while the daemon remains
the sole execution authority.

## Visual System

Mission Control keeps the existing Deep Teal / Dark Elegance territory. The
refactor changes hierarchy and density, not the visual identity.

- **Territory:** Dark Elegance with a precision-operations bias: deep blue-black
  depth layers, restrained teal selection/action states, and amber attention
  states.
- **Typography:** Space Grotesk for display and navigation, Inter for body copy,
  and JetBrains Mono for paths, commands, model IDs, and telemetry. Keep the
  existing minor-third (1.2) scale in `pkg/ipc/gosxui/styles.css`.
- **Palette:** `--void` is the dominant canvas, `--abyss` and `--depth` form
  the secondary shell layers, and `--accent` is the reserved action/selection
  accent. New UI must consume the custom properties already defined in
  `pkg/ipc/gosxui/styles.css`.
- **Contrast:** primary text targets at least 7:1 against the void (AAA), body
  text at least 4.5:1 (AA), and muted metadata at least 3:1 for large text.
  Focus indicators use the accent border/glow tokens and remain visible in
  high-contrast mode.
- **Motion:** subtle, state-driven motion only. Use the existing 150ms/250ms
  durations and `ease-out`/`ease-out-expo`; never animate every transcript row.
  All non-essential motion remains disabled by the existing reduced-motion
  media query.
- **Spacing:** retain the existing compact plain-CSS rhythm and 6–10px corner
  radii instead of introducing a second styling system.

The new product hierarchy is **attention → run → steering → evidence →
infrastructure**. Mission Control is an agent/workspace manager: directories
are the primary grouping, agents and subagents are the operating units, and a
session transcript is the detail view for selected work. The daemon and IPC
view-model remain authoritative; the browser never becomes a second agent
runtime.

## What You Get

- **Workspaces**: group active work by repository/directory, with attention counts and compact run lists
- **Agents and subagents**: inspect the project-local agent catalog plus the
  shared runtime tree, including nested parent/child runs, persona, model,
  task, and retained completed/failed/cancelled status, then launch a selected
  profile
- **Start work**: create a daemon-owned run in a directory with an optional agent, subagent, model override, and initial task
- **Transcript**: read the live conversation transcript (user/assistant/system)
- **Controls**: pause/resume, steer, queue input, interrupt a running workflow, and send messages or slash commands
- **Approvals**: approve/reject pending tool calls when Buckley’s safety gate requests it
- **Run inspector**: compact status windows for workflow, todos, approvals, and basic session metrics
- **Agent activity**: follow nested run status, task, persona, and model from the shared telemetry projection without turning the browser into a second agent runtime
- **Terminal bridge**: the selected run’s authenticated `/ws/pty` endpoint stays
  available to a separate terminal client rooted at the session’s worktree;
  the current GoSX page does not embed a terminal

## Quick Start

```bash
# Start IPC + Mission Control
buckley serve --bind 127.0.0.1:4488 --browser
# open http://127.0.0.1:4488
```

The UI is compiled into Buckley through GoSX. Check the server-rendered surface with:

```bash
./scripts/build-ui.sh
```

## UI Development

```bash
# GoSX renders Mission Control directly from the daemon.
buckley serve --bind 127.0.0.1:4488 --browser
# open http://localhost:4488
```

Edit GoSX components in `pkg/ipc/gosxui/` and rerun `go test ./pkg/ipc/gosxui`.
No JavaScript package manager or browser build step is required.

## Authentication Model

Mission Control supports the same auth modes as the IPC server:

- **Bearer token**: set `BUCKLEY_IPC_TOKEN` (or `buckley serve --auth-token ...`) and enable enforcement with `--require-token`.
- **HTTP Basic Auth**: `BUCKLEY_BASIC_AUTH_ENABLED=1` plus `BUCKLEY_BASIC_AUTH_USER/BUCKLEY_BASIC_AUTH_PASSWORD` (or the `--basic-auth-*` flags).

When token auth is enabled, the GoSX page prompts for a token. You can also
open `/?token=...` once; the daemon exchanges it for an HTTP-only session
cookie and the token is not stored in browser JavaScript.

The signed `buckley_session` cookie is also used for the PTY WebSocket
handshake and native GoSX action forms.

## Runtime Boundary

Mission Control is a local-first, thin IPC client and workspace manager. The Buckley daemon remains
the authority for tool execution, approvals, policy, persistence, and replay.
The web UI consumes the ordered session event stream and issues ordinary
scoped actions; it does not create a second orchestration runtime. Workspace
creation and agent selection are authenticated IPC actions; profile metadata is
read-only in the browser and resolved by the daemon before a runner starts.

GoSX is the production browser application. It renders the document on the
server, uses native forms for authenticated actions, and keeps the daemon as
the only agent runtime. GoSX navigation and a bounded refresh keep active runs
current without a client-side React bundle. The existing PTY WebSocket remains
the terminal transport.

The browser and IPC assembler consume the same telemetry-derived agent
projection; GoSX does not query the retired Mission active-agent list. For
headless sessions, the runner is also the single canonical approval owner, so
an approved write cannot stall behind a second browser-invisible Mission gate.

## Protocol / Endpoints

- **Workspace catalog**: `GET /api/agent-specs?project=<directory>` returns
  safe metadata for project-local agents and subagents.
- **Start work**: `POST /api/headless/sessions` accepts a local `project` plus
  optional `agent`, `subagent`, `model`, and initial `prompt`; the daemon
  resolves the profile and owns the runner.
- **Unary RPCs (Connect/JSON)**: `POST /buckley.ipc.v1.BuckleyIPC/<Method>`
- **Event stream (Connect streaming)**: `POST /buckley.ipc.v1.BuckleyIPC/Subscribe` (`application/connect+json`, framed)
  - Payloads are framed with a 5-byte header (`flags` + 4-byte big-endian length) followed by a JSON envelope like `{ "result": { ... } }` or `{ "error": { "code": "...", "message": "..." } }`.
- **Terminal PTY**: `GET /ws/pty` (WebSocket)
  - A per-session terminal token is issued by `POST /api/sessions/<sessionId>/tokens`
  - The WebSocket client sends `{ "type": "auth", "data": "<sessionToken>" }` as the first message

## Troubleshooting

- **401 / token prompt**: ensure `BUCKLEY_IPC_TOKEN` matches what the server expects (or Basic Auth is enabled and you’re logged in).
- **Terminal won’t connect**: the PTY WebSocket needs the cookie session and a per-session token; check the UI is authenticated and the session is selected.
