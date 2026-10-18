package util

import (
	"fmt"
	"os"
	"path/filepath"
)

// WritePrivateFileAtomic replaces path without exposing a partially written or
// group/world-readable file. The parent directory is created when necessary,
// but permissions on an existing application data directory are not changed.
func WritePrivateFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	closeWithError := func(writeErr error) error {
		if closeErr := tmp.Close(); writeErr == nil {
			return closeErr
		}
		return writeErr
	}
	if err := tmp.Chmod(0600); err != nil {
		return closeWithError(err)
	}
	if _, err := tmp.Write(data); err != nil {
		return closeWithError(err)
	}
	if err := tmp.Sync(); err != nil {
		return closeWithError(err)
	}
	if err := closeWithError(nil); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return os.Chmod(path, 0600)
}
