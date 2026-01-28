//! Servo browser engine adapter.
//!
//! Implements the BrowserEngine trait using the Servo web engine for real
//! browser functionality including navigation, DOM access, and rendering.

use super::{BrowserEngine, EngineError};
use crate::proto as pb;
use std::cell::RefCell;
use std::rc::Rc;
use std::sync::mpsc;
use std::thread;
use std::time::{Duration, Instant};

use dpi::PhysicalSize;
use euclid::Point2D;
use servo::{
    CSSPixel, Code, EventLoopWaker, InputEvent, JSValue, JavaScriptEvaluationError, Key, KeyState,
    KeyboardEvent, LoadStatus, Location, Modifiers, MouseButton, MouseButtonAction,
    MouseButtonEvent, MouseMoveEvent, NamedKey, RenderingContext, Servo, ServoBuilder,
    SoftwareRenderingContext, WebView, WebViewBuilder, WebViewPoint, WheelDelta, WheelEvent,
    WheelMode,
};
use url::Url;

const DEFAULT_FRAME_RATE: u32 = 12;
const DEFAULT_VIEWPORT_WIDTH: u32 = 1280;
const DEFAULT_VIEWPORT_HEIGHT: u32 = 720;
const NAVIGATION_TIMEOUT_SECS: u64 = 30;
const JS_EVALUATION_TIMEOUT_MS: u64 = 3000;
const SPIN_POLL_INTERVAL_MS: u64 = 10;
const DOM_MAX_DEPTH: usize = 5;
const DOM_MAX_CHILDREN: usize = 50;
const DOM_MAX_TEXT_CHARS: usize = 200;
const A11Y_MAX_DEPTH: usize = 5;
const A11Y_MAX_CHILDREN: usize = 50;
const A11Y_MAX_NAME_CHARS: usize = 120;
const HIT_TEST_MAX_REGIONS: usize = 250;

pub struct ServoEngine {
    frame_rate: u32,
    runtime: ServoRuntime,
}

impl ServoEngine {
    pub fn new(config: &pb::SessionConfig) -> Result<Self, EngineError> {
        if config.session_id.trim().is_empty() {
            return Err(EngineError::new(
                "invalid_request",
                "session_id is required",
            ));
        }
        let frame_rate = if config.frame_rate > 0 {
            config.frame_rate
        } else {
            DEFAULT_FRAME_RATE
        };
        let runtime = ServoRuntime::spawn(config)?;
        Ok(Self {
            frame_rate,
            runtime,
        })
    }
}

impl BrowserEngine for ServoEngine {
    fn state_version(&self) -> u64 {
        self.runtime.state_version()
    }

    fn frame_rate(&self) -> u32 {
        self.frame_rate
    }

    fn navigate(&mut self, url: &str) -> Result<pb::Observation, EngineError> {
        self.runtime.navigate(url.to_string())
    }

    fn observe(&mut self, opts: &pb::ObserveOptions) -> Result<pb::Observation, EngineError> {
        self.runtime.observe(opts.clone())
    }

    fn act(&mut self, action: &pb::Action) -> Result<pb::ActionResult, EngineError> {
        self.runtime.act(action.clone())
    }

    fn stream_event(
        &mut self,
        event_type: pb::StreamEventType,
    ) -> Result<pb::StreamEvent, EngineError> {
        self.runtime.stream_event(event_type)
    }
}

impl Drop for ServoEngine {
    fn drop(&mut self) {
        self.runtime.shutdown();
    }
}

// Commands sent to the Servo runtime thread
enum ServoCommand {
    Navigate {
        url: String,
        respond_to: mpsc::Sender<Result<pb::Observation, EngineError>>,
    },
    Observe {
        opts: pb::ObserveOptions,
        respond_to: mpsc::Sender<Result<pb::Observation, EngineError>>,
    },
    Act {
        action: pb::Action,
        respond_to: mpsc::Sender<Result<pb::ActionResult, EngineError>>,
    },
    StreamEvent {
        event_type: pb::StreamEventType,
        respond_to: mpsc::Sender<Result<pb::StreamEvent, EngineError>>,
    },
    GetStateVersion {
        respond_to: mpsc::Sender<u64>,
    },
    Shutdown,
}

struct ServoRuntime {
    tx: mpsc::Sender<ServoCommand>,
}

impl ServoRuntime {
    fn spawn(config: &pb::SessionConfig) -> Result<Self, EngineError> {
        let (tx, rx) = mpsc::channel();
        let config = config.clone();

        thread::spawn(move || {
            if let Err(e) = run_servo_runtime(config, rx) {
                log::error!("Servo runtime error: {}", e.message);
            }
        });

        Ok(Self { tx })
    }

    fn state_version(&self) -> u64 {
        let (tx, rx) = mpsc::channel();
        let _ = self
            .tx
            .send(ServoCommand::GetStateVersion { respond_to: tx });
        rx.recv().unwrap_or(0)
    }

    fn navigate(&self, url: String) -> Result<pb::Observation, EngineError> {
        let (tx, rx) = mpsc::channel();
        let _ = self.tx.send(ServoCommand::Navigate {
            url,
            respond_to: tx,
        });
        rx.recv()
            .unwrap_or_else(|_| Err(EngineError::new("unavailable", "servo runtime unavailable")))
    }

    fn observe(&self, opts: pb::ObserveOptions) -> Result<pb::Observation, EngineError> {
        let (tx, rx) = mpsc::channel();
        let _ = self.tx.send(ServoCommand::Observe {
            opts,
            respond_to: tx,
        });
        rx.recv()
            .unwrap_or_else(|_| Err(EngineError::new("unavailable", "servo runtime unavailable")))
    }

    fn act(&self, action: pb::Action) -> Result<pb::ActionResult, EngineError> {
        let (tx, rx) = mpsc::channel();
        let _ = self.tx.send(ServoCommand::Act {
            action,
            respond_to: tx,
        });
        rx.recv()
            .unwrap_or_else(|_| Err(EngineError::new("unavailable", "servo runtime unavailable")))
    }

    fn stream_event(
        &self,
        event_type: pb::StreamEventType,
    ) -> Result<pb::StreamEvent, EngineError> {
        let (tx, rx) = mpsc::channel();
        let _ = self.tx.send(ServoCommand::StreamEvent {
            event_type,
            respond_to: tx,
        });
        rx.recv()
            .unwrap_or_else(|_| Err(EngineError::new("unavailable", "servo runtime unavailable")))
    }

    fn shutdown(&self) {
        let _ = self.tx.send(ServoCommand::Shutdown);
    }
}

/// Dummy event loop waker for headless operation
struct HeadlessEventLoopWaker;

impl EventLoopWaker for HeadlessEventLoopWaker {
    fn wake(&self) {
        // No-op for headless - we poll manually
    }

    fn clone_box(&self) -> Box<dyn EventLoopWaker> {
        Box::new(HeadlessEventLoopWaker)
    }
}

/// State maintained by the Servo runtime thread
struct ServoState {
    servo: Servo,
    webview: Option<WebView>,
    rendering_context: Rc<dyn RenderingContext>,
    state_version: u64,
    current_url: String,
    current_title: String,
    viewport_width: u32,
    viewport_height: u32,
    device_scale_factor: f32,
    last_hit_test: Option<pb::HitTestMap>,
}

fn run_servo_runtime(
    config: pb::SessionConfig,
    rx: mpsc::Receiver<ServoCommand>,
) -> Result<(), EngineError> {
    // Get viewport dimensions
    let (width, height, device_scale_factor) = if let Some(ref viewport) = config.viewport {
        let width = if viewport.width > 0 {
            viewport.width
        } else {
            DEFAULT_VIEWPORT_WIDTH
        };
        let height = if viewport.height > 0 {
            viewport.height
        } else {
            DEFAULT_VIEWPORT_HEIGHT
        };
        let scale = if viewport.device_scale_factor > 0.0 {
            viewport.device_scale_factor as f32
        } else {
            1.0
        };
        (width, height, scale)
    } else {
        (DEFAULT_VIEWPORT_WIDTH, DEFAULT_VIEWPORT_HEIGHT, 1.0)
    };
    let size = PhysicalSize::new(width, height);

    // Initialize rendering context
    let rendering_context: Rc<dyn RenderingContext> =
        Rc::new(SoftwareRenderingContext::new(size).map_err(|e| {
            EngineError::new(
                "rendering_init",
                format!("failed to create rendering context: {:?}", e),
            )
        })?);

    // Build Servo instance
    let servo = ServoBuilder::default()
        .event_loop_waker(Box::new(HeadlessEventLoopWaker))
        .build();

    let mut state = ServoState {
        servo,
        webview: None,
        rendering_context,
        state_version: 0,
        current_url: String::new(),
        current_title: String::new(),
        viewport_width: width,
        viewport_height: height,
        device_scale_factor,
        last_hit_test: None,
    };

    // Command loop
    while let Ok(cmd) = rx.recv() {
        // Process pending Servo events
        state.servo.spin_event_loop();

        match cmd {
            ServoCommand::Navigate { url, respond_to } => {
                let result = handle_navigate(&mut state, &url);
                let _ = respond_to.send(result);
            }
            ServoCommand::Observe { opts, respond_to } => {
                let result = handle_observe(&mut state, &opts);
                let _ = respond_to.send(result);
            }
            ServoCommand::Act { action, respond_to } => {
                let result = handle_act(&mut state, &action);
                let _ = respond_to.send(result);
            }
            ServoCommand::StreamEvent {
                event_type,
                respond_to,
            } => {
                let result = handle_stream_event(&mut state, event_type);
                let _ = respond_to.send(result);
            }
            ServoCommand::GetStateVersion { respond_to } => {
                let _ = respond_to.send(state.state_version);
            }
            ServoCommand::Shutdown => {
                break;
            }
        }
    }

    Ok(())
}

fn handle_navigate(state: &mut ServoState, url_str: &str) -> Result<pb::Observation, EngineError> {
    let url = Url::parse(url_str)
        .map_err(|e| EngineError::new("invalid_url", format!("failed to parse URL: {}", e)))?;

    // Create or reuse webview
    if state.webview.is_none() {
        let webview = WebViewBuilder::new(&state.servo, state.rendering_context.clone())
            .url(url.clone())
            .build();
        state.webview = Some(webview);
    } else if let Some(ref webview) = state.webview {
        webview.load(url.clone());
    }

    let webview = state
        .webview
        .clone()
        .ok_or_else(|| EngineError::new("no_webview", "failed to create webview"))?;
    wait_for_load(
        state,
        &webview,
        Duration::from_secs(NAVIGATION_TIMEOUT_SECS),
    )?;

    state.state_version += 1;
    state.last_hit_test = None;
    state.current_url = url_str.to_string();
    state.current_title.clear();
    refresh_page_metadata(state, &webview);

    build_observation(state, &pb::ObserveOptions::default())
}

fn handle_observe(
    state: &mut ServoState,
    opts: &pb::ObserveOptions,
) -> Result<pb::Observation, EngineError> {
    // Pump event loop
    state.servo.spin_event_loop();

    build_observation(state, opts)
}

fn handle_act(
    state: &mut ServoState,
    action: &pb::Action,
) -> Result<pb::ActionResult, EngineError> {
    let webview = state
        .webview
        .as_ref()
        .ok_or_else(|| EngineError::new("no_webview", "no webview active - navigate first"))?;

    // Check state version if provided
    if action.expected_state_version > 0 && action.expected_state_version != state.state_version {
        return Err(EngineError::new(
            "stale_state",
            format!(
                "expected state version {} but current is {}",
                action.expected_state_version, state.state_version
            ),
        ));
    }

    // Dispatch action based on type
    let action_type =
        pb::ActionType::try_from(action.r#type).unwrap_or(pb::ActionType::Unspecified);
    match action_type {
        pb::ActionType::Click => {
            let point = action_point(state, action.target.as_ref()).ok_or_else(|| {
                EngineError::new("invalid_target", "click requires a target point")
            })?;
            send_mouse_move(webview, point);
            send_mouse_button(webview, point, MouseButtonAction::Down);
            send_mouse_button(webview, point, MouseButtonAction::Up);
        }
        pb::ActionType::Type => {
            if action.text.is_empty() {
                return Err(EngineError::new(
                    "invalid_request",
                    "type action requires text",
                ));
            }
            if let Some(point) = action_point(state, action.target.as_ref()) {
                send_mouse_move(webview, point);
                send_mouse_button(webview, point, MouseButtonAction::Down);
                send_mouse_button(webview, point, MouseButtonAction::Up);
            }
            let modifiers = modifiers_from_action(action);
            send_text(webview, &action.text, modifiers);
        }
        pb::ActionType::Scroll => {
            let scroll = action.scroll.as_ref().ok_or_else(|| {
                EngineError::new("invalid_request", "scroll action requires delta")
            })?;
            let point =
                action_point(state, action.target.as_ref()).unwrap_or_else(|| default_point(state));
            send_scroll(webview, point, scroll);
        }
        pb::ActionType::Hover => {
            let point = action_point(state, action.target.as_ref()).ok_or_else(|| {
                EngineError::new("invalid_target", "hover requires a target point")
            })?;
            send_mouse_move(webview, point);
        }
        pb::ActionType::Key => {
            if action.key.is_empty() {
                return Err(EngineError::new(
                    "invalid_request",
                    "key action requires key",
                ));
            }
            let modifiers = modifiers_from_action(action);
            send_key(webview, &action.key, modifiers);
        }
        pb::ActionType::Focus => {
            let point = action_point(state, action.target.as_ref()).ok_or_else(|| {
                EngineError::new("invalid_target", "focus requires a target point")
            })?;
            send_mouse_move(webview, point);
            send_mouse_button(webview, point, MouseButtonAction::Down);
            send_mouse_button(webview, point, MouseButtonAction::Up);
        }
        pb::ActionType::ClipboardRead => {
            log::debug!("Clipboard read action");
        }
        pb::ActionType::ClipboardWrite => {
            log::debug!("Clipboard write: {} bytes", action.text.len());
        }
        pb::ActionType::Unspecified => {
            return Err(EngineError::new(
                "invalid_request",
                "unsupported action type",
            ));
        }
    }

    // Pump events after action
    state.servo.spin_event_loop();
    state.state_version += 1;

    // Build observation for result
    let observation = build_observation(state, &pb::ObserveOptions::default())?;

    Ok(pb::ActionResult {
        state_version: state.state_version,
        observation: Some(observation),
        effects: vec![],
    })
}

fn modifiers_from_action(action: &pb::Action) -> Modifiers {
    let mut modifiers = Modifiers::empty();
    for raw in &action.modifiers {
        let modifier = pb::KeyModifier::from_i32(*raw).unwrap_or(pb::KeyModifier::Unspecified);
        match modifier {
            pb::KeyModifier::Shift => modifiers.insert(Modifiers::SHIFT),
            pb::KeyModifier::Alt => modifiers.insert(Modifiers::ALT),
            pb::KeyModifier::Ctrl => modifiers.insert(Modifiers::CONTROL),
            pb::KeyModifier::Meta => modifiers.insert(Modifiers::META),
            pb::KeyModifier::Unspecified => {}
        }
    }
    modifiers
}

fn action_point(state: &ServoState, target: Option<&pb::ActionTarget>) -> Option<WebViewPoint> {
    let target = target?;
    if let Some(point) = target.point.as_ref() {
        return Some(webview_point(state, point.x, point.y));
    }
    if target.node_id != 0 {
        let rect = rect_for_node_id(state, target.node_id)?;
        let half_width = rect.width.max(0) / 2;
        let half_height = rect.height.max(0) / 2;
        let center_x = rect.x.saturating_add(half_width);
        let center_y = rect.y.saturating_add(half_height);
        return Some(webview_point(state, center_x, center_y));
    }
    None
}

fn rect_for_node_id(state: &ServoState, node_id: u64) -> Option<&pb::Rect> {
    state
        .last_hit_test
        .as_ref()?
        .regions
        .iter()
        .find(|region| region.node_id == node_id)
        .and_then(|region| region.bounds.as_ref())
}

fn default_point(state: &ServoState) -> WebViewPoint {
    let scale = if state.device_scale_factor > 0.0 {
        state.device_scale_factor
    } else {
        1.0
    };
    let x = (state.viewport_width as f32 / 2.0) / scale;
    let y = (state.viewport_height as f32 / 2.0) / scale;
    WebViewPoint::Page(Point2D::<f32, CSSPixel>::new(x, y))
}

fn webview_point(state: &ServoState, x: i32, y: i32) -> WebViewPoint {
    let scale = if state.device_scale_factor > 0.0 {
        state.device_scale_factor
    } else {
        1.0
    };
    let max_x = state.viewport_width.saturating_sub(1) as f32 / scale;
    let max_y = state.viewport_height.saturating_sub(1) as f32 / scale;
    let xf = (x as f32) / scale;
    let yf = (y as f32) / scale;
    let clamped_x = xf.max(0.0).min(max_x);
    let clamped_y = yf.max(0.0).min(max_y);
    WebViewPoint::Page(Point2D::<f32, CSSPixel>::new(clamped_x, clamped_y))
}

fn send_mouse_move(webview: &WebView, point: WebViewPoint) {
    webview.notify_input_event(InputEvent::MouseMove(MouseMoveEvent::new(point)));
}

fn send_mouse_button(webview: &WebView, point: WebViewPoint, action: MouseButtonAction) {
    webview.notify_input_event(InputEvent::MouseButton(MouseButtonEvent::new(
        action,
        MouseButton::Left,
        point,
    )));
}

fn send_scroll(webview: &WebView, point: WebViewPoint, delta: &pb::ScrollDelta) {
    let mode = match pb::ScrollUnit::try_from(delta.unit).unwrap_or(pb::ScrollUnit::Unspecified) {
        pb::ScrollUnit::Pixels | pb::ScrollUnit::Unspecified => WheelMode::DeltaPixel,
        pb::ScrollUnit::Lines => WheelMode::DeltaLine,
    };
    let wheel_delta = WheelDelta {
        x: delta.x as f64,
        y: delta.y as f64,
        z: 0.0,
        mode,
    };
    webview.notify_input_event(InputEvent::Wheel(WheelEvent::new(wheel_delta, point)));
}

fn send_key(webview: &WebView, key: &str, modifiers: Modifiers) {
    let (key, code) = key_from_string(key);
    send_keyboard_event(webview, key.clone(), code, modifiers, KeyState::Down);
    send_keyboard_event(webview, key, code, modifiers, KeyState::Up);
}

fn send_text(webview: &WebView, text: &str, modifiers: Modifiers) {
    for ch in text.chars() {
        let (key, code) = match ch {
            '\n' => (Key::Named(NamedKey::Enter), Code::Enter),
            '\t' => (Key::Named(NamedKey::Tab), Code::Tab),
            _ => (
                Key::Character(ch.to_string()),
                code_for_char(ch).unwrap_or(Code::Unidentified),
            ),
        };
        send_keyboard_event(webview, key.clone(), code, modifiers, KeyState::Down);
        send_keyboard_event(webview, key, code, modifiers, KeyState::Up);
    }
}

fn send_keyboard_event(
    webview: &WebView,
    key: Key,
    code: Code,
    modifiers: Modifiers,
    state: KeyState,
) {
    let event = KeyboardEvent::new_without_event(
        state,
        key,
        code,
        Location::Standard,
        modifiers,
        false,
        false,
    );
    webview.notify_input_event(InputEvent::Keyboard(event));
}

fn key_from_string(key: &str) -> (Key, Code) {
    let trimmed = key.trim();
    if trimmed.is_empty() {
        return (Key::Named(NamedKey::Unidentified), Code::Unidentified);
    }
    if trimmed == " " {
        return (Key::Character(" ".to_string()), Code::Space);
    }
    let normalized = trimmed
        .to_ascii_lowercase()
        .replace('_', "")
        .replace('-', "");

    let (named, code) = match normalized.as_str() {
        "enter" | "return" => (NamedKey::Enter, Code::Enter),
        "tab" => (NamedKey::Tab, Code::Tab),
        "escape" | "esc" => (NamedKey::Escape, Code::Escape),
        "backspace" => (NamedKey::Backspace, Code::Backspace),
        "delete" | "del" => (NamedKey::Delete, Code::Delete),
        "arrowup" | "up" => (NamedKey::ArrowUp, Code::ArrowUp),
        "arrowdown" | "down" => (NamedKey::ArrowDown, Code::ArrowDown),
        "arrowleft" | "left" => (NamedKey::ArrowLeft, Code::ArrowLeft),
        "arrowright" | "right" => (NamedKey::ArrowRight, Code::ArrowRight),
        "home" => (NamedKey::Home, Code::Home),
        "end" => (NamedKey::End, Code::End),
        "pageup" | "pgup" => (NamedKey::PageUp, Code::PageUp),
        "pagedown" | "pgdown" => (NamedKey::PageDown, Code::PageDown),
        "insert" => (NamedKey::Insert, Code::Insert),
        "shift" => (NamedKey::Shift, Code::ShiftLeft),
        "control" | "ctrl" => (NamedKey::Control, Code::ControlLeft),
        "alt" => (NamedKey::Alt, Code::AltLeft),
        "meta" | "cmd" | "command" => (NamedKey::Meta, Code::MetaLeft),
        "space" => return (Key::Character(" ".to_string()), Code::Space),
        _ => {
            if let Some((named, code)) = named_function_key(&normalized) {
                return (Key::Named(named), code);
            }
            if trimmed.chars().count() == 1 {
                let ch = trimmed.chars().next().unwrap();
                return (
                    Key::Character(ch.to_string()),
                    code_for_char(ch).unwrap_or(Code::Unidentified),
                );
            }
            return (Key::Named(NamedKey::Unidentified), Code::Unidentified);
        }
    };

    (Key::Named(named), code)
}

fn named_function_key(normalized: &str) -> Option<(NamedKey, Code)> {
    if !normalized.starts_with('f') {
        return None;
    }
    let num = normalized.trim_start_matches('f');
    let Ok(num) = num.parse::<u8>() else {
        return None;
    };
    let (named, code) = match num {
        1 => (NamedKey::F1, Code::F1),
        2 => (NamedKey::F2, Code::F2),
        3 => (NamedKey::F3, Code::F3),
        4 => (NamedKey::F4, Code::F4),
        5 => (NamedKey::F5, Code::F5),
        6 => (NamedKey::F6, Code::F6),
        7 => (NamedKey::F7, Code::F7),
        8 => (NamedKey::F8, Code::F8),
        9 => (NamedKey::F9, Code::F9),
        10 => (NamedKey::F10, Code::F10),
        11 => (NamedKey::F11, Code::F11),
        12 => (NamedKey::F12, Code::F12),
        _ => return None,
    };
    Some((named, code))
}

fn code_for_char(ch: char) -> Option<Code> {
    let lower = ch.to_ascii_lowercase();
    let code = match lower {
        'a' => Code::KeyA,
        'b' => Code::KeyB,
        'c' => Code::KeyC,
        'd' => Code::KeyD,
        'e' => Code::KeyE,
        'f' => Code::KeyF,
        'g' => Code::KeyG,
        'h' => Code::KeyH,
        'i' => Code::KeyI,
        'j' => Code::KeyJ,
        'k' => Code::KeyK,
        'l' => Code::KeyL,
        'm' => Code::KeyM,
        'n' => Code::KeyN,
        'o' => Code::KeyO,
        'p' => Code::KeyP,
        'q' => Code::KeyQ,
        'r' => Code::KeyR,
        's' => Code::KeyS,
        't' => Code::KeyT,
        'u' => Code::KeyU,
        'v' => Code::KeyV,
        'w' => Code::KeyW,
        'x' => Code::KeyX,
        'y' => Code::KeyY,
        'z' => Code::KeyZ,
        '0' => Code::Digit0,
        '1' => Code::Digit1,
        '2' => Code::Digit2,
        '3' => Code::Digit3,
        '4' => Code::Digit4,
        '5' => Code::Digit5,
        '6' => Code::Digit6,
        '7' => Code::Digit7,
        '8' => Code::Digit8,
        '9' => Code::Digit9,
        ' ' => Code::Space,
        '-' => Code::Minus,
        '=' => Code::Equal,
        '[' => Code::BracketLeft,
        ']' => Code::BracketRight,
        '\\' => Code::Backslash,
        ';' => Code::Semicolon,
        '\'' => Code::Quote,
        '`' => Code::Backquote,
        ',' => Code::Comma,
        '.' => Code::Period,
        '/' => Code::Slash,
        _ => return None,
    };
    Some(code)
}

fn handle_stream_event(
    state: &mut ServoState,
    event_type: pb::StreamEventType,
) -> Result<pb::StreamEvent, EngineError> {
    state.servo.spin_event_loop();

    let mut event = pb::StreamEvent {
        r#type: event_type as i32,
        state_version: state.state_version,
        timestamp: Some(timestamp_now()),
        frame: None,
        dom_diff: vec![],
        accessibility_diff: vec![],
        hit_test: None,
    };

    match event_type {
        pb::StreamEventType::Frame => {
            event.frame = capture_frame(state);
        }
        pb::StreamEventType::DomDiff => {
            if let Some(snapshot) = dom_snapshot_bytes(state) {
                event.dom_diff = wrap_diff_json(state.state_version, &snapshot);
            }
        }
        pb::StreamEventType::AccessibilityDiff => {
            if let Some(snapshot) = accessibility_snapshot_bytes(state) {
                event.accessibility_diff = wrap_diff_json(state.state_version, &snapshot);
            }
        }
        pb::StreamEventType::HitTest => {
            if let Some(map) = build_hit_test_map(state) {
                state.last_hit_test = Some(map.clone());
                event.hit_test = Some(map);
            }
        }
        pb::StreamEventType::Unspecified => {}
    }

    Ok(event)
}

fn build_observation(
    state: &mut ServoState,
    opts: &pb::ObserveOptions,
) -> Result<pb::Observation, EngineError> {
    if let Some(webview) = state.webview.clone() {
        refresh_page_metadata(state, &webview);
    }

    let mut obs = pb::Observation {
        state_version: state.state_version,
        url: state.current_url.clone(),
        title: state.current_title.clone(),
        timestamp: Some(timestamp_now()),
        frame: None,
        dom_snapshot: vec![],
        accessibility_tree: vec![],
        hit_test: None,
    };

    // Capture frame if requested
    if opts.include_frame {
        if let Some(frame) = capture_frame(state) {
            obs.frame = Some(frame);
        }
    }

    if opts.include_dom_snapshot {
        if let Some(snapshot) = dom_snapshot_bytes(state) {
            obs.dom_snapshot = snapshot;
        }
    }

    if opts.include_accessibility {
        if let Some(snapshot) = accessibility_snapshot_bytes(state) {
            obs.accessibility_tree = snapshot;
        }
    }

    if opts.include_hit_test {
        if let Some(map) = build_hit_test_map(state) {
            state.last_hit_test = Some(map.clone());
            obs.hit_test = Some(map);
        }
    }

    Ok(obs)
}

fn wait_for_load(
    state: &mut ServoState,
    webview: &WebView,
    timeout: Duration,
) -> Result<(), EngineError> {
    let deadline = Instant::now() + timeout;
    loop {
        state.servo.spin_event_loop();
        if webview.load_status() == LoadStatus::Complete {
            return Ok(());
        }
        if Instant::now() >= deadline {
            return Err(EngineError::new("load_timeout", "navigation timed out"));
        }
        thread::sleep(Duration::from_millis(SPIN_POLL_INTERVAL_MS));
    }
}

fn refresh_page_metadata(state: &mut ServoState, webview: &WebView) {
    if let Some(url) = webview.url() {
        state.current_url = url.to_string();
    }
    if let Some(title) = webview.page_title() {
        state.current_title = title;
    }
}

fn dom_snapshot_bytes(state: &mut ServoState) -> Option<Vec<u8>> {
    let webview = state.webview.clone()?;
    let script = dom_snapshot_script();
    match evaluate_javascript_sync(state, &webview, &script) {
        Ok(value) => match js_value_to_string(value) {
            Ok(json) => Some(json.into_bytes()),
            Err(err) => {
                log::warn!("DOM snapshot string error: {}", err.message);
                None
            }
        },
        Err(err) => {
            log::warn!("DOM snapshot evaluation error: {}", err.message);
            None
        }
    }
}

fn accessibility_snapshot_bytes(state: &mut ServoState) -> Option<Vec<u8>> {
    let webview = state.webview.clone()?;
    let script = accessibility_snapshot_script();
    match evaluate_javascript_sync(state, &webview, &script) {
        Ok(value) => match js_value_to_string(value) {
            Ok(json) => Some(json.into_bytes()),
            Err(err) => {
                log::warn!("accessibility snapshot string error: {}", err.message);
                None
            }
        },
        Err(err) => {
            log::warn!("accessibility snapshot evaluation error: {}", err.message);
            None
        }
    }
}

fn build_hit_test_map(state: &mut ServoState) -> Option<pb::HitTestMap> {
    let webview = state.webview.clone()?;
    let script = hit_test_script();
    let value = evaluate_javascript_sync(state, &webview, &script).ok()?;
    let json = js_value_to_string(value).ok()?;

    #[derive(serde::Deserialize)]
    struct HitRegionJson {
        id: u64,
        x: f32,
        y: f32,
        width: f32,
        height: f32,
    }

    let regions: Vec<HitRegionJson> = match serde_json::from_str(&json) {
        Ok(regions) => regions,
        Err(err) => {
            log::warn!("hit test JSON parse error: {}", err);
            return None;
        }
    };

    let mut map = pb::HitTestMap {
        width: state.viewport_width,
        height: state.viewport_height,
        regions: Vec::new(),
    };

    for region in regions {
        if region.width <= 0.0 || region.height <= 0.0 {
            continue;
        }
        map.regions.push(pb::HitRegion {
            node_id: region.id,
            bounds: Some(pb::Rect {
                x: region.x.round() as i32,
                y: region.y.round() as i32,
                width: region.width.round() as i32,
                height: region.height.round() as i32,
            }),
        });
    }

    Some(map)
}

fn wrap_diff_json(state_version: u64, snapshot: &[u8]) -> Vec<u8> {
    let snapshot_str = std::str::from_utf8(snapshot).unwrap_or("{}");
    format!(
        "{{\"type\":\"replace\",\"state_version\":{},\"snapshot\":{}}}",
        state_version, snapshot_str
    )
    .into_bytes()
}

fn evaluate_javascript_sync(
    state: &mut ServoState,
    webview: &WebView,
    script: &str,
) -> Result<JSValue, EngineError> {
    let result_cell: Rc<RefCell<Option<Result<JSValue, JavaScriptEvaluationError>>>> =
        Rc::new(RefCell::new(None));
    let callback_cell = result_cell.clone();
    webview.evaluate_javascript(script, move |result| {
        *callback_cell.borrow_mut() = Some(result);
    });

    let deadline = Instant::now() + Duration::from_millis(JS_EVALUATION_TIMEOUT_MS);
    loop {
        state.servo.spin_event_loop();
        if let Some(result) = result_cell.borrow_mut().take() {
            return result.map_err(|err| {
                EngineError::new(
                    "script_error",
                    format!("javascript evaluation failed: {:?}", err),
                )
            });
        }
        if Instant::now() >= deadline {
            return Err(EngineError::new(
                "script_timeout",
                "javascript evaluation timed out",
            ));
        }
        thread::sleep(Duration::from_millis(SPIN_POLL_INTERVAL_MS));
    }
}

fn js_value_to_string(value: JSValue) -> Result<String, EngineError> {
    match value {
        JSValue::String(value) => Ok(value),
        JSValue::Null | JSValue::Undefined => Ok("{}".to_string()),
        _ => Err(EngineError::new(
            "script_error",
            "javascript result was not a string",
        )),
    }
}

fn dom_snapshot_script() -> String {
    format!(
        r#"(function() {{
            const MAX_DEPTH = {max_depth};
            const MAX_CHILDREN = {max_children};
            const MAX_TEXT = {max_text};
            const NEXT_ID_KEY = "__buckleyNextId";

            function ensureId(el) {{
                if (!el) return 0;
                if (!el.__buckleyId) {{
                    const next = (window[NEXT_ID_KEY] || 1);
                    el.__buckleyId = next;
                    window[NEXT_ID_KEY] = next + 1;
                }}
                return el.__buckleyId;
            }}

            function attrValue(el, name) {{
                if (!el.hasAttribute || !el.hasAttribute(name)) return null;
                const value = el.getAttribute(name);
                if (!value) return null;
                return value.slice(0, 200);
            }}

            function serializeNode(node, depth) {{
                if (!node || depth > MAX_DEPTH) return null;
                if (node.nodeType === Node.ELEMENT_NODE) {{
                    const el = node;
                    const attrs = {{}};
                    const names = ["id","class","name","type","value","href","src","role","aria-label","title","alt"];
                    for (const name of names) {{
                        const value = attrValue(el, name);
                        if (value) attrs[name] = value;
                    }}
                    const children = [];
                    let count = 0;
                    for (const child of el.childNodes) {{
                        if (count >= MAX_CHILDREN) break;
                        const serialized = serializeNode(child, depth + 1);
                        if (serialized) {{
                            children.push(serialized);
                            count += 1;
                        }}
                    }}
                    return {{
                        node_id: ensureId(el),
                        tag: el.tagName.toLowerCase(),
                        attrs: attrs,
                        children: children
                    }};
                }}
                if (node.nodeType === Node.TEXT_NODE) {{
                    const text = node.textContent || "";
                    const trimmed = text.trim();
                    if (!trimmed) return null;
                    return {{ text: trimmed.slice(0, MAX_TEXT) }};
                }}
                return null;
            }}

            const root = document.documentElement || document.body;
            const snapshot = {{
                url: document.URL,
                title: document.title || "",
                root: root ? serializeNode(root, 0) : null
            }};
            return JSON.stringify(snapshot);
        }})()"#,
        max_depth = DOM_MAX_DEPTH,
        max_children = DOM_MAX_CHILDREN,
        max_text = DOM_MAX_TEXT_CHARS,
    )
}

fn accessibility_snapshot_script() -> String {
    format!(
        r#"(function() {{
            const MAX_DEPTH = {max_depth};
            const MAX_CHILDREN = {max_children};
            const MAX_NAME = {max_name};
            const NEXT_ID_KEY = "__buckleyNextId";

            function ensureId(el) {{
                if (!el) return 0;
                if (!el.__buckleyId) {{
                    const next = (window[NEXT_ID_KEY] || 1);
                    el.__buckleyId = next;
                    window[NEXT_ID_KEY] = next + 1;
                }}
                return el.__buckleyId;
            }}

            function roleFor(el) {{
                const role = el.getAttribute && el.getAttribute("role");
                if (role) return role.toLowerCase();
                const tag = el.tagName.toLowerCase();
                if (tag === "a") return "link";
                if (tag === "button") return "button";
                if (tag === "input") {{
                    const type = (el.getAttribute("type") || "text").toLowerCase();
                    if (type === "checkbox") return "checkbox";
                    if (type === "radio") return "radio";
                    if (type === "submit" || type === "button") return "button";
                    return "textbox";
                }}
                if (tag === "textarea") return "textbox";
                if (tag === "select") return "combobox";
                if (tag === "option") return "option";
                if (tag === "img") return "img";
                if (tag === "ul" || tag === "ol") return "list";
                if (tag === "li") return "listitem";
                if (tag.startsWith("h") && tag.length === 2) return "heading";
                return "generic";
            }}

            function nameFor(el) {{
                const aria = el.getAttribute && el.getAttribute("aria-label");
                if (aria) return aria.slice(0, MAX_NAME);
                const alt = el.getAttribute && el.getAttribute("alt");
                if (alt) return alt.slice(0, MAX_NAME);
                const title = el.getAttribute && el.getAttribute("title");
                if (title) return title.slice(0, MAX_NAME);
                const text = el.textContent || "";
                const trimmed = text.trim();
                if (!trimmed) return "";
                return trimmed.slice(0, MAX_NAME);
            }}

            function isFocusable(el) {{
                if (!el) return false;
                if (el.tabIndex >= 0) return true;
                const tag = el.tagName.toLowerCase();
                return ["a","button","input","textarea","select"].includes(tag);
            }}

            function nodeBounds(el) {{
                if (!el || !el.getBoundingClientRect) return null;
                const rect = el.getBoundingClientRect();
                return {{
                    x: Math.round(rect.left),
                    y: Math.round(rect.top),
                    width: Math.round(rect.width),
                    height: Math.round(rect.height)
                }};
            }}

            function buildNode(el, depth) {{
                if (!el || depth > MAX_DEPTH) return null;
                const role = roleFor(el);
                const name = nameFor(el);
                const node = {{
                    node_id: ensureId(el),
                    role: role,
                }};
                if (name) node.name = name;
                if (role === "heading") {{
                    const level = parseInt(el.tagName.substring(1), 10);
                    if (!Number.isNaN(level)) node.level = level;
                }}
                if (document.activeElement === el) node.focused = true;
                if (isFocusable(el)) node.focusable = true;
                const bounds = nodeBounds(el);
                if (bounds && bounds.width > 0 && bounds.height > 0) node.bounds = bounds;

                const children = [];
                let count = 0;
                for (const child of el.children) {{
                    if (count >= MAX_CHILDREN) break;
                    const childNode = buildNode(child, depth + 1);
                    if (childNode) {{
                        children.push(childNode);
                        count += 1;
                    }}
                }}
                if (children.length) node.children = children;

                if (!node.name && !node.children && role === "generic") return null;
                return node;
            }}

            const rootEl = document.documentElement || document.body;
            const root = {{
                role: "document",
                name: document.title || "",
                node_id: rootEl ? ensureId(rootEl) : 0,
                children: rootEl ? (function() {{
                    const nodes = [];
                    let count = 0;
                    for (const child of rootEl.children) {{
                        if (count >= MAX_CHILDREN) break;
                        const node = buildNode(child, 1);
                        if (node) {{
                            nodes.push(node);
                            count += 1;
                        }}
                    }}
                    return nodes;
                }})() : []
            }};
            return JSON.stringify(root);
        }})()"#,
        max_depth = A11Y_MAX_DEPTH,
        max_children = A11Y_MAX_CHILDREN,
        max_name = A11Y_MAX_NAME_CHARS,
    )
}

fn hit_test_script() -> String {
    format!(
        r#"(function() {{
            const MAX_REGIONS = {max_regions};
            const NEXT_ID_KEY = "__buckleyNextId";

            function ensureId(el) {{
                if (!el) return 0;
                if (!el.__buckleyId) {{
                    const next = (window[NEXT_ID_KEY] || 1);
                    el.__buckleyId = next;
                    window[NEXT_ID_KEY] = next + 1;
                }}
                return el.__buckleyId;
            }}

            function isVisible(el, rect) {{
                if (!rect || rect.width <= 0 || rect.height <= 0) return false;
                const style = window.getComputedStyle(el);
                if (style.display === "none" || style.visibility === "hidden") return false;
                const vw = window.innerWidth || document.documentElement.clientWidth;
                const vh = window.innerHeight || document.documentElement.clientHeight;
                return rect.right > 0 && rect.bottom > 0 && rect.left < vw && rect.top < vh;
            }}

            const selectors = [
                "a[href]",
                "button",
                "input",
                "textarea",
                "select",
                "option",
                "[role]",
                "[onclick]",
                "[tabindex]"
            ];

            const regions = [];
            const root = document.documentElement || document.body;
            if (root && regions.length < MAX_REGIONS) {{
                const rect = root.getBoundingClientRect();
                regions.push({{
                    id: ensureId(root),
                    x: Math.max(0, Math.round(rect.left)),
                    y: Math.max(0, Math.round(rect.top)),
                    width: Math.round(rect.width),
                    height: Math.round(rect.height)
                }});
            }}

            const elements = document.querySelectorAll(selectors.join(","));
            for (const el of elements) {{
                if (regions.length >= MAX_REGIONS) break;
                if (!el || !el.getBoundingClientRect) continue;
                const rect = el.getBoundingClientRect();
                if (!isVisible(el, rect)) continue;
                const id = ensureId(el);
                regions.push({{
                    id: id,
                    x: Math.round(rect.left),
                    y: Math.round(rect.top),
                    width: Math.round(rect.width),
                    height: Math.round(rect.height)
                }});
            }}
            return JSON.stringify(regions);
        }})()"#,
        max_regions = HIT_TEST_MAX_REGIONS,
    )
}

fn capture_frame(state: &ServoState) -> Option<pb::Frame> {
    use servo::{DeviceIntPoint, DeviceIntRect, DeviceIntSize};

    let rect = DeviceIntRect::from_origin_and_size(
        DeviceIntPoint::new(0, 0),
        DeviceIntSize::new(state.viewport_width as i32, state.viewport_height as i32),
    );

    let image = state.rendering_context.read_to_image(rect)?;

    // Encode as PNG using image crate
    use std::io::Cursor;
    let mut png_data = Vec::new();
    let mut cursor = Cursor::new(&mut png_data);

    image.write_to(&mut cursor, image::ImageFormat::Png).ok()?;

    Some(pb::Frame {
        state_version: state.state_version,
        format: pb::FrameFormat::Png as i32,
        data: png_data,
        width: image.width(),
        height: image.height(),
        timestamp: Some(timestamp_now()),
    })
}

fn timestamp_now() -> prost_types::Timestamp {
    let now = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .unwrap_or_default();
    prost_types::Timestamp {
        seconds: now.as_secs() as i64,
        nanos: now.subsec_nanos() as i32,
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::Value;
    use std::path::PathBuf;

    fn fixture_url(name: &str) -> String {
        let path = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
            .join("tests")
            .join("fixtures")
            .join(name);
        Url::from_file_path(path)
            .expect("fixture path should be absolute")
            .to_string()
    }

    fn test_config() -> pb::SessionConfig {
        pb::SessionConfig {
            session_id: "servo-test".to_string(),
            initial_url: "".to_string(),
            viewport: Some(pb::Viewport {
                width: 800,
                height: 600,
                device_scale_factor: 1.0,
            }),
            user_agent: "".to_string(),
            locale: "".to_string(),
            timezone: "".to_string(),
            frame_rate: 12,
            network_allowlist: Vec::new(),
            clipboard: None,
        }
    }

    #[test]
    fn test_navigate_and_dom_snapshot() {
        let mut engine = ServoEngine::new(&test_config()).expect("engine init");
        let url = fixture_url("simple.html");
        let obs = engine.navigate(&url).expect("navigate");
        assert!(obs.url.contains("simple.html"));
        assert!(obs.title.contains("Test Page"));

        let obs = engine
            .observe(&pb::ObserveOptions {
                include_frame: false,
                include_dom_snapshot: true,
                include_accessibility: false,
                include_hit_test: false,
            })
            .expect("observe");
        assert!(!obs.dom_snapshot.is_empty());
        let dom: Value = serde_json::from_slice(&obs.dom_snapshot).expect("dom json");
        assert_eq!(dom["title"], "Test Page");
    }

    #[test]
    fn test_accessibility_and_hit_test() {
        let mut engine = ServoEngine::new(&test_config()).expect("engine init");
        let url = fixture_url("simple.html");
        let _ = engine.navigate(&url).expect("navigate");

        let obs = engine
            .observe(&pb::ObserveOptions {
                include_frame: false,
                include_dom_snapshot: false,
                include_accessibility: true,
                include_hit_test: true,
            })
            .expect("observe");

        assert!(!obs.accessibility_tree.is_empty());
        let tree: Value = serde_json::from_slice(&obs.accessibility_tree).expect("a11y json");
        assert_eq!(tree["role"], "document");

        let hit_test = obs.hit_test.expect("hit test map");
        assert!(hit_test.width > 0);
        assert!(hit_test.height > 0);
        assert!(!hit_test.regions.is_empty());
    }

    #[test]
    fn test_actions_increment_state_version() {
        let mut engine = ServoEngine::new(&test_config()).expect("engine init");
        let url = fixture_url("simple.html");
        let _ = engine.navigate(&url).expect("navigate");

        let initial = engine.state_version();
        let result = engine
            .act(&pb::Action {
                r#type: pb::ActionType::Click as i32,
                expected_state_version: 0,
                target: Some(pb::ActionTarget {
                    node_id: 0,
                    point: Some(pb::Point { x: 10, y: 10 }),
                }),
                text: "".to_string(),
                key: "".to_string(),
                scroll: None,
                modifiers: vec![],
            })
            .expect("click");
        assert!(result.state_version > initial);
    }
}
