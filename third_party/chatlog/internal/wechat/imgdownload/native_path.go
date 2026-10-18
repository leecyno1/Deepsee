package imgdownload

import (
	"path/filepath"
	"strings"
)

// nativeDownloadPath resolves the destination expected by WeChat's native CDN
// task. Message XML stores an account-relative extensionless base path, while
// the native downloader expects an absolute .dat filename.
func nativeDownloadPath(req Request) string {
	mediaPath := filepath.FromSlash(strings.TrimSpace(req.MediaPath))
	if mediaPath == "" {
		return ""
	}
	if !filepath.IsAbs(mediaPath) {
		dataDir := filepath.Clean(strings.TrimSpace(req.DataDir))
		if dataDir == "" || dataDir == "." {
			return ""
		}
		mediaPath = filepath.Join(dataDir, mediaPath)
	}
	mediaPath = filepath.Clean(mediaPath)
	lower := strings.ToLower(mediaPath)
	for _, suffix := range []string{"_t.dat", "_t.jpg", "_t.jpeg", "_t.png"} {
		if strings.HasSuffix(lower, suffix) {
			return mediaPath[:len(mediaPath)-len(suffix)] + ".dat"
		}
	}
	if filepath.Ext(mediaPath) == "" {
		mediaPath += ".dat"
	}
	return mediaPath
}
