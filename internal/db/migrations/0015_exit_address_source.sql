ALTER TABLE servers ADD COLUMN exit_address_custom INTEGER NOT NULL DEFAULT 0 CHECK (exit_address_custom IN (0,1));
-- Older rows do not record how the address was chosen. Preserve distinct
-- addresses, including origin IPs behind a subscription domain.
UPDATE servers SET exit_address_custom=1 WHERE exit_address<>'' AND exit_address<>public_host;
