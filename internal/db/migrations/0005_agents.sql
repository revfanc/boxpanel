CREATE TABLE new_servers (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    public_host TEXT NOT NULL,
    is_local INTEGER NOT NULL DEFAULT 0 CHECK (is_local IN (0, 1)),
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('local', 'online', 'offline', 'pending', 'removed')),
    last_seen_at INTEGER,
    version TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL DEFAULT (unixepoch()),
    updated_at INTEGER NOT NULL DEFAULT (unixepoch()),
    agent_public_key TEXT,
    alert_preferences TEXT NOT NULL DEFAULT '{"interface":"","quota_gb":0,"billing_day":1,"billing_mode":"out"}',
    CHECK (status <> 'removed' OR (is_local = 0 AND agent_public_key IS NULL))
);
INSERT INTO new_servers SELECT * FROM servers;
DROP TABLE servers;
ALTER TABLE new_servers RENAME TO servers;

CREATE TABLE agent_join_tokens (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    token_hash BLOB NOT NULL UNIQUE CHECK (length(token_hash) = 32),
    name TEXT NOT NULL,
    expires_at INTEGER NOT NULL
);
