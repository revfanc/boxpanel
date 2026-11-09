package main

import (
	"bytes"
	"testing"
)

func TestRun(t *testing.T) {
	tests := []struct {
		name   string
		args   []string
		code   int
		stdout string
		stderr string
	}{
		{name: "version", args: []string{"version"}, stdout: "boxpanel " + version + "\n"},
		{name: "no command", stdout: usage},
		{name: "help", args: []string{"help"}, stdout: usage},
		{name: "short help", args: []string{"-h"}, stdout: usage},
		{name: "long help", args: []string{"--help"}, stdout: usage},
		{name: "unknown command", args: []string{"unknown"}, code: 2,
			stderr: "boxpanel: unknown command \"unknown\"; run 'boxpanel help' for usage\n"},
		{name: "extra argument", args: []string{"version", "extra"}, code: 2,
			stderr: "boxpanel: unexpected arguments; run 'boxpanel help' for usage\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(tt.args, &stdout, &stderr); code != tt.code {
				t.Errorf("exit code = %d, want %d", code, tt.code)
			}
			if got := stdout.String(); got != tt.stdout {
				t.Errorf("stdout = %q, want %q", got, tt.stdout)
			}
			if got := stderr.String(); got != tt.stderr {
				t.Errorf("stderr = %q, want %q", got, tt.stderr)
			}
		})
	}
}
