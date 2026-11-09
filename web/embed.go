// Package web contains the frontend assets embedded at build time.
package web

import (
	"embed"
	"fmt"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Assets is rooted at dist, ready for the SPA handler added in T0.2.
var Assets = frontendAssets()

func frontendAssets() fs.FS {
	assets, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(fmt.Errorf("open embedded frontend: %w", err))
	}
	return assets
}
