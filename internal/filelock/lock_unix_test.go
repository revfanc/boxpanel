//go:build linux || darwin

package filelock

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestTryAcquireBusyAndRelease(t *testing.T) {
	name := filepath.Join(t.TempDir(), "install.lock")
	release, err := TryAcquire(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := TryAcquire(name); !errors.Is(err, ErrBusy) {
		t.Fatalf("second TryAcquire = %v, want ErrBusy", err)
	}
	release()
	if _, err := os.Lstat(name); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lock file remains after release: %v", err)
	}
	again, err := TryAcquire(name)
	if err != nil {
		t.Fatalf("TryAcquire after release: %v", err)
	}
	again()
}

func TestTryAcquireIgnoresLeftoverFile(t *testing.T) {
	name := filepath.Join(t.TempDir(), "restore-lock")
	if err := os.WriteFile(name, nil, 0600); err != nil {
		t.Fatal(err)
	}
	release, err := TryAcquire(name)
	if err != nil {
		t.Fatalf("leftover unlocked file blocked TryAcquire: %v", err)
	}
	release()
}

// A SIGKILLed holder runs no deferred cleanup; the kernel must still release it.
func TestTryAcquireAfterHolderKilled(t *testing.T) {
	if name := os.Getenv("FILELOCK_HOLD"); name != "" {
		if _, err := TryAcquire(name); err != nil {
			os.Exit(2)
		}
		os.Stdout.WriteString("held\n")
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	name := filepath.Join(t.TempDir(), "migrate.lock")
	cmd := exec.Command(os.Args[0], "-test.run=^TestTryAcquireAfterHolderKilled$")
	cmd.Env = append(os.Environ(), "FILELOCK_HOLD="+name)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 5)
	if _, err := out.Read(buf); err != nil || string(buf[:4]) != "held" {
		t.Fatalf("child did not take the lock: %q %v", buf, err)
	}
	if _, err := TryAcquire(name); !errors.Is(err, ErrBusy) {
		t.Fatalf("TryAcquire while child holds = %v, want ErrBusy", err)
	}
	if err := cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	if _, err := os.Lstat(name); err != nil {
		t.Fatalf("killed holder should leave its file behind: %v", err)
	}
	release, err := TryAcquire(name)
	if err != nil {
		t.Fatalf("TryAcquire after SIGKILL: %v", err)
	}
	release()
}
