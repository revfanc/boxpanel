//go:build linux || darwin

// Package filelock coordinates immutable certificate capture and pruning across
// the serving process and CLI. The kernel releases locks if a process exits.
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
