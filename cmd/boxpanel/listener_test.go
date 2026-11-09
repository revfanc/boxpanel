package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/revfanc/boxpanel/internal/httpapi"
)

func TestListenerReservationActivationAndCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	p := &livePanel{ctx: ctx, host: "127.0.0.1", port: port, dev: true, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), grace: 30 * time.Millisecond}
	var active atomic.Bool
	p.build = func(s httpapi.PanelSettings) (http.Handler, error) {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/"+s.WebPath {
				w.WriteHeader(404)
				return
			}
			active.Store(true)
			fmt.Fprint(w, "new")
		}), nil
	}
	done := make(chan error, 1)
	go func() {
		done <- p.run(listener, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "old") }))
	}()
	client := &http.Client{Timeout: time.Second}
	get := func(port int, path string) (int, string, error) {
		resp, err := client.Get("http://127.0.0.1:" + strconv.Itoa(port) + path)
		if err != nil {
			return 0, "", err
		}
		defer resp.Body.Close()
		b, e := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b), e
	}
	if _, body, err := get(port, "/"); err != nil || body != "old" {
		t.Fatal(body, err)
	}
	occupied, _ := net.Listen("tcp", "127.0.0.1:0")
	occupiedPort := occupied.Addr().(*net.TCPAddr).Port
	if _, _, err := p.prepare(ctx, httpapi.PanelSettings{WebPort: occupiedPort}); err == nil {
		t.Fatal("accepted occupied port")
	}
	occupied.Close()
	commit, abort, err := p.prepare(ctx, httpapi.PanelSettings{WebPort: occupiedPort, WebPath: "next"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.prepare(ctx, httpapi.PanelSettings{WebPort: port}); err == nil {
		t.Fatal("accepted parallel pending change")
	}
	abort()
	probe, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(occupiedPort))
	if err != nil {
		t.Fatal("cancel did not release listener")
	}
	probe.Close()
	commit, abort, err = p.prepare(ctx, httpapi.PanelSettings{WebPort: occupiedPort, WebPath: "next"})
	if err != nil {
		t.Fatal(err)
	}
	commit()
	abort()
	if _, body, err := get(port, "/"); err != nil || body != "old" {
		t.Fatal("changed before grace period")
	}
	deadline := time.Now().Add(2 * time.Second)
	for !active.Load() && time.Now().Before(deadline) {
		_, _, _ = get(occupiedPort, "/next")
		time.Sleep(time.Millisecond)
	}
	if !active.Load() {
		t.Fatal("new listener not activated")
	}
	if status, _, err := get(occupiedPort, "/old"); err != nil || status != 404 {
		t.Fatal("old path still available")
	}
	// Changing only the path reuses the listener and removes the old mount.
	commit, abort, err = p.prepare(ctx, httpapi.PanelSettings{WebPort: occupiedPort, WebPath: "final"})
	if err != nil {
		t.Fatal(err)
	}
	commit()
	abort()
	for time.Now().Before(deadline) {
		status, _, _ := get(occupiedPort, "/final")
		if status == 200 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if status, _, err := get(occupiedPort, "/final"); err != nil || status != 200 {
		t.Fatal("path-only activation failed")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, _, err := get(occupiedPort, "/final"); err == nil {
		t.Fatal("shutdown left listener open")
	}
}
