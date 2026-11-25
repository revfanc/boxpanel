// Package backup implements the versioned, bounded backup format shared by CLI,
// HTTP and the daily worker. Archives never contain generated configuration.
package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/filelock"
	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/scrypt"
)

const MaxSize = 64 << 20
const maxExpanded = 256 << 20
const maxFiles = 512
const magic = "BOXPANEL-BACKUP\x00\x01"

var ErrInvalid = errors.New("备份无效或口令错误；请核对口令并重新选择完整备份")
var ErrNewer = errors.New("备份 schema 比当前程序新；请先升级 boxpanel 再恢复")

func ValidPassphrase(p string) bool {
	return utf8.ValidString(p) && utf8.RuneCountInString(p) >= 12 && len(p) <= 1024
}

type Manifest struct {
	Format     int               `json:"format"`
	Version    string            `json:"boxpanel_version"`
	Schema     int               `json:"schema_version"`
	CreatedAt  int64             `json:"created_at"`
	PublicHost string            `json:"public_host"`
	Files      map[string]string `json:"files"`
}

type Archive struct {
	Manifest Manifest
	Files    map[string][]byte
}

// Create uses SQLite's consistent VACUUM INTO snapshot, not a DB/WAL copy.
// The caller owns conn; it must be the writer pool (query_only rejects VACUUM).
func Create(ctx context.Context, conn *sql.DB, certDir, version, passphrase string, now time.Time) ([]byte, error) {
	if passphrase != "" && !ValidPassphrase(passphrase) {
		return nil, ErrInvalid
	}
	var out bytes.Buffer
	if err := WriteSnapshot(ctx, &out, conn, certDir, version, now); err != nil {
		return nil, err
	}
	if passphrase != "" {
		return Encrypt(out.Bytes(), passphrase)
	}
	return out.Bytes(), nil
}

// WriteSnapshot writes the same bounded gzip archive used for automatic backups
// to a private file or stream. No HTTP response buffering is needed for migration.
func WriteSnapshot(ctx context.Context, out io.Writer, conn *sql.DB, certDir, version string, now time.Time) error {
	a, err := capture(ctx, conn, certDir, version, now)
	if err != nil {
		return err
	}
	return packTo(out, a)
}

func capture(ctx context.Context, conn *sql.DB, certDir, version string, now time.Time) (Archive, error) {
	var a Archive
	unlock, err := filelock.Acquire(ctx, certDir+".lock")
	if err != nil {
		return a, err
	}
	defer unlock()
	if info, e := os.Lstat(certDir); e == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
		return a, ErrInvalid
	}
	dir, err := os.MkdirTemp("", "boxpanel-snapshot-")
	if err != nil {
		return a, fmt.Errorf("create snapshot directory: %w", err)
	}
	defer os.RemoveAll(dir)
	name := filepath.Join(dir, "boxpanel.db")
	if _, err = conn.ExecContext(ctx, "VACUUM INTO ?", name); err != nil {
		return a, fmt.Errorf("snapshot database: %w", err)
	}
	if err = os.Chmod(name, 0600); err != nil {
		return a, err
	}
	snapshot, err := db.Open(ctx, name)
	if err != nil {
		return a, fmt.Errorf("read snapshot: %w", err)
	}
	m := Manifest{Format: 1, Version: version, CreatedAt: now.Unix(), Files: map[string]string{}}
	err = snapshot.QueryRowContext(ctx, "SELECT max(version) FROM schema_migrations").Scan(&m.Schema)
	if err == nil {
		m.PublicHost, err = db.GetSetting(ctx, snapshot, db.PublicHost)
	}
	if err == nil {
		_, err = snapshot.ExecContext(ctx, "PRAGMA secure_delete=ON")
	}
	if err == nil {
		err = db.ClearMasterMigration(ctx, snapshot)
	}
	if err == nil {
		_, err = snapshot.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)")
	}
	// Close before reading the standalone snapshot file.
	closeErr := snapshot.Close()
	if err != nil {
		return a, err
	}
	if closeErr != nil {
		return a, closeErr
	}
	info, err := os.Stat(name)
	if err != nil || info.Size() > MaxSize {
		return a, ErrInvalid
	}
	data, err := os.ReadFile(name)
	if err != nil {
		return a, err
	}
	files := map[string][]byte{"boxpanel.db": data}
	root, err := os.OpenRoot(certDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return a, fmt.Errorf("open certificates: %w", err)
	}
	if err == nil {
		defer root.Close()
		entries, e := os.ReadDir(certDir)
		if e != nil {
			return a, e
		}
		total := len(data)
		for _, entry := range entries {
			if !entry.Type().IsRegular() {
				return a, fmt.Errorf("backup certificates: only ordinary files allowed; remove links or subdirectories")
			}
			info, e := root.Lstat(entry.Name())
			if e != nil || !info.Mode().IsRegular() {
				return a, ErrInvalid
			}
			if info.Size() > MaxSize || len(files) >= maxFiles {
				return a, ErrInvalid
			}
			f, e := root.Open(entry.Name())
			if e != nil {
				return a, e
			}
			b, e := io.ReadAll(io.LimitReader(f, MaxSize+1))
			f.Close()
			if e != nil {
				return a, e
			}
			total += len(b)
			if total > maxExpanded || len(b) > MaxSize {
				return a, ErrInvalid
			}
			files["certs/"+entry.Name()] = b
		}
	}
	validation, err := db.Open(ctx, name)
	if err != nil {
		return a, err
	}
	err = validateMaterials(ctx, validation, files)
	validation.Close()
	if err != nil {
		return a, err
	}
	for n, b := range files {
		sum := sha256.Sum256(b)
		m.Files[n] = hex.EncodeToString(sum[:])
	}
	return Archive{m, files}, nil
}

func pack(a Archive) ([]byte, error) {
	var out bytes.Buffer
	if err := packTo(&out, a); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

type snapshotWriter struct {
	out       io.Writer
	remaining int
}

func (w *snapshotWriter) Write(data []byte) (int, error) {
	if len(data) > w.remaining {
		return 0, ErrInvalid
	}
	n, err := w.out.Write(data)
	w.remaining -= n
	return n, err
}

func packTo(out io.Writer, a Archive) error {
	gz := gzip.NewWriter(&snapshotWriter{out, MaxSize - 128})
	tw := tar.NewWriter(gz)
	manifest, err := json.Marshal(a.Manifest)
	if err != nil {
		return err
	}
	write := func(name string, b []byte) error {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(b)), Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		_, err := tw.Write(b)
		return err
	}
	if err = write("manifest.json", manifest); err != nil {
		return err
	}
	// Stable ordering helps inspection; ciphertext is deliberately randomized.
	names := make([]string, 0, len(a.Files))
	for n := range a.Files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if err = write(n, a.Files[n]); err != nil {
			return err
		}
	}
	if err = tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

// Encrypt authenticates the entire fixed version header, salt, nonce and payload.
// KDF parameters are fixed per format, never read from untrusted input.
func Encrypt(data []byte, p string) ([]byte, error) {
	if !ValidPassphrase(p) {
		return nil, ErrInvalid
	}
	header := make([]byte, len(magic)+16+chacha20poly1305.NonceSizeX)
	copy(header, magic)
	if _, err := rand.Read(header[len(magic):]); err != nil {
		return nil, fmt.Errorf("generate backup salt and nonce: %w", err)
	}
	key, err := scrypt.Key([]byte(p), header[len(magic):len(magic)+16], 32768, 8, 1, 32)
	if err != nil {
		return nil, err
	}
	defer clear(key)
	cipher, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	if len(data)+len(header)+chacha20poly1305.Overhead > MaxSize {
		return nil, ErrInvalid
	}
	return cipher.Seal(header, header[len(magic)+16:], data, header), nil
}

func IsEncrypted(data []byte) bool { return bytes.HasPrefix(data, []byte(magic)) }

func Decode(data []byte, p string, requireEncrypted bool) (Archive, error) {
	var a Archive
	if len(data) > MaxSize {
		return a, ErrInvalid
	}
	if IsEncrypted(data) {
		n := len(magic) + 16 + chacha20poly1305.NonceSizeX
		if len(data) < n+chacha20poly1305.Overhead || !ValidPassphrase(p) {
			return a, ErrInvalid
		}
		key, err := scrypt.Key([]byte(p), data[len(magic):len(magic)+16], 32768, 8, 1, 32)
		if err != nil {
			return a, ErrInvalid
		}
		defer clear(key)
		cipher, err := chacha20poly1305.NewX(key)
		if err != nil {
			return a, ErrInvalid
		}
		data, err = cipher.Open(nil, data[len(magic)+16:n], data[n:], data[:n])
		if err != nil {
			return a, ErrInvalid
		}
	} else if requireEncrypted || p != "" {
		return a, ErrInvalid
	}
	compressed := bytes.NewReader(data)
	gz, err := gzip.NewReader(compressed)
	if err != nil {
		return a, ErrInvalid
	}
	defer gz.Close()
	gz.Multistream(false)
	limited := &io.LimitedReader{R: gz, N: maxExpanded + 1}
	tr := tar.NewReader(limited)
	a.Files = map[string][]byte{}
	var raw []byte
	seen := map[string]bool{}
	for {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return Archive{}, ErrInvalid
		}
		// Flat allowlist prevents traversal, links, device files and duplicate paths.
		n := h.Name
		allowed := n == "manifest.json" || n == "boxpanel.db" || strings.HasPrefix(n, "certs/") && path.Base(n) != "." && path.Dir(n) == "certs"
		if !allowed || path.Clean(n) != n || strings.ContainsAny(n, "\\:\x00") || strings.HasPrefix(n, "/") || h.Typeflag != tar.TypeReg || h.Linkname != "" || seen[n] || len(seen) >= maxFiles+1 || h.Size < 0 || h.Size > MaxSize || n == "manifest.json" && h.Size > 128<<10 {
			return Archive{}, ErrInvalid
		}
		seen[n] = true
		b, e := io.ReadAll(tr)
		if e != nil || int64(len(b)) != h.Size {
			return Archive{}, ErrInvalid
		}
		if n == "manifest.json" {
			if len(b) > 128<<10 {
				return Archive{}, ErrInvalid
			}
			raw = b
		} else {
			a.Files[n] = b
		}
	}
	// Consume gzip footer and reject concatenated streams/trailing bytes, including
	// nonzero padding after tar EOF. Reading only tar EOF would miss CRC failures.
	tail, err := io.ReadAll(limited)
	if err != nil || limited.N <= 0 || compressed.Len() != 0 {
		return Archive{}, ErrInvalid
	}
	for _, b := range tail {
		if b != 0 {
			return Archive{}, ErrInvalid
		}
	}
	unique := json.NewDecoder(bytes.NewReader(raw))
	if uniqueJSON(unique) != nil {
		return Archive{}, ErrInvalid
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&a.Manifest) != nil || dec.Decode(new(any)) != io.EOF || a.Manifest.Format != 1 || a.Manifest.Schema < 1 || a.Manifest.CreatedAt <= 0 || a.Manifest.Version == "" || a.Manifest.PublicHost == "" || len(a.Files) != len(a.Manifest.Files) || len(a.Files["boxpanel.db"]) == 0 {
		return Archive{}, ErrInvalid
	}
	for n, b := range a.Files {
		sum := sha256.Sum256(b)
		if a.Manifest.Files[n] != hex.EncodeToString(sum[:]) {
			return Archive{}, ErrInvalid
		}
	}
	return a, nil
}

// Reject ambiguous manifests before decoding into structs/maps, which would
// otherwise silently accept duplicate JSON keys.
func uniqueJSON(d *json.Decoder) error {
	token, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			token, e := d.Token()
			if e != nil {
				return e
			}
			key, ok := token.(string)
			if !ok || seen[key] {
				return ErrInvalid
			}
			seen[key] = true
			if e = uniqueJSON(d); e != nil {
				return e
			}
		}
	case '[':
		for d.More() {
			if e := uniqueJSON(d); e != nil {
				return e
			}
		}
	default:
		return ErrInvalid
	}
	_, err = d.Token()
	return err
}
