ALTER TABLE servers ADD COLUMN monitoring TEXT;
ALTER TABLE servers ADD COLUMN restart_id TEXT NOT NULL DEFAULT '';
ALTER TABLE servers ADD COLUMN restart_status TEXT NOT NULL DEFAULT '' CHECK (restart_status IN ('', 'pending', 'succeeded', 'failed'));
ALTER TABLE servers ADD COLUMN restart_error TEXT NOT NULL DEFAULT '';

-- Remote servers previously ignored this threshold even when the JSON
-- included the default value from the local form.
UPDATE servers SET alert_preferences=json_set(alert_preferences, '$.events.server_quota_90', json('false')) WHERE is_local=0;

-- The node claims a command before touching the kernel. Keep the claim across
-- node restarts and master changes, including when the result was never received.
CREATE TABLE agent_restart_commands (
  id TEXT PRIMARY KEY,
  error TEXT NOT NULL DEFAULT 'interrupted'
);
