ALTER TABLE users ADD COLUMN alert_identity INTEGER NOT NULL DEFAULT 0 CHECK (alert_identity >= 0);
UPDATE users SET alert_identity=traffic_epoch;

CREATE TABLE server_traffic_samples (
    server_id INTEGER NOT NULL,
    sampled_at INTEGER NOT NULL,
    interface TEXT NOT NULL,
    rx INTEGER NOT NULL CHECK (rx >= 0),
    tx INTEGER NOT NULL CHECK (tx >= 0),
    PRIMARY KEY (server_id, sampled_at)
) WITHOUT ROWID;

CREATE TABLE alert_events (
    event_key TEXT PRIMARY KEY,
    server_id INTEGER NOT NULL,
    kind TEXT NOT NULL,
    address TEXT NOT NULL,
    username TEXT NOT NULL,
    used INTEGER NOT NULL,
    limit_bytes INTEGER NOT NULL,
    threshold INTEGER NOT NULL,
    deadline INTEGER NOT NULL,
    created_at INTEGER NOT NULL,
    read_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX alert_events_unread ON alert_events (server_id, created_at DESC) WHERE read_at = 0;

CREATE TABLE alert_outbox (
    id INTEGER PRIMARY KEY,
    event_key TEXT REFERENCES alert_events(event_key),
    channel TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'sent', 'failed')),
    attempts INTEGER NOT NULL DEFAULT 0,
    attempt_id INTEGER,
    next_at INTEGER NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    error TEXT NOT NULL DEFAULT '',
    grouped INTEGER NOT NULL DEFAULT 0,
    UNIQUE (event_key, channel)
);
CREATE INDEX alert_outbox_due ON alert_outbox (channel, next_at) WHERE status = 'pending';
CREATE INDEX alert_outbox_attempt ON alert_outbox (attempt_id);
CREATE INDEX alert_outbox_recent ON alert_outbox (updated_at DESC, id DESC);

CREATE TABLE alert_attempts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    channel TEXT NOT NULL,
    started_at INTEGER NOT NULL
);
CREATE INDEX alert_attempts_rate ON alert_attempts (channel, started_at);
