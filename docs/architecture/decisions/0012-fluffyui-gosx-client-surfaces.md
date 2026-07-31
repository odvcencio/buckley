# ADR 0012: FluffyUI Terminal and GoSX Web/Desktop Clients

## Status

Accepted, staged migration

## Context

Buckley has one agent runtime but two independently implemented presentation
stacks: a terminal client under `pkg/ui` and a React/Vite browser client under
`web`. This duplicates interaction state and makes features such as steering,
model switching, approvals, task progress, and tool timelines drift between
surfaces.

Adelie's desktop-assistant architecture demonstrates a useful boundary: keep
the assistant daemon and durable task state transport-neutral, share one client
state model, and make terminal, native, and web applications thin adapters.
Buckley already has the corresponding foundations in `pkg/orchestrator`,
`pkg/storage`, `pkg/telemetry`, `pkg/ui/viewmodel`, and `pkg/ipc`.

FluffyUI is Buckley's terminal framework. GoSX is the preferred Go-native
browser and packaged-desktop framework, but current GoSX releases require Go
1.26 while Buckley's root module remains on Go 1.25.1.

## Decision

Buckley will keep one durable runtime and expose it through two first-party
client adapters:

- FluffyUI owns terminal rendering, input, accessibility, and terminal-native
  interaction.
- GoSX will own browser and packaged-desktop rendering.
- `pkg/ui/viewmodel` and the IPC protocol own shared client state and commands;
  neither client may reimplement orchestration, tool execution, approval, or
  persistence rules.
- Every foreground turn and background/subagent operation is represented by a
  stable task identity with cancellable execution and an ordered, durable event
  stream. Tool calls and provider-supplied reasoning remain events, not ad hoc
  chat bubbles.
- Per-conversation model selection, steering, queued input, interrupt, approval,
  and task inspection must have equivalent semantics on both clients.

The GoSX client starts as a separate Go 1.26 module so it does not silently
raise Buckley's core toolchain requirement. Its production build emits static
assets into `pkg/ipc/ui`, preserving Buckley's single-binary distribution and
existing `buckley serve --browser` contract. `gosx desktop` packages that same
client; it does not introduce a second backend.

The browser remains loopback-only by default. Existing scopes, signed sessions,
origin checks, CSP, and WebSocket/Connect authentication stay authoritative.
GoSX server components render the shell, actions handle ordinary mutations,
small islands handle local interaction, and a hub carries the live task/event
stream.

## Migration

1. Treat the existing IPC protocol and `pkg/ui/viewmodel` behavior as the
   compatibility contract.
2. Add a separate GoSX client module and reproduce login, session navigation,
   conversation streaming, steering, interrupt, approvals, model selection,
   and the task/tool timeline.
3. Add deterministic GoSX build, size, and visual gates; embed its output beside
   the existing browser assets during parity testing.
4. Switch `pkg/ipc/ui` to the GoSX output after smoke and accessibility parity.
5. Remove the React/Vite source only after the GoSX browser and packaged desktop
   clients pass the same protocol fixtures.

## Consequences

### Positive

- One Go-native product stack across terminal, browser, and desktop surfaces.
- Shared task semantics prevent client drift.
- The browser stays a thin client instead of becoming a second agent runtime.
- Desktop packaging reuses the browser client and Buckley IPC security model.

### Negative

- GoSX needs a separate toolchain/module until Buckley adopts Go 1.26.
- React remains temporarily during parity migration.
- Protocol fixtures and visual tests become release requirements for client
  changes.
