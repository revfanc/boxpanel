ALTER TABLE servers ADD COLUMN desired_generation INTEGER NOT NULL DEFAULT 1;
ALTER TABLE servers ADD COLUMN applied_generation INTEGER NOT NULL DEFAULT 0;
ALTER TABLE servers ADD COLUMN sync_error TEXT NOT NULL DEFAULT '';
ALTER TABLE servers ADD COLUMN singbox_active INTEGER;
ALTER TABLE servers ADD COLUMN desired_expiry_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE inbounds ADD COLUMN agent_key_id TEXT NOT NULL DEFAULT '';
ALTER TABLE inbounds ADD COLUMN agent_keys_ready INTEGER NOT NULL DEFAULT 0 CHECK (agent_keys_ready IN (0,1));
ALTER TABLE inbounds ADD COLUMN agent_cert_id TEXT NOT NULL DEFAULT '';

-- Earlier versions did not allow remote writes through the API. Require
-- node-generated material for any remote entries provisioned outside it.
UPDATE inbounds SET agent_key_id='legacy-' || id,
    options=CASE protocol
        WHEN 'vless_reality' THEN json_remove(options,'$.private_key','$.public_key','$.short_ids')
        WHEN 'hysteria2' THEN json_remove(options,'$.fingerprint','$.certificate')
        ELSE options END
WHERE server_id IN (SELECT id FROM servers WHERE is_local=0);

-- All user writers (including traffic flushes and scheduled resets) use
-- these triggers. Traffic samples only invalidate on a quota transition.
CREATE TRIGGER desired_user_insert AFTER INSERT ON users
WHEN NEW.enabled=1
BEGIN
    UPDATE servers SET desired_generation=desired_generation+1 WHERE is_local=0 AND status<>'removed';
END;
CREATE TRIGGER desired_user_delete AFTER DELETE ON users
WHEN OLD.enabled=1
BEGIN
    UPDATE servers SET desired_generation=desired_generation+1 WHERE is_local=0 AND status<>'removed';
END;
CREATE TRIGGER desired_user_update AFTER UPDATE OF name,uuid,password,enabled,expire_at,traffic_limit,traffic_up,traffic_down ON users
WHEN (OLD.enabled=1 AND (OLD.traffic_limit=0 OR (OLD.traffic_up<OLD.traffic_limit AND OLD.traffic_down<OLD.traffic_limit-OLD.traffic_up)))
     <> (NEW.enabled=1 AND (NEW.traffic_limit=0 OR (NEW.traffic_up<NEW.traffic_limit AND NEW.traffic_down<NEW.traffic_limit-NEW.traffic_up)))
     OR (NEW.enabled=1 AND (NEW.traffic_limit=0 OR (NEW.traffic_up<NEW.traffic_limit AND NEW.traffic_down<NEW.traffic_limit-NEW.traffic_up))
         AND (OLD.name<>NEW.name OR OLD.uuid<>NEW.uuid OR OLD.password<>NEW.password OR OLD.expire_at<>NEW.expire_at))
BEGIN
    UPDATE servers SET desired_generation=desired_generation+1 WHERE is_local=0 AND status<>'removed';
END;
CREATE TRIGGER desired_route_insert AFTER INSERT ON settings
WHEN NEW.key='block_bittorrent'
BEGIN
    UPDATE servers SET desired_generation=desired_generation+1 WHERE is_local=0 AND status<>'removed';
END;
CREATE TRIGGER desired_route_update AFTER UPDATE OF value ON settings
WHEN NEW.key='block_bittorrent' AND OLD.value<>NEW.value
BEGIN
    UPDATE servers SET desired_generation=desired_generation+1 WHERE is_local=0 AND status<>'removed';
END;
