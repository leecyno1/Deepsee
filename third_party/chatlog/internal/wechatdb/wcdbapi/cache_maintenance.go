package wcdbapi

import (
	"os"
	"path/filepath"
	"strings"
)

// ClearDecryptedCacheDirectory removes artifacts left by pre-direct-reader
// versions. Current runtime queries never access this directory.
func ClearDecryptedCacheDirectory(root string) (int64, int64, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return 0, 0, os.ErrInvalid
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0, nil
		}
		return 0, 0, err
	}
	var files int64
	var bytes int64
	for _, entry := range entries {
		path := filepath.Join(root, entry.Name())
		entryFiles, entryBytes, usageErr := cacheArtifactUsage(path)
		if usageErr != nil {
			return files, bytes, usageErr
		}
		if err := os.RemoveAll(path); err != nil {
			return files, bytes, err
		}
		files += entryFiles
		bytes += entryBytes
	}
	return files, bytes, nil
}

func cacheArtifactUsage(root string) (int64, int64, error) {
	var files int64
	var bytes int64
	err := filepath.Walk(root, func(_ string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.Mode().IsRegular() {
			files++
			bytes += info.Size()
		}
		return nil
	})
	return files, bytes, err
}
