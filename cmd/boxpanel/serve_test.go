package main

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestServeOptions(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
		want serveOptions
	}{
		{name: "defaults", want: serveOptions{webPath: "boxpanel", port: 2053}},
		{name: "custom mount and port", args: []string{"--web-path", "k3m9x2p7q1ab", "--port", "23053"}, want: serveOptions{webPath: "k3m9x2p7q1ab", port: 23053}},
		{name: "root mount", args: []string{"--web-path", ""}, want: serveOptions{port: 2053}},
		{name: "dev", args: []string{"--dev"}, want: serveOptions{port: 2053}},
		{name: "dev overrides", args: []string{"--port", "23053", "--web-path", "panel", "--dev"}, want: serveOptions{port: 2053}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseServeOptions(tt.args, io.Discard)
			if err != nil || got != tt.want {
				t.Fatalf("options=%+v error=%v, want %+v", got, err, tt.want)
			}
		})
	}
}

func TestServeCLIValidation(t *testing.T) {
	for _, args := range [][]string{
		{"--port", "0"}, {"--port", "65536"}, {"--port", "-1"}, {"--port", "invalid"},
		{"--port"}, {"--web-path", "a/b"}, {"--unknown"}, {"extra"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(append([]string{"serve"}, args...), &stdout, &stderr); code != 2 {
				t.Fatalf("exit code=%d, want 2", code)
			}
			if !strings.Contains(stderr.String(), "boxpanel serve --help") {
				t.Fatalf("missing next step: %s", stderr.String())
			}
		})
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"serve", "--help"}, &stdout, &stderr); code != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), "-web-path") {
		t.Fatalf("help: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
}
