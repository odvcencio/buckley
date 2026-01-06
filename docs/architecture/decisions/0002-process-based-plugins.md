# ADR 0002: Process-Based Plugin Architecture

## Status

Accepted

## Context

Buckley needs an extensible tool system that allows:
- User-defined tools beyond the built-in set
- Language-agnostic plugin development
- Isolation between plugins and core application
- Easy discovery and loading

Options considered:
1. **Shared libraries (.so/.dll)** - Platform-specific, complex build requirements, crash propagation
2. **WASM plugins** - Limited I/O, incomplete ecosystem, complex debugging
3. **In-process Go plugins** - Go version coupling, platform limitations
4. **Process-based (stdin/stdout)** - Simple, language-agnostic, isolated

## Decision

Use process-based plugins with JSON communication over stdin/stdout:

```yaml
# tool.yaml manifest
name: my_tool
description: Custom tool description
parameters:
  type: object
  properties:
    input:
      type: string
executable: ./my_tool.sh
timeout_ms: 60000
```

Plugin contract:
1. Read JSON input from stdin
2. Execute tool logic
3. Write JSON output to stdout
4. Exit with appropriate code

## Consequences

### Positive
- Language agnostic - shell scripts, Python, Go, Node.js all work
- Complete isolation - plugin crash doesn't affect Buckley
- Simple debugging - run plugins directly from command line
- No Go version coupling or platform-specific builds
- Easy discovery via manifest files in standard paths

### Negative
- Process spawn overhead (~10-50ms per invocation)
- IPC serialization cost (JSON encode/decode)
- No persistent state between invocations
- stderr handling requires explicit forwarding

### Discovery Paths
```
~/.buckley/plugins/<name>/tool.yaml
./.buckley/plugins/<name>/tool.yaml
./plugins/<name>/tool.yaml
```

### Mitigations
- Cache plugin manifests at startup
- Pool long-running plugins if needed (future enhancement)
- Structured error handling for stderr output
