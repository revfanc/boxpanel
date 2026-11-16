ALTER TABLE users ADD COLUMN traffic_reset_day INTEGER NOT NULL DEFAULT 0 CHECK (traffic_reset_day BETWEEN 0 AND 28);
ALTER TABLE users ADD COLUMN traffic_reset_at INTEGER NOT NULL DEFAULT 0 CHECK (traffic_reset_at >= 0);
-- Internal epoch prevents pending samples from undoing a reset, even twice in one second.
ALTER TABLE users ADD COLUMN traffic_epoch INTEGER NOT NULL DEFAULT 0 CHECK (traffic_epoch >= 0);
