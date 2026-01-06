-- Buckley Database Schema
-- Phase 3: Memory & Sessions

-- Schema migrations tracking
CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    applied_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Sessions table: tracks conversation sessions
CREATE TABLE IF NOT EXISTS sessions (
    session_id TEXT PRIMARY KEY,
    principal TEXT,
    project_path TEXT,
    git_repo TEXT,
    git_branch TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    last_active TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    message_count INT DEFAULT 0,
    total_tokens INT DEFAULT 0,
    total_cost REAL DEFAULT 0.0,
    status TEXT NOT NULL DEFAULT 'active' CHECK(status IN ('active','paused','completed')),
    completed_at TIMESTAMP,
    -- Pause state for workflow continuity across restarts
    pause_reason TEXT,
    pause_question TEXT,
    paused_at TIMESTAMP
);

-- Messages table: stores conversation messages
CREATE TABLE IF NOT EXISTS messages (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL,
    role TEXT NOT NULL,
    content TEXT NOT NULL,
    content_json TEXT,
    content_type TEXT NOT NULL DEFAULT 'text',
    reasoning TEXT,
    timestamp TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    tokens INT DEFAULT 0,
    is_summary BOOLEAN DEFAULT FALSE,
    is_truncated BOOLEAN DEFAULT FALSE,
    FOREIGN KEY (session_id) REFERENCES sessions(session_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_messages_session ON messages(session_id);
CREATE INDEX IF NOT EXISTS idx_messages_timestamp ON messages(timestamp);

-- Episodic memories: compacted summaries / decisions for retrieval
CREATE TABLE IF NOT EXISTS memories (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    content TEXT NOT NULL,
    embedding BLOB NOT NULL,
    metadata TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (session_id) REFERENCES sessions(session_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_memories_session ON memories(session_id);
CREATE INDEX IF NOT EXISTS idx_memories_created ON memories(created_at);

-- Embeddings table: vector embeddings for semantic search (Phase 6)
CREATE TABLE IF NOT EXISTS embeddings (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    file_path TEXT NOT NULL UNIQUE,
    content_hash TEXT NOT NULL,
    content TEXT NOT NULL,
    embedding BLOB NOT NULL,
    metadata TEXT,
    source_mod_time TIMESTAMP,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- API calls table: tracks API usage and costs
CREATE TABLE IF NOT EXISTS api_calls (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL,
    model TEXT NOT NULL,
    prompt_tokens INT NOT NULL,
    completion_tokens INT NOT NULL,
    cost REAL NOT NULL,
    timestamp TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (session_id) REFERENCES sessions(session_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_api_calls_session ON api_calls(session_id);
CREATE INDEX IF NOT EXISTS idx_api_calls_timestamp ON api_calls(timestamp);

-- Feature sessions table: links sessions to feature development plans and worktrees (Phase 4)
CREATE TABLE IF NOT EXISTS feature_sessions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL,
    plan_id TEXT NOT NULL,
    task_id TEXT,
    worktree_path TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (session_id) REFERENCES sessions(session_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_feature_sessions_session ON feature_sessions(session_id);
CREATE INDEX IF NOT EXISTS idx_feature_sessions_plan ON feature_sessions(plan_id);

-- Todos table: task lists for systematic plan execution
CREATE TABLE IF NOT EXISTS todos (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL,
    content TEXT NOT NULL,
    active_form TEXT NOT NULL,
    status TEXT NOT NULL CHECK(status IN ('pending', 'in_progress', 'completed', 'failed')),
    order_index INT NOT NULL,
    parent_id INTEGER,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    completed_at TIMESTAMP,
    error_message TEXT,
    metadata TEXT,
    FOREIGN KEY (session_id) REFERENCES sessions(session_id) ON DELETE CASCADE,
    FOREIGN KEY (parent_id) REFERENCES todos(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_todos_session ON todos(session_id);
CREATE INDEX IF NOT EXISTS idx_todos_status ON todos(status);
CREATE INDEX IF NOT EXISTS idx_todos_order ON todos(session_id, order_index);
CREATE INDEX IF NOT EXISTS idx_todos_parent ON todos(parent_id);

-- Todo checkpoints: periodic snapshots for recovery
CREATE TABLE IF NOT EXISTS todo_checkpoints (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL,
    checkpoint_type TEXT NOT NULL CHECK(checkpoint_type IN ('auto', 'manual', 'compaction')),
    todo_count INT NOT NULL,
    completed_count INT NOT NULL,
    conversation_summary TEXT,
    conversation_tokens INT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    metadata TEXT,
    FOREIGN KEY (session_id) REFERENCES sessions(session_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_checkpoints_session ON todo_checkpoints(session_id);
CREATE INDEX IF NOT EXISTS idx_checkpoints_created ON todo_checkpoints(created_at);

-- Session skills table: tracks active skills per session for resumability
CREATE TABLE IF NOT EXISTS session_skills (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL,
    skill_name TEXT NOT NULL,
    activated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    activated_by TEXT NOT NULL CHECK(activated_by IN ('model', 'user', 'phase')),
    scope TEXT NOT NULL,
    is_active BOOLEAN DEFAULT TRUE,
    deactivated_at TIMESTAMP,
    FOREIGN KEY (session_id) REFERENCES sessions(session_id) ON DELETE CASCADE,
    UNIQUE(session_id, skill_name)
);

CREATE INDEX IF NOT EXISTS idx_session_skills_session ON session_skills(session_id);
CREATE INDEX IF NOT EXISTS idx_session_skills_active ON session_skills(session_id, is_active);

-- Executions table: tracks orchestrator execution metadata
CREATE TABLE IF NOT EXISTS executions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    plan_id TEXT NOT NULL,
    task_id TEXT NOT NULL,
    session_id TEXT,
    status TEXT NOT NULL,
    validation_errors TEXT,
    verification_results TEXT,
    artifacts TEXT,
    started_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    completed_at TIMESTAMP,
    execution_time_ms INTEGER,
    retry_count INTEGER DEFAULT 0,
    FOREIGN KEY (session_id) REFERENCES sessions(session_id) ON DELETE SET NULL
);

CREATE INDEX IF NOT EXISTS idx_executions_plan ON executions(plan_id);
CREATE INDEX IF NOT EXISTS idx_executions_task ON executions(task_id);
CREATE INDEX IF NOT EXISTS idx_executions_status ON executions(status);
CREATE INDEX IF NOT EXISTS idx_executions_session ON executions(session_id);

-- RLM scratchpad entries
CREATE TABLE IF NOT EXISTS rlm_scratchpad_entries (
    key TEXT PRIMARY KEY,
    entry_type TEXT NOT NULL,
    raw BLOB,
    summary TEXT,
    metadata TEXT,
    created_by TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_rlm_scratchpad_type ON rlm_scratchpad_entries(entry_type);
CREATE INDEX IF NOT EXISTS idx_rlm_scratchpad_created ON rlm_scratchpad_entries(created_at);

-- File search index (Phase 5)
CREATE TABLE IF NOT EXISTS fs_files (
    path TEXT PRIMARY KEY,
    checksum TEXT,
    language TEXT,
    size_bytes INTEGER,
    summary TEXT,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS fs_symbols (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    file_path TEXT NOT NULL,
    name TEXT NOT NULL,
    kind TEXT NOT NULL,
    signature TEXT,
    start_line INTEGER,
    end_line INTEGER,
    FOREIGN KEY (file_path) REFERENCES fs_files(path) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS fs_imports (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    file_path TEXT NOT NULL,
    import_path TEXT NOT NULL,
    FOREIGN KEY (file_path) REFERENCES fs_files(path) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_fs_symbols_file ON fs_symbols(file_path);
CREATE INDEX IF NOT EXISTS idx_fs_symbols_name ON fs_symbols(name);
CREATE INDEX IF NOT EXISTS idx_fs_imports_file ON fs_imports(file_path);

-- API tokens for remote CLI access
CREATE TABLE IF NOT EXISTS api_tokens (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    token_hash TEXT NOT NULL,
    token_prefix TEXT NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    last_used_at TIMESTAMP,
    revoked INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_api_tokens_hash ON api_tokens(token_hash);

CREATE TABLE IF NOT EXISTS api_token_metadata (
    token_id TEXT PRIMARY KEY,
    owner TEXT,
    scope TEXT NOT NULL DEFAULT 'member',
    FOREIGN KEY (token_id) REFERENCES api_tokens(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS audit_logs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    actor TEXT,
    scope TEXT,
    action TEXT NOT NULL,
    payload TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Operator-configurable settings (e.g., remote metadata)
CREATE TABLE IF NOT EXISTS settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Web auth sessions for hosted UI / CLI
CREATE TABLE IF NOT EXISTS web_sessions (
    id TEXT PRIMARY KEY,
    principal TEXT NOT NULL,
    scope TEXT NOT NULL,
    token_id TEXT,
    expires_at TIMESTAMP NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    last_seen_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_web_sessions_expires ON web_sessions(expires_at);

-- CLI tickets approved in browser (for remote login)
CREATE TABLE IF NOT EXISTS cli_tickets (
    id TEXT PRIMARY KEY,
    secret_hash TEXT NOT NULL,
    label TEXT,
    approved INTEGER NOT NULL DEFAULT 0,
    principal TEXT,
    scope TEXT,
    token_id TEXT,
    session_token TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    expires_at TIMESTAMP NOT NULL,
    consumed INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_cli_tickets_expires ON cli_tickets(expires_at);

-- Session tokens for remote attach (per workflow session)
CREATE TABLE IF NOT EXISTS session_tokens (
    session_id TEXT PRIMARY KEY,
    token_hash TEXT NOT NULL,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (session_id) REFERENCES sessions(session_id) ON DELETE CASCADE
);

-- Mission Control: Pending changes for approval (diff workflow)
CREATE TABLE IF NOT EXISTS pending_changes (
    id TEXT PRIMARY KEY,
    agent_id TEXT NOT NULL,
    session_id TEXT NOT NULL,
    file_path TEXT NOT NULL,
    diff TEXT NOT NULL,
    reason TEXT,
    status TEXT DEFAULT 'pending' CHECK(status IN ('pending', 'approved', 'rejected')),
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    reviewed_at TIMESTAMP,
    reviewed_by TEXT,
    FOREIGN KEY (session_id) REFERENCES sessions(session_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_pending_changes_session ON pending_changes(session_id);
CREATE INDEX IF NOT EXISTS idx_pending_changes_status ON pending_changes(status);
CREATE INDEX IF NOT EXISTS idx_pending_changes_agent ON pending_changes(agent_id);

-- Mission Control: Agent activity tracking
CREATE TABLE IF NOT EXISTS agent_activity (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_id TEXT NOT NULL,
    session_id TEXT NOT NULL,
    agent_type TEXT,
    action TEXT NOT NULL,
    details TEXT,
    status TEXT DEFAULT 'active' CHECK(status IN ('active', 'idle', 'working', 'waiting', 'error', 'stopped')),
    timestamp TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (session_id) REFERENCES sessions(session_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_agent_activity_agent ON agent_activity(agent_id);
CREATE INDEX IF NOT EXISTS idx_agent_activity_session ON agent_activity(session_id);
CREATE INDEX IF NOT EXISTS idx_agent_activity_timestamp ON agent_activity(timestamp);
CREATE INDEX IF NOT EXISTS idx_agent_activity_status ON agent_activity(status);

-- Push notification subscriptions
CREATE TABLE IF NOT EXISTS push_subscriptions (
    id TEXT PRIMARY KEY,
    endpoint TEXT NOT NULL UNIQUE,
    p256dh TEXT NOT NULL,
    auth TEXT NOT NULL,
    principal TEXT NOT NULL,
    user_agent TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_push_subscriptions_principal ON push_subscriptions(principal);
CREATE INDEX IF NOT EXISTS idx_push_subscriptions_endpoint ON push_subscriptions(endpoint);

-- VAPID keys storage (single row)
CREATE TABLE IF NOT EXISTS vapid_keys (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    public_key TEXT NOT NULL,
    private_key TEXT NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Approval policies: configurable rules for tool execution
CREATE TABLE IF NOT EXISTS approval_policies (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    is_active INTEGER NOT NULL DEFAULT 0,
    config TEXT NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_approval_policies_active ON approval_policies(is_active);

-- Pending tool approvals queue
CREATE TABLE IF NOT EXISTS pending_approvals (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL,
    tool_name TEXT NOT NULL,
    tool_input TEXT NOT NULL,
    risk_score INTEGER NOT NULL DEFAULT 0,
    risk_reasons TEXT,
    status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending', 'approved', 'rejected', 'expired', 'auto')),
    decided_by TEXT,
    decided_at TIMESTAMP,
    decision_reason TEXT,
    expires_at TIMESTAMP NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (session_id) REFERENCES sessions(session_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_pending_approvals_session ON pending_approvals(session_id, status);
CREATE INDEX IF NOT EXISTS idx_pending_approvals_expires ON pending_approvals(expires_at);
CREATE INDEX IF NOT EXISTS idx_pending_approvals_status ON pending_approvals(status);

-- Tool execution audit log
CREATE TABLE IF NOT EXISTS tool_audit_log (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL,
    approval_id TEXT,
    tool_name TEXT NOT NULL,
    tool_input TEXT NOT NULL,
    tool_output TEXT,
    risk_score INTEGER,
    decision TEXT NOT NULL CHECK(decision IN ('auto', 'approved', 'rejected')),
    decided_by TEXT,
    executed_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    duration_ms INTEGER,
    FOREIGN KEY (session_id) REFERENCES sessions(session_id) ON DELETE CASCADE,
    FOREIGN KEY (approval_id) REFERENCES pending_approvals(id) ON DELETE SET NULL
);

CREATE INDEX IF NOT EXISTS idx_tool_audit_session ON tool_audit_log(session_id);
CREATE INDEX IF NOT EXISTS idx_tool_audit_time ON tool_audit_log(executed_at);
CREATE INDEX IF NOT EXISTS idx_tool_audit_tool ON tool_audit_log(tool_name);

-- Experiments table: groups related variant runs
CREATE TABLE IF NOT EXISTS experiments (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    description TEXT,
    hypothesis TEXT,
    task_prompt TEXT NOT NULL,
    task_context TEXT,
    task_working_dir TEXT,
    task_timeout_ms INTEGER,
    status TEXT NOT NULL DEFAULT 'pending'
        CHECK(status IN ('pending', 'running', 'completed', 'failed', 'cancelled')),
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    completed_at TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_experiments_status ON experiments(status);
CREATE INDEX IF NOT EXISTS idx_experiments_created ON experiments(created_at);

-- Experiment variants: different configurations to test
CREATE TABLE IF NOT EXISTS experiment_variants (
    id TEXT PRIMARY KEY,
    experiment_id TEXT NOT NULL,
    name TEXT NOT NULL,
    model_id TEXT NOT NULL,
    provider_id TEXT,
    system_prompt TEXT,
    temperature REAL,
    max_tokens INTEGER,
    tools_allowed TEXT,
    custom_config TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (experiment_id) REFERENCES experiments(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_variants_experiment ON experiment_variants(experiment_id);

-- Experiment runs: individual executions of variants
CREATE TABLE IF NOT EXISTS experiment_runs (
    id TEXT PRIMARY KEY,
    experiment_id TEXT NOT NULL,
    variant_id TEXT NOT NULL,
    session_id TEXT,
    branch TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending'
        CHECK(status IN ('pending', 'running', 'completed', 'failed', 'cancelled')),
    output TEXT,
    files_changed TEXT,
    error TEXT,
    duration_ms INTEGER,
    prompt_tokens INTEGER,
    completion_tokens INTEGER,
    total_cost REAL,
    tool_calls INTEGER,
    tool_successes INTEGER,
    tool_failures INTEGER,
    files_modified INTEGER,
    lines_changed INTEGER,
    started_at TIMESTAMP,
    completed_at TIMESTAMP,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (experiment_id) REFERENCES experiments(id) ON DELETE CASCADE,
    FOREIGN KEY (variant_id) REFERENCES experiment_variants(id) ON DELETE CASCADE,
    FOREIGN KEY (session_id) REFERENCES sessions(session_id) ON DELETE SET NULL
);

CREATE INDEX IF NOT EXISTS idx_runs_experiment ON experiment_runs(experiment_id);
CREATE INDEX IF NOT EXISTS idx_runs_variant ON experiment_runs(variant_id);
CREATE INDEX IF NOT EXISTS idx_runs_status ON experiment_runs(status);

-- Success criteria definitions
CREATE TABLE IF NOT EXISTS experiment_criteria (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    experiment_id TEXT NOT NULL,
    name TEXT NOT NULL,
    criterion_type TEXT NOT NULL
        CHECK(criterion_type IN ('test_pass', 'file_exists', 'contains', 'command', 'manual')),
    target TEXT NOT NULL,
    weight REAL DEFAULT 1.0,
    FOREIGN KEY (experiment_id) REFERENCES experiments(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_criteria_experiment ON experiment_criteria(experiment_id);

-- Criterion evaluations per run
CREATE TABLE IF NOT EXISTS experiment_evaluations (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id TEXT NOT NULL,
    criterion_id INTEGER NOT NULL,
    passed INTEGER NOT NULL,
    score REAL,
    details TEXT,
    evaluated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (run_id) REFERENCES experiment_runs(id) ON DELETE CASCADE,
    FOREIGN KEY (criterion_id) REFERENCES experiment_criteria(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_evaluations_run ON experiment_evaluations(run_id);
