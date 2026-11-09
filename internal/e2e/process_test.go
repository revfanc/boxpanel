package e2e_test

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/revfanc/boxpanel/internal/singbox"
)

func singboxBinary(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("..", "..", ".tools", "bin", "sing-box"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		t.Skip(".tools/bin/sing-box missing; run make tools, then make test")
	} else if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, path, "version").Output()
	if err != nil || !strings.HasPrefix(string(output), "sing-box version "+singbox.PinnedVersion+"\n") {
		t.Fatal("loopback tests require the pinned sing-box version; run make tools")
	}
	return path
}

type tcpReservation struct{ net.Listener }

func reserveTCP(t *testing.T) tcpReservation {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := tcpReservation{l}
	t.Cleanup(p.release)
	return p
}

func (p tcpReservation) address() string { return p.Addr().String() }
func (p tcpReservation) port() int       { return p.Addr().(*net.TCPAddr).Port }
func (p tcpReservation) release()        { _ = p.Close() }

func reserveDualPort(t *testing.T) (tcpReservation, net.PacketConn) {
	t.Helper()
	for attempt := 0; attempt < 32; attempt++ {
		tcp := reserveTCP(t)
		udp, err := net.ListenPacket("udp4", tcp.address())
		if err == nil {
			t.Cleanup(func() { _ = udp.Close() })
			return tcp, udp
		}
		tcp.release()
		if !errors.Is(err, syscall.EADDRINUSE) {
			t.Fatalf("reserve UDP port: %v", err)
		}
	}
	t.Fatal("could not reserve a loopback port for both TCP and UDP")
	return tcpReservation{}, nil
}

type coreProcess struct {
	done chan struct{}
	err  error // read only after done closes
}

func startCore(t *testing.T, binary, name string, config object, releasePorts func(), secrets []string) *coreProcess {
	t.Helper()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	writePrivate(t, configPath, encode(t, config))
	checkCtx, checkCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer checkCancel()
	check := exec.CommandContext(checkCtx, binary, "check", "-c", configPath)
	check.Dir = dir
	if output, err := check.CombinedOutput(); err != nil {
		t.Fatalf("%s sing-box check: %v\n%s", name, err, redact(string(output), secrets))
	}
	logPath := filepath.Join(dir, "core.log")
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	// The hard lifetime also kills the core if a future assertion forgets to
	// bound its work. Normal success/failure uses Cleanup and always Waits.
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	cmd := exec.CommandContext(ctx, binary, "run", "-c", configPath)
	cmd.Dir, cmd.Stdout, cmd.Stderr = dir, log, log
	p := &coreProcess{done: make(chan struct{})}
	releasePorts() // hold reservations through generation and check
	if err := cmd.Start(); err != nil {
		cancel()
		_ = log.Close()
		t.Fatalf("start %s sing-box: %v", name, err)
	}
	go func() {
		p.err = cmd.Wait()
		close(p.done)
	}()
	t.Cleanup(func() {
		select {
		case <-p.done:
			t.Errorf("%s sing-box exited before cleanup: %v", name, p.err)
		default:
		}
		cancel() // CommandContext kills the exact child, never a name/PID search
		select {
		case <-p.done:
		case <-time.After(5 * time.Second):
			t.Errorf("%s sing-box did not exit after kill", name)
		}
		_ = log.Close()
		if t.Failed() {
			output, err := os.ReadFile(logPath)
			if err != nil {
				t.Logf("read %s sing-box log: %v", name, err)
			} else {
				t.Logf("%s sing-box log:\n%s", name, redact(string(output), secrets))
			}
		}
	})
	return p
}

func (p *coreProcess) assertAlive(t *testing.T) {
	t.Helper()
	select {
	case <-p.done:
		t.Fatalf("sing-box exited unexpectedly: %v", p.err)
	default:
	}
}

func (p *coreProcess) waitReady(t *testing.T, address string, ready func() bool) {
	t.Helper()
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		p.assertAlive(t)
		if ready() {
			p.assertAlive(t)
			return
		}
		select {
		case <-p.done:
			t.Fatalf("sing-box exited while waiting for %s: %v", address, p.err)
		case <-timeout.C:
			t.Fatalf("sing-box port %s was not ready within 5 seconds", address)
		case <-tick.C:
		}
	}
}

func (p *coreProcess) waitTCP(t *testing.T, address string) {
	t.Helper()
	p.waitReady(t, address, func() bool {
		conn, err := net.DialTimeout("tcp4", address, 100*time.Millisecond)
		if err != nil {
			return false
		}
		_ = conn.Close()
		return true
	})
}

func (p *coreProcess) waitUDP(t *testing.T, address string) {
	t.Helper()
	// UDP Dial succeeds even without a listener. Instead wait for the reserved
	// port to be bound, alongside TCP/control readiness and a live process.
	// The subsequent proxied request verifies the actual QUIC service.
	p.waitReady(t, address, func() bool {
		conn, err := net.ListenPacket("udp4", address)
		if err == nil {
			_ = conn.Close()
			return false
		}
		if !errors.Is(err, syscall.EADDRINUSE) {
			t.Fatalf("probe UDP readiness: %v", err)
		}
		return true
	})
}

func redact(output string, secrets []string) string {
	for _, secret := range secrets {
		output = strings.ReplaceAll(output, secret, "[REDACTED]")
	}
	if len(output) > 64*1024 {
		output = output[len(output)-64*1024:]
	}
	return output
}
