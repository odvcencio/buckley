use prost::Message;
use std::collections::HashMap;
use std::env;
use std::fs::{self, OpenOptions};
use std::io;
use std::io::Read;
use std::io::Write;
use std::os::unix::net::{UnixListener, UnixStream};
use std::path::{Path, PathBuf};
use std::sync::{Arc, Mutex};
use std::thread;
use std::time::{Duration, SystemTime, UNIX_EPOCH};
use url::Url;

mod engine;

mod proto {
    include!(concat!(env!("OUT_DIR"), "/buckley.browserd.v1.rs"));
}

use engine::{BrowserEngine, EngineError};
use proto as pb;

const DEFAULT_SOCKET: &str = "/tmp/buckley/browserd.sock";
const DEFAULT_FRAME_RATE: u32 = 12;

struct Args {
    socket: PathBuf,
    session_id: Option<String>,
}

struct SessionEntry {
    session_id: String,
    allowlist: Vec<String>,
    engine: Box<dyn BrowserEngine>,
}

type SharedSessions = Arc<Mutex<HashMap<String, SessionEntry>>>;

fn main() -> io::Result<()> {
    let args = match parse_args() {
        Ok(args) => args,
        Err(err) => {
            eprintln!("{err}");
            print_usage();
            std::process::exit(2);
        }
    };

    run(args)
}

fn run(args: Args) -> io::Result<()> {
    let socket_path = args.socket;
    ensure_socket_dir(&socket_path)?;
    remove_existing_socket(&socket_path)?;
    apply_security_config(&SecurityConfig::from_env())?;

    let _guard = SocketGuard::new(socket_path.clone());
    let listener = UnixListener::bind(&socket_path)?;
    eprintln!("browserd listening on {}", socket_path.display());

    let sessions: SharedSessions = Arc::new(Mutex::new(HashMap::new()));
    let audit_logger = AuditLogger::from_env();

    for stream in listener.incoming() {
        match stream {
            Ok(stream) => {
                let sessions = Arc::clone(&sessions);
                if let Err(err) = handle_connection(
                    stream,
                    args.session_id.as_deref(),
                    sessions,
                    audit_logger.as_ref(),
                ) {
                    eprintln!("connection error: {err}");
                }
            }
            Err(err) => eprintln!("accept error: {err}"),
        }
    }

    Ok(())
}

fn handle_connection(
    mut stream: UnixStream,
    session_id: Option<&str>,
    sessions: SharedSessions,
    audit_logger: Option<&AuditLogger>,
) -> io::Result<()> {
    let default_session_id = session_id.unwrap_or_default().to_string();

    loop {
        let envelope = match read_envelope(&mut stream)? {
            Some(env) => env,
            None => return Ok(()),
        };

        let req = match envelope.message {
            Some(pb::envelope::Message::Request(req)) => req,
            _ => {
                let resp = error_response("", "", "invalid_request", "expected request");
                write_envelope(&mut stream, resp)?;
                continue;
            }
        };

        match handle_request(req, &default_session_id, &sessions, audit_logger) {
            RequestOutcome::Response(resp, should_close) => {
                write_envelope(&mut stream, resp)?;
                if should_close {
                    return Ok(());
                }
            }
            RequestOutcome::Stream(plan) => {
                write_envelope(&mut stream, plan.response)?;
                stream_events(&mut stream, &plan.session_id, &sessions, &plan.options)?;
                return Ok(());
            }
        }
    }
}

enum RequestOutcome {
    Response(pb::Envelope, bool),
    Stream(StreamPlan),
}

struct StreamPlan {
    response: pb::Envelope,
    session_id: String,
    options: StreamSettings,
}

struct StreamSettings {
    include_frames: bool,
    include_dom_diffs: bool,
    include_accessibility_diffs: bool,
    include_hit_test: bool,
    target_fps: u32,
}

struct AuditLogger {
    dir: PathBuf,
}

impl AuditLogger {
    fn from_env() -> Option<Self> {
        let dir = env::var("BROWSERD_AUDIT_LOG_DIR")
            .unwrap_or_else(|_| "/tmp/buckley/browserd/audit".to_string());
        let trimmed = dir.trim();
        if trimmed.is_empty()
            || trimmed.eq_ignore_ascii_case("off")
            || trimmed.eq_ignore_ascii_case("disabled")
        {
            return None;
        }
        Some(Self {
            dir: PathBuf::from(trimmed),
        })
    }

    fn write_line(&self, session_id: &str, line: &str) {
        if let Err(err) = fs::create_dir_all(&self.dir) {
            eprintln!("audit log: {err}");
            return;
        }
        let file_name = format!("{}.jsonl", sanitize_session_id(session_id));
        let path = self.dir.join(file_name);
        match OpenOptions::new().create(true).append(true).open(path) {
            Ok(mut file) => {
                if let Err(err) = file.write_all(line.as_bytes()) {
                    eprintln!("audit log: {err}");
                }
            }
            Err(err) => eprintln!("audit log: {err}"),
        }
    }
}

struct SecurityConfig {
    enforce_non_root: bool,
    require_seccomp: bool,
    require_cgroup: bool,
    require_readonly_root: bool,
    require_netns: bool,
    assume_external: bool,
    strict: bool,
    downloads_enabled: bool,
    js_budget_ms: Option<u64>,
    dom_mutation_limit: Option<u64>,
}

impl SecurityConfig {
    fn from_env() -> Self {
        Self {
            enforce_non_root: env_bool("BROWSERD_SECURITY_ENFORCE_NON_ROOT"),
            require_seccomp: env_bool("BROWSERD_SECURITY_REQUIRE_SECCOMP"),
            require_cgroup: env_bool("BROWSERD_SECURITY_REQUIRE_CGROUP"),
            require_readonly_root: env_bool("BROWSERD_SECURITY_REQUIRE_READONLY_ROOT"),
            require_netns: env_bool("BROWSERD_SECURITY_REQUIRE_NETNS"),
            assume_external: env_bool("BROWSERD_SECURITY_ASSUME_EXTERNAL"),
            strict: env_bool("BROWSERD_SECURITY_STRICT"),
            downloads_enabled: env_bool("BROWSERD_SECURITY_DOWNLOADS_ENABLED"),
            js_budget_ms: env_u64("BROWSERD_SECURITY_JS_BUDGET_MS"),
            dom_mutation_limit: env_u64("BROWSERD_SECURITY_DOM_MUTATION_LIMIT"),
        }
    }
}

fn handle_request(
    req: pb::Request,
    default_session_id: &str,
    sessions: &SharedSessions,
    audit_logger: Option<&AuditLogger>,
) -> RequestOutcome {
    let request_id = req.request_id.clone();
    let session_id = resolve_session_id(&req.session_id, default_session_id);

    match req.payload {
        Some(pb::request::Payload::CreateSession(create)) => {
            let mut config = create.config.unwrap_or_default();
            let requested_id = if !config.session_id.is_empty() {
                config.session_id.clone()
            } else {
                session_id.clone()
            };
            if requested_id.is_empty() {
                return RequestOutcome::Response(
                    error_response(&request_id, &session_id, "invalid_request", "session_id is required"),
                    false,
                );
            }
            config.session_id = requested_id.clone();
            if !config.initial_url.is_empty() {
                if let Err(message) = validate_url(&config.initial_url, &config.network_allowlist)
                {
                    return RequestOutcome::Response(
                        error_response(&request_id, &requested_id, "invalid_request", &message),
                        false,
                    );
                }
            }
            let engine = match engine::new_engine(&config) {
                Ok(engine) => engine,
                Err(err) => {
                    return RequestOutcome::Response(
                        engine_error_response(&request_id, &requested_id, err),
                        false,
                    );
                }
            };
            let mut entry = SessionEntry {
                session_id: requested_id.clone(),
                allowlist: config.network_allowlist.clone(),
                engine,
            };
            let observe_opts = pb::ObserveOptions {
                include_frame: false,
                include_dom_snapshot: true,
                include_accessibility: true,
                include_hit_test: false,
            };
            let observation = match entry.engine.observe(&observe_opts) {
                Ok(obs) => obs,
                Err(err) => {
                    return RequestOutcome::Response(
                        engine_error_response(&request_id, &requested_id, err),
                        false,
                    );
                }
            };
            let response = pb::CreateSessionResponse {
                session: Some(pb::SessionInfo {
                    session_id: entry.session_id.clone(),
                    state_version: observation.state_version,
                    url: observation.url.clone(),
                }),
                observation: Some(observation),
            };
            insert_session(sessions, entry);
            RequestOutcome::Response(
                wrap_response(
                    request_id,
                    requested_id,
                    pb::response::Payload::CreateSession(response),
                ),
                false,
            )
        }
        Some(pb::request::Payload::Navigate(navigate)) => {
            if navigate.url.is_empty() {
                return RequestOutcome::Response(
                    error_response(&request_id, &session_id, "invalid_request", "url is required"),
                    false,
                );
            }
            let result = with_session(sessions, &session_id, |entry| {
                if let Err(message) = validate_url(&navigate.url, &entry.allowlist) {
                    return Err(EngineError::new("invalid_request", message));
                }
                entry.engine.navigate(&navigate.url)
            });
            let observation = match result {
                Some(Ok(obs)) => obs,
                Some(Err(err)) => {
                    return RequestOutcome::Response(
                        engine_error_response(&request_id, &session_id, err),
                        false,
                    );
                }
                None => {
                    return RequestOutcome::Response(
                        error_response(&request_id, &session_id, "invalid_session", "session not initialized"),
                        false,
                    );
                }
            };
            log_audit_navigation(audit_logger, &session_id, &navigate.url);
            let response = pb::NavigateResponse {
                observation: Some(observation),
            };
            RequestOutcome::Response(
                wrap_response(
                    request_id,
                    session_id,
                    pb::response::Payload::Navigate(response),
                ),
                false,
            )
        }
        Some(pb::request::Payload::Observe(observe)) => {
            let opts = observe.options.unwrap_or_default();
            let result = with_session(sessions, &session_id, |entry| entry.engine.observe(&opts));
            let observation = match result {
                Some(Ok(obs)) => obs,
                Some(Err(err)) => {
                    return RequestOutcome::Response(
                        engine_error_response(&request_id, &session_id, err),
                        false,
                    );
                }
                None => {
                    return RequestOutcome::Response(
                        error_response(&request_id, &session_id, "invalid_session", "session not initialized"),
                        false,
                    );
                }
            };
            let response = pb::ObserveResponse {
                observation: Some(observation),
            };
            RequestOutcome::Response(
                wrap_response(
                    request_id,
                    session_id,
                    pb::response::Payload::Observe(response),
                ),
                false,
            )
        }
        Some(pb::request::Payload::Act(act)) => {
            let action = match act.action {
                Some(action) => action,
                None => {
                    return RequestOutcome::Response(
                        error_response(&request_id, &session_id, "invalid_request", "action is required"),
                        false,
                    );
                }
            };
            let expected_state = action.expected_state_version;
            let result = with_session(sessions, &session_id, |entry| {
                if expected_state != 0 && expected_state != entry.engine.state_version() {
                    return Err(EngineError::new("stale_state", "stale state version"));
                }
                entry.engine.act(&action)
            });
            let action_result = match result {
                Some(Ok(res)) => res,
                Some(Err(err)) => {
                    return RequestOutcome::Response(
                        engine_error_response(&request_id, &session_id, err),
                        false,
                    );
                }
                None => {
                    return RequestOutcome::Response(
                        error_response(&request_id, &session_id, "invalid_session", "session not initialized"),
                        false,
                    );
                }
            };
            log_audit_action(audit_logger, &session_id, &action, action_result.state_version);
            let response = pb::ActResponse {
                result: Some(action_result),
            };
            RequestOutcome::Response(
                wrap_response(
                    request_id,
                    session_id,
                    pb::response::Payload::Act(response),
                ),
                false,
            )
        }
        Some(pb::request::Payload::CloseSession(_close)) => {
            if !remove_session(sessions, &session_id) {
                return RequestOutcome::Response(
                    error_response(&request_id, &session_id, "invalid_session", "session not initialized"),
                    true,
                );
            }
            let response = pb::CloseSessionResponse { closed: true };
            RequestOutcome::Response(
                wrap_response(
                    request_id,
                    session_id.clone(),
                    pb::response::Payload::CloseSession(response),
                ),
                true,
            )
        }
        Some(pb::request::Payload::StreamSubscribe(stream)) => {
            let default_fps = match with_session(sessions, &session_id, |entry| entry.engine.frame_rate()) {
                Some(rate) => rate,
                None => {
                    return RequestOutcome::Response(
                        error_response(&request_id, &session_id, "invalid_session", "session not initialized"),
                        false,
                    );
                }
            };
            let options = normalize_stream_options(stream.options, default_fps);
            let response = wrap_response(
                request_id,
                session_id.clone(),
                pb::response::Payload::StreamSubscribe(pb::StreamSubscribeResponse { subscribed: true }),
            );
            RequestOutcome::Stream(StreamPlan {
                response,
                session_id,
                options,
            })
        }
        None => RequestOutcome::Response(
            error_response(&request_id, &session_id, "invalid_request", "missing payload"),
            false,
        ),
    }
}

fn engine_error_response(request_id: &str, session_id: &str, err: EngineError) -> pb::Envelope {
    error_response(request_id, session_id, err.code, &err.message)
}

fn resolve_session_id(requested: &str, default_session_id: &str) -> String {
    if !requested.is_empty() {
        requested.to_string()
    } else {
        default_session_id.to_string()
    }
}

fn insert_session(sessions: &SharedSessions, entry: SessionEntry) {
    let mut map = sessions.lock().unwrap();
    map.insert(entry.session_id.clone(), entry);
}

fn with_session<T, F>(sessions: &SharedSessions, session_id: &str, op: F) -> Option<T>
where
    F: FnOnce(&mut SessionEntry) -> T,
{
    let mut map = sessions.lock().unwrap();
    let entry = map.get_mut(session_id)?;
    Some(op(entry))
}

fn remove_session(sessions: &SharedSessions, session_id: &str) -> bool {
    let mut map = sessions.lock().unwrap();
    map.remove(session_id).is_some()
}

fn normalize_stream_options(
    options: Option<pb::StreamOptions>,
    default_fps: u32,
) -> StreamSettings {
    let mut settings = StreamSettings {
        include_frames: false,
        include_dom_diffs: false,
        include_accessibility_diffs: false,
        include_hit_test: false,
        target_fps: default_fps,
    };
    if let Some(opts) = options {
        settings.include_frames = opts.include_frames;
        settings.include_dom_diffs = opts.include_dom_diffs;
        settings.include_accessibility_diffs = opts.include_accessibility_diffs;
        settings.include_hit_test = opts.include_hit_test;
        if opts.target_fps > 0 {
            settings.target_fps = opts.target_fps;
        }
    }
    if !(settings.include_frames
        || settings.include_dom_diffs
        || settings.include_accessibility_diffs
        || settings.include_hit_test)
    {
        settings.include_frames = true;
    }
    if settings.target_fps == 0 {
        settings.target_fps = DEFAULT_FRAME_RATE;
    }
    settings
}

fn stream_events(
    stream: &mut UnixStream,
    session_id: &str,
    sessions: &SharedSessions,
    options: &StreamSettings,
) -> io::Result<()> {
    let mut fps = options.target_fps;
    if fps == 0 {
        fps = DEFAULT_FRAME_RATE;
    }
    let interval_ms = std::cmp::max(1, 1000 / fps) as u64;

    loop {
        let mut send_event = |event_type| -> io::Result<bool> {
            let result = with_session(sessions, session_id, |entry| entry.engine.stream_event(event_type));
            let event = match result {
                Some(Ok(event)) => event,
                Some(Err(_)) => return Ok(false),
                None => return Ok(false),
            };
            write_envelope(stream, wrap_event(event))?;
            Ok(true)
        };

        if options.include_frames && !send_event(pb::StreamEventType::Frame)? {
            return Ok(());
        }
        if options.include_dom_diffs && !send_event(pb::StreamEventType::DomDiff)? {
            return Ok(());
        }
        if options.include_accessibility_diffs
            && !send_event(pb::StreamEventType::AccessibilityDiff)?
        {
            return Ok(());
        }
        if options.include_hit_test && !send_event(pb::StreamEventType::HitTest)? {
            return Ok(());
        }

        thread::sleep(Duration::from_millis(interval_ms));
    }
}

fn wrap_event(event: pb::StreamEvent) -> pb::Envelope {
    pb::Envelope {
        message: Some(pb::envelope::Message::Event(event)),
    }
}

fn validate_url(url: &str, allowlist: &[String]) -> Result<(), String> {
    let parsed = Url::parse(url).map_err(|_| "invalid url".to_string())?;
    let scheme = parsed.scheme().to_ascii_lowercase();
    if scheme == "file" || scheme == "data" || scheme == "javascript" {
        return Err(format!("blocked scheme: {scheme}"));
    }
    if scheme == "about" {
        return Ok(());
    }
    if scheme != "http" && scheme != "https" {
        return Err(format!("unsupported scheme: {scheme}"));
    }
    let host = parsed.host_str().ok_or_else(|| "missing host".to_string())?;
    if allowlist.is_empty() {
        return Ok(());
    }
    let port = parsed.port_or_known_default();
    if allowlist_allows(host, port, allowlist) {
        Ok(())
    } else {
        Err("host not in allowlist".to_string())
    }
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

fn apply_security_config(cfg: &SecurityConfig) -> io::Result<()> {
    if cfg.enforce_non_root && unsafe { libc::geteuid() } == 0 {
        return Err(io::Error::new(
            io::ErrorKind::PermissionDenied,
            "browserd must not run as root",
        ));
    }

    let mut unmet = Vec::new();
    if cfg.require_seccomp {
        unmet.push("seccomp");
    }
    if cfg.require_cgroup {
        unmet.push("cgroup");
    }
    if cfg.require_readonly_root {
        unmet.push("read_only_root");
    }
    if cfg.require_netns {
        unmet.push("netns");
    }
    if !unmet.is_empty() && !cfg.assume_external {
        let message = format!(
            "security requirements requested but not enforced: {}",
            unmet.join(", ")
        );
        if cfg.strict {
            return Err(io::Error::new(io::ErrorKind::PermissionDenied, message));
        }
        eprintln!("{message}");
    }

    if cfg.downloads_enabled {
        eprintln!("security: downloads enabled (not enforced by stub runtime)");
    }
    if cfg.js_budget_ms.is_some() {
        eprintln!("security: js budget configured but not enforced by stub runtime");
    }
    if cfg.dom_mutation_limit.is_some() {
        eprintln!("security: dom mutation limit configured but not enforced by stub runtime");
    }

    Ok(())
}

fn env_bool(key: &str) -> bool {
    match env::var(key) {
        Ok(value) => matches!(
            value.trim().to_ascii_lowercase().as_str(),
            "1" | "true" | "yes" | "on"
        ),
        Err(_) => false,
    }
}

fn env_u64(key: &str) -> Option<u64> {
    let value = env::var(key).ok()?;
    if value.trim().is_empty() {
        return None;
    }
    value.trim().parse::<u64>().ok()
}

fn log_audit_navigation(logger: Option<&AuditLogger>, session_id: &str, url: &str) {
    let details = format!("\"url\":\"{}\"", escape_json_string(url));
    log_audit_event(logger, session_id, "navigate", &details);
}

fn log_audit_action(
    logger: Option<&AuditLogger>,
    session_id: &str,
    action: &pb::Action,
    state_version: u64,
) {
    let mut fields = Vec::new();
    fields.push(format!(
        "\"action\":\"{}\"",
        escape_json_string(action_type_name(action.r#type))
    ));
    fields.push(format!("\"state_version\":{state_version}"));
    if action.expected_state_version != 0 {
        fields.push(format!(
            "\"expected_state_version\":{}",
            action.expected_state_version
        ));
    }
    if !action.text.is_empty() {
        fields.push(format!("\"text_len\":{}", action.text.chars().count()));
    }
    if !action.key.is_empty() {
        fields.push(format!("\"key_len\":{}", action.key.chars().count()));
    }
    if let Some(scroll) = action.scroll.as_ref() {
        fields.push(format!("\"scroll_x\":{}", scroll.x));
        fields.push(format!("\"scroll_y\":{}", scroll.y));
        fields.push(format!(
            "\"scroll_unit\":\"{}\"",
            escape_json_string(scroll_unit_name(scroll.unit))
        ));
    }
    if let Some(target) = action.target.as_ref() {
        if target.node_id != 0 {
            fields.push(format!("\"target_node_id\":{}", target.node_id));
        }
        if let Some(point) = target.point.as_ref() {
            fields.push(format!("\"target_x\":{}", point.x));
            fields.push(format!("\"target_y\":{}", point.y));
        }
    }
    log_audit_event(logger, session_id, "action", &fields.join(","));
}

fn log_audit_event(logger: Option<&AuditLogger>, session_id: &str, event: &str, details: &str) {
    let Some(logger) = logger else {
        return;
    };
    let mut line = String::new();
    line.push_str("{\"ts_ms\":");
    line.push_str(&current_millis().to_string());
    line.push_str(",\"event\":\"");
    line.push_str(&escape_json_string(event));
    line.push_str("\",\"session_id\":\"");
    line.push_str(&escape_json_string(session_id));
    line.push_str("\"");
    if !details.trim().is_empty() {
        line.push(',');
        line.push_str(details);
    }
    line.push_str("}\n");
    logger.write_line(session_id, &line);
}

fn current_millis() -> u128 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_millis()
}

fn action_type_name(action_type: i32) -> &'static str {
    match pb::ActionType::try_from(action_type).unwrap_or(pb::ActionType::Unspecified) {
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

fn scroll_unit_name(unit: i32) -> &'static str {
    match pb::ScrollUnit::try_from(unit).unwrap_or(pb::ScrollUnit::Unspecified) {
        pb::ScrollUnit::Pixels => "pixels",
        pb::ScrollUnit::Lines => "lines",
        pb::ScrollUnit::Unspecified => "units",
    }
}

fn sanitize_session_id(session_id: &str) -> String {
    let mut out = String::new();
    for ch in session_id.chars() {
        if ch.is_ascii_alphanumeric() || ch == '-' || ch == '_' {
            out.push(ch);
        } else {
            out.push('_');
        }
    }
    if out.is_empty() {
        "browser".to_string()
    } else {
        out
    }
}

fn escape_json_string(value: &str) -> String {
    value
        .replace('\\', "\\\\")
        .replace('"', "\\\"")
        .replace('\n', "\\n")
        .replace('\r', "\\r")
        .replace('\t', "\\t")
}

fn wrap_response(
    request_id: String,
    session_id: String,
    payload: pb::response::Payload,
) -> pb::Envelope {
    pb::Envelope {
        message: Some(pb::envelope::Message::Response(pb::Response {
            request_id,
            session_id,
            error: None,
            payload: Some(payload),
        })),
    }
}

fn error_response(request_id: &str, session_id: &str, code: &str, message: &str) -> pb::Envelope {
    pb::Envelope {
        message: Some(pb::envelope::Message::Response(pb::Response {
            request_id: request_id.to_string(),
            session_id: session_id.to_string(),
            error: Some(pb::Error {
                code: code.to_string(),
                message: message.to_string(),
            }),
            payload: None,
        })),
    }
}

fn read_envelope(stream: &mut UnixStream) -> io::Result<Option<pb::Envelope>> {
    let mut len_buf = [0u8; 4];
    if let Err(err) = stream.read_exact(&mut len_buf) {
        if err.kind() == io::ErrorKind::UnexpectedEof {
            return Ok(None);
        }
        return Err(err);
    }
    let len = u32::from_be_bytes(len_buf) as usize;
    if len == 0 {
        return Ok(None);
    }
    let mut buf = vec![0u8; len];
    stream.read_exact(&mut buf)?;
    let envelope = pb::Envelope::decode(&*buf)
        .map_err(|err| io::Error::new(io::ErrorKind::InvalidData, err))?;
    Ok(Some(envelope))
}

fn write_envelope(stream: &mut UnixStream, envelope: pb::Envelope) -> io::Result<()> {
    let mut buf = Vec::new();
    envelope
        .encode(&mut buf)
        .map_err(|err| io::Error::new(io::ErrorKind::InvalidData, err))?;
    if buf.len() > u32::MAX as usize {
        return Err(io::Error::new(
            io::ErrorKind::InvalidData,
            "message too large",
        ));
    }
    let len = (buf.len() as u32).to_be_bytes();
    stream.write_all(&len)?;
    stream.write_all(&buf)?;
    stream.flush()?;
    Ok(())
}

fn ensure_socket_dir(path: &Path) -> io::Result<()> {
    if let Some(parent) = path.parent() {
        if !parent.as_os_str().is_empty() {
            fs::create_dir_all(parent)?;
        }
    }
    Ok(())
}

fn remove_existing_socket(path: &Path) -> io::Result<()> {
    if path.exists() {
        fs::remove_file(path)?;
    }
    Ok(())
}

fn parse_args() -> Result<Args, String> {
    let mut socket = env::var("BROWSERD_SOCKET").unwrap_or_else(|_| DEFAULT_SOCKET.to_string());
    let mut session_id = env::var("BROWSERD_SESSION_ID").ok();

    let mut args = env::args().skip(1);
    while let Some(arg) = args.next() {
        match arg.as_str() {
            "--socket" => {
                socket = args
                    .next()
                    .ok_or_else(|| "missing value for --socket".to_string())?;
            }
            "--session-id" => {
                session_id = Some(
                    args.next()
                        .ok_or_else(|| "missing value for --session-id".to_string())?,
                );
            }
            "-h" | "--help" => {
                print_usage();
                std::process::exit(0);
            }
            "--version" => {
                print_version();
                std::process::exit(0);
            }
            _ => return Err(format!("unknown argument: {arg}")),
        }
    }

    Ok(Args {
        socket: PathBuf::from(socket),
        session_id,
    })
}

fn print_usage() {
    eprintln!(
        "Usage: browserd [--socket <path>] [--session-id <id>]\n\nOptions:\n  --socket <path>       Unix socket path (env: BROWSERD_SOCKET)\n  --session-id <id>     Optional session identifier (env: BROWSERD_SESSION_ID)\n  -h, --help            Show this help message\n  --version             Show version"
    );
}

fn print_version() {
    println!("browserd {}", env!("CARGO_PKG_VERSION"));
}

struct SocketGuard {
    path: PathBuf,
}

impl SocketGuard {
    fn new(path: PathBuf) -> Self {
        Self { path }
    }
}

impl Drop for SocketGuard {
    fn drop(&mut self) {
        let _ = fs::remove_file(&self.path);
    }
}
