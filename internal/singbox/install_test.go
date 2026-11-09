package singbox

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type testClient func(*http.Request) (*http.Response, error)

func (f testClient) Do(r *http.Request) (*http.Response, error) { return f(r) }

type testRunner func(context.Context, string, ...string) ([]byte, error)

func (r testRunner) LookPath(string) (string, error) { return "", os.ErrNotExist }
func (r testRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return r(ctx, name, args...)
}

func TestArchiveURLAndArchitecture(t *testing.T) {
	for _, platform := range []string{"linux", "darwin"} {
		for _, arch := range [][2]string{{"amd64", "amd64"}, {"x86_64", "amd64"}, {"arm64", "arm64"}, {"aarch64", "arm64"}} {
			for _, version := range []string{PinnedVersion, "v" + PinnedVersion} {
				got, err := DownloadURL(version, platform, arch[0])
				want := releaseBase + "v" + PinnedVersion + "/sing-box-" + PinnedVersion + "-" + platform + "-" + arch[1] + ".tar.gz"
				if err != nil || got != want {
					t.Fatalf("%s/%s: %s, %v", platform, arch[0], got, err)
				}
			}
		}
	}
	for _, input := range []struct{ version, platform, arch string }{
		{PinnedVersion, "windows", "amd64"}, {PinnedVersion, "linux", "386"},
		{"", "linux", "amd64"}, {"../1.14.4", "linux", "amd64"}, {"1.15.0-alpha.13", "linux", "amd64"},
		{"1.15.0-rc.1", "linux", "amd64"}, {"1.15.0-beta.1", "linux", "amd64"}, {"01.2.3", "linux", "amd64"},
	} {
		if _, err := DownloadURL(input.version, input.platform, input.arch); err == nil {
			t.Errorf("accepted %+v", input)
		}
	}
}

type tarEntry struct {
	name string
	kind byte
	data string
	size int64
}

func archiveBytes(t *testing.T, entries ...tarEntry) []byte {
	t.Helper()
	var output bytes.Buffer
	gz := gzip.NewWriter(&output)
	tw := tar.NewWriter(gz)
	for _, entry := range entries {
		size := int64(len(entry.data))
		if entry.size != 0 {
			size = entry.size
		}
		h := &tar.Header{Name: entry.name, Typeflag: entry.kind, Size: size, Mode: 0755}
		if entry.kind == tar.TypeSymlink || entry.kind == tar.TypeLink {
			h.Linkname = "/outside"
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(entry.data)); err != nil {
			t.Fatal(err)
		}
		if entry.size != 0 {
			_ = gz.Close()
			return output.Bytes()
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func installFixture(t *testing.T) (Installer, *release, map[string][]byte, *int) {
	t.Helper()
	name, _ := ArchiveName(PinnedVersion, "darwin", "arm64")
	root := strings.TrimSuffix(name, ".tar.gz")
	archive := archiveBytes(t, tarEntry{root + "/", tar.TypeDir, "", 0}, tarEntry{root + "/LICENSE", tar.TypeReg, "license", 0}, tarEntry{root + "/sing-box", tar.TypeReg, "fake executable", 0})
	digest := sha256.Sum256(archive)
	published, _ := time.Parse(time.RFC3339, PinnedPublishedAt)
	rel := &release{Tag: "v" + PinnedVersion, Published: published, Assets: []asset{{Name: name, Digest: "sha256:" + hex.EncodeToString(digest[:])}}}
	url, _ := DownloadURL(PinnedVersion, "darwin", "arm64")
	responses := map[string][]byte{url: archive}
	calls := new(int)
	i := Installer{OS: "darwin", Arch: "arm64", Now: func() time.Time { return published.Add(8 * 24 * time.Hour) }}
	i.Client = testClient(func(r *http.Request) (*http.Response, error) {
		if err := r.Context().Err(); err != nil {
			return nil, err
		}
		if r.Method != "GET" {
			t.Fatalf("method=%s", r.Method)
		}
		var data []byte
		if r.URL.String() == releaseAPI+"v"+PinnedVersion {
			data, _ = json.Marshal(rel)
		} else {
			var ok bool
			data, ok = responses[r.URL.String()]
			if !ok {
				t.Fatalf("unexpected network request %s", r.URL)
			}
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(data)), ContentLength: int64(len(data))}, nil
	})
	i.Runner = testRunner(func(_ context.Context, binary string, args ...string) ([]byte, error) {
		*calls++
		if !reflect.DeepEqual(args, []string{"version"}) {
			t.Fatalf("unexpected command arguments %v", args)
		}
		data, err := os.ReadFile(binary)
		if err != nil || string(data) != "fake executable" {
			t.Fatalf("staged binary=%s %v", data, err)
		}
		info, err := os.Stat(binary)
		if err != nil || info.Mode().Perm()&0100 == 0 {
			t.Fatal("binary is not executable")
		}
		return []byte("sing-box version " + PinnedVersion + "\nEnvironment: go1.27 darwin/arm64\n"), nil
	})
	return i, rel, responses, calls
}

func TestInstallVerifiedBinary(t *testing.T) {
	for _, method := range []string{"asset digest", "checksum file", "structural fallback"} {
		t.Run(method, func(t *testing.T) {
			i, rel, responses, calls := installFixture(t)
			if method == "checksum file" {
				responses[releaseBase+"v"+PinnedVersion+"/checksums.txt"] = []byte(strings.TrimPrefix(rel.Assets[0].Digest, "sha256:") + "  " + rel.Assets[0].Name + "\n")
				rel.Assets = append(rel.Assets, asset{Name: "checksums.txt"})
			}
			if method == "structural fallback" {
				rel.Assets[0].Digest = ""
			}
			destination := filepath.Join(t.TempDir(), "bin", "sing-box")
			if err := i.Install(context.Background(), destination, PinnedVersion); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(destination)
			if err != nil || string(data) != "fake executable" || *calls != 1 {
				t.Fatalf("installed=%s calls=%d err=%v", data, *calls, err)
			}
			entries, _ := os.ReadDir(filepath.Dir(destination))
			if len(entries) != 1 {
				t.Fatal("staging files left behind")
			}
		})
	}
}

func TestInstallFailurePreservesOldBinary(t *testing.T) {
	for _, scenario := range []string{"digest", "missing archive", "prerelease", "draft", "fresh", "seven days", "missing date", "tag", "checksum missing", "checksum mismatch", "corrupt tar", "version", "command", "cancelled", "http"} {
		t.Run(scenario, func(t *testing.T) {
			i, rel, responses, calls := installFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch scenario {
			case "digest":
				rel.Assets[0].Digest = "sha256:" + strings.Repeat("0", 64)
			case "missing archive":
				rel.Assets = nil
			case "prerelease":
				rel.Prerelease = true
			case "draft":
				rel.Draft = true
			case "fresh":
				rel.Published = i.Now().Add(-time.Hour)
			case "seven days":
				rel.Published = i.Now().Add(-7 * 24 * time.Hour)
			case "missing date":
				rel.Published = time.Time{}
			case "tag":
				rel.Tag = "v0.0.0"
			case "checksum missing", "checksum mismatch":
				filename := "missing.tar.gz"
				if scenario == "checksum mismatch" {
					filename = rel.Assets[0].Name
				}
				rel.Assets = append(rel.Assets, asset{Name: "checksums.txt"})
				responses[releaseBase+"v"+PinnedVersion+"/checksums.txt"] = []byte(strings.Repeat("0", 64) + "  " + filename)
			case "corrupt tar":
				rel.Assets[0].Digest = ""
				url, _ := DownloadURL(PinnedVersion, "darwin", "arm64")
				responses[url] = []byte("not a tar")
			case "version":
				i.Runner = testRunner(func(context.Context, string, ...string) ([]byte, error) { return []byte("sing-box version 0.0.0"), nil })
			case "command":
				i.Runner = testRunner(func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("exec format") })
			case "cancelled":
				cancel()
			case "http":
				i.Client = testClient(func(*http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: 429, Body: io.NopCloser(strings.NewReader("rate limit"))}, nil
				})
			}
			destination := filepath.Join(t.TempDir(), "sing-box")
			if err := os.WriteFile(destination, []byte("previous"), 0755); err != nil {
				t.Fatal(err)
			}
			if err := i.Install(ctx, destination, PinnedVersion); err == nil {
				t.Fatal("accepted invalid install")
			}
			data, _ := os.ReadFile(destination)
			if string(data) != "previous" {
				t.Fatal("old binary was changed")
			}
			if *calls != 0 {
				t.Fatal("executed unverified binary")
			}
			entries, _ := os.ReadDir(filepath.Dir(destination))
			if len(entries) != 1 {
				t.Fatal("staging files left behind")
			}
		})
	}
}

func TestArchiveRejectsUnsafeStructure(t *testing.T) {
	root := "sing-box-" + PinnedVersion + "-linux-amd64"
	valid := tarEntry{root + "/sing-box", tar.TypeReg, "binary", 0}
	cases := map[string][]tarEntry{
		"traversal":  {valid, {root + "/../escape", tar.TypeReg, "x", 0}},
		"absolute":   {valid, {"/escape", tar.TypeReg, "x", 0}},
		"other root": {{"other/sing-box", tar.TypeReg, "x", 0}},
		"symlink":    {valid, {root + "/link", tar.TypeSymlink, "", 0}},
		"hardlink":   {valid, {root + "/link", tar.TypeLink, "", 0}},
		"device":     {valid, {root + "/device", tar.TypeChar, "", 0}},
		"duplicate":  {valid, valid}, "empty": {{root + "/sing-box", tar.TypeReg, "", 0}},
		"missing":   {{root + "/LICENSE", tar.TypeReg, "license", 0}},
		"oversized": {{root + "/sing-box", tar.TypeReg, "", maxExtractedSize + 1}},
	}
	for name, entries := range cases {
		t.Run(name, func(t *testing.T) {
			if err := extractBinary(bytes.NewReader(archiveBytes(t, entries...)), root, filepath.Join(t.TempDir(), "binary")); err == nil {
				t.Fatal("accepted unsafe archive")
			}
		})
	}
	data := archiveBytes(t, valid)
	data[len(data)-8] ^= 0xff
	if err := extractBinary(bytes.NewReader(data), root, filepath.Join(t.TempDir(), "binary")); err == nil {
		t.Fatal("ignored gzip CRC")
	}
}

func TestDownloadLimitsAndVersionParsing(t *testing.T) {
	i := Installer{Client: testClient(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, ContentLength: -1, Body: io.NopCloser(strings.NewReader("12345"))}, nil
	})}
	if err := i.download(context.Background(), "https://example.test", io.Discard, 4); err == nil {
		t.Fatal("ignored streaming limit")
	}
	for _, output := range []string{"1.14.4", "sing-box version", "sing-box version 1.15.0-alpha.1", "sing-box version 1.14.4 extra", "password secret"} {
		runner := testRunner(func(context.Context, string, ...string) ([]byte, error) { return []byte(output), nil })
		if _, err := BinaryVersion(context.Background(), runner, "/fake"); err == nil || strings.Contains(err.Error(), "password secret") {
			t.Fatalf("invalid or leaked output: %v", err)
		}
	}
	if _, err := checksumFor([]byte(fmt.Sprintf("%s  x\n%s  x", strings.Repeat("0", 64), strings.Repeat("0", 64))), "x"); err == nil {
		t.Fatal("duplicate checksum accepted")
	}
}
