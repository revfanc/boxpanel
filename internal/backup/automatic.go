package backup

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Status struct {
	LastAttempt int64  `json:"last_attempt"`
	LastSuccess int64  `json:"last_success"`
	Result      string `json:"result"`
}

type Service struct {
	DB               *sql.DB
	DataDir, Version string
	mu               sync.Mutex
	last             Status
}

func (s *Service) Create(ctx context.Context, p string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Create(ctx, s.DB, filepath.Join(s.DataDir, "certs"), s.Version, p, time.Now())
}
func (s *Service) Status() (Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status()
}
func (s *Service) status() (Status, error) {
	var status Status
	b, err := os.ReadFile(filepath.Join(s.DataDir, "backups", "status.json"))
	if errors.Is(err, os.ErrNotExist) {
		if s.last.LastAttempt != 0 {
			return s.last, nil
		}
		return status, nil
	}
	if err != nil {
		return status, err
	}
	err = json.Unmarshal(b, &status)
	if s.last.LastAttempt >= status.LastAttempt && s.last.LastAttempt != 0 {
		return s.last, nil
	}
	return status, err
}

// Tick retries failed attempts the next day, including across process restarts.
func (s *Service) Tick(ctx context.Context, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	status, err := s.status()
	if err != nil {
		return fmt.Errorf("read automatic backup status: %w", err)
	}
	if status.LastAttempt != 0 && now.UTC().Format("2006-01-02") == time.Unix(status.LastAttempt, 0).UTC().Format("2006-01-02") {
		return nil
	}
	dir := filepath.Join(s.DataDir, "backups")
	status.LastAttempt = now.Unix()
	status.Result = "failed"
	s.last = status
	if err = privateDir(dir); err != nil {
		return err
	}
	data, workErr := Create(ctx, s.DB, filepath.Join(s.DataDir, "certs"), s.Version, "", now)
	if workErr == nil {
		workErr = WriteNew(filepath.Join(dir, "boxpanel-auto-"+now.UTC().Format("20060102T150405.000000000Z")+".tar.gz"), data)
	}
	if workErr == nil {
		workErr = rotate(dir)
	}
	if workErr == nil {
		status.LastSuccess = now.Unix()
		status.Result = "success"
	}
	s.last = status
	b, _ := json.Marshal(status)
	temp, err := os.CreateTemp(dir, ".status-")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if _, err = temp.Write(b); err == nil {
		err = temp.Sync()
	}
	closeErr := temp.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(temp.Name(), filepath.Join(dir, "status.json"))
	}
	if err == nil {
		err = syncDir(dir)
	}
	if workErr != nil {
		return fmt.Errorf("automatic backup failed; check disk space and data directory permissions")
	}
	return err
}
func (s *Service) Run(ctx context.Context, report func(error)) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		if err := s.Tick(ctx, time.Now()); err != nil && ctx.Err() == nil {
			report(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func privateDir(dir string) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("backup directory must be an ordinary directory")
	}
	return os.Chmod(dir, 0700)
}

// WriteNew never overwrites an existing file (including symlinks).
func WriteNew(name string, data []byte) error {
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("create backup output: %w", err)
	}
	ok := false
	defer func() {
		f.Close()
		if !ok {
			os.Remove(name)
		}
	}()
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = syncDir(filepath.Dir(name)); err != nil {
		return err
	}
	ok = true
	return nil
}
func rotate(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var names []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "boxpanel-auto-") && strings.HasSuffix(e.Name(), ".tar.gz") {
			if !e.Type().IsRegular() {
				return fmt.Errorf("automatic backup is not an ordinary file")
			}
			if err = os.Chmod(filepath.Join(dir, e.Name()), 0600); err != nil {
				return err
			}
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for len(names) > 7 {
		if err = os.Remove(filepath.Join(dir, names[0])); err != nil {
			return err
		}
		names = names[1:]
	}
	return syncDir(dir)
}
