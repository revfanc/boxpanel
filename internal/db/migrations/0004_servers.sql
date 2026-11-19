CREATE TABLE servers (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    public_host TEXT NOT NULL,
    is_local INTEGER NOT NULL DEFAULT 0 CHECK (is_local IN (0, 1)),
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('local', 'online', 'offline', 'pending')),
    last_seen_at INTEGER,
    version TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL DEFAULT (unixepoch()),
    updated_at INTEGER NOT NULL DEFAULT (unixepoch()),
    agent_public_key TEXT,
    alert_preferences TEXT NOT NULL DEFAULT '{"interface":"","quota_gb":0,"billing_day":1,"billing_mode":"out"}'
);
CREATE UNIQUE INDEX servers_one_local ON servers (is_local) WHERE is_local = 1;

INSERT INTO servers (id, name, public_host, is_local, status)
VALUES (1, '本机', COALESCE((SELECT json_extract(value, '$') FROM settings WHERE key = 'public_host'), ''), 1, 'local');

UPDATE servers SET alert_preferences = (SELECT value FROM settings WHERE key = 'alert_preferences')
WHERE is_local = 1 AND EXISTS (SELECT 1 FROM settings WHERE key = 'alert_preferences');
DELETE FROM settings WHERE key = 'alert_preferences';

INSERT INTO settings (key, value)
SELECT 'node_role', '"master"' WHERE EXISTS (SELECT 1 FROM admins)
    OR EXISTS (SELECT 1 FROM settings WHERE key = 'public_host')
ON CONFLICT (key) DO NOTHING;

CREATE TRIGGER settings_public_host_insert AFTER INSERT ON settings
WHEN NEW.key = 'public_host'
BEGIN
    UPDATE servers SET public_host = json_extract(NEW.value, '$'), updated_at = unixepoch() WHERE is_local = 1;
END;
CREATE TRIGGER settings_public_host_update AFTER UPDATE OF value ON settings
WHEN NEW.key = 'public_host' AND NEW.value <> OLD.value
BEGIN
    UPDATE servers SET public_host = json_extract(NEW.value, '$'), updated_at = unixepoch() WHERE is_local = 1;
END;

CREATE TABLE new_inbounds (
    id INTEGER PRIMARY KEY,
    tag TEXT NOT NULL,
    protocol TEXT NOT NULL,
    port INTEGER NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1,
    remark TEXT NOT NULL DEFAULT '',
    options TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    server_id INTEGER NOT NULL DEFAULT 1 REFERENCES servers(id),
    UNIQUE (server_id, port),
    UNIQUE (server_id, tag)
);
INSERT INTO new_inbounds (id, tag, protocol, port, enabled, remark, options, created_at, updated_at, server_id)
SELECT id, tag, protocol, port, enabled, remark, options, created_at, updated_at, 1 FROM inbounds;
DROP TABLE inbounds;
ALTER TABLE new_inbounds RENAME TO inbounds;

CREATE TABLE new_server_traffic_samples (
    server_id INTEGER NOT NULL REFERENCES servers(id),
    sampled_at INTEGER NOT NULL,
    interface TEXT NOT NULL,
    rx INTEGER NOT NULL CHECK (rx >= 0),
    tx INTEGER NOT NULL CHECK (tx >= 0),
    PRIMARY KEY (server_id, sampled_at)
) WITHOUT ROWID;
INSERT INTO new_server_traffic_samples SELECT * FROM server_traffic_samples;
DROP TABLE server_traffic_samples;
ALTER TABLE new_server_traffic_samples RENAME TO server_traffic_samples;

CREATE TABLE new_alert_events (
    event_key TEXT PRIMARY KEY,
    server_id INTEGER NOT NULL REFERENCES servers(id),
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
INSERT INTO new_alert_events SELECT * FROM alert_events;
DROP TABLE alert_events;
ALTER TABLE new_alert_events RENAME TO alert_events;
