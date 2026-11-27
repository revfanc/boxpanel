ALTER TABLE servers ADD COLUMN exit_address TEXT NOT NULL DEFAULT '';
ALTER TABLE servers ADD COLUMN exit_ports TEXT;
ALTER TABLE servers ADD COLUMN exit_ports_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE servers ADD COLUMN exit_cleanup_key TEXT;
ALTER TABLE servers ADD COLUMN exit_used INTEGER NOT NULL DEFAULT 0 CHECK (exit_used IN (0,1));
ALTER TABLE servers ADD COLUMN exit_served INTEGER NOT NULL DEFAULT 0 CHECK (exit_served IN (0,1));
ALTER TABLE servers ADD COLUMN exit_traffic_up INTEGER NOT NULL DEFAULT 0 CHECK (exit_traffic_up >= 0);
ALTER TABLE servers ADD COLUMN exit_traffic_down INTEGER NOT NULL DEFAULT 0 CHECK (exit_traffic_down >= 0);

CREATE TABLE exit_links (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    entry_id INTEGER NOT NULL UNIQUE REFERENCES servers(id),
    exit_id INTEGER NOT NULL REFERENCES servers(id),
    revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
    port INTEGER NOT NULL CHECK (port BETWEEN 20000 AND 29999),
    password TEXT NOT NULL CHECK (length(password) = 44),
    mode TEXT NOT NULL CHECK (mode IN ('split', 'all')),
    failure TEXT NOT NULL CHECK (failure IN ('block', 'direct')),
    rules TEXT NOT NULL CHECK (json_valid(rules)),
    checked_at INTEGER NOT NULL DEFAULT 0,
    available INTEGER CHECK (available IN (0, 1)),
    policy_down INTEGER NOT NULL DEFAULT 0 CHECK (policy_down IN (0,1)),
    incident_at INTEGER NOT NULL DEFAULT 0,
    CHECK (entry_id <> exit_id),
    UNIQUE (exit_id, port)
);

CREATE TRIGGER exit_link_insert AFTER INSERT ON exit_links BEGIN
    UPDATE servers SET exit_used=1 WHERE id IN (NEW.entry_id,NEW.exit_id);
    UPDATE servers SET exit_served=1 WHERE id=NEW.exit_id;
    UPDATE servers SET desired_generation=desired_generation+1
    WHERE is_local=0 AND id IN (NEW.entry_id,NEW.exit_id) AND status<>'removed';
END;

CREATE TRIGGER exit_link_delete AFTER DELETE ON exit_links BEGIN
    UPDATE servers SET desired_generation=desired_generation+1
    WHERE is_local=0 AND id IN (OLD.entry_id,OLD.exit_id) AND status<>'removed';
END;

CREATE TRIGGER exit_link_update AFTER UPDATE OF mode,failure,rules,revision ON exit_links
WHEN OLD.mode<>NEW.mode OR OLD.failure<>NEW.failure OR OLD.rules<>NEW.rules OR OLD.revision<>NEW.revision
BEGIN
    UPDATE servers SET desired_generation=desired_generation+1
    WHERE is_local=0 AND id IN (NEW.entry_id,NEW.exit_id) AND status<>'removed';
END;

CREATE TRIGGER exit_address_update AFTER UPDATE OF exit_address ON servers
WHEN OLD.exit_address<>NEW.exit_address
BEGIN
    UPDATE exit_links SET revision=revision+1,checked_at=0,available=NULL,policy_down=0,incident_at=0
    WHERE entry_id=NEW.id OR exit_id=NEW.id;
END;

CREATE TRIGGER exit_server_removed AFTER UPDATE OF status ON servers
WHEN NEW.status='removed' AND OLD.status<>'removed'
BEGIN
    DELETE FROM exit_links WHERE entry_id=NEW.id OR exit_id=NEW.id;
END;
