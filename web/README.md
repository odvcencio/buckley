# Buckley Web UI (Mission Control)

This directory contains the source for Buckley’s embedded IPC web UI (a mobile-friendly “mission control” for sessions, transcripts, workflow controls, and tool approvals).

## How It’s Shipped

`bun run build` emits static assets into `pkg/ipc/ui/` (embedded into the Go binary via `//go:embed`). When the IPC server is enabled (`buckley serve --browser`), Buckley serves those assets directly.

## Development

1. Start the IPC server:
   ```bash
   go run ./cmd/buckley serve --bind 127.0.0.1:4488 --browser
   ```
2. Run the web dev server:
   ```bash
   bun install
   bun run dev
   ```

Vite is configured to proxy Buckley IPC endpoints to `http://localhost:4488` (see `web/vite.config.ts`).

## Authentication

If IPC auth is enabled, the UI expects a bearer token (the same value as `BUCKLEY_IPC_TOKEN` / `buckley serve --auth-token`).

Mission Control supports:

- Token entry via the login screen (or open `/?token=...` once to store it in your browser)
- HTTP-only cookie sessions for browser/WebSocket auth (`/api/auth/session` upgrades a bearer token into a signed `buckley_session` cookie)

RPC and streaming requests include `Authorization: Bearer <token>` when a token is present. WebSockets (PTY) rely on the cookie session.
