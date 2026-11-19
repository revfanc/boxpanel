//go:build !linux

package singbox

// systemd only runs on Linux; elsewhere fall back to /proc/uptime handling.
func monotonicSeconds() (float64, error) { return 0, errNoMonotonic }
