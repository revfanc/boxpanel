package app

import (
	"context"
	"github.com/revfanc/boxpanel/internal/sysinfo"
	"net/netip"
	"time"
)

// Keep the init injection points stable while sharing probes with system APIs.
type HTTPDoer = sysinfo.HTTPDoer
type IPProbe = sysinfo.IPProbe

func DefaultIPProbe(ctx context.Context) (netip.Addr, error) {
	return sysinfo.DefaultIPProbe(ctx)
}

func ProbePublicIP(ctx context.Context, client HTTPDoer, sources []string, timeout time.Duration) (netip.Addr, error) {
	return sysinfo.ProbePublicIP(ctx, client, sources, timeout)
}
