package agent

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/singbox"
	"github.com/revfanc/boxpanel/internal/sysinfo"
	"github.com/revfanc/boxpanel/internal/traffic"
)

type NodeManager interface {
	Apply(context.Context, singbox.Mutation) error
	AgentCertificate(id int64, sni, revision string) ([]byte, string, error)
}

type Node struct {
	Reader     *sql.DB
	Writer     *sql.DB
	Master     db.AgentConnection
	Key        ed25519.PrivateKey
	Client     *Client
	Manager    NodeManager
	Accounting *traffic.Collector
	Logger     *slog.Logger
	// Upgrade, when set, sees every heartbeat reply and may start an
	// automatic upgrade; it must return quickly and never stop the sync.
	Upgrade func(context.Context, HeartbeatReply)
	// ReadLogs provides redacted journal lines after an acknowledged cursor.
	ReadLogs  func(context.Context, string, int) ([]string, string, error)
	ExitPorts func(context.Context) (map[sysinfo.Socket]bool, error)

	syncError              string
	published              map[int64]InboundKeys
	reportedTraffic        map[int64]TrafficCounter
	interfaceEndpointReady bool
	interfacePreference    *string
	logs                   nodeLogs
	exitCapable            bool
}

// Sync is called serially by the heartbeat loop. An unreachable master leaves
// the last committed configuration and credentials in service.
func (n *Node) Sync(ctx context.Context, beat Heartbeat, coreActive func() bool) error {
	applied, err := db.GetSetting(ctx, n.Reader, db.AgentAppliedGeneration)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	beat.Protocol, beat.AppliedGeneration, beat.SyncError = ProtocolVersion, &applied, n.syncError
	if n.exitCapable {
		beat.Exit = n.exitReport(ctx)
	}
	reply, err := n.connect(ctx, beat)
	if errors.Is(err, ErrExitRemoved) {
		if err := n.clearRemovedExit(ctx); err != nil {
			return err
		}
		return ErrUnauthorized
	}
	if err != nil {
		return err
	}
	n.exitCapable = reply.Protocol >= ExitProtocol
	n.syncLogs(ctx, reply)
	if reply.ReadOnly {
		return nil
	}
	if n.Upgrade != nil {
		// Before the protocol checks: an upgrade is how an outdated node recovers.
		n.Upgrade(ctx, reply)
	}
	if reply.Protocol < MinMasterProtocol {
		return n.reportOutdatedMaster(ctx, beat, coreActive)
	}
	if err := n.setInterfacePreference(ctx, reply.Interface); err != nil {
		n.logUploadError(ctx, err)
	}
	uploadCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	interfaceErr := n.reportInterfaceSamples(uploadCtx)
	cancel()
	if errors.Is(interfaceErr, ErrMasterOutdated) {
		return n.reportOutdatedMaster(ctx, beat, coreActive)
	}
	if errors.Is(interfaceErr, ErrAgentUpgradeRequired) {
		return interfaceErr
	}
	n.logUploadError(ctx, interfaceErr)

	// Refresh the generation after a report that may have crossed a quota.
	uploadCtx, cancel = context.WithTimeout(ctx, 10*time.Second)
	changed, trafficErr := n.reportTraffic(uploadCtx)
	cancel()
	if errors.Is(trafficErr, ErrMasterOutdated) {
		return n.reportOutdatedMaster(ctx, beat, coreActive)
	}
	if errors.Is(trafficErr, ErrAgentUpgradeRequired) {
		return trafficErr
	}
	n.logUploadError(ctx, trafficErr)
	if changed {
		reply, err = n.Client.SyncHeartbeat(ctx, n.Master.ServerID, n.Key, beat)
		if err != nil {
			return err
		}
		if reply.ReadOnly {
			n.logs = nodeLogs{}
			return nil
		}
		if reply.Protocol < MinMasterProtocol {
			return n.reportOutdatedMaster(ctx, beat, coreActive)
		}
	}
	if n.syncError == SyncMasterOutdated && interfaceErr != nil {
		return interfaceErr
	}

	keys, materialErr := n.publicKeys(ctx)
	needsApply := reply.DesiredGeneration != applied || materialErr != nil || !beat.SingboxActive
	if !needsApply && !reply.RestartPending && n.syncError == "" && !n.needsPublication(keys) {
		return nil
	}

	if needsApply || reply.RestartPending {
		desired, err := n.Client.Desired(ctx, n.Master.ServerID, n.Key)
		if errors.Is(err, ErrMasterOutdated) {
			return n.reportOutdatedMaster(ctx, beat, coreActive)
		}
		if err != nil {
			return err
		}
		var applyErr error
		if desired.Generation != applied || materialErr != nil || !beat.SingboxActive && desired.RestartID == "" {
			mutation := func(ctx context.Context, tx *sql.Tx) error {
				return n.replaceMirror(ctx, tx, desired)
			}
			if n.Accounting != nil {
				applyErr = n.Accounting.ApplyMirror(ctx, n.Manager, mutation)
			} else {
				applyErr = n.Manager.Apply(ctx, mutation)
			}
			if applyErr == nil {
				applied = desired.Generation
				keys, materialErr = n.publicKeys(ctx)
			}
		}
		if desired.RestartID != "" {
			beat.RestartResult, err = n.restart(ctx, desired.RestartID)
			if err != nil {
				return err
			}
		}
		if applyErr != nil {
			n.syncError = syncErrorCode(applyErr)
			return n.report(ctx, beat, coreActive)
		}
	}

	n.syncError = ""
	if materialErr != nil {
		n.syncError = SyncKeys
	} else if err := n.publish(ctx, keys); err != nil {
		if errors.Is(err, ErrMasterOutdated) {
			return n.reportOutdatedMaster(ctx, beat, coreActive)
		}
		n.syncError = SyncKeys
	}
	return n.report(ctx, beat, coreActive)
}

func (n *Node) logUploadError(ctx context.Context, err error) {
	if err != nil && ctx.Err() == nil && n.Logger != nil {
		n.Logger.Warn("节点流量设置或采样未同步，将自动重试。请检查主控版本、连接与节点数据库。")
	}
}

func (n *Node) reportOutdatedMaster(ctx context.Context, beat Heartbeat, coreActive func() bool) error {
	n.interfaceEndpointReady = false
	if n.syncError != SyncMasterOutdated {
		n.syncError = SyncMasterOutdated
		if err := n.report(ctx, beat, coreActive); err != nil {
			return err
		}
	}
	return ErrMasterOutdated
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
	if !sameMasterConnection(current, n.Master) {
		return fmt.Errorf("node identity changed during synchronization")
	}
	return nil
}
