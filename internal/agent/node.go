package agent

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/singbox"
)

type NodeManager interface {
	Apply(context.Context, singbox.Mutation) error
	AgentCertificate(id int64, sni, revision string) ([]byte, string, error)
}

type Node struct {
	Reader  *sql.DB
	Master  db.AgentConnection
	Key     ed25519.PrivateKey
	Client  *Client
	Manager NodeManager

	syncError string
	published map[int64]InboundKeys
}

// Sync is called serially by the heartbeat loop. An unreachable master leaves
// the last committed configuration and credentials in service.
func (n *Node) Sync(ctx context.Context, beat Heartbeat, coreActive func() bool) error {
	applied, err := db.GetSetting(ctx, n.Reader, db.AgentAppliedGeneration)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	beat.AppliedGeneration, beat.SyncError = &applied, n.syncError
	reply, err := n.Client.SyncHeartbeat(ctx, n.Master.ServerID, n.Key, beat)
	if err != nil || reply.DesiredGeneration == 0 {
		return err
	}

	keys, materialErr := n.publicKeys(ctx)
	apply := reply.DesiredGeneration != applied || materialErr != nil || !beat.SingboxActive
	if !apply && n.syncError == "" && !n.needsPublication(keys) {
		return nil
	}

	if apply {
		desired, err := n.Client.Desired(ctx, n.Master.ServerID, n.Key)
		if err != nil {
			return err
		}
		if err := n.Manager.Apply(ctx, func(ctx context.Context, tx *sql.Tx) error {
			return n.replaceMirror(ctx, tx, desired)
		}); err != nil {
			n.syncError = syncErrorCode(err)
			return n.report(ctx, beat, coreActive)
		}
		applied = desired.Generation
		keys, materialErr = n.publicKeys(ctx)
	}

	n.syncError = ""
	if materialErr != nil {
		n.syncError = SyncKeys
	} else if err := n.publish(ctx, keys); err != nil {
		n.syncError = SyncKeys
	}
	return n.report(ctx, beat, coreActive)
}

func (n *Node) report(ctx context.Context, beat Heartbeat, coreActive func() bool) error {
	beat.SyncError, beat.SingboxActive = n.syncError, coreActive()
	_, err := n.Client.SyncHeartbeat(ctx, n.Master.ServerID, n.Key, beat)
	return err
}

func (n *Node) needsPublication(keys []InboundKeys) bool {
	for _, in := range keys {
		if n.published[in.InboundID] != in {
			return true
		}
	}
	return false
}

func (n *Node) publish(ctx context.Context, keys []InboundKeys) error {
	next := make(map[int64]InboundKeys, len(keys))
	for _, in := range keys {
		if n.published[in.InboundID] != in {
			if err := n.Client.PublishKeys(ctx, n.Master.ServerID, n.Key, in); err != nil {
				return err
			}
			// Retain successful uploads even if another inbound needs retry.
			if n.published == nil {
				n.published = make(map[int64]InboundKeys)
			}
			n.published[in.InboundID] = in
		}
		next[in.InboundID] = in
	}
	n.published = next
	return nil
}

func syncErrorCode(err error) string {
	var apply *singbox.ApplyError
	if !errors.As(err, &apply) {
		return SyncApply
	}
	if apply.RollbackFailed {
		return SyncRollback
	}
	// Match only recognized diagnostics. Arbitrary commands, hostnames and
	// output from sing-box or systemd are never sent over the wire.
	detail := strings.ToLower(apply.Detail)
	cause := errors.Unwrap(apply)
	if strings.Contains(detail, "address already in use") ||
		cause != nil && strings.Contains(cause.Error(), "different from the Clash API port") {
		return SyncPortInUse
	}
	switch apply.Code {
	case "singbox_check_failed":
		return SyncCheckFailed
	case "singbox_restart_failed":
		return SyncRestart
	case "singbox_firewall_failed":
		return SyncFirewall
	case "singbox_apply_timeout":
		return SyncTimeout
	default:
		return SyncApply
	}
}

func (n *Node) verifyIdentity(ctx context.Context, q db.SettingsDB) error {
	current, err := db.GetSetting(ctx, q, db.AgentMaster)
	if err != nil {
		return err
	}
	if current != n.Master {
		return fmt.Errorf("node identity changed during synchronization")
	}
	return nil
}
