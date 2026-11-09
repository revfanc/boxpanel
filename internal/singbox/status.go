package singbox

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"runtime"

	"github.com/revfanc/boxpanel/internal/host"
)

type Status struct {
	Installed bool   `json:"installed"`
	Running   bool   `json:"running"`
	Version   string `json:"version"`
	Uptime    int64  `json:"uptime"`
}

type Inspector struct {
	OS         string
	Dev        bool
	Binary     string
	Runner     host.Runner
	Controller Controller
	Stat       func(string) (fs.FileInfo, error)
}

func (i Inspector) Status(ctx context.Context) (Status, error) {
	var result Status
	if i.OS == "" {
		i.OS = runtime.GOOS
	}
	if i.Dev || i.OS != "linux" {
		return result, nil
	}
	if i.Binary == "" {
		i.Binary = DefaultBinary
	}
	if i.Runner == nil {
		i.Runner = host.Native{}
	}
	if i.Controller == nil {
		i.Controller = NewSystemd(SystemdOptions{OS: i.OS, Runner: i.Runner})
	}
	if i.Stat == nil {
		i.Stat = os.Stat
	}
	info, err := i.Stat(i.Binary)
	if errors.Is(err, fs.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, fmt.Errorf("inspect sing-box binary: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return result, fmt.Errorf("sing-box path is not an executable regular file")
	}
	result.Installed = true
	result.Version, err = BinaryVersion(ctx, i.Runner, i.Binary)
	if err != nil {
		return result, err
	}
	state, err := i.Controller.State(ctx, SingboxUnit)
	if err != nil {
		return result, err
	}
	result.Running, result.Uptime = state.Running, state.Uptime
	return result, nil
}
