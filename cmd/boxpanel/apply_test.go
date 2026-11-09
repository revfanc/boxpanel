package main

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/revfanc/boxpanel/internal/app"
	"github.com/revfanc/boxpanel/internal/singbox"
)

func TestApplyCLIUsageAndUnsupported(t *testing.T) {
	clearEnvironment(t)
	for _, tt := range []struct {
		args []string
		code int
	}{
		{[]string{"apply", "--help"}, 0}, {[]string{"apply", "--unknown"}, 2}, {[]string{"apply", "extra"}, 2},
		{[]string{"apply", "--data-dir", t.TempDir()}, 1},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(tt.args, &stdout, &stderr); code != tt.code {
			t.Fatalf("args=%v exit=%d stdout=%s stderr=%s", tt.args, code, stdout.String(), stderr.String())
		}
	}
}
func TestApplyPathCollisionIncludesAuxiliaryFiles(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "sing-box.json")
	paths := app.Paths{Database: filepath.Join(dir, "db"), CertFile: filepath.Join(dir, "panel.crt"), KeyFile: filepath.Join(dir, "panel.key")}
	for _, suffix := range []string{"", ".bak", ".tmp", ".pending", ".lock"} {
		paths.Database = config + suffix
		if _, err := newApplyManager(nil, paths, singboxPaths{binary: filepath.Join(dir, "binary"), config: config}, singbox.ApplyOptions{OS: "linux"}); err == nil {
			t.Fatalf("accepted config%s database collision", suffix)
		}
	}
}
