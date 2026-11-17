package singbox

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type captureTransport struct{ auth map[string]string }

func (c *captureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.auth[r.URL.Scheme+"://"+r.URL.Host] = r.Header.Get("Authorization")
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}")), Header: http.Header{}}, nil
}

// GITHUB_TOKEN must reach only the GitHub API over HTTPS, never download hosts.
func TestDownloadSendsTokenOnlyToGitHubAPI(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "test-token")
	c := &captureTransport{auth: map[string]string{}}
	i := Installer{Client: &http.Client{Transport: c}}
	for _, u := range []string{"https://api.github.com/repos/x/y", "https://github.com/x/y/releases/download/a", "http://api.github.com/x", "https://objects.githubusercontent.com/z"} {
		if _, err := i.get(context.Background(), u, 1<<10); err != nil {
			t.Fatal(err)
		}
	}
	want := map[string]string{"https://api.github.com": "Bearer test-token", "https://github.com": "", "http://api.github.com": "", "https://objects.githubusercontent.com": ""}
	for host, auth := range want {
		if c.auth[host] != auth {
			t.Fatalf("%s: Authorization=%q, want %q", host, c.auth[host], auth)
		}
	}
	t.Setenv("GITHUB_TOKEN", "")
	c.auth = map[string]string{}
	if _, err := i.get(context.Background(), "https://api.github.com/repos/x/y", 1<<10); err != nil || c.auth["https://api.github.com"] != "" {
		t.Fatal("no token must mean no Authorization header")
	}
}
