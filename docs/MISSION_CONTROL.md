# Mission Control (Web UI)

Mission Control is Buckley’s browser UI for monitoring and steering sessions. It’s designed to feel like “terminal control from anywhere”: a live transcript, workflow controls, approvals, and a real PTY connected to the host running Buckley.

## What You Get

- **Sessions**: browse active sessions and switch between them
- **Transcript**: read the live conversation transcript (user/assistant/system)
- **Controls**: pause/resume a running workflow, send messages and slash commands
- **Approvals**: approve/reject pending tool calls when Buckley’s safety gate requests it
- **Portholes panel**: compact “status windows” for workflow, todos, approvals, activity, and terminal
- **Terminal**: interactive xterm.js PTY rooted at the session’s worktree (remote shell in the browser)
- **Operator console** (operator scope): manage API tokens, remote settings, and audit history
- **Mobile-first UX**: the portholes panel becomes a drawer on small screens

## Quick Start

```bash
# Start IPC + Mission Control
buckley serve --bind 127.0.0.1:4488 --browser
# open http://127.0.0.1:4488
```

If you’re developing the UI (or you’ve edited `web/` and want to refresh the embedded assets), rebuild them with:

```bash
./scripts/build-ui.sh
```
Requires `bun` (https://bun.sh).

## UI Development (Hot Reload)

```bash
# backend
buckley serve --bind 127.0.0.1:4488 --browser

# frontend
cd web && bun install && bun run dev
# open http://localhost:5173
```

Vite proxies Buckley IPC endpoints to `http://localhost:4488` (see `web/vite.config.ts`).

## Authentication Model

Mission Control supports the same auth modes as the IPC server:

- **Bearer token**: set `BUCKLEY_IPC_TOKEN` (or `buckley serve --auth-token ...`) and enable enforcement with `--require-token`.
- **HTTP Basic Auth**: `BUCKLEY_BASIC_AUTH_ENABLED=1` plus `BUCKLEY_BASIC_AUTH_USER/BUCKLEY_BASIC_AUTH_PASSWORD` (or the `--basic-auth-*` flags).

When token auth is enabled, the UI prompts for a token. You can also open `/?token=...` once to store it in your browser.

For WebSockets (PTY), the UI upgrades the bearer token into a signed `buckley_session` cookie via `GET /api/auth/session`, then uses the cookie for the WebSocket handshake.

## Protocol / Endpoints

- **Unary RPCs (Connect/JSON)**: `POST /buckley.ipc.v1.BuckleyIPC/<Method>`
- **Event stream (Connect streaming)**: `POST /buckley.ipc.v1.BuckleyIPC/Subscribe` (`application/connect+json`, framed)
  - Payloads are framed with a 5-byte header (`flags` + 4-byte big-endian length) followed by a JSON envelope like `{ "result": { ... } }` or `{ "error": { "code": "...", "message": "..." } }`.
- **Terminal PTY**: `GET /ws/pty` (WebSocket)
  - A per-session terminal token is issued by `POST /api/sessions/<sessionId>/tokens`
  - The WebSocket client sends `{ "type": "auth", "data": "<sessionToken>" }` as the first message

## Troubleshooting

- **401 / token prompt**: ensure `BUCKLEY_IPC_TOKEN` matches what the server expects (or Basic Auth is enabled and you’re logged in).
- **Terminal won’t connect**: the PTY WebSocket needs the cookie session and a per-session token; check the UI is authenticated and the session is selected.
