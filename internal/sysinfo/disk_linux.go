//go:build linux

package sysinfo

import (
	"fmt"
	"math"
	"syscall"
)

func diskUsage(path string) (Usage, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return Usage{}, fmt.Errorf("stat filesystem: %w", err)
	}
	if stat.Bsize <= 0 || stat.Bfree > stat.Blocks || stat.Blocks > math.MaxUint64/uint64(stat.Bsize) {
		return Usage{}, fmt.Errorf("invalid filesystem blocks")
	}
	return Usage{Used: (stat.Blocks - stat.Bfree) * uint64(stat.Bsize), Total: stat.Blocks * uint64(stat.Bsize)}, nil
}
