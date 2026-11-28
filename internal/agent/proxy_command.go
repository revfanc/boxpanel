package agent

import (
	"context"
	"errors"
	"fmt"
	"net/netip"

	"github.com/revfanc/boxpanel/internal/auth"
	"github.com/revfanc/boxpanel/internal/exitlink"
)

var ErrProxyBusy = errors.New("a proxy test is already pending")

type ProxyTestCommand struct {
	ID    string         `json:"id"`
	Proxy exitlink.Proxy `json:"proxy"`
}

type ProxyTestResult struct {
	ID string `json:"id"`
	IP string `json:"ip,omitempty"`
}

type pendingProxyTest struct {
	command ProxyTestCommand
	result  chan string
}

func (m *Master) TestNodeProxy(ctx context.Context, id int64, proxy exitlink.Proxy) (netip.Addr, error) {
	token, err := auth.RandomToken(16)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("create proxy test ID: %w", err)
	}
	job := &pendingProxyTest{command: ProxyTestCommand{ID: token, Proxy: proxy}, result: make(chan string, 1)}
	m.proxyMu.Lock()
	if m.proxyTests[id] != nil {
		m.proxyMu.Unlock()
		return netip.Addr{}, ErrProxyBusy
	}
	m.proxyTests[id] = job
	m.proxyMu.Unlock()
	defer func() { m.proxyMu.Lock(); delete(m.proxyTests, id); m.proxyMu.Unlock() }()
	select {
	case value := <-job.result:
		ip, err := netip.ParseAddr(value)
		if err != nil || !ValidPublicIP(ip) {
			return netip.Addr{}, ErrProxyTest
		}
		return ip.Unmap(), nil
	case <-ctx.Done():
		return netip.Addr{}, ctx.Err()
	}
}

func (m *Master) proxyCommand(id int64) *ProxyTestCommand {
	m.proxyMu.Lock()
	defer m.proxyMu.Unlock()
	if job := m.proxyTests[id]; job != nil {
		return &job.command
	}
	return nil
}

func (m *Master) FinishProxyTest(identity authenticated, result ProxyTestResult) {
	m.proxyMu.Lock()
	defer m.proxyMu.Unlock()
	job := m.proxyTests[identity.id]
	if job == nil || job.command.ID != result.ID {
		return
	}
	select {
	case job.result <- result.IP:
	default:
	}
}

func (n *Node) runProxyTest(ctx context.Context, cmd ProxyTestCommand) {
	probe := n.ProbeProxy
	if probe == nil {
		probe = TestProxy
	}
	ip, err := probe(ctx, cmd.Proxy)
	result := ProxyTestResult{ID: cmd.ID}
	if err == nil && ip.IsValid() {
		result.IP = ip.String()
	}
	resp, err := n.Client.post(ctx, "/proxy-test-result", result, n.Master.ServerID, n.Key)
	if err == nil {
		resp.Body.Close()
	}
}
