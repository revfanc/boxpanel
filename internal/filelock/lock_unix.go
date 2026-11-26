//go:build linux || darwin

// Package filelock provides flock-based locks shared by the serving process and
// CLI. The kernel releases a lock when its process exits, even after SIGKILL.
package filelock

import (
	"context"
	"fmt"
	"os"
	"syscall"
	"time"
)

func Acquire(ctx context.Context, name string) (func(), error) {
	return acquire(ctx, name, syscall.LOCK_EX)
}

func AcquireShared(ctx context.Context, name string) (func(), error) {
	return acquire(ctx, name, syscall.LOCK_SH)
}

// TryAcquire takes an exclusive lock on name without waiting and returns
// ErrBusy while another live process holds it. A crashed or killed holder
// releases the lock with its process, so a leftover file never blocks the next
// run. Release removes the file while still holding the lock; an opener that
// raced with the removal sees a different inode and retries on the new file.
func TryAcquire(name string) (func(), error) {
	for attempt := 0; attempt < 8; attempt++ {
		f, err := os.OpenFile(name, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
		if err != nil {
			return nil, fmt.Errorf("open lock file: %w", err)
		}
		if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			f.Close()
			if err == syscall.EWOULDBLOCK || err == syscall.EAGAIN {
				return nil, ErrBusy
			}
			return nil, fmt.Errorf("lock file: %w", err)
		}
		held, err := f.Stat()
		current, statErr := os.Lstat(name)
		if err == nil && statErr == nil && os.SameFile(held, current) {
			return func() {
				_ = os.Remove(name)
				_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				_ = f.Close()
			}, nil
		}
		// The previous holder removed the path after we opened it.
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}
	return nil, ErrBusy
}

func acquire(ctx context.Context, name string, mode int) (func(), error) {
	f, err := os.OpenFile(name, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, fmt.Errorf("open material lock: %w", err)
	}
	for {
		err = syscall.Flock(int(f.Fd()), mode|syscall.LOCK_NB)
		if err == nil {
			return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			f.Close()
			return nil, fmt.Errorf("lock certificate material: %w", err)
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}
