package agent

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/revfanc/boxpanel/internal/db"
)

func TestDesiredUsersFollowServerAccess(t *testing.T) {
	ctx := context.Background()
	conn, err := db.Open(ctx, filepath.Join(t.TempDir(), "master.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	run := func(query string, args ...any) {
		t.Helper()
		if _, err := conn.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	run(`INSERT INTO servers (id,name,public_host,status) VALUES (2,'edge','edge.example','online'),(3,'west','west.example','online')`)
	run(`INSERT INTO users (id,name,uuid,password,sub_token,created_at) VALUES
		(1,'all','11111111-1111-4111-8111-111111111111','p1','t1',0),
		(2,'edge-only','22222222-2222-4222-8222-222222222222','p2','t2',0),
		(3,'nowhere','33333333-3333-4333-8333-333333333333','p3','t3',0)`)
	generation := func(id int64) int64 {
		t.Helper()
		var g int64
		if err := conn.QueryRow("SELECT desired_generation FROM servers WHERE id=?", id).Scan(&g); err != nil {
			t.Fatal(err)
		}
		return g
	}
	edge, west := generation(2), generation(3)
	run("UPDATE users SET all_servers=0 WHERE id IN (2,3)")
	run("INSERT INTO user_servers (user_id,server_id) VALUES (2,2)")
	// Two scope changes reach every node; the selection only reaches edge.
	if generation(2) != edge+3 || generation(3) != west+2 {
		t.Fatalf("access changes did not invalidate nodes: edge %d->%d, west %d->%d", edge, generation(2), west, generation(3))
	}
	users := func(server int64) []int64 {
		t.Helper()
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		list, err := desiredUsers(ctx, tx, server, 1)
		if err != nil {
			t.Fatal(err)
		}
		var ids []int64
		for _, u := range list {
			ids = append(ids, u.ID)
		}
		return ids
	}
	if got := users(2); !slices.Equal(got, []int64{1, 2}) {
		t.Fatalf("edge users = %v", got)
	}
	if got := users(3); !slices.Equal(got, []int64{1}) {
		t.Fatalf("west users = %v", got)
	}
	// Returning to every server includes servers added later.
	run("UPDATE users SET all_servers=1 WHERE id=3")
	run(`INSERT INTO servers (id,name,public_host,status) VALUES (4,'late','late.example','online')`)
	if got := users(4); !slices.Equal(got, []int64{1, 3}) {
		t.Fatalf("new server users = %v", got)
	}
	run("DELETE FROM users WHERE id=2")
	var rows int
	if err := conn.QueryRow("SELECT count(*) FROM user_servers").Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("user_servers rows after delete = %d, %v", rows, err)
	}
}
