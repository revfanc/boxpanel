package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"github.com/revfanc/boxpanel/internal/acme"
	"github.com/revfanc/boxpanel/internal/app"
	"github.com/revfanc/boxpanel/internal/firewall"
	"github.com/revfanc/boxpanel/internal/singbox"
	"github.com/revfanc/boxpanel/internal/sysinfo"
)

type systemDependencies struct {
	os     string
	opener app.PortOpener
	bbr    func(context.Context) (sysinfo.BBR, error)
	apply  singbox.ApplyOptions
}

func runSystem(args []string, stdout, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return systemCommand(ctx, args, systemDependencies{}, stdout, stderr)
}

func systemCommand(ctx context.Context, args []string, deps systemDependencies, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintln(stdout, "Usage: boxpanel system firewall-open|firewall-cleanup|bbr|ready [path options]\nfirewall-cleanup requires both managed services to be stopped; ready probes local HTTPS using the installed certificate.")
		return 0
	}
	command := args[0]
	if command != "firewall-open" && command != "firewall-cleanup" && command != "bbr" && command != "ready" {
		fmt.Fprintln(stderr, "未知系统命令；请运行 boxpanel system --help。")
		return 2
	}
	flags := flag.NewFlagSet("system "+command, flag.ContinueOnError)
	flags.SetOutput(stdout)
	paths := addPathFlags(flags)
	core := addSingboxPaths(flags)
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintln(stderr, "请运行 boxpanel system --help 检查参数。")
		return 2
	}
	resolved, err := paths.resolve(false)
	if err != nil || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "请运行 boxpanel system --help 检查路径参数。")
		return 2
	}
	if deps.os == "" {
		deps.os = runtime.GOOS
	}
	if deps.os != "linux" {
		fmt.Fprintln(stderr, "系统操作需要 Linux/systemd；请在 VPS 上重试。")
		return 1
	}
	if command == "bbr" {
		if deps.bbr == nil {
			deps.bbr = sysinfo.New(sysinfo.Options{}).EnableBBR
		}
		_, err = deps.bbr(ctx)
	} else {
		conn, openErr := app.OpenExisting(ctx, resolved.Database)
		err = openErr
		if err == nil {
			defer conn.Close()
			var cfg app.Config
			if command != "firewall-cleanup" {
				cfg, err = app.LoadConfig(ctx, conn)
			}
			if err == nil && command == "firewall-open" {
				if deps.opener == nil {
					deps.opener = firewall.New(firewall.Options{})
				}
				err = app.OpenPanelFirewall(ctx, conn, cfg.WebPort, true, deps.opener)
			} else if err == nil && command == "ready" {
				var file string
				file, err = acme.ActiveFile(ctx, conn, resolved.CertificateDir(), resolved.CertFile)
				if err == nil {
					err = panelReady(ctx, cfg, file)
				}
			} else if err == nil {
				deps.apply.OS = deps.os
				var manager *singbox.Manager
				manager, err = newApplyManager(conn, resolved, core.resolve(paths, false), deps.apply)
				if err == nil {
					err = manager.CleanupFirewall(ctx)
				}
			}
		}
	}
	if err != nil {
		// Raw database/command errors may contain credentials. Keep output actionable.
		fmt.Fprintln(stderr, "系统操作未完成。请检查已初始化的数据库、Linux 防火墙权限或 BBR 支持；清理规则前须停止 boxpanel 和 boxpanel-singbox 服务，保留文件并重试。")
		return 1
	}
	fmt.Fprintf(stdout, "系统操作 %s 已完成。\n", command)
	return 0
}

// Probe loopback, never public_host or an environment proxy. Trust the installed
// certificate and validate its SAN/time normally, including self-signed panels.
func panelReady(ctx context.Context, cfg app.Config, certFile string) error {
	certificate, err := os.ReadFile(certFile)
	if err != nil {
		return fmt.Errorf("read readiness certificate: %w", err)
	}
	roots := x509.NewCertPool()
	block, _ := pem.Decode(certificate)
	if block == nil || block.Type != "CERTIFICATE" || !roots.AppendCertsFromPEM(certificate) {
		return fmt.Errorf("invalid readiness certificate")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return fmt.Errorf("parse readiness certificate: %w", err)
	}
	var name string
	if len(leaf.DNSNames) > 0 {
		name = leaf.DNSNames[0]
	} else if len(leaf.IPAddresses) > 0 {
		name = leaf.IPAddresses[0].String()
	} else {
		return fmt.Errorf("readiness certificate has no SAN")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: name}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	url := "https://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.WebPort)) + "/"
	if cfg.WebPath != "" {
		url += cfg.WebPath + "/"
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"api/v1/auth/me", nil)
	if err != nil {
		return fmt.Errorf("create readiness request: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("probe local panel: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		return fmt.Errorf("panel authentication endpoint is not ready")
	}
	return nil
}
