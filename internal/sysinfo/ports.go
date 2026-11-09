package sysinfo

import (
	"context"
	"fmt"

	"github.com/revfanc/boxpanel/internal/host"
)

type Socket struct {
	Port  int
	Proto string
}

// ListeningPorts takes a read-only snapshot before an API apply transaction.
// It does not reserve ports; apply's restart/liveness check handles later races.
func ListeningPorts(ctx context.Context, runner host.Runner) (map[Socket]bool, error) {
	output, err := runner.Run(ctx, "ss", "-H", "-lntu")
	if err != nil {
		return nil, fmt.Errorf("inspect listening ports: %w", err)
	}
	parsed, err := parseSockets(output)
	if err != nil {
		return nil, fmt.Errorf("parse listening ports: %w", err)
	}
	result := map[Socket]bool{}
	for key := range parsed {
		result[Socket{key.port, key.proto}] = true
	}
	return result, nil
}
