# Servo Browser Engine Integration Design

**Date**: 2026-01-22
**Status**: Approved
**Author**: Claude + draco

## Overview

Integrate the Servo browser engine into `browserd` to enable agentic web workflows in environments where traditional browsers cannot run: headless servers, embedded devices, sandboxed enclaves, and airgapped systems.

### Goals

- Single static binary (~60-100MB) that runs anywhere without dependencies
- Full DOM and accessibility tree extraction for agent decision-making
- Visual frame capture with GPU acceleration when available, software fallback otherwise
- JavaScript execution with configurable budgets to prevent runaway scripts
- Maintains ADR 0011 security model (process isolation, resource limits, audit logging)

### Non-Goals

- Full browser UI (this is an embedding, not a browser)
- Browser extensions or plugins
- 100% web compatibility (Servo is ~70-80%, acceptable for agent workflows)

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                        browserd                              │
│  ┌──────────────┐    ┌──────────────────────────────────┐  │
│  │   IPC Layer  │◄──►│         ServoRuntime             │  │
│  │  (protobuf)  │    │  ┌────────────────────────────┐  │  │
│  │   [EXISTS]   │    │  │     Servo WebView          │  │  │
│  └──────────────┘    │  │  ┌──────┐ ┌────────────┐   │  │  │
│                      │  │  │Script│ │  Layout    │   │  │  │
│                      │  │  │ (JS) │ │ (WebRender)│   │  │  │
│                      │  │  └──────┘ └────────────┘   │  │  │
│                      │  └────────────────────────────┘  │  │
│                      │  ┌────────────────────────────┐  │  │
│                      │  │   Rendering Backend        │  │  │
│                      │  │  [GPU] ──► [Software]      │  │  │
│                      │  └────────────────────────────┘  │  │
│                      └──────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────┘
```

### Key Decisions

1. **Servo's WebView API** (delegate pattern) maps to existing `ServoCommand` enum
2. **WebRender + surfman** for rendering with headless software fallback
3. **SpiderMonkey** for JS with configurable execution limits
4. **Single-threaded embedding** - Servo manages its own thread pool internally
5. **Feature flag** - `--features servo` enables real engine, stub remains default

## Dependencies

When `servo` feature is enabled:

```toml
[features]
servo = ["libservo", "surfman", "webrender"]

[dependencies]
# Servo core - point to servo repo components/servo
libservo = { git = "https://github.com/servo/servo", rev = "TBD" }
surfman = { version = "0.9", features = ["sm-raw-window-handle-06"] }
webrender = { version = "0.64" }

# For headless software rendering
osmesa-sys = { version = "0.1", optional = true }
```

## API Mapping

| Our Command | Servo WebView API |
|-------------|-------------------|
| `Navigate { url }` | `webview.load_url(url)` |
| `Observe { opts }` | `webview.dom_tree()` + `webview.accessibility_tree()` + `compositor.read_pixels()` |
| `Act { click }` | `webview.send_mouse_event(...)` |
| `Act { type }` | `webview.send_keyboard_event(...)` |
| `Act { scroll }` | `webview.send_scroll_event(...)` |
| `StreamEvent` | `webview.set_delegate(impl WebViewDelegate)` |
| `Shutdown` | `webview.drop()` |

## Core Implementation

### run_servo_runtime()

```rust
fn run_servo_runtime(config: pb::SessionConfig, rx: mpsc::Receiver<ServoCommand>) {
    // 1. Initialize Servo with headless compositor
    let rendering_ctx = create_rendering_context();
    let mut servo = Servo::new(
        rendering_ctx,
        ServoConfig {
            user_agent: config.user_agent.clone(),
            viewport: (config.viewport_width, config.viewport_height),
            js_execution_budget_ms: config.js_budget_ms.unwrap_or(5000),
        },
    );

    // 2. Create webview
    let webview = servo.new_webview();
    let mut state_version: u64 = 0;

    // 3. Command loop
    while let Ok(cmd) = rx.recv() {
        servo.handle_events();

        match cmd {
            ServoCommand::Navigate { url, respond_to } => {
                webview.load_url(&url);
                servo.spin_until_loaded();
                state_version += 1;
                let obs = build_observation(&webview, state_version, &Default::default());
                let _ = respond_to.send(Ok(obs));
            }
            ServoCommand::Observe { opts, respond_to } => {
                let obs = build_observation(&webview, state_version, &opts);
                let _ = respond_to.send(Ok(obs));
            }
            ServoCommand::Act { action, respond_to } => {
                let result = dispatch_action(&webview, &action, &mut state_version);
                let _ = respond_to.send(result);
            }
            ServoCommand::StreamEvent { event_type, respond_to } => {
                let event = capture_stream_event(&webview, event_type, state_version);
                let _ = respond_to.send(event);
            }
            ServoCommand::Shutdown => break,
        }
    }
}
```

### Rendering Backend Selection

```rust
fn create_rendering_context() -> Box<dyn RenderingContext> {
    // Try GPU first
    if let Ok(ctx) = surfman::Context::new_hardware() {
        log::info!("Using GPU-accelerated rendering");
        return Box::new(GpuContext(ctx));
    }

    // Fall back to software (OSMesa or swrast)
    log::info!("Falling back to software rendering");
    Box::new(SoftwareContext::new())
}
```

### Observation Builder

```rust
fn build_observation(
    webview: &WebView,
    state_version: u64,
    opts: &pb::ObserveOptions,
) -> pb::Observation {
    pb::Observation {
        state_version,
        url: webview.url().to_string(),
        title: webview.title().unwrap_or_default(),
        dom_snapshot: if opts.include_dom_snapshot {
            serialize_dom(webview.dom_tree())
        } else {
            vec![]
        },
        frame: if opts.include_frame {
            Some(capture_frame(webview))
        } else {
            None
        },
        accessibility_tree: if opts.include_accessibility {
            serialize_a11y(webview.accessibility_tree())
        } else {
            vec![]
        },
        hit_test: if opts.include_hit_test {
            Some(build_hit_test_map(webview))
        } else {
            None
        },
        timestamp: Some(now_timestamp()),
    }
}
```

## Implementation Phases

### Phase 1: Minimal Navigation (1 week)

- Add libservo dependency with pinned revision
- Initialize Servo in `run_servo_runtime()`
- Implement `Navigate` - return URL/title in Observation
- Implement `Observe` - DOM snapshot as JSON (no frames yet)
- Software rendering only, no GPU path yet

**Milestone**: Agent can navigate and read DOM

### Phase 2: Input & Interaction (1 week)

- Implement `Act` variants: click, type, scroll, key
- Wire mouse/keyboard events through Servo's input pipeline
- Add `state_version` tracking (increment on DOM mutations)
- Implement `expected_state_version` guards

**Milestone**: Agent can fill forms, click buttons

### Phase 3: Visual Frames (1 week)

- Add `compositor.read_pixels()` → PNG encoding
- Implement GPU detection and fallback
- Add frame rate limiting (default 12 FPS)
- Wire `include_frame` option in Observe

**Milestone**: Agent can "see" the page

### Phase 4: Accessibility & Streaming (1 week)

- Extract Servo's accessibility tree → JSON
- Implement hit-test map (screen coords → node IDs)
- Wire `StreamEvent` with WebViewDelegate callbacks
- DOM diff computation for incremental updates

**Milestone**: Full API parity with stub

### Phase 5: Hardening (1 week)

- JS execution budgets (kill after N ms)
- Memory limits per session
- Network allowlist enforcement
- Audit logging hooks
- Static binary builds for Linux/macOS/Windows

**Milestone**: Production-ready

## Build System

### Cargo Profile

```toml
[profile.release]
lto = "fat"
codegen-units = 1
strip = true

[target.x86_64-unknown-linux-gnu]
rustflags = ["-C", "target-feature=+crt-static"]
```

### CI Matrix

| Target | Artifact | Notes |
|--------|----------|-------|
| `x86_64-unknown-linux-musl` | browserd-linux-amd64 | Fully static, runs on Alpine |
| `aarch64-apple-darwin` | browserd-macos-arm64 | Apple Silicon |
| `x86_64-pc-windows-msvc` | browserd-windows-amd64.exe | Windows |

### Expected Binary Sizes

| Target | Size (est.) |
|--------|-------------|
| Linux musl | 60-80MB |
| macOS arm64 | 50-70MB |
| Windows | 70-90MB |

## Testing Strategy

```
tests/
├── unit/           # Mock Servo, test command routing
├── integration/    # Real Servo, test against local HTML files
└── e2e/            # Full browserd binary, test via IPC
```

### Integration Test Example

```rust
#[test]
fn test_navigate_and_extract_dom() {
    let browserd = spawn_browserd();
    let session = browserd.create_session();

    session.navigate("file://tests/fixtures/form.html");
    let obs = session.observe(ObserveOptions {
        include_dom_snapshot: true,
        ..default()
    });

    assert!(obs.dom_snapshot.contains("input"));
    assert_eq!(obs.title, "Test Form");
}

#[test]
fn test_form_interaction() {
    let browserd = spawn_browserd();
    let session = browserd.create_session();

    session.navigate("file://tests/fixtures/form.html");
    session.act(Action::Click { node_id: "email-input" });
    session.act(Action::Type { text: "test@example.com" });
    session.act(Action::Click { node_id: "submit-btn" });

    let obs = session.observe(default());
    assert!(obs.url.contains("submitted"));
}
```

## Risks & Mitigations

| Risk | Likelihood | Mitigation |
|------|------------|------------|
| Servo API churn | Medium | Pin to specific revision, update quarterly |
| Build complexity | High | Docker build environment, CI caching |
| Web compatibility gaps | Medium | Document known gaps, fallback to stub for testing |
| Binary size bloat | Low | LTO + strip, accept 60-80MB as reasonable |
| Platform-specific bugs | Medium | CI matrix, integration tests per platform |

## Success Criteria

1. Agent can navigate to a URL and extract DOM
2. Agent can fill a form and submit it
3. Agent can capture a visual frame
4. Works on Linux without GPU/display
5. Single static binary under 100MB
6. JS execution respects configured budgets
7. All existing browser tools work without modification

## References

- [ADR 0011: Servo Embedder Browser Runtime](../architecture/decisions/0011-servo-embedder-browser-runtime.md)
- [Servo WebView API](https://servo.org/blog/2025/02/19/this-month-in-servo/)
- [Servo Embedding Example](https://github.com/paulrouget/servo-embedding-example)
- [browserd protobuf definitions](../../pkg/browser/adapters/servo/proto/browserd.proto)
