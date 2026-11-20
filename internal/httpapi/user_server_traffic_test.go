package httpapi

import (
	"slices"
	"strings"
	"testing"
)

func TestUserDetailTrafficByServer(t *testing.T) {
	f := newResources(t, false)
	f.call(t, "POST", "users", `{"name":"alice"}`, 201)
	const detail = "users/1"
	assertOmitted := func(path string) {
		t.Helper()
		w := f.call(t, "GET", path, "", 200)
		if strings.Contains(w.Body.String(), "traffic_by_server") {
			t.Fatal("unexpected server breakdown", path)
		}
	}
	assertOmitted(detail)
	if _, err := f.conn.Exec(`UPDATE users SET traffic_up=60,traffic_down=90,traffic_epoch=4 WHERE id=1;
		INSERT INTO servers (id,name,public_host,status) VALUES
		(2,'edge','edge.example','online'),(3,'stale','stale.example','offline'),
		(4,'removed','removed.example','removed'),(5,'pending','pending.example','pending');
		INSERT INTO server_user_traffic (server_id,user_id,epoch,up,down,total_up,total_down,updated_at,user_epoch,cycle_up,cycle_down)
		VALUES (2,1,10,10,20,120,140,123,4,70,20),
		(3,1,10,30,40,130,140,123,3,30,40),(4,1,10,0,10,0,10,123,4,0,10);`); err != nil {
		t.Fatal(err)
	}
	assertOmitted("users")
	assertRows := func(want []userServerTraffic) {
		t.Helper()
		got := decodeResult[struct {
			TrafficByServer []userServerTraffic `json:"traffic_by_server"`
		}](t, f.call(t, "GET", detail, "", 200))
		if !slices.Equal(got.TrafficByServer, want) {
			t.Fatalf("traffic by server = %+v, want %+v", got.TrafficByServer, want)
		}
	}
	want := []userServerTraffic{
		{ServerID: 1, Name: "本机", IsLocal: true, Down: 70},
		{ServerID: 2, Name: "edge", Up: 70, Down: 20},
		{ServerID: 3, Name: "stale"},
		{ServerID: 5, Name: "pending"},
	}
	assertRows(want)
	f.call(t, "POST", detail+"/reset-traffic", "", 200)
	want[0].Down = 0
	want[1].Up, want[1].Down = 0, 0
	assertRows(want)
	if _, err := f.conn.Exec("UPDATE servers SET status='removed' WHERE is_local=0"); err != nil {
		t.Fatal(err)
	}
	assertOmitted(detail)
}
