-- Preserve the effective link until this server reports a fresh address.
ALTER TABLE servers ADD COLUMN public_ip TEXT NOT NULL DEFAULT '';
ALTER TABLE servers DROP COLUMN exit_address_custom;

-- Before 0016, the exit selector fell back to a numeric subscription host.
-- Snapshot that fallback once for older installations awaiting their first probe.
UPDATE servers SET exit_address=public_host
WHERE exit_address='' AND (
    (public_host GLOB '*.*.*.*' AND public_host NOT GLOB '*[^0-9.]*')
    OR instr(public_host, ':')>0
);
