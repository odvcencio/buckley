use crate::proto as pb;
use super::{BrowserEngine, EngineError};
use std::sync::mpsc;
use std::thread;

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

    fn not_implemented(&self) -> EngineError {
        EngineError::new("not_implemented", "servo engine not wired")
    }
}

impl BrowserEngine for ServoEngine {
    fn state_version(&self) -> u64 {
        0
    }

    fn frame_rate(&self) -> u32 {
        self.frame_rate
    }

    fn navigate(&mut self, url: &str) -> Result<pb::Observation, EngineError> {
        self.runtime.navigate(url.to_string()).map_err(|_| self.not_implemented())
    }

    fn observe(&mut self, opts: &pb::ObserveOptions) -> Result<pb::Observation, EngineError> {
        self.runtime.observe(opts.clone()).map_err(|_| self.not_implemented())
    }

    fn act(&mut self, action: &pb::Action) -> Result<pb::ActionResult, EngineError> {
        self.runtime.act(action.clone()).map_err(|_| self.not_implemented())
    }

    fn stream_event(&mut self, event_type: pb::StreamEventType) -> Result<pb::StreamEvent, EngineError> {
        self.runtime
            .stream_event(event_type)
            .map_err(|_| self.not_implemented())
    }
}

impl Drop for ServoEngine {
    fn drop(&mut self) {
        self.runtime.shutdown();
    }
}

struct ServoRuntime {
    tx: mpsc::Sender<ServoCommand>,
}

impl ServoRuntime {
    fn spawn(config: &pb::SessionConfig) -> Result<Self, EngineError> {
        let (tx, rx) = mpsc::channel();
        let config = config.clone();
        thread::spawn(move || run_servo_runtime(config, rx));
        Ok(Self { tx })
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
    Shutdown,
}

fn run_servo_runtime(_config: pb::SessionConfig, rx: mpsc::Receiver<ServoCommand>) {
    while let Ok(cmd) = rx.recv() {
        match cmd {
            ServoCommand::Navigate { respond_to, .. } => {
                let _ = respond_to.send(Err(EngineError::new(
                    "not_implemented",
                    "servo runtime not wired",
                )));
            }
            ServoCommand::Observe { respond_to, .. } => {
                let _ = respond_to.send(Err(EngineError::new(
                    "not_implemented",
                    "servo runtime not wired",
                )));
            }
            ServoCommand::Act { respond_to, .. } => {
                let _ = respond_to.send(Err(EngineError::new(
                    "not_implemented",
                    "servo runtime not wired",
                )));
            }
            ServoCommand::StreamEvent { respond_to, .. } => {
                let _ = respond_to.send(Err(EngineError::new(
                    "not_implemented",
                    "servo runtime not wired",
                )));
            }
            ServoCommand::Shutdown => {
                break;
            }
        }
    }
}
