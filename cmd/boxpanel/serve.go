package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/revfanc/boxpanel/internal/spa"
	"github.com/revfanc/boxpanel/web"
)

type serveOptions struct {
	webPath string
	port    int
}

func parseServeOptions(args []string, output io.Writer) (serveOptions, error) {
	opts := serveOptions{}
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(output)
	flags.StringVar(&opts.webPath, "web-path", "boxpanel", "panel path segment (lowercase letters and digits; empty mounts at /)")
	flags.IntVar(&opts.port, "port", 2053, "HTTP port on 127.0.0.1 (1-65535)")
	dev := flags.Bool("dev", false, "use HTTP on 127.0.0.1:2053 with an empty web_path (overrides --port and --web-path)")
	if err := flags.Parse(args); err != nil {
		return opts, fmt.Errorf("parse serve options: %w", err)
	}
	if flags.NArg() != 0 {
		return opts, fmt.Errorf("unexpected serve arguments; run 'boxpanel serve --help' for usage")
	}
	if *dev {
		opts.webPath = ""
		opts.port = 2053
	}
	if opts.port < 1 || opts.port > 65535 {
		return opts, fmt.Errorf("invalid port: choose --port between 1 and 65535")
	}
	if err := spa.ValidateWebPath(opts.webPath); err != nil {
		return opts, fmt.Errorf("parse serve options: %w", err)
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
	if err := serve(ctx, opts, logger); err != nil {
		logger.Error("无法启动面板，请按错误提示修复后重试。", "error", err)
		return 1
	}
	return 0
}

func serve(ctx context.Context, opts serveOptions, logger *slog.Logger) error {
	handler, err := spa.New(web.Assets, spa.Options{WebPath: opts.webPath, Version: version})
	if err != nil {
		return fmt.Errorf("prepare HTTP server: %w", err)
	}
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(opts.port))
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("listen on %s (stop the conflicting process or choose another --port): %w", address, err)
	}
	defer listener.Close()
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
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
	panelPath := "/"
	if opts.webPath != "" {
		panelPath += opts.webPath + "/"
	}
	logger.Info("panel listening", "url", "http://"+address+panelPath)
	if err := server.Serve(listener); err != nil {
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve HTTP (restart the panel and retry): %w", err)
		}
		<-shutdownDone
	}
	return nil
}
