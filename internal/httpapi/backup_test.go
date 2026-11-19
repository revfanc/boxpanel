package httpapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"

	"github.com/revfanc/boxpanel/internal/auth"
	"github.com/revfanc/boxpanel/internal/backup"
)

type backupStub struct {
	calls int
	err   error
}

func (b *backupStub) Create(context.Context, string) ([]byte, error) {
	b.calls++
	return []byte("encrypted binary attachment"), b.err
}
func (b *backupStub) Status() (backup.Status, error) {
	return backup.Status{LastAttempt: 10, LastSuccess: 9, Result: "failed"}, b.err
}
func (b *backupStub) Restore(context.Context, []byte, string) error { b.calls++; return b.err }
func multipartBackup(t *testing.T, data []byte, pass, confirm string) (string, string) {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("file", "untrusted-name.enc")
	if err != nil {
		t.Fatal(err)
	}
	part.Write(data)
	w.WriteField("passphrase", pass)
	w.WriteField("confirm", confirm)
	w.Close()
	return body.String(), w.FormDataContentType()
}
func TestBackupAPIAuthenticationCSRFAndBinary(t *testing.T) {
	_, conn, _ := testAPI(t, false)
	service, err := auth.New(conn, nil)
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.Login(context.Background(), "192.0.2.8", "admin", "test-password", "")
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	b := &backupStub{}
	handler := New(service, Options{Backups: b, Logger: slog.New(slog.NewTextHandler(&logs, nil))})
	cookie := &http.Cookie{Name: CookieName, Value: session.Token}
	for _, path := range []string{"/backup", "/restore", "/backup/status"} {
		method := "POST"
		if path == "/backup/status" {
			method = "GET"
		}
		w := request(t, handler, method, "http://panel/api/v1"+path, "", nil, nil)
		requireStatus(t, w, 401)
	}
	w := request(t, handler, "POST", "http://panel/api/v1/backup", `{"passphrase":"test-passphrase-long"}`, cookie, map[string]string{"Origin": "https://evil.invalid"})
	requireStatus(t, w, 403)
	for _, body := range []string{`{"passphrase":"short"}`, `{"passphrase":null}`, `{"passphrase":"test-passphrase-long","passphrase":"test-passphrase-long"}`, `{"passphrase":"test-passphrase-long","extra":true}`} {
		w = request(t, handler, "POST", "http://panel/api/v1/backup", body, cookie, nil)
		requireStatus(t, w, 400)
	}
	if b.calls != 0 {
		t.Fatal("invalid requests reached backend")
	}
	w = request(t, handler, "POST", "http://panel/api/v1/backup", `{"passphrase":"test-passphrase-long"}`, cookie, nil)
	requireStatus(t, w, 200)
	if w.Header().Get("Content-Type") != "application/octet-stream" || !strings.Contains(w.Header().Get("Content-Disposition"), ".tar.gz.enc") || w.Body.String() != "encrypted binary attachment" {
		t.Fatal("binary response corrupted")
	}
	w = request(t, handler, "GET", "http://panel/api/v1/backup", "", cookie, nil)
	requireStatus(t, w, 405)
	w = request(t, handler, "GET", "http://panel/api/v1/backup/status", "", cookie, nil)
	requireStatus(t, w, 200)
	w = request(t, handler, "HEAD", "http://panel/api/v1/backup/status", "", cookie, nil)
	requireStatus(t, w, 200)
	if w.Body.Len() != 0 {
		t.Fatal("HEAD body")
	}
	if strings.Contains(logs.String(), "test-passphrase-long") {
		t.Fatal("passphrase logged")
	}
}
func TestRestoreAPIBoundsConfirmationAndErrors(t *testing.T) {
	_, conn, _ := testAPI(t, false)
	service, err := auth.New(conn, nil)
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.Login(context.Background(), "192.0.2.8", "admin", "test-password", "")
	if err != nil {
		t.Fatal(err)
	}
	b := &backupStub{}
	handler := New(service, Options{Backups: b, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	cookie := &http.Cookie{Name: CookieName, Value: session.Token}
	encrypted, err := backup.Encrypt([]byte("test payload"), "test-passphrase-long")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		data          []byte
		pass, confirm string
		status        int
	}{{encrypted, "test-passphrase-long", "true", 202}, {encrypted, "short", "true", 400}, {encrypted, "test-passphrase-long", "false", 400}, {[]byte("plaintext"), "test-passphrase-long", "true", 400}} {
		body, content := multipartBackup(t, tt.data, tt.pass, tt.confirm)
		w := request(t, handler, "POST", "http://panel/api/v1/restore", body, cookie, map[string]string{"Content-Type": content})
		requireStatus(t, w, tt.status)
	}
	if b.calls != 1 {
		t.Fatal(b.calls)
	}
	for _, e := range []error{backup.ErrInvalid, backup.ErrNewer, errors.New("credential-do-not-expose")} {
		b.err = e
		body, content := multipartBackup(t, encrypted, "test-passphrase-long", "true")
		w := request(t, handler, "POST", "http://panel/api/v1/restore", body, cookie, map[string]string{"Content-Type": content})
		if w.Code < 400 || strings.Contains(w.Body.String(), "credential-do-not-expose") {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	body, content := multipartBackup(t, encrypted, "test-passphrase-long", "true")
	w := request(t, handler, "POST", "http://panel/api/v1/restore", body, cookie, map[string]string{"Content-Type": content, "Origin": "https://other.invalid"})
	requireStatus(t, w, 403)
}
