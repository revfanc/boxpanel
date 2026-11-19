//go:build linux

package singbox

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// monotonicSeconds reads CLOCK_MONOTONIC, which systemd uses for its
// *TimestampMonotonic properties and which LXCFS does not virtualise.
func monotonicSeconds() (float64, error) {
	var ts unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC, &ts); err != nil {
		return 0, fmt.Errorf("read monotonic clock: %w", err)
	}
	return float64(ts.Sec) + float64(ts.Nsec)/1e9, nil
}
