package httpapi

import (
	"cmp"
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/revfanc/boxpanel/internal/alerts"
	"github.com/revfanc/boxpanel/internal/traffic"
)

type cycleBytes struct {
	Up   int64 `json:"up"`
	Down int64 `json:"down"`
}

func (total *cycleBytes) add(value cycleBytes) {
	total.Up = traffic.AddBytes(total.Up, value.Up)
	total.Down = traffic.AddBytes(total.Down, value.Down)
}

func (total cycleBytes) remaining(remote cycleBytes) cycleBytes {
	return cycleBytes{max(0, total.Up-remote.Up), max(0, total.Down-remote.Down)}
}

func (total cycleBytes) bytes() int64 { return traffic.AddBytes(total.Up, total.Down) }

type trafficUser struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	cycleBytes
	ExpireAt int64 `json:"expire_at"`
	Limit    int64 `json:"traffic_limit"`
	enabled  bool
}

// walkUserCycles reads a user's committed total and each attributed remote
// share from one snapshot. Removed servers and earlier epochs are excluded
// from the shares. Their previously counted bytes remain in the user's total.
func walkUserCycles(ctx context.Context, tx *sql.Tx, visit func(trafficUser, map[int64]cycleBytes, cycleBytes)) error {
	rows, err := tx.QueryContext(ctx, `SELECT u.id,u.name,u.traffic_up,u.traffic_down,u.expire_at,u.traffic_limit,u.enabled,
		t.server_id,coalesce(t.cycle_up,0),coalesce(t.cycle_down,0)
		FROM users u LEFT JOIN (
			server_user_traffic t JOIN servers s ON s.id=t.server_id AND s.is_local=0 AND s.status<>'removed'
		) ON t.user_id=u.id AND t.user_epoch=u.traffic_epoch ORDER BY u.id`)
	if err != nil {
		return fmt.Errorf("read users and server cycle traffic: %w", err)
	}
	defer rows.Close()
	var user trafficUser
	var remotes map[int64]cycleBytes
	var remoteTotal cycleBytes
	for rows.Next() {
		var current trafficUser
		var serverID sql.NullInt64
		var usage cycleBytes
		if err := rows.Scan(&current.ID, &current.Name, &current.Up, &current.Down, &current.ExpireAt, &current.Limit,
			&current.enabled, &serverID, &usage.Up, &usage.Down); err != nil {
			return fmt.Errorf("scan user cycle traffic: %w", err)
		}
		if current.ID != user.ID {
			if user.ID != 0 {
				visit(user, remotes, remoteTotal)
			}
			user, remotes, remoteTotal = current, nil, cycleBytes{}
		}
		if serverID.Valid {
			if remotes == nil {
				remotes = make(map[int64]cycleBytes)
			}
			remotes[serverID.Int64] = usage
			remoteTotal.add(usage)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate user cycle traffic: %w", err)
	}
	if user.ID != 0 {
		visit(user, remotes, remoteTotal)
	}
	return nil
}

func sortByTraffic(users []trafficUser) {
	slices.SortFunc(users, func(a, b trafficUser) int {
		if n := cmp.Compare(b.bytes(), a.bytes()); n != 0 {
			return n
		}
		return cmp.Compare(a.ID, b.ID)
	})
}

type overviewServer struct {
	serverRecord
	System      serverStatusView `json:"system"`
	Billing     alerts.Billing   `json:"billing"`
	UserTraffic cycleBytes       `json:"user_traffic"`
	Share       float64          `json:"share_percent"`
}

type panelOverview struct {
	Servers     []overviewServer `json:"servers"`
	UserTraffic cycleBytes       `json:"user_traffic"`
	TopUsers    []trafficUser    `json:"top_users"`
	Expiring    []trafficUser    `json:"expiring_users"`
	Exhausted   []trafficUser    `json:"exhausted_users"`
	alerts.Notifications
}

func (a *api) panelOverview(w http.ResponseWriter, r *http.Request) {
	if !a.alertsAvailable(w) {
		return
	}
	out, err := a.readPanelOverview(r.Context())
	if err != nil {
		a.resourceError(w, err)
		return
	}
	writeJSON(w, 200, out)
}

func (a *api) readPanelOverview(ctx context.Context) (panelOverview, error) {
	out := panelOverview{Servers: []overviewServer{}, TopUsers: []trafficUser{}, Expiring: []trafficUser{}, Exhausted: []trafficUser{}}
	conn, err := a.readDB()
	if err != nil {
		return out, err
	}
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, fmt.Errorf("begin overview read: %w", err)
	}
	defer tx.Rollback()
	servers, err := readServers(ctx, tx)
	if err != nil {
		return out, err
	}
	indices := make(map[int64]int, len(servers))
	localIndex := -1
	for i, server := range servers {
		billing, err := a.opts.Alerts.ReadBilling(ctx, tx, server.ID)
		if err != nil {
			return out, err
		}
		out.Servers = append(out.Servers, overviewServer{serverRecord: server, Billing: billing})
		indices[server.ID] = i
		if server.IsLocal {
			localIndex = i
		}
	}
	now := time.Now()
	expiringBefore := now.Add(72 * time.Hour).Unix()
	err = walkUserCycles(ctx, tx, func(user trafficUser, remotes map[int64]cycleBytes, remoteTotal cycleBytes) {
		out.UserTraffic.add(user.cycleBytes)
		if localIndex >= 0 {
			out.Servers[localIndex].UserTraffic.add(user.cycleBytes.remaining(remoteTotal))
		}
		for id, usage := range remotes {
			out.Servers[indices[id]].UserTraffic.add(usage)
		}
		if user.bytes() > 0 {
			out.TopUsers = append(out.TopUsers, user)
		}
		if user.enabled && user.ExpireAt > now.Unix() && user.ExpireAt <= expiringBefore {
			out.Expiring = append(out.Expiring, user)
		}
		if user.enabled && user.Limit > 0 && user.bytes() >= user.Limit {
			out.Exhausted = append(out.Exhausted, user)
		}
	})
	if err != nil {
		return out, err
	}
	out.Notifications, err = alerts.ReadNotifications(ctx, tx)
	if err != nil {
		return out, err
	}
	// Host probes can take longer than SQLite reads. Release this snapshot
	// before collecting the live status of the local server.
	if err := tx.Rollback(); err != nil {
		return out, fmt.Errorf("close overview snapshot: %w", err)
	}
	total := float64(out.UserTraffic.Up) + float64(out.UserTraffic.Down)
	for i := range out.Servers {
		server := &out.Servers[i]
		server.System, err = a.readServerStatus(ctx, server.serverRecord)
		if err != nil {
			return out, err
		}
		if total > 0 {
			server.Share = 100 * (float64(server.UserTraffic.Up) + float64(server.UserTraffic.Down)) / total
		}
	}
	sortByTraffic(out.TopUsers)
	sortByTraffic(out.Exhausted)
	slices.SortFunc(out.Expiring, func(a, b trafficUser) int {
		if n := cmp.Compare(a.ExpireAt, b.ExpireAt); n != 0 {
			return n
		}
		return cmp.Compare(a.ID, b.ID)
	})
	const overviewLimit = 5
	out.TopUsers = out.TopUsers[:min(overviewLimit, len(out.TopUsers))]
	out.Exhausted = out.Exhausted[:min(overviewLimit, len(out.Exhausted))]
	out.Expiring = out.Expiring[:min(overviewLimit, len(out.Expiring))]
	return out, nil
}
