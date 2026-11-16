//go:build linux

package firewall

import (
	"bytes"
	"context"
	"io"
	"net"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/revfanc/boxpanel/internal/fwmark"
	"github.com/revfanc/boxpanel/internal/host"
	"golang.org/x/sys/unix"
)

// Reexec inside a private network namespace: never change host firewall rules.
func TestKernelTrafficCounters(t *testing.T) {
	if testing.Short() {
		t.Skip("kernel integration disabled by -short")
	}
	if os.Geteuid() != 0 {
		t.Skip("requires root and nftables")
	}
	for _, command := range []string{"nft", "ip", "unshare"} {
		if _, err := exec.LookPath(command); err != nil {
			t.Skip("requires root, nft, ip and unshare")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	if os.Getenv("BOXPANEL_COUNTER_NETNS") != "1" {
		if output, err := exec.CommandContext(ctx, "unshare", "-n", "true").CombinedOutput(); err != nil {
			t.Skipf("network namespace capability unavailable: %s", output)
		}
		cmd := exec.CommandContext(ctx, "unshare", "-n", os.Args[0], "-test.run=^TestKernelTrafficCounters$", "-test.v")
		cmd.Env = append(os.Environ(), "BOXPANEL_COUNTER_NETNS=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("isolated kernel test: %v\n%s", err, output)
		}
		return
	}
	native := host.Native{}
	if _, err := native.Run(ctx, "ip", "link", "set", "lo", "up"); err != nil {
		t.Fatal(err)
	}
	m := New(Options{OS: "linux", Runner: native})
	if err := m.syncTraffic(ctx, []int64{1, 2}); err != nil {
		t.Fatal(err)
	}
	mark, _ := fwmark.User(1)
	dialer := net.Dialer{Timeout: time.Second, Control: func(_, _ string, raw syscall.RawConn) error {
		var err error
		if e := raw.Control(func(fd uintptr) { err = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_MARK, int(mark)) }); e != nil {
			return e
		}
		return err
	}}
	// Closed short connections, TCP and UDP, both families. No active-connection
	// API can see these once we read the counters below.
	for _, address := range []string{"127.0.0.1:0", "[::1]:0"} {
		listener, err := net.Listen("tcp", address)
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() {
			conn, err := listener.Accept()
			if err != nil {
				done <- err
				return
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
			_, err = io.CopyN(conn, conn, 4096)
			done <- err
		}()
		conn, err := dialer.DialContext(ctx, "tcp", listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		payload := bytes.Repeat([]byte("x"), 4096)
		if _, err := conn.Write(payload); err != nil {
			t.Fatal(err)
		}
		if _, err := io.ReadFull(conn, payload); err != nil {
			t.Fatal(err)
		}
		conn.Close()
		listener.Close()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		udp, err := net.ListenPacket("udp", address)
		if err != nil {
			t.Fatal(err)
		}
		go func() {
			buf := make([]byte, 2048)
			_ = udp.SetDeadline(time.Now().Add(2 * time.Second))
			n, from, err := udp.ReadFrom(buf)
			if err == nil {
				_, err = udp.WriteTo(buf[:n], from)
			}
			done <- err
		}()
		uc, err := dialer.DialContext(ctx, "udp", udp.LocalAddr().String())
		if err != nil {
			t.Fatal(err)
		}
		_ = uc.SetDeadline(time.Now().Add(2 * time.Second))
		if _, err := uc.Write(payload[:1024]); err != nil {
			t.Fatal(err)
		}
		if _, err := uc.Read(payload); err != nil {
			t.Fatal(err)
		}
		uc.Close()
		udp.Close()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	before, err := ReadCounters(ctx, native)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 2 || before[0].Upload < 10240 || before[0].Download < 10240 || before[0].Upload > 40000 || before[0].Download > 40000 || before[1].Upload != 0 || before[1].Download != 0 {
		t.Fatal("TCP/UDP accounting or user isolation:", before)
	}
	if err := m.syncTraffic(ctx, []int64{1, 3}); err != nil {
		t.Fatal(err)
	}
	after, err := ReadCounters(ctx, native)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 2 || after[0].Upload < before[0].Upload || after[0].Download < before[0].Download || after[1].UserID != 3 || after[1].Upload != 0 {
		t.Fatal("counter reset on sync", before, after)
	}
}
