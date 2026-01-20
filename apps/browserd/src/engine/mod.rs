use crate::proto as pb;

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
