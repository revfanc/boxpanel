// tools installs the pinned host binary for local and CI configuration checks.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/revfanc/boxpanel/internal/singbox"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	destination, err := filepath.Abs(".tools/bin/sing-box")
	if err == nil {
		err = (singbox.Installer{}).Install(ctx, destination, singbox.PinnedVersion)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "make tools: %v; 请检查 GitHub 连接与目录权限后重试。\n", err)
		os.Exit(1)
	}
	fmt.Printf("sing-box %s: %s\n", singbox.PinnedVersion, destination)
}
