package web

import (
	"io/fs"
	"testing"
)

func TestEmbeddedAssets(t *testing.T) {
	// The tracked placeholder keeps an unbuilt checkout compilable. It must
	// survive frontend builds and be accessible without a dist/ prefix.
	if _, err := fs.ReadFile(Assets, ".gitkeep"); err != nil {
		t.Fatalf("read embedded placeholder: %v", err)
	}
}
