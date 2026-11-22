-- Opt-in per node: follow the master's release version automatically.
ALTER TABLE servers ADD COLUMN auto_upgrade INTEGER NOT NULL DEFAULT 0 CHECK (auto_upgrade IN (0,1));
