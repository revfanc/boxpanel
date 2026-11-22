-- Per-user server access. all_servers=1 keeps the previous behaviour (every
-- server, including servers added later); otherwise user_servers lists the
-- servers whose configuration and subscription include the user.
ALTER TABLE users ADD COLUMN all_servers INTEGER NOT NULL DEFAULT 1 CHECK (all_servers IN (0,1));

CREATE TABLE user_servers (
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    server_id INTEGER NOT NULL REFERENCES servers(id),
    PRIMARY KEY (user_id,server_id)
) WITHOUT ROWID;
CREATE INDEX user_servers_server ON user_servers(server_id);

-- Access changes alter the user lists of remote nodes. Over-invalidation is
-- harmless: nodes skip the restart when the generated config is unchanged.
CREATE TRIGGER desired_user_scope AFTER UPDATE OF all_servers ON users
WHEN OLD.all_servers<>NEW.all_servers
BEGIN
    UPDATE servers SET desired_generation=desired_generation+1 WHERE is_local=0 AND status<>'removed';
END;
CREATE TRIGGER desired_user_server_insert AFTER INSERT ON user_servers
BEGIN
    UPDATE servers SET desired_generation=desired_generation+1 WHERE id=NEW.server_id AND is_local=0 AND status<>'removed';
END;
CREATE TRIGGER desired_user_server_delete AFTER DELETE ON user_servers
BEGIN
    UPDATE servers SET desired_generation=desired_generation+1 WHERE id=OLD.server_id AND is_local=0 AND status<>'removed';
END;
