package http

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fsnotify/fsnotify"

	"github.com/sjzar/chatlog/internal/chatlog/ocr"
)

func (s *Service) startOCRMediaWatcher(runtime *imageOCRRuntime) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		runtime.setError(fmt.Errorf("watch OCR media files: %w", err))
		return
	}
	runtime.mediaWatcher = watcher
	runtime.mediaWatchDirs = make(map[string]struct{})
	runtime.wg.Add(1)
	go s.runOCRMediaWatcher(runtime)
}

func (s *Service) stopOCRMediaWatcher(runtime *imageOCRRuntime) {
	if runtime == nil || runtime.mediaWatcher == nil {
		return
	}
	_ = runtime.mediaWatcher.Close()
	runtime.mediaWatcher = nil
}

// ocrMediaWatchDir resolves only the bounded directory belonging to one OCR
// record. Watching that directory lets a late mid/original file wake the media
// gate without restoring an account-wide polling scan.
func (s *Service) ocrMediaWatchDir(ref ocr.ImageRef, resolvedPath string) string {
	if path := strings.TrimSpace(resolvedPath); path != "" {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return filepath.Clean(filepath.Dir(path))
		}
	}
	mediaPath := strings.TrimSpace(ref.MediaPath)
	if mediaPath == "" {
		return ""
	}
	base, _, err := s.safeDataPath(mediaPath)
	if err != nil {
		return ""
	}
	dir := filepath.Clean(filepath.Dir(base))
	if info, statErr := os.Stat(dir); statErr == nil && info.IsDir() {
		return dir
	}
	return ""
}

func (s *Service) syncOCRMediaWatchDirs(runtime *imageOCRRuntime, desired map[string]struct{}) error {
	if runtime == nil || runtime.mediaWatcher == nil {
		return nil
	}
	runtime.mediaWatchMu.Lock()
	defer runtime.mediaWatchMu.Unlock()
	if runtime.mediaWatchDirs == nil {
		runtime.mediaWatchDirs = make(map[string]struct{})
	}
	for dir := range desired {
		dir = filepath.Clean(strings.TrimSpace(dir))
		if dir == "." || dir == "" {
			continue
		}
		if _, watched := runtime.mediaWatchDirs[dir]; watched {
			continue
		}
		if err := runtime.mediaWatcher.Add(dir); err != nil {
			return fmt.Errorf("watch OCR media directory %s: %w", dir, err)
		}
		runtime.mediaWatchDirs[dir] = struct{}{}
	}
	for dir := range runtime.mediaWatchDirs {
		if _, keep := desired[dir]; keep {
			continue
		}
		_ = runtime.mediaWatcher.Remove(dir)
		delete(runtime.mediaWatchDirs, dir)
	}
	return nil
}

func (s *Service) runOCRMediaWatcher(runtime *imageOCRRuntime) {
	defer runtime.wg.Done()
	watcher := runtime.mediaWatcher
	if watcher == nil {
		return
	}
	for {
		select {
		case <-runtime.ctx.Done():
			return
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if event.Op.Has(fsnotify.Create) || event.Op.Has(fsnotify.Write) ||
				event.Op.Has(fsnotify.Rename) {
				runtime.wakeMediaGate()
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			if err != nil {
				runtime.setError(fmt.Errorf("OCR media watcher: %w", err))
			}
		}
	}
}
