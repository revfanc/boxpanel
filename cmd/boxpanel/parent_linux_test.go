package main

import (
	"context"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The installer can die by SIGKILL; boxpanel migrate must still be told to stop.
func TestStopWithParentDeliversSIGTERM(t *testing.T) {
	if marker := os.Getenv("PDEATH_MARKER"); marker != "" {
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
		defer stop()
		if !stopWithParent() {
			os.Exit(3)
		}
		_ = os.WriteFile(marker+".ready", nil, 0600)
		select {
		case <-ctx.Done():
			_ = os.WriteFile(marker, []byte("terminated"), 0600)
		case <-time.After(20 * time.Second):
		}
		os.Exit(0)
	}
	marker := filepath.Join(t.TempDir(), "child")
	// sh stands in for the installer: it starts the child and waits on it.
	shell := exec.Command("sh", "-c", `"$0" -test.run='^TestStopWithParentDeliversSIGTERM$' & wait`, os.Args[0])
	shell.Env = append(os.Environ(), "PDEATH_MARKER="+marker)
	if err := shell.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(marker + ".ready"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child did not start")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := shell.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_ = shell.Wait()
	for deadline = time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if b, _ := os.ReadFile(marker); strings.TrimSpace(string(b)) == "terminated" {
			return
		}
	}
	t.Fatal("child was not signalled after its parent was killed")
}
