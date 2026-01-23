//! Servo browser engine adapter.
//!
//! Implements the BrowserEngine trait using the Servo web engine for real
//! browser functionality including navigation, DOM access, and rendering.

use crate::proto as pb;
use super::{BrowserEngine, EngineError};
use std::rc::Rc;
use std::sync::mpsc;
use std::thread;

use dpi::PhysicalSize;
use servo::{
    Servo, ServoBuilder, WebView, WebViewBuilder,
    SoftwareRenderingContext, RenderingContext,
    EventLoopWaker,
};
use url::Url;

pub struct ServoEngine {
    frame_rate: u32,
    runtime: ServoRuntime,
}

impl ServoEngine {
    pub fn new(config: &pb::SessionConfig) -> Result<Self, EngineError> {
        if config.session_id.trim().is_empty() {
            return Err(EngineError::new("invalid_request", "session_id is required"));
        }
        let frame_rate = if config.frame_rate > 0 { config.frame_rate } else { 12 };
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

    fn stream_event(&mut self, event_type: pb::StreamEventType) -> Result<pb::StreamEvent, EngineError> {
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
        let _ = self.tx.send(ServoCommand::GetStateVersion { respond_to: tx });
        rx.recv().unwrap_or(0)
    }

    fn navigate(&self, url: String) -> Result<pb::Observation, EngineError> {
        let (tx, rx) = mpsc::channel();
        let _ = self.tx.send(ServoCommand::Navigate { url, respond_to: tx });
        rx.recv().unwrap_or_else(|_| Err(EngineError::new("unavailable", "servo runtime unavailable")))
    }

    fn observe(&self, opts: pb::ObserveOptions) -> Result<pb::Observation, EngineError> {
        let (tx, rx) = mpsc::channel();
        let _ = self.tx.send(ServoCommand::Observe { opts, respond_to: tx });
        rx.recv().unwrap_or_else(|_| Err(EngineError::new("unavailable", "servo runtime unavailable")))
    }

    fn act(&self, action: pb::Action) -> Result<pb::ActionResult, EngineError> {
        let (tx, rx) = mpsc::channel();
        let _ = self.tx.send(ServoCommand::Act { action, respond_to: tx });
        rx.recv().unwrap_or_else(|_| Err(EngineError::new("unavailable", "servo runtime unavailable")))
    }

    fn stream_event(&self, event_type: pb::StreamEventType) -> Result<pb::StreamEvent, EngineError> {
        let (tx, rx) = mpsc::channel();
        let _ = self.tx.send(ServoCommand::StreamEvent {
            event_type,
            respond_to: tx,
        });
        rx.recv().unwrap_or_else(|_| Err(EngineError::new("unavailable", "servo runtime unavailable")))
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
}

fn run_servo_runtime(
    config: pb::SessionConfig,
    rx: mpsc::Receiver<ServoCommand>,
) -> Result<(), EngineError> {
    // Get viewport dimensions
    let (width, height) = if let Some(ref viewport) = config.viewport {
        (
            if viewport.width > 0 { viewport.width } else { 1280 },
            if viewport.height > 0 { viewport.height } else { 720 },
        )
    } else {
        (1280, 720)
    };
    let size = PhysicalSize::new(width, height);

    // Initialize rendering context
    let rendering_context: Rc<dyn RenderingContext> = Rc::new(
        SoftwareRenderingContext::new(size)
            .map_err(|e| EngineError::new("rendering_init", format!("failed to create rendering context: {:?}", e)))?
    );

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
            ServoCommand::StreamEvent { event_type, respond_to } => {
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

    // Spin until page loads (simplified - real impl would track load events)
    for _ in 0..100 {
        state.servo.spin_event_loop();
        std::thread::sleep(std::time::Duration::from_millis(50));
    }

    state.state_version += 1;
    state.current_url = url_str.to_string();

    // Get page title if available
    if let Some(ref webview) = state.webview {
        if let Some(title) = webview.page_title() {
            state.current_title = title;
        }
    }

    build_observation(state, &pb::ObserveOptions::default())
}

fn handle_observe(state: &mut ServoState, opts: &pb::ObserveOptions) -> Result<pb::Observation, EngineError> {
    // Pump event loop
    state.servo.spin_event_loop();

    build_observation(state, opts)
}

fn handle_act(state: &mut ServoState, action: &pb::Action) -> Result<pb::ActionResult, EngineError> {
    let _webview = state.webview.as_ref()
        .ok_or_else(|| EngineError::new("no_webview", "no webview active - navigate first"))?;

    // Check state version if provided
    if action.expected_state_version > 0 && action.expected_state_version != state.state_version {
        return Err(EngineError::new(
            "stale_state",
            format!("expected state version {} but current is {}",
                action.expected_state_version, state.state_version)
        ));
    }

    // Dispatch action based on type
    let action_type = pb::ActionType::try_from(action.r#type).unwrap_or(pb::ActionType::Unspecified);
    match action_type {
        pb::ActionType::Click => {
            log::debug!("Click action: target={:?}", action.target);
        }
        pb::ActionType::Type => {
            log::debug!("Type action: text={}", action.text);
        }
        pb::ActionType::Scroll => {
            log::debug!("Scroll action: delta={:?}", action.scroll);
        }
        pb::ActionType::Hover => {
            log::debug!("Hover action: target={:?}", action.target);
        }
        pb::ActionType::Key => {
            log::debug!("Key action: key={}", action.key);
        }
        pb::ActionType::Focus => {
            log::debug!("Focus action: target={:?}", action.target);
        }
        pb::ActionType::ClipboardRead => {
            log::debug!("Clipboard read action");
        }
        pb::ActionType::ClipboardWrite => {
            log::debug!("Clipboard write: {} bytes", action.text.len());
        }
        pb::ActionType::Unspecified => {
            log::warn!("Unspecified action type");
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

fn handle_stream_event(state: &mut ServoState, event_type: pb::StreamEventType) -> Result<pb::StreamEvent, EngineError> {
    state.servo.spin_event_loop();

    Ok(pb::StreamEvent {
        r#type: event_type as i32,
        state_version: state.state_version,
        timestamp: Some(timestamp_now()),
        frame: None,
        dom_diff: vec![],
        accessibility_diff: vec![],
        hit_test: None,
    })
}

fn build_observation(state: &ServoState, opts: &pb::ObserveOptions) -> Result<pb::Observation, EngineError> {
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

    // TODO: DOM snapshot extraction
    if opts.include_dom_snapshot {
        obs.dom_snapshot = b"{}".to_vec();
    }

    // TODO: Accessibility tree extraction
    if opts.include_accessibility {
        obs.accessibility_tree = b"{}".to_vec();
    }

    // TODO: Hit test map
    if opts.include_hit_test {
        obs.hit_test = Some(pb::HitTestMap {
            width: state.viewport_width,
            height: state.viewport_height,
            regions: vec![],
        });
    }

    Ok(obs)
}

fn capture_frame(state: &ServoState) -> Option<pb::Frame> {
    use servo::{DeviceIntRect, DeviceIntPoint, DeviceIntSize};

    let rect = DeviceIntRect::from_origin_and_size(
        DeviceIntPoint::new(0, 0),
        DeviceIntSize::new(
            state.viewport_width as i32,
            state.viewport_height as i32,
        ),
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
