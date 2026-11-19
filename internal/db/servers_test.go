package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"testing/fstest"
)

func legacyServersDB(t *testing.T) *sql.DB {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v03.db")
	conn, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	conn.SetMaxOpenConns(1)
	t.Cleanup(func() { conn.Close() })
	source := fstest.MapFS{}
	for _, name := range []string{"migrations/0001_initial.sql", "migrations/0002_user_traffic.sql", "migrations/0003_alerts.sql"} {
		raw, err := migrations.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		source[name] = &fstest.MapFile{Data: raw}
	}
	if err := migrate(ctx, conn, source); err != nil {
		t.Fatal(err)
	}
	mustExec(t, conn, `INSERT INTO settings VALUES ('public_host','"old.example"'),
		('alert_preferences','{"interface":"eth9","quota_gb":12,"billing_day":7,"billing_mode":"in","events":{"core_down":false}}');
		INSERT INTO admins VALUES (2,'admin','hash',NULL,1);
		INSERT INTO sessions VALUES ('session',2,999,1);
		INSERT INTO inbounds VALUES (3,'local','vless_reality',443,1,' original ','{ "fixture": true }',2,3);
		CREATE TABLE inbound_links (inbound_id INTEGER REFERENCES inbounds(id) ON DELETE CASCADE);
		INSERT INTO inbound_links VALUES (3);
		CREATE TABLE port_log (port INTEGER);
		CREATE VIEW inbound_ports AS SELECT port FROM inbounds;
		CREATE TRIGGER log_inbound_port AFTER UPDATE OF port ON inbounds BEGIN INSERT INTO port_log VALUES (NEW.port); END;
		INSERT INTO server_traffic_samples VALUES (1,123,'eth9',34,56);
		INSERT INTO alert_events VALUES ('existing',1,'core_down','old.example','',0,0,0,0,123,0);
		INSERT INTO alert_outbox (id,event_key,channel,next_at,created_at,updated_at) VALUES (4,'existing','bark',123,123,123);`)
	return conn
}

func TestServersMigrationPreservesRowsAndConstraints(t *testing.T) {
	ctx := context.Background()
	conn := legacyServersDB(t)
	if err := Migrate(ctx, conn); err != nil {
		t.Fatal(err)
	}
	var name, host, status string
	var local bool
	if err := conn.QueryRow("SELECT name, public_host, status, is_local FROM servers WHERE id=1").Scan(&name, &host, &status, &local); err != nil || name != "本机" || host != "old.example" || status != "local" || !local {
		t.Fatal(name, host, status, local, err)
	}
	if role, err := GetSetting(ctx, conn, NodeRole); err != nil || role != RoleMaster {
		t.Fatal(role, err)
	}

	for _, query := range []string{
		"SELECT id FROM sessions WHERE admin_id=2",
		"SELECT inbound_id FROM inbound_links WHERE inbound_id=3",
		"SELECT port FROM inbound_ports WHERE port=443",
		`SELECT id FROM inbounds WHERE id=3 AND server_id=1 AND remark=' original ' AND options='{ "fixture": true }' AND created_at=2 AND updated_at=3`,
		"SELECT rx FROM server_traffic_samples WHERE server_id=1 AND rx=34 AND tx=56",
		"SELECT o.id FROM alert_outbox o JOIN alert_events e USING(event_key) JOIN servers s ON s.id=e.server_id WHERE o.id=4",
		"SELECT id FROM servers WHERE json_extract(alert_preferences, '$.quota_gb')=12 AND json_extract(alert_preferences, '$.events.core_down')=0",
	} {
		var value any
		if err := conn.QueryRow(query).Scan(&value); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}

	mustExec(t, conn, `INSERT INTO servers(id,name,public_host,status) VALUES(2,'Remote','remote.example','offline');
		INSERT INTO inbounds(server_id,tag,protocol,port,options,created_at,updated_at)
		VALUES(2,'local','vless_reality',443,'{}',1,1);`)
	for _, query := range []string{
		"INSERT INTO servers(name,public_host,is_local) VALUES('Duplicate','another.example',1)",
		"INSERT INTO servers(name,public_host,status) VALUES('Invalid','another.example','invalid')",
		"UPDATE inbounds SET server_id=999 WHERE id=3",
		"UPDATE inbounds SET server_id=2 WHERE id=3",
		"INSERT INTO inbounds(server_id,tag,protocol,port,options,created_at,updated_at) VALUES(2,'local','vless_reality',444,'{}',1,1)",
		"INSERT INTO inbounds(server_id,tag,protocol,port,options,created_at,updated_at) VALUES(2,'dup','vless_reality',443,'{}',1,1)",
		"UPDATE server_traffic_samples SET server_id=999",
		"UPDATE alert_events SET server_id=999",
		"UPDATE alert_outbox SET event_key='missing'",
		"DELETE FROM servers WHERE id=2",
	} {
		if _, err := conn.Exec(query); err == nil {
			t.Errorf("accepted invalid write: %s", query)
		}
	}
	mustExec(t, conn, "UPDATE inbounds SET port=444 WHERE id=3")
	var port int
	if err := conn.QueryRow("SELECT port FROM port_log").Scan(&port); err != nil || port != 444 {
		t.Fatal(port, err)
	}
	if err := SetSetting(ctx, conn, PublicHost, "2001:db8::8"); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow("SELECT public_host FROM servers WHERE is_local=1").Scan(&host); err != nil || host != "2001:db8::8" {
		t.Fatal(host, err)
	}
}

func TestServersMigrationRollsBackOnOrphanedSample(t *testing.T) {
	ctx := context.Background()
	conn := legacyServersDB(t)
	mustExec(t, conn, "INSERT INTO server_traffic_samples VALUES (999,234,'eth9',1,2)")
	if err := Migrate(ctx, conn); err == nil {
		t.Fatal("migrated an orphaned sample")
	}
	var version, servers int
	if err := conn.QueryRow("SELECT (SELECT max(version) FROM schema_migrations), (SELECT count(*) FROM sqlite_schema WHERE name='servers')").Scan(&version, &servers); err != nil || version != 3 || servers != 0 {
		t.Fatal(version, servers, err)
	}
	if _, err := conn.Exec("INSERT INTO sessions VALUES ('orphan',999,999,1)"); err == nil {
		t.Fatal("foreign key enforcement not restored after failure")
	}
	mustExec(t, conn, "DELETE FROM server_traffic_samples WHERE server_id=999")
	if err := Migrate(ctx, conn); err != nil {
		t.Fatal(err)
	}
}

func TestAgentMigrationPreservesRemoteServerReferences(t *testing.T) {
	ctx := t.Context()
	conn := legacyServersDB(t)
	source := fstest.MapFS{}
	for _, name := range []string{"migrations/0001_initial.sql", "migrations/0002_user_traffic.sql", "migrations/0003_alerts.sql", serversMigration} {
		raw, err := migrations.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		source[name] = &fstest.MapFile{Data: raw}
	}
	if err := migrate(ctx, conn, source); err != nil {
		t.Fatal(err)
	}
	mustExec(t, conn, `INSERT INTO servers (id,name,public_host,status,agent_public_key) VALUES(2,'edge','edge.example','online','key');
		INSERT INTO server_traffic_samples VALUES (2,456,'eth0',12,34);
		UPDATE inbounds SET server_id=2 WHERE id=3;`)
	if err := Migrate(ctx, conn); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{"UPDATE servers SET status='removed' WHERE id=2", "UPDATE servers SET status='removed' WHERE is_local=1"} {
		if _, err := conn.Exec(invalid); err == nil {
			t.Errorf("invalid removal accepted: %s", invalid)
		}
	}
	mustExec(t, conn, "UPDATE servers SET status='removed',agent_public_key=NULL WHERE id=2")
	var host string
	var rx int
	if err := conn.QueryRow(`SELECT s.public_host,t.rx FROM servers s JOIN inbounds i ON i.server_id=s.id
		JOIN server_traffic_samples t ON t.server_id=s.id WHERE s.id=2 AND s.status='removed'`).Scan(&host, &rx); err != nil || host != "edge.example" || rx != 12 {
		t.Fatal("migration or removal changed server data", host, rx, err)
	}
}
