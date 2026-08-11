# Buckley Mission Control

Mission Control is a GoSX server-rendered browser application. Its source now
lives in [`pkg/ipc/gosxui`](../pkg/ipc/gosxui): Go components render the
workspace, agent/subagent, transcript, approval, telemetry, and terminal
surfaces, while the daemon remains the only agent runtime.

There is no React, Vite, Bun, or browser bundle in the Buckley UI path.

## Development

Run the focused UI check:

```bash
go test ./pkg/ipc/gosxui
```

Then start the local app:

```bash
go run ./cmd/buckley serve --bind 127.0.0.1:4488 --browser
```

Open `http://127.0.0.1:4488`. GoSX navigation, native forms, and the daemon's
bounded refresh keep the page current without a client-side runtime.

## Authentication

If IPC auth is enabled, Mission Control expects the same token as
`BUCKLEY_IPC_TOKEN` / `buckley serve --auth-token`.

Mission Control supports:

- Token entry via the login screen (or open `/?token=...` once to exchange it)
- HTTP-only cookie sessions for browser/WebSocket auth (the daemon upgrades a
  bearer token into a signed `buckley_session` cookie)

It is a local-first operator client. It consumes the same session state and
ordered event stream as Buckley’s other clients. Tool execution, approvals,
policy, persistence, and replay remain in the Buckley daemon.

RPC and streaming requests include `Authorization: Bearer <token>` when a token is present. WebSockets (PTY) rely on the cookie session.
