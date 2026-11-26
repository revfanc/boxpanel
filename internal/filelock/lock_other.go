//go:build !linux && !darwin

package filelock

import (
	"context"
	"fmt"
)

func Acquire(context.Context, string) (func(), error) {
	return nil, fmt.Errorf("certificate backup requires Linux or macOS")
}

func TryAcquire(string) (func(), error) {
	return nil, fmt.Errorf("file locks require Linux or macOS")
}

func AcquireShared(ctx context.Context, name string) (func(), error) {
	return Acquire(ctx, name)
}
