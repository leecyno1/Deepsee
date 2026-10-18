package http

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// resolvePlainImageAttachment repairs reads of WeChat image attachments whose
// filename says jpg/png while the stored bytes remain encrypted. RWTemp keeps
// the source talker directory, which lets us resolve the already-downloaded
// plaintext image by exact size and nearest modification time without changing
// any WeChat-managed file or database.
func resolvePlainImageAttachment(dataDir, path string) string {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "." || !isImageFileName(path) || hasPlainImageHeader(path) {
		return path
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Size() <= 0 {
		return path
	}

	type scope struct{ talker, month string }
	scopes := make([]scope, 0, 2)
	seen := make(map[scope]struct{}, 2)
	addScope := func(talker, month string) {
		value := scope{strings.TrimSpace(talker), strings.TrimSpace(month)}
		if value.talker == "" || value.month == "" {
			return
		}
		if _, ok := seen[value]; ok {
			return
		}
		seen[value] = struct{}{}
		scopes = append(scopes, value)
	}
	if talker, month, ok := rwTempImageScope(path); ok {
		addScope(talker, month)
	}
	if month, ok := messageFileMonth(path); ok {
		pattern := filepath.Join(filepath.Clean(dataDir), "temp", "RWTemp", month, "*", filepath.Base(path))
		matches, _ := filepath.Glob(pattern)
		for _, match := range matches {
			if talker, matchedMonth, scopeOK := rwTempImageScope(match); scopeOK {
				addScope(talker, matchedMonth)
			}
		}
	}

	type candidate struct {
		path  string
		delta time.Duration
		same  bool
	}
	candidates := make([]candidate, 0, 4)
	for _, source := range scopes {
		dir := filepath.Join(filepath.Clean(dataDir), "msg", "attach", source.talker, source.month, "Img")
		entries, readErr := os.ReadDir(dir)
		if readErr != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || isThumbnailImageName(entry.Name()) {
				continue
			}
			candidatePath := filepath.Join(dir, entry.Name())
			candidateInfo, statErr := entry.Info()
			if statErr != nil || candidateInfo.Size() != info.Size() || !hasPlainImageHeader(candidatePath) {
				continue
			}
			delta := info.ModTime().Sub(candidateInfo.ModTime())
			if delta < 0 {
				delta = -delta
			}
			candidates = append(candidates, candidate{
				path:  candidatePath,
				delta: delta,
				same:  strings.EqualFold(filepath.Ext(candidatePath), filepath.Ext(path)),
			})
		}
	}
	if len(candidates) == 0 {
		return path
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].delta != candidates[j].delta {
			return candidates[i].delta < candidates[j].delta
		}
		if candidates[i].same != candidates[j].same {
			return candidates[i].same
		}
		return candidates[i].path < candidates[j].path
	})
	log.Warn().Str("encrypted_path", path).Str("plain_path", candidates[0].path).
		Int64("size", info.Size()).Msg("resolved encrypted image attachment to plaintext source")
	return candidates[0].path
}

func rwTempImageScope(path string) (talker, month string, ok bool) {
	parts := strings.Split(filepath.ToSlash(filepath.Clean(path)), "/")
	for i := 0; i+3 < len(parts); i++ {
		if parts[i] == "RWTemp" {
			return strings.TrimSpace(parts[i+2]), strings.TrimSpace(parts[i+1]),
				strings.TrimSpace(parts[i+1]) != "" && strings.TrimSpace(parts[i+2]) != ""
		}
	}
	return "", "", false
}

func messageFileMonth(path string) (string, bool) {
	parts := strings.Split(filepath.ToSlash(filepath.Clean(path)), "/")
	for i := 0; i+3 < len(parts); i++ {
		if parts[i] == "msg" && parts[i+1] == "file" {
			month := strings.TrimSpace(parts[i+2])
			return month, month != ""
		}
	}
	return "", false
}

func isImageFileName(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp", ".tif", ".tiff":
		return true
	default:
		return false
	}
}

func isThumbnailImageName(name string) bool {
	base := strings.ToLower(strings.TrimSuffix(name, filepath.Ext(name)))
	return strings.HasSuffix(base, "_t") || strings.HasSuffix(base, ".t") ||
		strings.HasSuffix(base, "_thumb") || strings.HasSuffix(base, ".thumb")
}

func hasPlainImageHeader(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	header := make([]byte, 16)
	n, _ := f.Read(header)
	header = header[:n]
	return bytes.HasPrefix(header, []byte{0xff, 0xd8, 0xff}) ||
		bytes.HasPrefix(header, []byte("\x89PNG\r\n\x1a\n")) ||
		bytes.HasPrefix(header, []byte("GIF87a")) || bytes.HasPrefix(header, []byte("GIF89a")) ||
		(len(header) >= 12 && bytes.Equal(header[:4], []byte("RIFF")) && bytes.Equal(header[8:12], []byte("WEBP"))) ||
		bytes.HasPrefix(header, []byte("BM")) || bytes.HasPrefix(header, []byte("II*\x00")) ||
		bytes.HasPrefix(header, []byte("MM\x00*"))
}
