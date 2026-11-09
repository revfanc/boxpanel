// Package host isolates filesystem access and external commands for Linux services.
package host

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

type Files interface {
	ReadFile(string) ([]byte, error)
	// WriteFile atomically replaces a file in an existing directory.
	WriteFile(string, []byte, fs.FileMode) error
	Remove(string) error
}

type Runner interface {
	LookPath(string) (string, error)
	Run(context.Context, string, ...string) ([]byte, error)
}

type Native struct{}

func (Native) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }
func (Native) Remove(path string) error             { return os.Remove(path) }
func (Native) LookPath(name string) (string, error) { return exec.LookPath(name) }

func (Native) WriteFile(path string, data []byte, mode fs.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".boxpanel-*")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := f.Chmod(mode); err != nil {
		return fmt.Errorf("set file permissions: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("write temporary file: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync temporary file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close temporary file: %w", err)
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return fmt.Errorf("replace file: %w", err)
	}
	return nil
}

// Commands never use a shell. Bound execution time and captured output, even
// when the caller has no deadline. C locale makes parsers deterministic.
func (Native) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	cmd.WaitDelay = time.Second
	var output limitedBuffer
	cmd.Stdout, cmd.Stderr = &output, &output
	err := cmd.Run()
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if output.overflow {
		err = errors.Join(err, errors.New("command output exceeds 1 MiB"))
	}
	if err != nil {
		return output.Bytes(), fmt.Errorf("run %s: %w", name, err)
	}
	return output.Bytes(), nil
}

type limitedBuffer struct {
	bytes.Buffer
	overflow bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := (1 << 20) - b.Len()
	if n > remaining {
		b.overflow = true
		p = p[:remaining]
	}
	_, _ = b.Buffer.Write(p)
	return n, nil
}

func ExitCode(err error, code int) bool {
	var status interface{ ExitCode() int }
	return errors.As(err, &status) && status.ExitCode() == code
}
