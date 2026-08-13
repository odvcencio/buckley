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

- session creation, prompting, cancellation, and per-session conversation state;
- streamed answer and reasoning chunks, usage updates, and tool-call status;
- model selection through ACP modes and session configuration options;
- skill discovery and activation through the editor's command palette;
- embedded text resources and client-provided stdio MCP servers;
- client permission requests for governed tool calls, with Buckley's local risk
  policy as a bounded fallback when the client cannot answer.

Buckley advertises only capabilities it implements. Durable session loading,
image/audio prompts, and HTTP/SSE MCP servers are not advertised by the stdio
agent.

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

The bridge speaks LSP JSON-RPC over stdio and forwards its implemented custom
`buckley/textQuery` request to the coordinator. The bridge package also exposes
coordinator-backed streaming, inline-completion, edit, and editor-state APIs to
integrations that call those APIs directly; the stdio command does not
advertise unimplemented standard LSP capabilities.

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
