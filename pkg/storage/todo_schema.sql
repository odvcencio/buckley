-- TODO System Schema
-- Stores task lists for systematic plan execution

-- Todos table: individual TODO items
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
    metadata TEXT, -- JSON for arbitrary data
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
    conversation_summary TEXT, -- Compressed summary of conversation so far
    conversation_tokens INT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    metadata TEXT, -- JSON for checkpoint-specific data
    FOREIGN KEY (session_id) REFERENCES sessions(session_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_checkpoints_session ON todo_checkpoints(session_id);
CREATE INDEX IF NOT EXISTS idx_checkpoints_created ON todo_checkpoints(created_at);
