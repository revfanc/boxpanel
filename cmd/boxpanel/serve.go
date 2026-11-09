package main

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/revfanc/boxpanel/internal/app"
	"github.com/revfanc/boxpanel/internal/auth"
	"github.com/revfanc/boxpanel/internal/httpapi"
	"github.com/revfanc/boxpanel/internal/spa"
	"github.com/revfanc/boxpanel/internal/sysinfo"
	"github.com/revfanc/boxpanel/web"
)

type serveOptions struct {
	webPath    string
	port       int
	webPathSet bool
	portSet    bool
	dev        bool
	paths      app.Paths
	listenHost string
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
	} else if cfg.TLSMode != "self_signed" {
		return cfg, fmt.Errorf("HTTP database requires serve --dev; initialize a separate data directory for HTTPS")
	}
	return cfg, nil
}

func parseServeOptions(args []string, output io.Writer) (serveOptions, error) {
	opts := serveOptions{}
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(output)
	paths := addPathFlags(flags)
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
		logger.Error("无法启动面板，请按错误提示修复后重试。", "error", err)
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
	cfg, err := resolveServeConfig(ctx, conn, opts)
	if err != nil {
		return fmt.Errorf("load panel settings (check the data directory and initialized database): %w", err)
	}
	service, err := auth.New(conn, nil)
	if err != nil {
		return fmt.Errorf("prepare authentication: %w", err)
	}
	system := sysinfo.New(sysinfo.Options{Dev: opts.dev, DiskPath: filepath.Dir(opts.paths.Database)})
	api := httpapi.New(service, httpapi.Options{WebPath: cfg.WebPath, Dev: opts.dev, Logger: logger, System: system})
	handler, err := spa.New(web.Assets, spa.Options{WebPath: cfg.WebPath, Version: version, API: api})
	if err != nil {
		return fmt.Errorf("prepare HTTP server: %w", err)
	}
	var certificate tls.Certificate
	if !opts.dev {
		certificate, err = tls.LoadX509KeyPair(opts.paths.CertFile, opts.paths.KeyFile)
		if err != nil {
			return fmt.Errorf("load panel TLS certificate (check --cert-file and --key-file): %w", err)
		}
	}
	address := net.JoinHostPort(opts.listenHost, strconv.Itoa(cfg.WebPort))
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("listen on %s (stop the conflicting process or choose another --port): %w", address, err)
	}
	defer listener.Close()
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 * 1024,
		TLSConfig:         &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}},
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}
	stopShutdown := make(chan struct{})
	shutdownDone := make(chan struct{})
	defer close(stopShutdown)
	go func() {
		defer close(shutdownDone)
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := server.Shutdown(shutdownCtx); err != nil {
				logger.Error("关闭面板超时，请确认进程已退出后再重启。", "error", err)
				_ = server.Close()
			}
		case <-stopShutdown:
		}
	}()
	logger.Info("panel listening", "url", cfg.URL(), "listen", address)
	if opts.dev {
		err = server.Serve(listener)
	} else {
		err = server.ServeTLS(listener, "", "")
	}
	if err != nil {
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve HTTP (restart the panel and retry): %w", err)
		}
		<-shutdownDone
	}
	return nil
}
