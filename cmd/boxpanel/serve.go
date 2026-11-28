package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/revfanc/boxpanel/internal/acme"
	"github.com/revfanc/boxpanel/internal/agent"
	"github.com/revfanc/boxpanel/internal/alerts"
	"github.com/revfanc/boxpanel/internal/app"
	"github.com/revfanc/boxpanel/internal/auth"
	"github.com/revfanc/boxpanel/internal/backup"
	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/firewall"
	"github.com/revfanc/boxpanel/internal/host"
	"github.com/revfanc/boxpanel/internal/httpapi"
	"github.com/revfanc/boxpanel/internal/migration"
	"github.com/revfanc/boxpanel/internal/singbox"
	"github.com/revfanc/boxpanel/internal/spa"
	"github.com/revfanc/boxpanel/internal/subscription"
	"github.com/revfanc/boxpanel/internal/sysinfo"
	"github.com/revfanc/boxpanel/internal/traffic"
	"github.com/revfanc/boxpanel/web"
)

type serveOptions struct {
	webPath       string
	port          int
	webPathSet    bool
	portSet       bool
	dev           bool
	paths         app.Paths
	listenHost    string
	core          singboxPaths
	acmeDirectory string
}

func resolveServeConfig(ctx context.Context, conn *sql.DB, opts serveOptions) (app.Config, error) {
	cfg, err := app.LoadConfig(ctx, conn)
	if err != nil {
		return cfg, err
	}
	if opts.portSet {
		cfg.WebPort = opts.port
	}
	if opts.webPathSet {
		cfg.WebPath = opts.webPath
	}
	if opts.dev {
		cfg.WebPort, cfg.WebPath, cfg.PublicHost, cfg.TLSMode = 2053, "", "127.0.0.1", "http"
	} else if cfg.TLSMode != "self_signed" && cfg.TLSMode != "acme" {
		return cfg, fmt.Errorf("HTTP database requires serve --dev; initialize a separate data directory for HTTPS")
	}
	return cfg, nil
}

func parseServeOptions(args []string, output io.Writer) (serveOptions, error) {
	opts := serveOptions{}
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(output)
	paths := addPathFlags(flags)
	paths.addACME(flags)
	core := addSingboxPaths(flags)
	flags.StringVar(&opts.webPath, "web-path", "", "override database web_path (lowercase letters and digits; empty mounts at /)")
	flags.IntVar(&opts.port, "port", 0, "override database web_port (1-65535)")
	flags.StringVar(&opts.listenHost, "listen-host", os.Getenv("BOXPANEL_LISTEN_HOST"), "listen IP (BOXPANEL_LISTEN_HOST; default all interfaces; use 127.0.0.1 for local testing)")
	flags.BoolVar(&opts.dev, "dev", false, "use HTTP on 127.0.0.1:2053 with an empty web_path; initialize dev DB on first start")
	if err := flags.Parse(args); err != nil {
		return opts, fmt.Errorf("parse serve options: %w", err)
	}
	if flags.NArg() != 0 {
		return opts, fmt.Errorf("unexpected serve arguments; run 'boxpanel serve --help' for usage")
	}
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "web-path" {
			opts.webPathSet = true
		}
		if f.Name == "port" {
			opts.portSet = true
		}
	})
	if opts.dev {
		opts.webPath = ""
		opts.port = 2053
		opts.listenHost = "127.0.0.1"
	}
	if opts.portSet && (opts.port < 1 || opts.port > 65535) {
		return opts, fmt.Errorf("invalid port: choose --port between 1 and 65535")
	}
	if err := spa.ValidateWebPath(opts.webPath); err != nil {
		return opts, fmt.Errorf("parse serve options: %w", err)
	}
	if opts.listenHost != "" && net.ParseIP(opts.listenHost) == nil {
		return opts, fmt.Errorf("invalid listen host: choose an IP address using --listen-host")
	}
	var err error
	opts.paths, err = paths.resolve(opts.dev)
	if err != nil {
		return opts, err
	}
	opts.core = core.resolve(paths, opts.dev)
	opts.acmeDirectory = paths.acmeDirectory
	return opts, nil
}

func runServe(args []string, stdout, stderr io.Writer) int {
	opts, err := parseServeOptions(args, stdout)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		fmt.Fprintf(stderr, "boxpanel: %v; run 'boxpanel serve --help' for usage\n", err)
		return 2
	}
	logger := slog.New(slog.NewTextHandler(stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := serve(ctx, opts, logger, stdout); err != nil {
		logger.Error("无法启动 boxpanel 服务，请按错误提示修复后重试。", "error", err)
		return 1
	}
	return 0
}

func serve(ctx context.Context, opts serveOptions, logger *slog.Logger, stdout io.Writer) error {
	if opts.dev {
		result, err := app.Initialize(ctx, app.InitOptions{Paths: opts.paths, Dev: true})
		if err != nil && !errors.Is(err, app.ErrInitialized) {
			return fmt.Errorf("initialize development database: %w", err)
		}
		if err == nil {
			printInitialized(stdout, result)
		}
	}
	conn, err := app.OpenExisting(ctx, opts.paths.Database)
	if err != nil {
		return err
	}
	defer conn.Close()
	role, err := db.GetSetting(ctx, conn, db.NodeRole)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if role == db.RoleAgent {
		return serveAgent(ctx, conn, opts, logger)
	}
	if role != "" && role != db.RoleMaster {
		return fmt.Errorf("节点角色无效，请检查数据库中的 node_role 设置")
	}
	cfg, err := resolveServeConfig(ctx, conn, opts)
	if err != nil {
		return fmt.Errorf("load panel settings (check the data directory and initialized database): %w", err)
	}
	if _, err := conn.ExecContext(ctx, "UPDATE servers SET version=?, updated_at=unixepoch() WHERE is_local=1 AND version<>?", version, version); err != nil {
		return fmt.Errorf("record local boxpanel version: %w", err)
	}
	reader, err := db.OpenReader(ctx, opts.paths.Database)
	if err != nil {
		return fmt.Errorf("prepare database reader: %w", err)
	}
	defer reader.Close()
	service, err := auth.NewWithReader(conn, reader, nil)
	if err != nil {
		return fmt.Errorf("prepare authentication: %w", err)
	}
	manager, err := newApplyManager(conn, opts.paths, opts.core, singbox.ApplyOptions{Dev: opts.dev, DevDBOnly: opts.dev, ReadDB: reader})
	if err != nil {
		return err
	}
	outgoing, err := db.LoadMasterMigration(ctx, reader, db.MigrationOutgoing)
	if err != nil {
		return err
	}
	switch {
	case outgoing.Phase == db.MigrationRetired:
		if err := stopMigratedCore(ctx); err != nil {
			logger.Warn("旧主控内核尚未停用，请检查 systemd；迁移服务会重试。")
			outgoing.CoreStopped = false
			if err := db.SetSetting(ctx, conn, db.MigrationOutgoing, outgoing); err != nil {
				return err
			}
		}
	case outgoing.Phase == db.MigrationFrozen && !outgoing.Expired(time.Now()):
		// The running core keeps serving its last checked configuration.
	default:
		if err := manager.Startup(ctx); err != nil {
			return fmt.Errorf("startup configuration apply failed; check core installation, certificates, service and firewall permissions, then run boxpanel apply")
		}
	}
	inspector := singbox.Inspector{Dev: opts.dev, Binary: opts.core.binary}
	system := sysinfo.New(sysinfo.Options{Dev: opts.dev, DiskPath: filepath.Dir(opts.paths.Database), Singbox: inspector})
	if opts.dev {
		logger.Warn("仅开发用：入站和用户修改只写 DB，不生成、校验或生效 sing-box 配置。")
	}
	store := &subscription.Store{DB: reader, Certificate: func(id int64, sni string) ([]byte, error) {
		hash := sha256.Sum256([]byte(sni))
		return os.ReadFile(filepath.Join(filepath.Dir(opts.core.config), "certs", fmt.Sprintf("hy2-%d-%x.crt", id, hash[:8])))
	}}
	limiter := &subscription.Limiter{}
	panel := &livePanel{ctx: ctx, host: opts.listenHost, port: cfg.WebPort, dev: opts.dev, logger: logger, grace: 2 * time.Second}
	certificates, err := acme.New(ctx, conn, acme.Options{ReadDB: reader, Dir: opts.paths.CertificateDir(), CertFile: opts.paths.CertFile, KeyFile: opts.paths.KeyFile, Directory: opts.acmeDirectory, Dev: opts.dev, Logger: logger, OpenPort: func(ctx context.Context) error {
		if runtime.GOOS != "linux" {
			return nil
		}
		return app.OpenPanelFirewall(ctx, conn, 80, true, firewall.New(firewall.Options{}))
	}})
	if err != nil {
		return fmt.Errorf("prepare panel certificates: %w", err)
	}
	monitorHost := !opts.dev && runtime.GOOS == "linux"
	alertOptions := alerts.Options{Certificate: certificates.Status, Logger: logger}
	if monitorHost {
		native := host.Native{}
		alertOptions.Network = &alerts.Network{Files: native, Runner: native}
		alertOptions.CoreRunning = manager.CoreActive
	}
	var backups *panelBackups
	if backupLayout(opts.paths, opts.core) == nil {
		backupOpts := opts
		backupOpts.paths, backupOpts.core, _ = absoluteBackupPaths(opts.paths, opts.core)
		backups = &panelBackups{service: &backup.Service{DB: conn, DataDir: opts.paths.DataDir, Version: version}, opts: backupOpts}
	}
	var backupAPI httpapi.Backups
	var migrations *migration.Service
	if backups != nil {
		backupAPI = backups
		if monitorHost {
			pin, err := certificates.FallbackPin()
			if err != nil {
				return err
			}
			migrations = &migration.Service{DB: conn, ReadDB: reader, DataDir: backups.opts.paths.DataDir, Version: version, Pin: pin,
				Signer: certificates.FallbackSigner(), StopCore: stopMigratedCore, BeforeClaim: func(context.Context) error {
					panel.mu.Lock()
					defer panel.mu.Unlock()
					if backups.queued.Load() || panel.pending || panel.closed {
						return migration.ErrBusy
					}
					return nil
				}}
			alertOptions.Maintenance = migrations.Maintenance
		}
	}
	alertService := alerts.New(conn, reader, alertOptions)
	nodes := agent.NewMaster(conn, reader, nil)
	if migrations != nil {
		nodes.Maintenance = migrations.Maintenance
	}
	nodes.OnStopped, nodes.Logger = alertService.UserStopped, logger
	nodes.OnAvailability = alertService.ServerAvailability
	nodes.OnExitAvailability = alertService.ExitAvailability
	var masterUpgrade httpapi.MasterUpgrade
	standardPaths, _ := (pathFlags{}).resolve(false)
	if monitorHost && agent.ReleaseTag(version) && opts.paths == standardPaths && opts.core == (singboxPaths{}).resolve(&pathFlags{}, false) {
		worker := newAutoUpgrader(conn, version, logger)
		if worker.checkInstalled() == nil {
			masterUpgrade = &panelUpgrader{worker: worker, latest: latestStableRelease}
		}
	}
	panel.build = func(settings httpapi.PanelSettings) (http.Handler, error) {
		api := httpapi.New(service, httpapi.Options{MasterUpgrade: masterUpgrade, WebPath: settings.WebPath, Dev: opts.dev, Logger: logger, System: system, Singbox: manager, DB: conn, ReadDB: reader, Applier: manager, PanelPort: settings.WebPort, Subscriptions: store, PreparePanel: panel.prepare, Certificates: certificates, Backups: backupAPI, Migration: migrations, Alerts: alertService, Agents: nodes, AgentPin: certificates.FallbackPin,
			RealityLookup: func(ctx context.Context, name string) ([]netip.Addr, error) {
				return net.DefaultResolver.LookupNetIP(ctx, "ip", name)
			}})
		spaHandler, err := spa.New(web.Assets, spa.Options{WebPath: settings.WebPath, Version: version, API: api})
		if err != nil {
			return nil, fmt.Errorf("prepare HTTP server: %w", err)
		}
		return &subscription.Handler{Store: store, Next: spaHandler, Limiter: limiter, PanelPort: settings.WebPort, Logger: logger}, nil
	}
	handler, err := panel.build(httpapi.PanelSettings{WebPath: cfg.WebPath, WebPort: cfg.WebPort})
	if err != nil {
		return err
	}
	if !opts.dev {
		panel.getCertificate, err = migrationCertificates(ctx, reader, certificates, cfg)
		if err != nil {
			return err
		}
	}
	address := net.JoinHostPort(opts.listenHost, strconv.Itoa(cfg.WebPort))
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("listen on %s (stop the conflicting process or choose another --port): %w", address, err)
	}
	if !opts.dev && runtime.GOOS == "linux" {
		if err := app.OpenPanelFirewall(ctx, conn, cfg.WebPort, false, firewall.New(firewall.Options{})); err != nil {
			listener.Close()
			return fmt.Errorf("restore panel firewall; run boxpanel system firewall-open and retry")
		}
		if cfg.TLSMode == "acme" {
			if err := app.OpenPanelFirewall(ctx, conn, 80, true, firewall.New(firewall.Options{})); err != nil {
				logger.Warn("无法放行 ACME TCP 80；面板继续使用当前证书，请检查本机防火墙权限，并在设置查看签发结果。")
			}
		}
	}
	logger.Info("panel listening", "url", cfg.URL(), "listen", address)
	workerCtx, cancelWorker := context.WithCancel(ctx)
	workerDone := make(chan struct{})
	go func() { defer close(workerDone); certificates.Run(workerCtx) }()
	backupDone := make(chan struct{})
	if backups != nil && !opts.dev {
		go func() {
			defer close(backupDone)
			backups.service.Run(workerCtx, func(error) {
				logger.Warn("自动备份失败；请检查磁盘空间与备份目录权限，并在设置页查看状态。")
			})
		}()
	} else {
		close(backupDone)
	}
	nodesDone := make(chan struct{})
	go func() { defer close(nodesDone); nodes.Run(workerCtx, logger) }()
	migrationDone := make(chan struct{})
	if migrations != nil {
		go func() { defer close(migrationDone); migrations.Run(workerCtx) }()
	} else {
		close(migrationDone)
	}
	alertsDone := make(chan struct{})
	go func() { defer close(alertsDone); alertService.Run(workerCtx) }()
	trafficDone := make(chan struct{})
	exitDone := make(chan struct{})
	if monitorHost {
		scheduler := traffic.NewScheduler(traffic.NewCollector(conn, reader, nil), manager, nil, logger)
		scheduler.OnStopped = alertService.UserStopped
		if migrations != nil {
			scheduler.Maintenance = migrations.Maintenance
		}
		go func() { defer close(trafficDone); scheduler.Run(workerCtx) }()
		bootID, _ := os.ReadFile("/proc/sys/kernel/random/boot_id")
		exitMonitor := agent.ExitMonitor{DetectIP: sysinfo.DefaultIPProbe, ManageAddresses: true, Writer: conn, Reader: reader, Manager: manager, BootID: strings.TrimSpace(string(bootID)),
			Logger: logger, OnTransition: alertService.ExitAvailability, Maintenance: alertOptions.Maintenance}
		go func() { defer close(exitDone); exitMonitor.Run(workerCtx) }()
	} else {
		close(trafficDone)
		close(exitDone)
	}
	defer func() {
		cancelWorker()
		<-workerDone
		<-trafficDone
		<-exitDone
		<-backupDone
		<-alertsDone
		<-nodesDone
		<-migrationDone
	}()
	return panel.run(listener, handler)
}

func stopMigratedCore(ctx context.Context) error {
	native := host.Native{}
	state, err := native.Run(ctx, "systemctl", "show", "--property=LoadState", "--value", singbox.SingboxUnit)
	if err == nil && strings.TrimSpace(string(state)) == "not-found" {
		return nil
	}
	_, err = native.Run(ctx, "systemctl", "disable", "--now", singbox.SingboxUnit)
	return err
}

func migrationCertificates(ctx context.Context, conn *sql.DB, certificates *acme.Manager, cfg app.Config) (func(*tls.ClientHelloInfo) (*tls.Certificate, error), error) {
	in, err := db.LoadMasterMigration(ctx, conn, db.MigrationReceived)
	if err != nil {
		return nil, err
	}
	if _, err := netip.ParseAddr(cfg.PublicHost); err == nil || in.TargetURL == "" {
		return certificates.GetCertificate, nil
	}
	target, err := url.Parse(in.TargetURL)
	if err != nil {
		return nil, fmt.Errorf("load migrated panel address: %w", err)
	}
	var mu sync.Mutex
	var pair *tls.Certificate
	return func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
		if hello.ServerName != "" {
			return certificates.GetCertificate(hello)
		}
		mu.Lock()
		defer mu.Unlock()
		if pair == nil || time.Until(pair.Leaf.NotAfter) < 24*time.Hour {
			var err error
			pair, err = certificates.FallbackCertificate(target.Hostname())
			if err != nil {
				return nil, err
			}
		}
		return pair, nil
	}, nil
}
