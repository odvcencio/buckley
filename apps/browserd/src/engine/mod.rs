use crate::proto as pb;
use url::Url;

mod stub;
#[cfg(feature = "servo")]
mod servo;

pub struct EngineError {
    pub code: &'static str,
    pub message: String,
}

impl EngineError {
    pub fn new(code: &'static str, message: impl Into<String>) -> Self {
        Self {
            code,
            message: message.into(),
        }
    }
}

pub trait BrowserEngine: Send {
    fn state_version(&self) -> u64;
    fn frame_rate(&self) -> u32;
    fn navigate(&mut self, url: &str) -> Result<pb::Observation, EngineError>;
    fn observe(&mut self, opts: &pb::ObserveOptions) -> Result<pb::Observation, EngineError>;
    fn act(&mut self, action: &pb::Action) -> Result<pb::ActionResult, EngineError>;
    fn stream_event(&mut self, event_type: pb::StreamEventType) -> Result<pb::StreamEvent, EngineError>;
}

pub fn new_engine(config: &pb::SessionConfig) -> Result<Box<dyn BrowserEngine>, EngineError> {
    #[cfg(feature = "servo")]
    {
        let engine = servo::ServoEngine::new(config)?;
        return Ok(Box::new(engine));
    }
    #[cfg(not(feature = "servo"))]
    {
        let engine = stub::StubEngine::new(config)?;
        return Ok(Box::new(engine));
    }
}

/// Check whether `host` (with optional `port`) matches any entry in `allowlist`.
pub(crate) fn allowlist_allows(host: &str, port: Option<u16>, allowlist: &[String]) -> bool {
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

/// Parse an allowlist entry into a `(host, optional_port)` pair.
pub(crate) fn parse_allowlist_entry(entry: &str) -> (String, Option<u16>) {
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
