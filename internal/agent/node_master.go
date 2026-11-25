package agent

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"errors"
	"fmt"
	"slices"

	"github.com/revfanc/boxpanel/internal/db"
)

func sameMasterConnection(a, b db.AgentConnection) bool {
	return a.SameIdentity(b) && a.MasterURL == b.MasterURL && slices.Equal(a.AlternateURLs, b.AlternateURLs)
}

func changeMaster(ctx context.Context, conn *sql.DB, current db.AgentConnection, addresses []string) (db.AgentConnection, error) {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return current, fmt.Errorf("begin master address update: %w", err)
	}
	defer tx.Rollback()
	saved, err := db.GetSetting(ctx, tx, db.AgentMaster)
	if err != nil {
		return current, err
	}
	if !sameMasterConnection(saved, current) {
		return current, fmt.Errorf("主控地址或节点身份已变更，请运行 boxpanel info 核对后重试")
	}
	saved.MasterURL, saved.AlternateURLs = addresses[0], addresses[1:]
	if err := db.SetSetting(ctx, tx, db.AgentMaster, saved); err != nil {
		return current, err
	}
	if err := tx.Commit(); err != nil {
		return current, fmt.Errorf("save master addresses: %w", err)
	}
	return saved, nil
}

func masterAddresses(first []string, current db.AgentConnection) []string {
	var out []string
	for _, addresses := range [][]string{first, {current.MasterURL}, current.AlternateURLs} {
		for _, address := range addresses {
			normalized, err := NormalizeMasterURL(address)
			if err == nil && !slices.Contains(out, normalized) {
				out = append(out, normalized)
			}
			if len(out) == MaxMasterURLs {
				return out
			}
		}
	}
	return out
}

func probeMaster(ctx context.Context, client *Client, current db.AgentConnection, key ed25519.PrivateKey, beat Heartbeat) (HeartbeatReply, error) {
	reply, err := client.ProbeHeartbeat(ctx, current.ServerID, key, beat)
	if err != nil {
		return reply, err
	}
	if reply.Protocol < MinMasterProtocol || reply.DesiredGeneration <= 0 {
		return reply, ErrMasterOutdated
	}
	if reply.ReadOnly {
		return reply, db.ErrMigrationReadOnly
	}
	return reply, nil
}

func SetMaster(ctx context.Context, conn *sql.DB, current db.AgentConnection, key ed25519.PrivateKey, address string, beat Heartbeat) (string, error) {
	client, err := NewClient(address, current.Pin)
	if err != nil {
		return "", err
	}
	defer client.Close()
	beat.Protocol = ProtocolVersion
	if _, err := probeMaster(ctx, client, current, key, beat); err != nil {
		return "", fmt.Errorf("验证新主控心跳: %w", err)
	}
	updated, err := changeMaster(ctx, conn, current, masterAddresses([]string{client.MasterURL()}, current))
	return updated.MasterURL, err
}

func (n *Node) Close() {
	if n.Client != nil {
		n.Client.Close()
	}
}

func (n *Node) clientFor(address string) (*Client, error) {
	if n.Client != nil && n.Client.MasterURL() == address {
		return n.Client, nil
	}
	return NewClient(address, n.Master.Pin)
}

func (n *Node) useClient(client *Client) {
	if n.Client != client {
		n.Close()
		n.Client = client
		n.published, n.reportedTraffic = nil, nil
		n.interfaceEndpointReady, n.interfacePreference = false, nil
	}
}

func (n *Node) connect(ctx context.Context, beat Heartbeat) (HeartbeatReply, error) {
	current, err := db.GetSetting(ctx, n.Reader, db.AgentMaster)
	if err != nil {
		return HeartbeatReply{}, err
	}
	if !current.SameIdentity(n.Master) {
		return HeartbeatReply{}, fmt.Errorf("node identity changed during synchronization")
	}
	n.Master = current
	tried := map[string]bool{}
	firstErr := ErrMasterUnreachable
	for i, address := range masterAddresses(nil, current) {
		tried[address] = true
		client, err := n.clientFor(address)
		if err != nil {
			return HeartbeatReply{}, err
		}
		reply, err := client.SyncHeartbeat(ctx, current.ServerID, n.Key, beat)
		if err == nil {
			n.useClient(client)
			return n.followMasterURLs(ctx, beat, reply, tried)
		}
		if client != n.Client {
			client.Close()
		}
		if i == 0 {
			firstErr = err
			// Honor a primary that can still reject this identity or require an
			// upgrade. An old backup may retain a former authorization.
			var skew *ClockSkewError
			if errors.Is(err, ErrUnauthorized) || errors.Is(err, ErrAgentUpgradeRequired) || errors.As(err, &skew) {
				return reply, err
			}
		}
	}
	return HeartbeatReply{}, firstErr
}

func (n *Node) followMasterURLs(ctx context.Context, beat Heartbeat, reply HeartbeatReply, tried map[string]bool) (HeartbeatReply, error) {
	for _, address := range reply.MasterURLs {
		if address == n.Client.MasterURL() && !reply.ReadOnly {
			return reply, nil
		}
		if tried[address] || address == n.Master.MasterURL {
			continue
		}
		client, err := n.clientFor(address)
		if err != nil {
			continue
		}
		if _, err := probeMaster(ctx, client, n.Master, n.Key, beat); err != nil {
			client.Close()
			continue
		}
		addresses := masterAddresses(append([]string{address}, reply.MasterURLs...), n.Master)
		updated, err := changeMaster(ctx, n.Writer, n.Master, addresses)
		if err != nil {
			client.Close()
			return HeartbeatReply{}, err
		}
		n.Master = updated
		n.useClient(client)
		// Only the normal heartbeat after persisting the URL counts as arrival.
		return client.SyncHeartbeat(ctx, updated.ServerID, n.Key, beat)
	}
	return reply, nil
}
