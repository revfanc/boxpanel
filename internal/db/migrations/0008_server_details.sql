ALTER TABLE agent_join_tokens ADD COLUMN server_id INTEGER REFERENCES servers(id);
ALTER TABLE servers ADD COLUMN uptime INTEGER CHECK (uptime >= 0);

ALTER TABLE server_user_traffic ADD COLUMN user_epoch INTEGER NOT NULL DEFAULT 0 CHECK (user_epoch >= 0);
ALTER TABLE server_user_traffic ADD COLUMN cycle_up INTEGER NOT NULL DEFAULT 0 CHECK (cycle_up >= 0);
ALTER TABLE server_user_traffic ADD COLUMN cycle_down INTEGER NOT NULL DEFAULT 0 CHECK (cycle_down >= 0);
UPDATE server_user_traffic SET user_epoch=(SELECT traffic_epoch FROM users WHERE id=user_id);
