ALTER TABLE servers ADD COLUMN protocol INTEGER NOT NULL DEFAULT 1 CHECK (protocol >= 1);
ALTER TABLE servers ADD COLUMN interfaces TEXT NOT NULL DEFAULT '[]';

ALTER TABLE alert_events ADD COLUMN offline_seconds INTEGER NOT NULL DEFAULT 0 CHECK (offline_seconds >= 0);
CREATE INDEX alert_events_unread_time ON alert_events (created_at DESC, event_key DESC) WHERE read_at = 0;
