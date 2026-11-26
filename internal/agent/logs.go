package agent

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/revfanc/boxpanel/internal/auth"
)

const (
	LogsPath        = Prefix + "/logs"
	LogLines        = 200
	MaxLogBody      = 2 << 20
	maxLogLineBytes = 1024
	logLease        = 60 * time.Second
	logInterval     = 3 * time.Second
	maxLogViewers   = 32
)

var (
	ErrInvalidLogs = errors.New("invalid node log batch")
	ErrLogInactive = errors.New("node log request expired")
	ErrLogViewers  = errors.New("too many simultaneous log viewers")
)

type LogRequest struct {
	ID    string `json:"id"`
	TTLMS int64  `json:"ttl_ms"`
}

type LogReport struct {
	ID       string   `json:"id"`
	BatchID  string   `json:"batch_id"`
	Snapshot bool     `json:"snapshot"`
	Lines    []string `json:"lines"`
}

type LogView struct {
	Ready bool     `json:"ready"`
	Lines []string `json:"lines"`
}

type logStream struct {
	id        string
	viewers   map[string]time.Time
	lastBatch string
	ready     bool
	lines     []string
}

func ValidLogViewer(value string) bool {
	if len(value) != 32 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

// activeLog drops expired leases and returns the expiry of the last viewer.
// All callers hold logMu.
func (m *Master) activeLog(serverID int64, now time.Time) (*logStream, time.Time) {
	stream := m.logs[serverID]
	if stream == nil {
		return nil, time.Time{}
	}
	var expires time.Time
	for id, until := range stream.viewers {
		if !until.After(now) {
			delete(stream.viewers, id)
		} else if until.After(expires) {
			expires = until
		}
	}
	if expires.IsZero() {
		delete(m.logs, serverID)
		return nil, expires
	}
	return stream, expires
}

func (m *Master) pruneLogs(now time.Time) {
	for id := range m.logs {
		m.activeLog(id, now)
	}
}

func (m *Master) WatchLogs(ctx context.Context, serverID int64, viewer string) error {
	m.logMu.Lock()
	defer m.logMu.Unlock()
	var supported bool
	err := m.reader.QueryRowContext(ctx, `SELECT monitoring IS NOT NULL AND protocol>=? FROM servers
		WHERE id=? AND is_local=0 AND status<>'removed'`, MinNodeProtocol, serverID).Scan(&supported)
	if err != nil {
		return fmt.Errorf("read node log capability: %w", err)
	}
	if !supported {
		return ErrMonitoringRequired
	}
	now := m.now()
	stream, _ := m.activeLog(serverID, now)
	if stream == nil {
		id, err := auth.RandomToken(16)
		if err != nil {
			return fmt.Errorf("generate log request ID: %w", err)
		}
		stream = &logStream{id: id, viewers: make(map[string]time.Time)}
		m.logs[serverID] = stream
	}
	if _, exists := stream.viewers[viewer]; !exists && len(stream.viewers) >= maxLogViewers {
		return ErrLogViewers
	}
	stream.viewers[viewer] = now.Add(logLease)
	return nil
}

func (m *Master) UnwatchLogs(serverID int64, viewer string) {
	m.logMu.Lock()
	defer m.logMu.Unlock()
	if stream := m.logs[serverID]; stream != nil {
		delete(stream.viewers, viewer)
		m.activeLog(serverID, m.now())
	}
}

func (m *Master) Logs(serverID int64) LogView {
	m.logMu.Lock()
	defer m.logMu.Unlock()
	stream, _ := m.activeLog(serverID, m.now())
	if stream == nil {
		return LogView{Lines: []string{}}
	}
	return LogView{Ready: stream.ready, Lines: append([]string{}, stream.lines...)}
}

func (m *Master) logRequest(serverID int64) *LogRequest {
	m.logMu.Lock()
	defer m.logMu.Unlock()
	now := m.now()
	stream, expires := m.activeLog(serverID, now)
	if stream == nil || expires.Sub(now) < time.Millisecond {
		return nil
	}
	return &LogRequest{ID: stream.id, TTLMS: expires.Sub(now).Milliseconds()}
}

func (m *Master) RecordLogs(ctx context.Context, identity authenticated, report LogReport) error {
	if len(decodeBytes(report.ID, 16)) == 0 || len(decodeBytes(report.BatchID, 16)) == 0 ||
		report.Lines == nil || len(report.Lines) > LogLines {
		return ErrInvalidLogs
	}
	for _, line := range report.Lines {
		if len(line) > maxLogLineBytes {
			return ErrInvalidLogs
		}
	}
	m.logMu.Lock()
	defer m.logMu.Unlock()
	var supported bool
	err := m.reader.QueryRowContext(ctx, `SELECT monitoring IS NOT NULL AND protocol>=? FROM servers WHERE `+agentIdentity,
		MinNodeProtocol, identity.id, identity.publicKey).Scan(&supported)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrUnauthorized
	}
	if err != nil {
		return fmt.Errorf("read node log authorization: %w", err)
	}
	if !supported {
		return ErrAgentUpgradeRequired
	}
	stream, _ := m.activeLog(identity.id, m.now())
	if stream == nil || stream.id != report.ID {
		return ErrLogInactive
	}
	if stream.lastBatch == report.BatchID {
		return nil
	}
	if !stream.ready && !report.Snapshot {
		return ErrInvalidLogs
	}
	if report.Snapshot {
		stream.lines = stream.lines[:0]
	}
	stream.lines = append(stream.lines, report.Lines...)
	if extra := len(stream.lines) - LogLines; extra > 0 {
		stream.lines = slices.Delete(stream.lines, 0, extra)
	}
	stream.lastBatch, stream.ready = report.BatchID, true
	return nil
}

type nodeLogs struct {
	id         string
	expires    time.Time
	cursor     string
	nextCursor string
	started    bool
	pending    *LogReport
}

func (n *Node) HeartbeatInterval() time.Duration {
	if time.Now().Before(n.logs.expires) {
		return logInterval
	}
	return SyncInterval
}

func (n *Node) syncLogs(ctx context.Context, reply HeartbeatReply) {
	request := reply.LogRequest
	if n.ReadLogs == nil || reply.ReadOnly || reply.Protocol < MinMasterProtocol || request == nil ||
		len(decodeBytes(request.ID, 16)) == 0 || request.TTLMS <= 0 || request.TTLMS > logLease.Milliseconds() {
		n.logs = nodeLogs{}
		return
	}
	state := &n.logs
	if state.id != request.ID {
		*state = nodeLogs{id: request.ID}
	}
	state.expires = time.Now().Add(time.Duration(request.TTLMS) * time.Millisecond)
	ctx, cancel := context.WithDeadline(ctx, minTime(state.expires, time.Now().Add(logInterval)))
	defer cancel()
	if state.pending == nil {
		lines, cursor, err := n.ReadLogs(ctx, state.cursor, LogLines)
		if err != nil {
			return // The next leased heartbeat retries without logging raw output.
		}
		if state.started && len(lines) == 0 {
			state.cursor = cursor
			return
		}
		batchID, err := auth.RandomToken(16)
		if err != nil {
			return
		}
		for i, line := range lines {
			if len(line) > maxLogLineBytes {
				end := maxLogLineBytes - len("…")
				for !utf8.RuneStart(line[end]) {
					end--
				}
				lines[i] = line[:end] + "…"
			}
		}
		state.pending = &LogReport{ID: request.ID, BatchID: batchID, Snapshot: !state.started, Lines: append([]string{}, lines...)}
		state.nextCursor = cursor
	}
	err := n.Client.ReportLogs(ctx, n.Master.ServerID, n.Key, *state.pending)
	if errors.Is(err, ErrLogInactive) {
		n.logs = nodeLogs{}
	} else if err == nil {
		state.cursor, state.started, state.pending = state.nextCursor, true, nil
	}
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
