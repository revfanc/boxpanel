package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/revfanc/boxpanel/internal/confgen"
	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/sysinfo"
)

type inboundQuery interface {
	rowReader
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

type portReservations struct {
	inbounds    []inboundRecord
	reserved    map[int]bool
	controlPort int // Clash API is TCP; Hysteria2 UDP may share its number.
	sockets     map[sysinfo.Socket]bool
}

func (a *api) inboundReservations(ctx context.Context, q inboundQuery, sockets map[sysinfo.Socket]bool) (portReservations, error) {
	p := portReservations{reserved: map[int]bool{a.opts.PanelPort: true}, sockets: sockets}
	for _, key := range []db.SettingKey[int]{db.WebPort, db.ClashAPIPort} {
		value, err := db.GetSetting(ctx, q, key)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return p, err
		}
		if key == db.ClashAPIPort {
			p.controlPort = value
		} else {
			p.reserved[value] = true
		}
	}
	rows, err := q.QueryContext(ctx, "SELECT "+inboundColumns+" FROM inbounds ORDER BY id")
	if err != nil {
		return p, fmt.Errorf("read port reservations: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		in, err := scanInbound(rows)
		if err != nil {
			return p, err
		}
		p.inbounds = append(p.inbounds, in)
	}
	if err := rows.Err(); err != nil {
		return p, fmt.Errorf("read port reservations: %w", err)
	}
	return p, nil
}
func hopping(in inboundRecord) confgen.PortHopping {
	var options confgen.Hysteria2Options
	if in.Protocol == "hysteria2" && in.Enabled {
		_ = json.Unmarshal(in.Options, &options)
	}
	return options.PortHopping
}
func containsPort(h confgen.PortHopping, port int) bool {
	return h.Enabled && port >= h.Start && port <= h.End
}
func (p portReservations) check(in inboundRecord) error {
	portErr := func() error {
		return conflict("port_in_use", "port", "请更换服务端口或跳跃范围，避开面板、控制接口、已有入站及其他程序占用的端口。")
	}
	if p.reserved[in.Port] || in.Protocol == "vless_reality" && in.Port == p.controlPort {
		return portErr()
	}
	proto := "tcp"
	if in.Protocol == "hysteria2" {
		proto = "udp"
	}
	owned := false
	oldUDPPort := 0
	h := hopping(in)
	for _, old := range p.inbounds {
		if old.ID == in.ID {
			owned = old.Enabled && old.Port == in.Port && old.Protocol == in.Protocol
			if old.Enabled && old.Protocol == "hysteria2" {
				oldUDPPort = old.Port
			}
			continue
		}
		if old.Tag == in.Tag {
			return conflict("tag_exists", "tag", "请换一个尚未使用的入站标识。")
		}
		if old.Port == in.Port {
			return portErr()
		}
		if !in.Enabled || in.Protocol != "hysteria2" || !old.Enabled || old.Protocol != "hysteria2" {
			continue
		}
		other := hopping(old)
		if containsPort(h, old.Port) || containsPort(other, in.Port) || h.Enabled && other.Enabled && h.Start <= other.End && other.Start <= h.End {
			return portErr()
		}
	}
	if in.Enabled && !owned && p.sockets[sysinfo.Socket{Port: in.Port, Proto: proto}] {
		return portErr()
	}
	if h.Enabled {
		for socket := range p.sockets {
			if socket.Proto == "udp" && socket.Port != in.Port && socket.Port != oldUDPPort && containsPort(h, socket.Port) {
				return portErr()
			}
		}
	}
	return nil
}
func (p portReservations) suggest(protocol string) (int, error) {
	preferred := 443
	if protocol == "hysteria2" {
		preferred = 8443
	}
	in := inboundRecord{Protocol: protocol, Enabled: true}
	for _, port := range []int{preferred, 10000} {
		in.Port = port
		if p.check(in) == nil {
			return port, nil
		}
	}
	for port := 10001; port <= 60000; port++ {
		in.Port = port
		if p.check(in) == nil {
			return port, nil
		}
	}
	return 0, conflict("port_in_use", "port", "没有可推荐的端口，请释放 10000–60000 范围内的端口后重试。")
}
