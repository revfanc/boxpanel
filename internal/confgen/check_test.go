package confgen_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/revfanc/boxpanel/internal/certs"
	"github.com/revfanc/boxpanel/internal/singbox"
)

// Keep the integration boundary in an external test package: singbox now
// orchestrates confgen, while the pure generator must not import its caller.
func TestServerGoldenSingboxCheck(t *testing.T) {
	paths, err := filepath.Glob("testdata/*.golden")
	if err != nil || len(paths) == 0 {
		t.Fatal("missing configuration goldens")
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			golden, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			checkGolden(t, golden)
		})
	}
}

func checkGolden(t *testing.T, golden []byte) {
	t.Helper()
	binary, err := exec.LookPath("sing-box")
	if err != nil {
		t.Skip("sing-box not in PATH; run make tools and make test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	version, err := exec.CommandContext(ctx, binary, "version").Output()
	if err != nil || !strings.HasPrefix(string(version), "sing-box version "+singbox.PinnedVersion+"\n") {
		t.Fatal("golden validation requires the pinned sing-box binary")
	}
	dir := t.TempDir()
	material, err := certs.Hysteria2("www.bing.com", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	certPath, keyPath := filepath.Join(dir, "hy2.crt"), filepath.Join(dir, "hy2.key")
	for path, data := range map[string][]byte{certPath: material.Certificate, keyPath: material.PrivateKey} {
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Check the actual golden bytes, replacing only deployment certificate paths.
	for old, path := range map[string]string{"/etc/boxpanel/certs/hy2.crt": certPath, "/etc/boxpanel/certs/hy2.key": keyPath} {
		from, _ := json.Marshal(old)
		to, _ := json.Marshal(path)
		golden = bytes.ReplaceAll(golden, from, to)
	}
	// The pinned macOS core deliberately rejects Linux socket marks during
	// check. Assert that restriction, then check all remaining generated fields
	// on this host. Linux checks the original marked golden without rewriting.
	if runtime.GOOS != "linux" && bytes.Contains(golden, []byte(`"routing_mark"`)) {
		marked := filepath.Join(dir, "marked.json")
		if err := os.WriteFile(marked, golden, 0600); err != nil {
			t.Fatal(err)
		}
		output, err := exec.CommandContext(ctx, binary, "check", "-c", marked).CombinedOutput()
		if err == nil || !bytes.Contains(output, []byte("`routing_mark` is only supported on Linux")) {
			t.Fatalf("expected Linux mark restriction: %v %s", err, output)
		}
		var cfg map[string]any
		if err := json.Unmarshal(golden, &cfg); err != nil {
			t.Fatal(err)
		}
		for _, value := range cfg["outbounds"].([]any) {
			delete(value.(map[string]any), "routing_mark")
		}
		golden, err = json.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
	}
	configPath := filepath.Join(dir, "sing-box.json")
	if err := os.WriteFile(configPath, golden, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, binary, "check", "-c", configPath)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sing-box check: %v\n%s", err, output)
	}
}
