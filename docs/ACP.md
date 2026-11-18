# Agent Client Protocol (ACP)

Buckley exposes its tool-first agent runtime to editors through ACP over
JSON-RPC stdio. A separate LSP bridge can connect an LSP-only editor to
Buckley's optional gRPC coordinator.

## ACP over stdio

Configure an ACP-capable editor to launch:

```bash
buckley acp
```

Use `--workdir <path>` to select the repository and `--log <path>` for a
private diagnostic log. The stdio agent supports:

- session creation, prompting, cancellation, and loading saved conversations;
- streamed answer and reasoning chunks, usage updates, and tool-call status;
- model selection through ACP modes and session configuration options;
- skill discovery and activation through the editor's command palette;
- embedded text resources and client-provided stdio MCP servers;
- client permission requests for governed tool calls, with Buckley's local risk
  policy as a bounded fallback when the client cannot answer.

New ACP sessions are saved to Buckley's local database. A client can load a
session after restart using its original session ID and workspace directory.
Loading replays saved conversation messages and tool results. The client
supplies MCP servers again in the load request, and new tool calls receive
their own permission decisions. Buckley keeps those decisions in its local
tool audit log.

### T3 Code

Add a local ACP command under **Settings → Providers → Add provider → Show
more**. Set the executable to `buckley` and add `acp` as its argument.
Install Buckley and configure its model provider on the machine that runs the
T3 Code server.

## Optional gRPC coordinator

`buckley serve` starts the local coordinator automatically on
`127.0.0.1:50051` when the HTTP server is bound to loopback and `acp.listen` is
otherwise empty. Configure it explicitly when another process, such as the LSP
bridge, needs a stable endpoint:

```yaml
# ~/.buckley/config.yaml
acp:
  listen: "127.0.0.1:50051"
  event_store: sqlite
  allow_insecure_local: true
```

An insecure coordinator is accepted only on loopback. Non-loopback listeners
require a server certificate, key, and client CA for mutual TLS. See the
[configuration reference](./CONFIGURATION.md#acp) for the full settings.

## LSP bridge

An LSP-only editor can launch:

```bash
buckley lsp --coordinator 127.0.0.1:50051 --agent-id editor
```

The bridge speaks LSP JSON-RPC over stdio. Send `buckley/textQuery` with a
`query` string for one response. Send `buckley/streamQuery` to receive
`buckley/streamChunk` notifications while the coordinator works. Each
notification carries the original `requestId`, its `streamId`, the chunk text,
and a `done` flag. The final response identifies the completed stream.
Use the standard `$/cancelRequest` notification with the original request ID
to stop either query.

The bridge package also exposes coordinator-backed inline-completion, edit,
and editor-state APIs for integrations that call the package directly.

## Editor setup

- ACP-compatible editors should launch `buckley acp` directly.
- LSP-only integrations should start `buckley serve` (or another configured
  coordinator) and point `buckley lsp` at its gRPC address.
- Keep ACP on stdio and coordinator traffic on loopback unless the configured
  mutual-TLS boundary is required.

## Related

- [CLI Reference](./CLI.md) - terminal commands and flags
- [Configuration](./CONFIGURATION.md#acp) - ACP event store, listener, and TLS
- [Mission Control](./MISSION_CONTROL.md) - local browser control and telemetry
