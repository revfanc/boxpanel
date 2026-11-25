CREATE TABLE master_migration_nodes (
  server_id INTEGER PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
  arrived INTEGER NOT NULL DEFAULT 0 CHECK (arrived IN (0, 1))
);
