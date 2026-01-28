use crate::proto as pb;
use super::{BrowserEngine, EngineError};
use prost_types::{value, Struct, Value};
use std::collections::BTreeMap;
use std::time::{Duration, SystemTime, UNIX_EPOCH};
use url::Url;

const DEFAULT_VIEWPORT_WIDTH: u32 = 1280;
const DEFAULT_VIEWPORT_HEIGHT: u32 = 720;
const DEFAULT_FRAME_RATE: u32 = 12;
const ROOT_NODE_ID: u64 = 1;
const BUTTON_NODE_ID: u64 = 2;
const INPUT_NODE_ID: u64 = 3;
const DEFAULT_CLIPBOARD_MAX_BYTES: usize = 64 * 1024;

pub struct StubEngine {
    url: String,
    title: String,
    state_version: u64,
    viewport_width: u32,
    viewport_height: u32,
    frame_rate: u32,
    last_action: String,
    last_action_detail: String,
    last_text_len: usize,
    last_key: String,
    scroll_x: i32,
    scroll_y: i32,
    focused_node: u64,
    hovered_node: u64,
    clipboard_mode: pb::ClipboardMode,
    clipboard_allow_read: bool,
    clipboard_allow_write: bool,
    clipboard_max_bytes: usize,
    clipboard_read_allowlist: Vec<String>,
    clipboard_text: String,
}

impl StubEngine {
    pub fn new(config: &pb::SessionConfig) -> Result<Self, EngineError> {
        if config.session_id.trim().is_empty() {
            return Err(EngineError::new("invalid_request", "session_id is required"));
        }
        let mut clipboard_mode = pb::ClipboardMode::Virtual;
        let mut clipboard_allow_read = false;
        let mut clipboard_allow_write = true;
        let mut clipboard_max_bytes = DEFAULT_CLIPBOARD_MAX_BYTES;
        let mut clipboard_read_allowlist = Vec::new();
        if let Some(policy) = config.clipboard.as_ref() {
            let mode = pb::ClipboardMode::try_from(policy.mode).unwrap_or(pb::ClipboardMode::Unspecified);
            if mode != pb::ClipboardMode::Unspecified {
                clipboard_mode = mode;
            }
            clipboard_allow_read = policy.allow_read;
            clipboard_allow_write = policy.allow_write;
            if policy.max_bytes > 0 {
                clipboard_max_bytes = policy.max_bytes as usize;
            }
            if !policy.read_allowlist.is_empty() {
                clipboard_read_allowlist = policy.read_allowlist.clone();
            }
        }
        let mut engine = StubEngine {
            url: "about:blank".to_string(),
            title: "Stub Page".to_string(),
            state_version: 1,
            viewport_width: DEFAULT_VIEWPORT_WIDTH,
            viewport_height: DEFAULT_VIEWPORT_HEIGHT,
            frame_rate: DEFAULT_FRAME_RATE,
            last_action: "idle".to_string(),
            last_action_detail: "ready".to_string(),
            last_text_len: 0,
            last_key: String::new(),
            scroll_x: 0,
            scroll_y: 0,
            focused_node: INPUT_NODE_ID,
            hovered_node: 0,
            clipboard_mode,
            clipboard_allow_read,
            clipboard_allow_write,
            clipboard_max_bytes,
            clipboard_read_allowlist,
            clipboard_text: String::new(),
        };
        if let Some(viewport) = &config.viewport {
            if viewport.width > 0 {
                engine.viewport_width = viewport.width;
            }
            if viewport.height > 0 {
                engine.viewport_height = viewport.height;
            }
        }
        if config.frame_rate > 0 {
            engine.frame_rate = config.frame_rate;
        }
        if !config.initial_url.is_empty() {
            engine.url = config.initial_url.clone();
        }
        Ok(engine)
    }

    fn bump_state(&mut self) {
        self.state_version = self.state_version.saturating_add(1);
    }

    fn build_observation(
        &self,
        include_dom: bool,
        include_accessibility: bool,
        include_frame: bool,
        include_hit_test: bool,
    ) -> pb::Observation {
        let dom = if include_dom {
            self.dom_snapshot_json().into_bytes()
        } else {
            Vec::new()
        };
        let a11y = if include_accessibility {
            self.accessibility_snapshot_json().into_bytes()
        } else {
            Vec::new()
        };
        pb::Observation {
            state_version: self.state_version,
            url: self.url.clone(),
            title: self.title.clone(),
            frame: if include_frame { Some(self.build_frame()) } else { None },
            dom_snapshot: dom,
            accessibility_tree: a11y,
            hit_test: if include_hit_test {
                Some(self.build_hit_test_map())
            } else {
                None
            },
            timestamp: Some(timestamp_now()),
        }
    }

    fn dom_snapshot_json(&self) -> String {
        format!(
            "{{\"url\":\"{}\",\"title\":\"{}\",\"state_version\":{},\"last_action\":\"{}\",\"last_action_detail\":\"{}\",\"last_text_len\":{},\"last_key\":\"{}\",\"scroll\":{{\"x\":{},\"y\":{}}},\"focused_node\":{},\"hovered_node\":{}}}",
            escape_json_string(&self.url),
            escape_json_string(&self.title),
            self.state_version,
            escape_json_string(&self.last_action),
            escape_json_string(&self.last_action_detail),
            self.last_text_len,
            escape_json_string(&self.last_key),
            self.scroll_x,
            self.scroll_y,
            self.focused_node,
            self.hovered_node
        )
    }

    fn accessibility_snapshot_json(&self) -> String {
        format!(
            "{{\"role\":\"document\",\"name\":\"{}\",\"focused_node\":{},\"hovered_node\":{},\"children\":[{{\"role\":\"button\",\"name\":\"Stub Button\",\"node_id\":{}}},{{\"role\":\"textbox\",\"name\":\"Stub Input\",\"node_id\":{}}}]}}",
            escape_json_string(&self.title),
            self.focused_node,
            self.hovered_node,
            BUTTON_NODE_ID,
            INPUT_NODE_ID
        )
    }

    fn dom_diff_json(&self) -> String {
        let snapshot = self.dom_snapshot_json();
        format!(
            "{{\"type\":\"replace\",\"state_version\":{},\"snapshot\":{}}}",
            self.state_version, snapshot
        )
    }

    fn accessibility_diff_json(&self) -> String {
        let snapshot = self.accessibility_snapshot_json();
        format!(
            "{{\"type\":\"replace\",\"state_version\":{},\"snapshot\":{}}}",
            self.state_version, snapshot
        )
    }

    fn build_stream_event(&self, event_type: pb::StreamEventType) -> pb::StreamEvent {
        let mut event = pb::StreamEvent {
            r#type: event_type as i32,
            state_version: self.state_version,
            frame: None,
            dom_diff: Vec::new(),
            accessibility_diff: Vec::new(),
            hit_test: None,
            timestamp: Some(timestamp_now()),
        };

        match event_type {
            pb::StreamEventType::Frame => {
                event.frame = Some(self.build_frame());
            }
            pb::StreamEventType::DomDiff => {
                event.dom_diff = self.dom_diff_json().into_bytes();
            }
            pb::StreamEventType::AccessibilityDiff => {
                event.accessibility_diff = self.accessibility_diff_json().into_bytes();
            }
            pb::StreamEventType::HitTest => {
                event.hit_test = Some(self.build_hit_test_map());
            }
            pb::StreamEventType::Unspecified => {}
        }

        event
    }

    fn build_frame(&self) -> pb::Frame {
        pb::Frame {
            state_version: self.state_version,
            width: self.viewport_width,
            height: self.viewport_height,
            format: pb::FrameFormat::Png as i32,
            data: Vec::new(),
            timestamp: Some(timestamp_now()),
        }
    }

    fn build_hit_test_map(&self) -> pb::HitTestMap {
        let (button_rect, input_rect) = self.control_regions();
        let root_rect = self.viewport_rect();
        pb::HitTestMap {
            width: self.viewport_width,
            height: self.viewport_height,
            regions: vec![
                pb::HitRegion {
                    node_id: BUTTON_NODE_ID,
                    bounds: Some(button_rect),
                },
                pb::HitRegion {
                    node_id: INPUT_NODE_ID,
                    bounds: Some(input_rect),
                },
                pb::HitRegion {
                    node_id: ROOT_NODE_ID,
                    bounds: Some(root_rect),
                },
            ],
        }
    }

    fn viewport_rect(&self) -> pb::Rect {
        pb::Rect {
            x: 0,
            y: 0,
            width: self.viewport_width as i32,
            height: self.viewport_height as i32,
        }
    }

    fn control_regions(&self) -> (pb::Rect, pb::Rect) {
        let vw = self.viewport_width.max(1);
        let vh = self.viewport_height.max(1);
        let button_width = (vw / 3).max(1);
        let button_height = (vh / 6).max(1);
        let button_x = (vw.saturating_sub(button_width)) / 2;
        let button_y = (vh / 3).saturating_sub(button_height / 2);

        let input_width = (vw / 2).max(1);
        let input_height = (vh / 8).max(1);
        let input_x = (vw.saturating_sub(input_width)) / 2;
        let input_y = (vh * 2 / 3).saturating_sub(input_height / 2);

        (
            pb::Rect {
                x: button_x as i32,
                y: button_y as i32,
                width: button_width as i32,
                height: button_height as i32,
            },
            pb::Rect {
                x: input_x as i32,
                y: input_y as i32,
                width: input_width as i32,
                height: input_height as i32,
            },
        )
    }

    fn hit_test_node_id(&self, point: &pb::Point) -> u64 {
        let (button_rect, input_rect) = self.control_regions();
        if point_in_rect(point, &button_rect) {
            BUTTON_NODE_ID
        } else if point_in_rect(point, &input_rect) {
            INPUT_NODE_ID
        } else {
            ROOT_NODE_ID
        }
    }

    fn resolve_target(&self, target: Option<&pb::ActionTarget>) -> (u64, Option<pb::Point>) {
        if let Some(target) = target {
            if target.node_id != 0 {
                return (target.node_id, None);
            }
            if let Some(point) = target.point.as_ref() {
                return (self.hit_test_node_id(point), Some(point.clone()));
            }
        }
        let fallback = if self.focused_node != 0 {
            self.focused_node
        } else {
            ROOT_NODE_ID
        };
        (fallback, None)
    }

    fn ensure_clipboard_read_allowed(&self) -> Result<(), EngineError> {
        if !self.clipboard_allow_read {
            return Err(EngineError::new("clipboard_denied", "clipboard read not allowed"));
        }
        if self.clipboard_read_allowlist.is_empty() {
            return Ok(());
        }
        let parsed = Url::parse(&self.url)
            .map_err(|_| EngineError::new("clipboard_denied", "clipboard read requires allowed domain"))?;
        let host = parsed
            .host_str()
            .ok_or_else(|| EngineError::new("clipboard_denied", "clipboard read requires allowed domain"))?;
        if allowlist_allows(host, parsed.port_or_known_default(), &self.clipboard_read_allowlist) {
            Ok(())
        } else {
            Err(EngineError::new(
                "clipboard_denied",
                "clipboard read denied by allowlist",
            ))
        }
    }

    fn ensure_clipboard_write_allowed(&self) -> Result<(), EngineError> {
        if !self.clipboard_allow_write {
            return Err(EngineError::new("clipboard_denied", "clipboard write not allowed"));
        }
        Ok(())
    }
}

impl BrowserEngine for StubEngine {
    fn state_version(&self) -> u64 {
        self.state_version
    }

    fn frame_rate(&self) -> u32 {
        self.frame_rate
    }

    fn navigate(&mut self, url: &str) -> Result<pb::Observation, EngineError> {
        if url.trim().is_empty() {
            return Err(EngineError::new("invalid_request", "url is required"));
        }
        self.url = url.to_string();
        self.title = "Stub Page".to_string();
        self.last_action = "navigate".to_string();
        self.last_action_detail = format!("navigate to {}", url);
        self.scroll_x = 0;
        self.scroll_y = 0;
        self.bump_state();
        Ok(self.build_observation(true, true, false, false))
    }

    fn observe(&mut self, opts: &pb::ObserveOptions) -> Result<pb::Observation, EngineError> {
        Ok(self.build_observation(
            opts.include_dom_snapshot,
            opts.include_accessibility,
            opts.include_frame,
            opts.include_hit_test,
        ))
    }

    fn act(&mut self, action: &pb::Action) -> Result<pb::ActionResult, EngineError> {
        let action_type = pb::ActionType::try_from(action.r#type)
            .unwrap_or(pb::ActionType::Unspecified);
        if action_type == pb::ActionType::Unspecified {
            return Err(EngineError::new("invalid_request", "unsupported action type"));
        }

        let (mut target_node, target_point) = self.resolve_target(action.target.as_ref());
        if action_type == pb::ActionType::Type && target_node == ROOT_NODE_ID {
            target_node = INPUT_NODE_ID;
        }

        let mut summary = String::new();
        let mut metadata = None;
        match action_type {
            pb::ActionType::Click => {
                self.focused_node = target_node;
                self.hovered_node = target_node;
                summary = action_point_summary("clicked", target_node, target_point.as_ref());
            }
            pb::ActionType::Type => {
                self.focused_node = target_node;
                self.last_text_len = action.text.chars().count();
                summary = format!("typed {} chars into node {}", self.last_text_len, target_node);
            }
            pb::ActionType::Scroll => {
                if let Some(scroll) = action.scroll.as_ref() {
                    self.scroll_x = self.scroll_x.saturating_add(scroll.x);
                    self.scroll_y = self.scroll_y.saturating_add(scroll.y);
                    summary = format!(
                        "scrolled {} {}",
                        scroll.y,
                        scroll_unit_label(scroll.unit)
                    );
                } else {
                    summary = "scrolled".to_string();
                }
            }
            pb::ActionType::Hover => {
                self.hovered_node = target_node;
                summary = action_point_summary("hovered", target_node, target_point.as_ref());
            }
            pb::ActionType::Key => {
                self.last_key = action.key.clone();
                if self.last_key.is_empty() {
                    summary = "pressed key".to_string();
                } else {
                    summary = format!("pressed key {}", self.last_key);
                }
            }
            pb::ActionType::Focus => {
                self.focused_node = target_node;
                summary = format!("focused node {}", target_node);
            }
            pb::ActionType::ClipboardRead => {
                self.ensure_clipboard_read_allowed()?;
                let bytes = self.clipboard_text.as_bytes().len();
                if bytes > self.clipboard_max_bytes {
                    return Err(EngineError::new("clipboard_limit", "clipboard exceeds size limit"));
                }
                summary = format!("clipboard read {} bytes", bytes);
                metadata = clipboard_metadata(
                    Some(&self.clipboard_text),
                    bytes,
                    clipboard_mode_label(self.clipboard_mode),
                    "virtual",
                );
            }
            pb::ActionType::ClipboardWrite => {
                self.ensure_clipboard_write_allowed()?;
                let bytes = action.text.as_bytes().len();
                if bytes > self.clipboard_max_bytes {
                    return Err(EngineError::new("clipboard_limit", "clipboard exceeds size limit"));
                }
                self.clipboard_text = action.text.clone();
                self.last_text_len = action.text.chars().count();
                summary = format!("clipboard wrote {} bytes", bytes);
                metadata = clipboard_metadata(
                    None,
                    bytes,
                    clipboard_mode_label(self.clipboard_mode),
                    "virtual",
                );
            }
            pb::ActionType::Unspecified => {}
        }

        self.last_action = action_type_label(action_type).to_string();
        self.last_action_detail = summary.clone();
        self.bump_state();
        let result = pb::ActionResult {
            state_version: self.state_version,
            observation: Some(self.build_observation(true, true, false, false)),
            effects: vec![pb::Effect {
                kind: action_type_label(action_type).to_string(),
                summary,
                metadata,
            }],
        };
        Ok(result)
    }

    fn stream_event(&mut self, event_type: pb::StreamEventType) -> Result<pb::StreamEvent, EngineError> {
        Ok(self.build_stream_event(event_type))
    }
}

fn point_in_rect(point: &pb::Point, rect: &pb::Rect) -> bool {
    let x = point.x;
    let y = point.y;
    x >= rect.x && y >= rect.y && x < rect.x + rect.width && y < rect.y + rect.height
}

fn action_type_label(action_type: pb::ActionType) -> &'static str {
    match action_type {
        pb::ActionType::Click => "click",
        pb::ActionType::Type => "type",
        pb::ActionType::Scroll => "scroll",
        pb::ActionType::Hover => "hover",
        pb::ActionType::Key => "key",
        pb::ActionType::Focus => "focus",
        pb::ActionType::ClipboardRead => "clipboard_read",
        pb::ActionType::ClipboardWrite => "clipboard_write",
        pb::ActionType::Unspecified => "unspecified",
    }
}

fn scroll_unit_label(unit: i32) -> &'static str {
    match pb::ScrollUnit::try_from(unit).unwrap_or(pb::ScrollUnit::Unspecified) {
        pb::ScrollUnit::Pixels => "pixels",
        pb::ScrollUnit::Lines => "lines",
        pb::ScrollUnit::Unspecified => "units",
    }
}

fn action_point_summary(action: &str, node_id: u64, point: Option<&pb::Point>) -> String {
    if let Some(point) = point {
        format!("{action} node {node_id} at {},{}", point.x, point.y)
    } else {
        format!("{action} node {node_id}")
    }
}

fn clipboard_mode_label(mode: pb::ClipboardMode) -> &'static str {
    match mode {
        pb::ClipboardMode::Virtual => "virtual",
        pb::ClipboardMode::Host => "host",
        pb::ClipboardMode::Unspecified => "virtual",
    }
}

fn clipboard_metadata(text: Option<&str>, bytes: usize, mode: &str, source: &str) -> Option<Struct> {
    let mut fields = BTreeMap::new();
    fields.insert(
        "bytes".to_string(),
        Value {
            kind: Some(value::Kind::NumberValue(bytes as f64)),
        },
    );
    fields.insert(
        "mode".to_string(),
        Value {
            kind: Some(value::Kind::StringValue(mode.to_string())),
        },
    );
    fields.insert(
        "source".to_string(),
        Value {
            kind: Some(value::Kind::StringValue(source.to_string())),
        },
    );
    if let Some(text) = text {
        fields.insert(
            "text".to_string(),
            Value {
                kind: Some(value::Kind::StringValue(text.to_string())),
            },
        );
    }
    Some(Struct { fields })
}

fn allowlist_allows(host: &str, port: Option<u16>, allowlist: &[String]) -> bool {
    let host = host.to_ascii_lowercase();
    for entry in allowlist {
        let entry = entry.trim();
        if entry.is_empty() {
            continue;
        }
        if let Some(suffix) = entry.strip_prefix("*.") {
            let suffix = suffix.to_ascii_lowercase();
            if host == suffix || host.ends_with(&format!(".{suffix}")) {
                return true;
            }
            continue;
        }
        let (entry_host, entry_port) = parse_allowlist_entry(entry);
        if entry_host.is_empty() || entry_host != host {
            continue;
        }
        if let Some(entry_port) = entry_port {
            if let Some(port) = port {
                if port == entry_port {
                    return true;
                }
            }
            continue;
        }
        return true;
    }
    false
}

fn parse_allowlist_entry(entry: &str) -> (String, Option<u16>) {
    if entry.contains("://") {
        if let Ok(url) = Url::parse(entry) {
            if let Some(host) = url.host_str() {
                return (host.to_ascii_lowercase(), url.port());
            }
        }
    }
    if let Some((host, port_str)) = entry.rsplit_once(':') {
        if port_str.chars().all(|c| c.is_ascii_digit()) && !host.contains(']') {
            if let Ok(port) = port_str.parse::<u16>() {
                return (host.to_ascii_lowercase(), Some(port));
            }
        }
    }
    (entry.to_ascii_lowercase(), None)
}

fn escape_json_string(value: &str) -> String {
    value
        .replace('\\', "\\\\")
        .replace('"', "\\\"")
        .replace('\n', "\\n")
        .replace('\r', "\\r")
        .replace('\t', "\\t")
}

fn timestamp_now() -> prost_types::Timestamp {
    let now = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or(Duration::from_secs(0));
    prost_types::Timestamp {
        seconds: now.as_secs() as i64,
        nanos: now.subsec_nanos() as i32,
    }
}
