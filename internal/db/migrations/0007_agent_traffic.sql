CREATE TABLE server_user_traffic (
    server_id INTEGER NOT NULL REFERENCES servers(id),
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    epoch INTEGER NOT NULL CHECK (epoch>0),
    up INTEGER NOT NULL CHECK (up>=0),
    down INTEGER NOT NULL CHECK (down>=0),
    total_up INTEGER NOT NULL CHECK (total_up>=0),
    total_down INTEGER NOT NULL CHECK (total_down>=0),
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (server_id,user_id)
) WITHOUT ROWID;
CREATE INDEX server_user_traffic_user ON server_user_traffic(user_id);

-- On nodes, retain the last committed counters for mirrors that are about
-- to disappear, until the master acknowledges them. These rows outlive the mirror.
CREATE TABLE agent_retired_traffic (
    user_id INTEGER NOT NULL,
    epoch INTEGER NOT NULL,
    up INTEGER NOT NULL,
    down INTEGER NOT NULL,
    PRIMARY KEY (user_id,epoch)
) WITHOUT ROWID;

-- An offline node can outlive the deletion of the master user it is serving.
INSERT INTO settings (key,value)
SELECT 'user_id_high_watermark',CAST(max(id) AS TEXT) FROM users
HAVING max(id)>0 AND NOT EXISTS (SELECT 1 FROM settings WHERE key='node_role' AND value='"agent"');
