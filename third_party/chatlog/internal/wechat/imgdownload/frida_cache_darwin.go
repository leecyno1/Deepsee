//go:build darwin

package imgdownload

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	fridaCacheInactiveRetention = 1
	fridaCacheMinimumAge        = 2 * time.Minute
)

type fridaCachePruneResult struct {
	RemovedDirectories int
	RemovedBytes       int64
}

func defaultFridaCacheRoot() string {
	base := strings.TrimSpace(os.Getenv("XDG_CACHE_HOME"))
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil || strings.TrimSpace(home) == "" {
			return ""
		}
		base = filepath.Join(home, ".cache")
	}
	return filepath.Join(base, "frida")
}

func pruneFridaCacheAsync() {
	root := defaultFridaCacheRoot()
	if root == "" {
		return
	}
	commands, _ := exec.Command("/bin/ps", "-axo", "command=").Output()
	_, _ = pruneFridaCache(root, string(commands), fridaCacheInactiveRetention, fridaCacheMinimumAge, time.Now())
}

// pruneFridaCache removes only inactive frida-* extraction directories. Frida
// gives each macOS helper launch a unique directory, so retaining every launch
// adds roughly 48 MB per Chatlog restart. Active helper paths, very recent
// directories and the newest inactive rollback copy are preserved.
func pruneFridaCache(root, processCommands string, keepInactive int, minimumAge time.Duration, now time.Time) (fridaCachePruneResult, error) {
	result := fridaCachePruneResult{}
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return result, nil
		}
		return result, err
	}
	type candidate struct {
		path    string
		modTime time.Time
		active  bool
	}
	candidates := make([]candidate, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !strings.HasPrefix(entry.Name(), "frida-") {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			continue
		}
		path := filepath.Join(root, entry.Name())
		candidates = append(candidates, candidate{
			path:    path,
			modTime: info.ModTime(),
			active:  strings.Contains(processCommands, path+string(os.PathSeparator)),
		})
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].modTime.After(candidates[j].modTime) })
	if keepInactive < 0 {
		keepInactive = 0
	}
	keptInactive := 0
	for _, item := range candidates {
		if item.active || now.Sub(item.modTime) < minimumAge {
			continue
		}
		if keptInactive < keepInactive {
			keptInactive++
			continue
		}
		size := directoryRegularFileSize(item.path)
		if removeErr := os.RemoveAll(item.path); removeErr != nil {
			return result, removeErr
		}
		result.RemovedDirectories++
		result.RemovedBytes += size
	}
	return result, nil
}

func directoryRegularFileSize(root string) int64 {
	var total int64
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry == nil || !entry.Type().IsRegular() {
			return nil
		}
		if info, infoErr := entry.Info(); infoErr == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}
