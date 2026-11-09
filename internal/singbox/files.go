package singbox

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/revfanc/boxpanel/internal/host"
)

type nativeApplyFiles struct{ host.Native }

func syncDirectory(path string) error {
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("open configuration directory: %w", err)
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("sync configuration directory: %w", err)
	}
	return nil
}
func (nativeApplyFiles) WriteFile(path string, data []byte, mode fs.FileMode) error {
	if err := (host.Native{}).WriteFile(path, data, mode); err != nil {
		return err
	}
	return syncDirectory(path)
}
func (nativeApplyFiles) Rename(from, to string) error {
	if err := os.Rename(from, to); err != nil {
		return fmt.Errorf("replace checked configuration: %w", err)
	}
	return syncDirectory(to)
}
func (nativeApplyFiles) Remove(path string) error {
	if err := os.Remove(path); err != nil {
		return err
	}
	return syncDirectory(path)
}
