//go:build !linux

package firewall

import "testing"

func TestKernelTrafficCounters(t *testing.T) {
	t.Skip("kernel counters require Linux, root and nftables")
}
