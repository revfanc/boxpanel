package httpapi

import (
	"slices"
	"testing"
)

func TestUserServerAccessAPI(t *testing.T) {
	f := newResources(t, false)
	type access struct {
		AllServers bool    `json:"all_servers"`
		ServerIDs  []int64 `json:"server_ids"`
	}
	created := decodeResult[access](t, f.call(t, "POST", "users", `{"name":"alice"}`, 201))
	if !created.AllServers || created.ServerIDs == nil || len(created.ServerIDs) != 0 {
		t.Fatalf("new user access = %+v, want every server", created)
	}
	if _, err := f.conn.Exec(`INSERT INTO servers (id,name,public_host,status) VALUES
		(2,'edge','edge.example','online'),(3,'gone','gone.example','removed')`); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{"all_servers":false}`,
		`{"all_servers":false,"server_ids":[]}`,
		`{"server_ids":[3]}`,
		`{"server_ids":[99]}`,
		`{"server_ids":[0]}`,
	} {
		f.call(t, "PUT", "users/1", body, 400)
	}
	got := decodeResult[access](t, f.call(t, "PUT", "users/1", `{"server_ids":[2,1,2]}`, 200))
	if got.AllServers || !slices.Equal(got.ServerIDs, []int64{1, 2}) {
		t.Fatalf("selected access = %+v", got)
	}
	// Other fields keep the selection; list and detail agree.
	f.call(t, "PUT", "users/1", `{"remark":"x"}`, 200)
	list := decodeResult[[]access](t, f.call(t, "GET", "users", "", 200))
	detail := decodeResult[access](t, f.call(t, "GET", "users/1", "", 200))
	if len(list) != 1 || list[0].AllServers || !slices.Equal(list[0].ServerIDs, []int64{1, 2}) || !slices.Equal(detail.ServerIDs, []int64{1, 2}) {
		t.Fatalf("list %+v detail %+v", list, detail)
	}
	// Removed servers disappear from the selection.
	if _, err := f.conn.Exec("UPDATE servers SET status='removed' WHERE id=2"); err != nil {
		t.Fatal(err)
	}
	if got := decodeResult[access](t, f.call(t, "GET", "users/1", "", 200)); !slices.Equal(got.ServerIDs, []int64{1}) {
		t.Fatalf("after removal = %+v", got)
	}
	got = decodeResult[access](t, f.call(t, "PUT", "users/1", `{"all_servers":true}`, 200))
	if !got.AllServers || len(got.ServerIDs) != 0 {
		t.Fatalf("all servers = %+v", got)
	}
	var rows int
	if err := f.conn.QueryRow("SELECT count(*) FROM user_servers").Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("stale rows %d, %v", rows, err)
	}
	created = decodeResult[access](t, f.call(t, "POST", "users", `{"name":"bob","all_servers":false,"server_ids":[1]}`, 201))
	if created.AllServers || !slices.Equal(created.ServerIDs, []int64{1}) {
		t.Fatalf("created with selection = %+v", created)
	}
	f.call(t, "POST", "users", `{"name":"carol","all_servers":false}`, 400)
}
