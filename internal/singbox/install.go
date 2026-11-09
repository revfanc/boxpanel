package singbox

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/revfanc/boxpanel/internal/host"
)

const maxArchiveSize = 128 << 20
const maxExtractedSize = 256 << 20

type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

type Installer struct {
	OS, Arch string
	Client   HTTPClient
	Runner   host.Runner
	Now      func() time.Time
}

type release struct {
	Tag        string    `json:"tag_name"`
	Draft      bool      `json:"draft"`
	Prerelease bool      `json:"prerelease"`
	Published  time.Time `json:"published_at"`
	Assets     []asset   `json:"assets"`
}
type asset struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
}

// Install only replaces the binary after all checks succeed. It does not write
// sing-box configuration or operate services. All network/command IO is injected;
// local staging uses ordinary files beneath the destination directory.
func (i Installer) Install(ctx context.Context, destination, version string) error {
	version, err := NormalizeVersion(version)
	if err != nil {
		return err
	}
	if i.OS == "" {
		i.OS = runtime.GOOS
	}
	if i.Arch == "" {
		i.Arch = runtime.GOARCH
	}
	if i.Runner == nil {
		i.Runner = host.Native{}
	}
	if i.Now == nil {
		i.Now = time.Now
	}
	if i.Client == nil {
		i.Client = &http.Client{Timeout: 2 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 || req.URL.Scheme != "https" {
				return fmt.Errorf("unsafe download redirect")
			}
			return nil
		}}
	}
	name, err := ArchiveName(version, i.OS, i.Arch)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	data, err := i.get(ctx, releaseAPI+"v"+version, 2<<20)
	if err != nil {
		return fmt.Errorf("read official sing-box release: %w", err)
	}
	var rel release
	if err := json.Unmarshal(data, &rel); err != nil {
		return fmt.Errorf("decode sing-box release: %w", err)
	}
	if rel.Tag != "v"+version || rel.Draft || rel.Prerelease || rel.Published.IsZero() || !i.Now().After(rel.Published.Add(7*24*time.Hour)) {
		return fmt.Errorf("sing-box release must be stable and published more than seven days ago")
	}
	var archive *asset
	var checksums []string
	for index := range rel.Assets {
		a := &rel.Assets[index]
		if a.Name == name {
			if archive != nil {
				return fmt.Errorf("duplicate release archive")
			}
			archive = a
		}
		lower := strings.ToLower(a.Name)
		if strings.Contains(lower, "checksum") || strings.Contains(lower, "sha256") {
			// Never silently bypass a checksum file we do not understand.
			checksums = append(checksums, a.Name)
		}
	}
	if archive == nil {
		return fmt.Errorf("official release does not contain the requested platform archive")
	}
	var expected []string
	if archive.Digest != "" {
		if !strings.HasPrefix(archive.Digest, "sha256:") {
			return fmt.Errorf("unsupported release digest")
		}
		digest, err := validDigest(strings.TrimPrefix(archive.Digest, "sha256:"))
		if err != nil {
			return err
		}
		expected = append(expected, digest)
	}
	for _, filename := range checksums {
		if path.Base(filename) != filename || strings.ContainsAny(filename, "?#\\") {
			return fmt.Errorf("invalid checksum asset name")
		}
		body, err := i.get(ctx, releaseBase+"v"+version+"/"+filename, 1<<20)
		if err != nil {
			return fmt.Errorf("download release checksums: %w", err)
		}
		digest, err := checksumFor(body, name)
		if err != nil {
			return err
		}
		expected = append(expected, digest)
	}
	if len(expected) == 0 {
		return fmt.Errorf("official release has no SHA-256 checksum; choose a release with a checksum file or asset digest")
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
		return fmt.Errorf("create binary directory: %w", err)
	}
	stage, err := os.MkdirTemp(filepath.Dir(destination), ".sing-box-*")
	if err != nil {
		return fmt.Errorf("stage sing-box: %w", err)
	}
	defer os.RemoveAll(stage)
	f, err := os.Create(filepath.Join(stage, "archive.tar.gz"))
	if err != nil {
		return fmt.Errorf("create archive: %w", err)
	}
	defer f.Close()
	url, _ := DownloadURL(version, i.OS, i.Arch)
	hash := sha256.New()
	if err := i.download(ctx, url, io.MultiWriter(f, hash), maxArchiveSize); err != nil {
		return fmt.Errorf("download sing-box: %w", err)
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	for _, digest := range expected {
		if digest != actual {
			return fmt.Errorf("sing-box SHA-256 mismatch; retry the official release download")
		}
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("read downloaded archive: %w", err)
	}
	binary := filepath.Join(stage, "sing-box")
	if err := extractBinary(f, strings.TrimSuffix(name, ".tar.gz"), binary); err != nil {
		return fmt.Errorf("validate sing-box archive: %w", err)
	}
	got, err := BinaryVersion(ctx, i.Runner, binary)
	if err != nil {
		return err
	}
	if got != version {
		return fmt.Errorf("downloaded sing-box version does not match requested version")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("install sing-box: %w", err)
	}
	if err := os.Rename(binary, destination); err != nil {
		return fmt.Errorf("replace sing-box binary: %w", err)
	}
	return nil
}

func BinaryVersion(ctx context.Context, runner host.Runner, binary string) (string, error) {
	output, err := runner.Run(ctx, binary, "version")
	if err != nil {
		return "", fmt.Errorf("execute sing-box version: %w", err)
	}
	line, _, _ := strings.Cut(string(output), "\n")
	fields := strings.Fields(line)
	if len(fields) != 3 || fields[0] != "sing-box" || fields[1] != "version" {
		return "", fmt.Errorf("invalid sing-box version output")
	}
	version, err := NormalizeVersion(fields[2])
	if err != nil {
		return "", fmt.Errorf("parse sing-box version: %w", err)
	}
	return version, nil
}

func (i Installer) get(ctx context.Context, url string, limit int64) ([]byte, error) {
	var body strings.Builder
	if err := i.download(ctx, url, &body, limit); err != nil {
		return nil, err
	}
	return []byte(body.String()), nil
}

func (i Installer) download(ctx context.Context, url string, dst io.Writer, limit int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("create download request: %w", err)
	}
	req.Header.Set("User-Agent", "boxpanel")
	resp, err := i.Client.Do(req)
	if err != nil {
		return fmt.Errorf("request official release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("official release returned HTTP %d; check connectivity or GitHub rate limits", resp.StatusCode)
	}
	if resp.ContentLength > limit {
		return fmt.Errorf("download exceeds size limit")
	}
	n, err := io.Copy(dst, io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return fmt.Errorf("read release download: %w", err)
	}
	if n > limit {
		return fmt.Errorf("download exceeds size limit")
	}
	return nil
}

func validDigest(value string) (string, error) {
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != sha256.Size {
		return "", fmt.Errorf("invalid release SHA-256")
	}
	return strings.ToLower(value), nil
}

func checksumFor(data []byte, name string) (string, error) {
	found := ""
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || strings.TrimPrefix(fields[1], "*") != name {
			continue
		}
		if found != "" {
			return "", fmt.Errorf("duplicate archive checksum")
		}
		var err error
		found, err = validDigest(fields[0])
		if err != nil {
			return "", err
		}
	}
	if found == "" {
		return "", fmt.Errorf("release checksum file has no SHA-256 entry for requested archive")
	}
	return found, nil
}

func extractBinary(source io.Reader, root, destination string) error {
	gz, err := gzip.NewReader(source)
	if err != nil {
		return fmt.Errorf("open gzip: %w", err)
	}
	defer gz.Close()
	limited := &io.LimitedReader{R: gz, N: maxExtractedSize + 1}
	tr := tar.NewReader(limited)
	seen := map[string]bool{}
	found := false
	for count := 0; ; count++ {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar: %w", err)
		}
		name := strings.TrimSuffix(h.Name, "/")
		if count >= 64 || h.Size < 0 || h.Size > maxExtractedSize || path.Clean(name) != name || strings.ContainsAny(name, "\\\x00") || (name != root && !strings.HasPrefix(name, root+"/")) || seen[name] {
			return fmt.Errorf("unsafe, duplicate or oversized tar entry")
		}
		seen[name] = true
		if h.Typeflag == tar.TypeDir && h.Size == 0 {
			continue
		}
		if h.Typeflag != tar.TypeReg {
			return fmt.Errorf("tar links and special files are not allowed")
		}
		if name == root {
			return fmt.Errorf("archive root must be a directory")
		}
		if name != root+"/sing-box" {
			continue
		}
		if h.Size == 0 {
			return fmt.Errorf("empty sing-box binary")
		}
		f, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0755)
		if err != nil {
			return fmt.Errorf("create staged binary: %w", err)
		}
		_, copyErr := io.Copy(f, tr)
		syncErr := f.Sync()
		closeErr := f.Close()
		for _, err := range []error{copyErr, syncErr, closeErr} {
			if err != nil {
				return fmt.Errorf("write staged binary: %w", err)
			}
		}
		found = true
	}
	// Consume the gzip trailer too: tar EOF alone does not verify gzip CRC.
	if _, err := io.Copy(io.Discard, limited); err != nil {
		return fmt.Errorf("read gzip trailer: %w", err)
	}
	if limited.N <= 0 {
		return fmt.Errorf("expanded archive exceeds size limit")
	}
	if !found {
		return fmt.Errorf("archive has no sing-box binary")
	}
	return nil
}
