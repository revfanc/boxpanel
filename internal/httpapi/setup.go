package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/reality"
)

const setupNetworkTimeout = 3 * time.Second

type setupStatus struct {
	Completed bool   `json:"completed"`
	Step      string `json:"step"`
	db.SetupChoices
}

func validSetupStep(step string) bool {
	switch step {
	case "welcome", "check", "scenario", "node", "user", "firewall", "finish":
		return true
	}
	return false
}

func readSetup(ctx context.Context, q db.SettingsDB) (setupStatus, error) {
	var s setupStatus
	var err error
	if s.Completed, err = db.GetSetting(ctx, q, db.SetupCompleted); err != nil {
		return s, err
	}
	if s.Step, err = db.GetSetting(ctx, q, db.SetupStep); err != nil {
		return s, err
	}
	if !validSetupStep(s.Step) {
		return s, fmt.Errorf("read setup: invalid stored step; repair setup_step")
	}
	s.SetupChoices, err = db.GetSetting(ctx, q, db.SetupPreferences)
	if errors.Is(err, sql.ErrNoRows) {
		// Existing initialized databases predate T5.1. No migration is needed.
		s.SetupChoices = db.SetupChoices{Hysteria2: true}
		err = nil
	}
	return s, err
}

func (a *api) setupGet(w http.ResponseWriter, r *http.Request) {
	conn, err := a.readDB()
	if err != nil {
		a.resourceError(w, err)
		return
	}
	tx, err := conn.BeginTx(r.Context(), nil)
	if err != nil {
		a.resourceError(w, err)
		return
	}
	defer tx.Rollback()
	s, err := readSetup(r.Context(), tx)
	if err != nil {
		a.resourceError(w, err)
		return
	}
	writeJSON(w, 200, s)
}

func (a *api) setupPut(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Completed *bool   `json:"completed"`
		Step      *string `json:"step"`
		Domain    *string `json:"domain"`
		Hysteria2 *bool   `json:"hysteria2"`
	}
	if !decodeResource(w, r, &in) {
		return
	}
	if in.Step != nil && !validSetupStep(*in.Step) {
		a.resourceError(w, invalid("step", "请选择 welcome、check、scenario、node、user、firewall 或 finish。"))
		return
	}
	a.settingsMu.Lock()
	defer a.settingsMu.Unlock()
	var publicHost string
	if in.Domain != nil {
		domain := strings.ToLower(strings.TrimSpace(*in.Domain))
		in.Domain = &domain
		if domain != "" {
			if _, err := netip.ParseAddr(domain); err == nil || !reality.ValidServer(domain) {
				a.resourceError(w, invalid("domain", "请填写 ASCII 域名，不含协议、端口或路径；无域名请选择没有域名。"))
				return
			}
			if !a.validateSetupDomain(w, r, domain) {
				return
			}
			publicHost = domain
		}
	}
	conn, err := a.readDB()
	if err != nil {
		a.resourceError(w, err)
		return
	}
	// Resolve a switch back to IP before taking the single-connection DB lock.
	if in.Domain != nil && *in.Domain == "" {
		choices, err := db.GetSetting(r.Context(), conn, db.SetupPreferences)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			a.resourceError(w, err)
			return
		}
		if choices.Domain != "" {
			ctx, cancel := context.WithTimeout(r.Context(), setupNetworkTimeout)
			ips, err := a.localPublicIPs(ctx)
			cancel()
			if err != nil {
				a.systemError(w, err)
				return
			}
			publicHost = ips[0].String()
		}
	}
	tx, err := conn.BeginTx(r.Context(), nil)
	if err != nil {
		a.resourceError(w, err)
		return
	}
	defer tx.Rollback()
	s, err := readSetup(r.Context(), tx)
	if err != nil {
		a.resourceError(w, err)
		return
	}
	if in.Completed != nil {
		s.Completed = *in.Completed
	}
	if in.Step != nil {
		s.Step = *in.Step
	}
	if in.Domain != nil {
		s.Domain = *in.Domain
	}
	if in.Hysteria2 != nil {
		s.Hysteria2 = *in.Hysteria2
	}
	// Only onboarding/client metadata is changed here, just like /settings.
	// Inbounds and users continue to use their existing Manager.Apply endpoints.
	for _, err = range []error{
		db.SetSetting(r.Context(), tx, db.SetupCompleted, s.Completed),
		db.SetSetting(r.Context(), tx, db.SetupStep, s.Step),
		db.SetSetting(r.Context(), tx, db.SetupPreferences, s.SetupChoices),
	} {
		if err != nil {
			a.resourceError(w, err)
			return
		}
	}
	if publicHost != "" {
		if err = db.SetSetting(r.Context(), tx, db.PublicHost, publicHost); err != nil {
			a.resourceError(w, err)
			return
		}
	}
	if err = tx.Commit(); err != nil {
		a.resourceError(w, fmt.Errorf("save setup: %w", err))
		return
	}
	if publicHost != "" && a.opts.Certificates != nil {
		a.opts.Certificates.Changed()
	}
	writeJSON(w, 200, s)
}

// The source is the server's IP discovery, never request input, DNS for
// public_host, Host/X-Forwarded-* headers, or saved user-editable settings.
func (a *api) localPublicIPs(ctx context.Context) ([]netip.Addr, error) {
	value, err := a.opts.System.PublicIP(ctx)
	if err != nil {
		return nil, fmt.Errorf("discover local public IP: %w", err)
	}
	var ips []netip.Addr
	for _, raw := range []string{value.IPv4, value.IPv6} {
		if raw == "" {
			continue
		}
		ip, err := netip.ParseAddr(raw)
		if err != nil || !reality.PublicIP(ip) {
			return nil, fmt.Errorf("discover local public IP: invalid public address")
		}
		ips = append(ips, ip.Unmap())
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("discover local public IP: no address available")
	}
	return ips, nil
}

func (a *api) validateSetupDomain(w http.ResponseWriter, r *http.Request, domain string) bool {
	ctx, cancel := context.WithTimeout(r.Context(), setupNetworkTimeout)
	defer cancel()
	// Discover first: unsupported/dev hosts must not issue a DNS query merely
	// to return system_unsupported. Both operations share the same deadline.
	local, err := a.localPublicIPs(ctx)
	if err != nil {
		a.systemError(w, err)
		return false
	}
	answers, dnsErr := a.opts.LookupIP(ctx, domain)
	matched := dnsErr == nil && len(answers) > 0 && ctx.Err() == nil
	for _, answer := range answers {
		found := false
		for _, ip := range local {
			if answer.Unmap() == ip && reality.PublicIP(answer) {
				found = true
			}
		}
		matched = matched && found
	}
	if !matched {
		writeError(w, 409, "domain_not_pointing_here", "域名尚未指向本机公网 IP", "请将 A/AAAA 记录全部指向本机公网地址，关闭 CDN 代理，等待 DNS 生效后重试；也可选择没有域名。")
		return false
	}
	return true
}

func (a *api) portProbe(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Port int `json:"port"`
	}
	if !decodeResource(w, r, &in) {
		return
	}
	if in.Port < 1 || in.Port > 65535 {
		a.resourceError(w, invalid("port", "请选择 1–65535 的 TCP 端口。"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), setupNetworkTimeout)
	defer cancel()
	ips, err := a.localPublicIPs(ctx)
	if err != nil {
		a.systemError(w, err)
		return
	}
	type result struct {
		IP string `json:"ip"`
		OK bool   `json:"ok"`
	}
	results := make([]result, len(ips))
	var wg sync.WaitGroup
	for i, ip := range ips {
		wg.Add(1)
		go func(i int, ip netip.Addr) {
			defer wg.Done()
			conn, err := a.opts.DialContext(ctx, "tcp", net.JoinHostPort(ip.String(), strconv.Itoa(in.Port)))
			results[i] = result{IP: ip.String(), OK: err == nil && ctx.Err() == nil}
			if conn != nil {
				_ = conn.Close()
			}
		}(i, ip)
	}
	wg.Wait()
	writeJSON(w, 200, struct {
		Port    int      `json:"port"`
		Results []result `json:"results"`
		Hint    string   `json:"hint"`
	}{in.Port, results, "仅检测本机 TCP 自连，不代表外网或云安全组已放行；失败请核对监听、本机防火墙、安全组及 NAT 回环支持，再从外部客户端验证。UDP 无法自动检测。"})
}
