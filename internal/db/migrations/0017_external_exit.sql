DROP TRIGGER exit_link_insert;
DROP TRIGGER exit_link_delete;
DROP TRIGGER exit_link_update;
DROP TRIGGER exit_address_update;
DROP TRIGGER exit_server_removed;
CREATE TABLE exit_links_new (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    entry_id INTEGER NOT NULL UNIQUE REFERENCES servers(id),
    exit_id INTEGER REFERENCES servers(id),
    revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
    port INTEGER NOT NULL CHECK (port BETWEEN 1 AND 65535),
    password TEXT NOT NULL,
    external_proxy TEXT CHECK (external_proxy IS NULL OR json_valid(external_proxy)),
    mode TEXT NOT NULL CHECK (mode IN ('split', 'all')),
    failure TEXT NOT NULL CHECK (failure IN ('block', 'direct')),
    rules TEXT NOT NULL CHECK (json_valid(rules)),
    checked_at INTEGER NOT NULL DEFAULT 0,
    available INTEGER CHECK (available IN (0, 1)),
    policy_down INTEGER NOT NULL DEFAULT 0 CHECK (policy_down IN (0,1)),
    incident_at INTEGER NOT NULL DEFAULT 0,
    CHECK ((exit_id IS NOT NULL AND entry_id <> exit_id AND external_proxy IS NULL AND port BETWEEN 20000 AND 29999 AND length(password)=44)
        OR (exit_id IS NULL AND external_proxy IS NOT NULL AND password='')),
    UNIQUE (exit_id, port)
);

INSERT INTO exit_links_new (id,entry_id,exit_id,revision,port,password,mode,failure,rules,checked_at,available,policy_down,incident_at) SELECT id,entry_id,exit_id,revision,port,password,mode,failure,rules,checked_at,available,policy_down,incident_at FROM exit_links;
-- Retain IDs of previously deleted links in the high-water mark.
UPDATE sqlite_sequence SET seq=max(seq,coalesce((SELECT seq FROM sqlite_sequence WHERE name='exit_links'),0)) WHERE name='exit_links_new';
INSERT INTO sqlite_sequence(name,seq) SELECT 'exit_links_new',seq FROM sqlite_sequence WHERE name='exit_links' AND NOT EXISTS (SELECT 1 FROM sqlite_sequence WHERE name='exit_links_new');
DROP TABLE exit_links;
ALTER TABLE exit_links_new RENAME TO exit_links;

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
    WHERE exit_id IS NOT NULL AND (entry_id=NEW.id OR exit_id=NEW.id);
END;

CREATE TRIGGER exit_server_removed AFTER UPDATE OF status ON servers
WHEN NEW.status='removed' AND OLD.status<>'removed'
BEGIN
    DELETE FROM exit_links WHERE entry_id=NEW.id OR exit_id=NEW.id;
END;
