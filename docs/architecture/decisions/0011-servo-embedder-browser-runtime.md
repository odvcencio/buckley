# ADR 0011: Servo Embedder Browser Runtime

## Status

Proposed

## Context

Buckley agents need real-time interaction with web pages. The browser runtime must:
- Provide low-latency perception/action loops (observe -> act -> verify).
- Run with strong security guardrails to prevent malicious activity.
- Align with the process-based isolation model (ADR 0002).

Options considered:
1. Chromium via CDP only (fast, not native, large attack surface).
2. Servo embedder in-process via FFI (fast, but weak isolation).
3. Servo embedder in a dedicated Rust process with IPC (native, isolated).
4. Custom minimal engine (high effort, limited compatibility).

## Decision

Adopt Option 3: a dedicated Rust "browserd" process embedding Servo, accessed via a narrow IPC API. We will not build a CDP fallback.

Key elements:
- Buckley talks to browserd over a structured IPC channel (protobuf over Unix socket).
- The adapter lives in `pkg/browser/adapters/servo` and implements a `BrowserSession` port.
- The runtime emits:
  - Frame stream (10-15 FPS) with `state_version` markers.
  - Accessibility tree and DOM snapshot diffs.
  - Hit-test map to map screen coordinates -> node IDs.
- Actions include `click`, `type`, `scroll`, `hover`, `key`, `focus`.
- Each action includes an expected `state_version` to avoid stale actions.
- Post-action response returns updated state and effect summary.

Security guardrails in browserd:
- Separate process per session, non-root user, seccomp/AppArmor, cgroup limits.
- Read-only filesystem with writable `/tmp` only.
- Network namespace with allowlist; deny `file:`, `data:`, `javascript:` schemes.
- Downloads disabled by default.
- JavaScript execution budgets (time/instruction caps) and DOM mutation limits.
- Audit log of navigation + actions.

Clipboard policy:
- Default to a per-session virtual clipboard managed by browserd.
- Allow clipboard writes without approval.
- Clipboard reads require explicit approval.
- Host clipboard bridging is opt-in and off by default.
- Size limits (default 64KB text; binary disabled).
- No auto-copy on page load; clipboard access only via explicit tool calls.
- Domain allowlist for clipboard reads.
- Audit log of read/write with secret redaction.

## Consequences

Positive:
- Native Rust runtime with a tight IPC surface.
- Strong isolation and explicit guardrails.
- Enables real-time agent interaction loop.

Negative:
- Servo embedder maturity risk and API churn.
- Added Rust build and runtime complexity.

## Follow-ups

- Add browserd lifecycle management in `pkg/browser`.
- Define metrics for frame latency and action success rates.
