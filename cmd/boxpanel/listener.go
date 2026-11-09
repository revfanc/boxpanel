package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/revfanc/boxpanel/internal/httpapi"
)

type handlerSlot struct{ handler http.Handler }
type livePanel struct {
	mu          sync.Mutex
	ctx         context.Context
	host        string
	port        int
	dev         bool
	certificate tls.Certificate
	logger      *slog.Logger
	build       func(httpapi.PanelSettings) (http.Handler, error)
	handler     atomic.Pointer[handlerSlot]
	server      *http.Server
	pending     bool
	closed      bool
	reserved    net.Listener
	timer       *time.Timer
	failures    chan error
	grace       time.Duration // production uses two seconds; injectable for tests
}

func (p *livePanel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.handler.Load().handler.ServeHTTP(w, r)
}
func (p *livePanel) start(listener net.Listener) *http.Server {
	s := &http.Server{Handler: p, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 * 1024, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{p.certificate}}, ErrorLog: slog.NewLogLogger(p.logger.Handler(), slog.LevelWarn)}
	go func() {
		var err error
		if p.dev {
			err = s.Serve(listener)
		} else {
			err = s.ServeTLS(listener, "", "")
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			select {
			case p.failures <- err:
			default:
			}
		}
	}()
	return s
}
func stopPanel(s *http.Server) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		_ = s.Close()
	}
}
func (p *livePanel) prepare(ctx context.Context, next httpapi.PanelSettings) (func(), func(), error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pending || p.closed || p.ctx.Err() != nil || ctx.Err() != nil {
		return nil, nil, fmt.Errorf("listener change already pending or shutting down")
	}
	handler, err := p.build(next)
	if err != nil {
		return nil, nil, fmt.Errorf("prepare next panel handler: %w", err)
	}
	var listener net.Listener
	if next.WebPort != p.port {
		listener, err = net.Listen("tcp", net.JoinHostPort(p.host, strconv.Itoa(next.WebPort)))
		if err != nil {
			return nil, nil, fmt.Errorf("reserve next panel listener: %w", err)
		}
	}
	p.pending = true
	p.reserved = listener
	committed := false
	cancel := func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if committed {
			return
		}
		if listener != nil {
			_ = listener.Close()
		}
		p.reserved = nil
		p.pending = false
	}
	commit := func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if committed {
			return
		}
		committed = true
		p.timer = time.AfterFunc(p.grace, func() {
			p.mu.Lock()
			if p.closed || p.ctx.Err() != nil {
				if listener != nil {
					_ = listener.Close()
				}
				p.reserved = nil
				p.pending = false
				p.mu.Unlock()
				return
			}
			p.handler.Store(&handlerSlot{handler})
			var old *http.Server
			if listener != nil {
				old = p.server
				p.server = p.start(listener)
				p.port = next.WebPort
			}
			p.reserved = nil
			p.pending = false
			p.mu.Unlock()
			if old != nil {
				stopPanel(old)
			}
			p.logger.Info("panel listener settings activated")
		})
	}
	return commit, cancel, nil
}
func (p *livePanel) run(listener net.Listener, handler http.Handler) error {
	p.mu.Lock()
	p.handler.Store(&handlerSlot{handler})
	p.failures = make(chan error, 1)
	p.server = p.start(listener)
	p.mu.Unlock()
	var err error
	select {
	case <-p.ctx.Done():
	case err = <-p.failures:
	}
	p.mu.Lock()
	p.closed = true
	if p.timer != nil {
		p.timer.Stop()
	}
	if p.reserved != nil {
		_ = p.reserved.Close()
	}
	s := p.server
	p.mu.Unlock()
	stopPanel(s)
	if err != nil {
		return fmt.Errorf("serve panel: %w; restart the panel and retry", err)
	}
	return nil
}
