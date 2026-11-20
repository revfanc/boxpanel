package subscription

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestMultiServerSubscription(t *testing.T) {
	_, store := publicFixture(t)
	original, err := store.Load(context.Background(), 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	legacy := fixture(t)
	hy := legacy.Nodes[1]
	remoteOptions, err := json.Marshal(struct {
		SNI         string `json:"sni"`
		Fingerprint string `json:"fingerprint"`
		Certificate string `json:"certificate"`
	}{hy.Hysteria2.SNI, hy.Fingerprint, hy.Certificate})
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{"UPDATE servers SET name='Edge' WHERE is_local=1", nil},
		{"INSERT INTO servers(id,name,public_host,status) VALUES(2,'Edge','edge.example','offline')", nil},
		{`INSERT INTO inbounds(server_id,tag,protocol,port,options,created_at,updated_at,agent_keys_ready)
			SELECT 2,tag,protocol,port,json_remove(options,'$.private_key'),0,0,1 FROM inbounds WHERE id=1`, nil},
		{`INSERT INTO inbounds(server_id,tag,protocol,port,remark,options,created_at,updated_at,agent_keys_ready)
			SELECT 2,'custom',protocol,9443,' Custom ',json_remove(options,'$.private_key'),0,0,1 FROM inbounds WHERE id=1`, nil},
		{`INSERT INTO inbounds(server_id,tag,protocol,port,remark,options,created_at,updated_at,agent_keys_ready)
			VALUES(2,'hy2','hysteria2',8443,'Custom',?,0,0,1)`, []any{string(remoteOptions)}},
		{`INSERT INTO inbounds(server_id,tag,protocol,port,enabled,options,created_at,updated_at)
			VALUES(2,'disabled','hysteria2',14443,0,'{}',0,0)`, nil},
	} {
		if _, err := store.DB.Exec(q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	store.Certificate = func(int64, string) ([]byte, error) {
		t.Fatal("remote certificate read from local filesystem")
		return nil, nil
	}
	data, err := store.Load(context.Background(), 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct{ name, host string }{
		{"Edge · Reality · 443", "2001:db8::8"},
		{"Edge · Reality · 443 (2)", "edge.example"},
		{"Custom", "edge.example"},
		{"Custom (2)", "edge.example"},
	}
	if len(data.Nodes) != len(want) {
		t.Fatalf("got %d nodes", len(data.Nodes))
	}
	for i, n := range data.Nodes {
		if n.Name != want[i].name || n.Host != want[i].host {
			t.Errorf("node %d: got %q at %s, want %q at %s", i, n.Name, n.Host, want[i].name, want[i].host)
		}
	}
	if data.Nodes[3].Fingerprint != hy.Fingerprint || data.Nodes[3].Certificate != hy.Certificate {
		t.Fatal("remote certificate changed")
	}
	for _, format := range []Format{Singbox, Clash, Base64} {
		got, err := Render(data.Snapshot, format)
		if err != nil {
			t.Fatal(err)
		}
		if format == Base64 {
			got, err = base64.StdEncoding.DecodeString(strings.TrimSpace(string(got)))
			if err != nil {
				t.Fatal(err)
			}
		}
		if !bytes.Contains(got, []byte("edge.example")) || !bytes.Contains(got, []byte("2001:db8::8")) || bytes.Contains(got, []byte("NEVER-IN-CLIENT")) {
			t.Fatalf("%s has missing hosts or leaked the Reality private key", format)
		}
	}
	if _, err := store.DB.Exec("UPDATE inbounds SET options=json_set(options,'$.fingerprint','incorrect') WHERE id=4"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(context.Background(), 1, nil); err != ErrUnavailable {
		t.Fatalf("remote certificate pin mismatch: %v", err)
	}
	if _, err := store.DB.Exec("UPDATE servers SET status='removed' WHERE id=2"); err != nil {
		t.Fatal(err)
	}
	filtered, err := store.Load(context.Background(), 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, format := range []Format{Singbox, Clash, Base64} {
		before, err := Render(original.Snapshot, format)
		if err != nil {
			t.Fatal(err)
		}
		after, err := Render(filtered.Snapshot, format)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("removed server affected %s subscription: %v", format, err)
		}
	}

}

func TestSubscriptionServerOrderAndStableNames(t *testing.T) {
	_, store := publicFixture(t)
	run := func(query string, args ...any) {
		t.Helper()
		if _, err := store.DB.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	run("UPDATE servers SET name='Primary' WHERE is_local=1")
	run("INSERT INTO servers(id,name,public_host,status) VALUES(2,'Edge <east>','east.example','offline'),(3,'West','west.example','online')")
	const insert = `INSERT INTO inbounds(id,server_id,tag,protocol,port,remark,options,created_at,updated_at,agent_keys_ready)
		SELECT ?,?,?,protocol,?,?,json_remove(options,'$.private_key'),0,0,? FROM inbounds WHERE id=1`
	run(insert, 2, 3, "west", 443, "Shared", 1)
	run(insert, 3, 2, "east-custom", 443, "Shared", 1)
	run(insert, 4, 1, "primary-custom", 8443, "Shared", 0)
	run(insert, 5, 2, "east-default", 9443, "", 1)
	run(insert, 6, 3, "west-waiting", 8443, "Waiting", 0)

	load := func() Data {
		t.Helper()
		d, err := store.Load(t.Context(), 1, nil)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	data := load()
	names := []string{"Primary · Reality · 443", "Shared", "Shared (2)", "Edge <east> · Reality · 9443", "Shared (3)"}
	assertRenderedNames(t, data.Snapshot, names)
	links, err := MakeLinks(data)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for i, node := range links.Nodes {
		ids = append(ids, node.InboundID)
		if node.Remark != names[i] {
			t.Fatalf("link %d: %q, want %q", node.InboundID, node.Remark, names[i])
		}
	}
	if !slices.Equal(ids, []int64{1, 4, 3, 5, 2}) {
		t.Fatalf("links are out of server order: %v", ids)
	}
	assertPageGolden(t, data, store.now(), "page_multiserver.golden")

	// Earlier server groups can gain and lose unrelated nodes without
	// changing either default names or remarks in the existing subscription.
	run(insert, 7, 1, "primary-other", 14443, "", 0)
	assertExistingNames := func() {
		t.Helper()
		byID := map[int64]string{}
		for _, n := range load().Nodes {
			byID[n.ID] = n.Name
		}
		for i, id := range ids {
			if byID[id] != names[i] {
				t.Fatalf("node %d name changed to %q", id, byID[id])
			}
		}
	}
	assertExistingNames()
	run("DELETE FROM inbounds WHERE id=7")
	assertExistingNames()
	run("UPDATE servers SET name='East' WHERE id=2")
	names[3] = "East · Reality · 9443"
	assertExistingNames()
}
