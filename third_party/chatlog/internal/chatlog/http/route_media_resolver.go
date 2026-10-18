package http

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/sjzar/chatlog/internal/model"
)

type imageDATFileCandidate struct {
	path  string
	tier  int
	size  int64
	mtime int64
}

func imageDATSuffixTier(name string) int {
	lower := strings.ToLower(strings.TrimSpace(name))
	base := strings.TrimSuffix(lower, ".dat")
	switch {
	case strings.HasSuffix(base, "_h"), strings.HasSuffix(base, ".h"), strings.HasSuffix(base, "_hd"), strings.HasSuffix(base, ".hd"):
		return 4
	case strings.HasSuffix(base, "_b"), strings.HasSuffix(base, ".b"):
		return 3
	case strings.HasSuffix(base, "_t"), strings.HasSuffix(base, ".t"), strings.HasSuffix(base, "_thumb"), strings.HasSuffix(base, ".thumb"):
		return 1
	case strings.HasSuffix(lower, ".dat"):
		return 2
	default:
		return 0
	}
}

func imageDATSuffixesByPriority() []string {
	// WeFlow order: H/HD -> B -> base -> T/thumb
	return []string{
		"_h.dat", ".h.dat", "_hd.dat", ".hd.dat",
		"_b.dat", ".b.dat",
		".dat",
		"_t.dat", ".t.dat", "_thumb.dat", ".thumb.dat",
	}
}

func collectImageDATCandidates(dataDir, basePath string) []string {
	fullBase := filepath.Join(dataDir, basePath)
	out := make([]string, 0, 16)
	seen := map[string]struct{}{}
	add := func(path string) {
		if path == "" {
			return
		}
		if _, ok := seen[path]; ok {
			return
		}
		if _, err := os.Stat(path); err != nil {
			return
		}
		seen[path] = struct{}{}
		out = append(out, path)
	}
	add(fullBase)
	for _, suffix := range imageDATSuffixesByPriority() {
		add(fullBase + suffix)
	}
	return out
}

func isImageHardlinkCandidateName(fileName, baseMD5 string) bool {
	lower := strings.ToLower(strings.TrimSpace(fileName))
	baseMD5 = strings.ToLower(strings.TrimSpace(baseMD5))
	if !strings.HasSuffix(lower, ".dat") || baseMD5 == "" {
		return false
	}
	base := strings.TrimSuffix(lower, ".dat")
	if base == baseMD5 {
		return true
	}
	if strings.HasPrefix(base, baseMD5+"_") || strings.HasPrefix(base, baseMD5+".") {
		return true
	}
	if len(base) == len(baseMD5)+1 && strings.HasPrefix(base, baseMD5) {
		return true
	}
	return false
}

func pickBetterImageDATCandidate(cur, next *imageDATFileCandidate) *imageDATFileCandidate {
	if cur == nil {
		return next
	}
	if next == nil {
		return cur
	}
	if next.tier > cur.tier {
		return next
	}
	if next.tier < cur.tier {
		return cur
	}
	if next.size > cur.size {
		return next
	}
	if next.size < cur.size {
		return cur
	}
	if next.mtime > cur.mtime {
		return next
	}
	if next.mtime < cur.mtime {
		return cur
	}
	if next.path < cur.path {
		return next
	}
	return cur
}

func buildImageDATCandidate(path string, info os.FileInfo) *imageDATFileCandidate {
	tier := imageDATSuffixTier(filepath.Base(path))
	if tier <= 0 {
		return nil
	}
	return &imageDATFileCandidate{
		path:  path,
		tier:  tier,
		size:  info.Size(),
		mtime: info.ModTime().Unix(),
	}
}

func imageSessionMonthRootFromPath(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	cleaned := filepath.ToSlash(filepath.Clean(path))
	parts := strings.Split(cleaned, "/")
	for i := 0; i+3 < len(parts); i++ {
		if strings.EqualFold(parts[i], "msg") && strings.EqualFold(parts[i+1], "attach") {
			if strings.TrimSpace(parts[i+2]) == "" || strings.TrimSpace(parts[i+3]) == "" {
				continue
			}
			return filepath.FromSlash(strings.Join(parts[:i+4], "/"))
		}
	}
	return ""
}

func (s *Service) imageSessionMonthRoots(token string) []string {
	token = strings.ToLower(strings.TrimSpace(token))
	dataDir := s.conf.GetDataDir()
	dedup := make(map[string]struct{})
	matched := make([]string, 0, 16)
	all := make([]string, 0, 32)

	for _, p := range s.md5PathCache.Values() {
		if strings.TrimSpace(p) == "" {
			continue
		}
		absPath := p
		if !filepath.IsAbs(absPath) {
			absPath = filepath.Join(dataDir, p)
		}
		root := imageSessionMonthRootFromPath(absPath)
		if root == "" {
			continue
		}
		if _, ok := dedup[root]; ok {
			continue
		}
		dedup[root] = struct{}{}
		all = append(all, root)

		if token == "" {
			matched = append(matched, root)
			continue
		}
		lowerPath := strings.ToLower(filepath.ToSlash(p))
		lowerBase := strings.ToLower(filepath.Base(p))
		if strings.Contains(lowerPath, token) || strings.Contains(lowerBase, token) {
			matched = append(matched, root)
		}
	}
	if len(matched) > 0 {
		return matched
	}
	return all
}

// findImageByMD5 searches encrypted image DAT in msg/attach directory.
// Key can be md5, dat basename, or numeric file token.
func (s *Service) findImageByMD5(md5 string) string {
	token := strings.ToLower(strings.TrimSpace(md5))
	if token == "" {
		return ""
	}
	roots := s.imageSessionMonthRoots(token)
	if len(roots) == 0 {
		return ""
	}

	var best *imageDATFileCandidate

	for _, root := range roots {
		if _, err := os.Stat(root); err != nil {
			continue
		}
		// Walk only session-month scoped directories to match WeFlow candidate selection.
		_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				if os.IsPermission(err) {
					return filepath.SkipDir
				}
				return nil
			}
			if info.IsDir() {
				return nil
			}
			baseName := strings.ToLower(filepath.Base(path))
			if !isImageHardlinkCandidateName(baseName, token) {
				return nil
			}
			candidate := buildImageDATCandidate(path, info)
			if candidate == nil {
				return nil
			}
			best = pickBetterImageDATCandidate(best, candidate)
			return nil
		})
	}
	if best != nil {
		return best.path
	}

	return ""
}

// getMD5FromCache retrieves path from md5->path cache
func (s *Service) getMD5FromCache(md5 string) string {
	if path, ok := s.md5PathCache.Get(md5); ok {
		log.Debug().Str("md5", md5).Str("path", path).Msg("Cache hit for md5")
		return path
	}

	log.Debug().Str("md5", md5).Msg("Cache miss for md5")
	return ""
}

func (s *Service) resolveImagePathFromRecentMessages(md5 string) string {
	md5 = strings.ToLower(strings.TrimSpace(md5))
	if md5 == "" {
		return ""
	}
	if p := s.getMD5FromCache(md5); p != "" {
		return p
	}

	sessions, err := s.db.GetSessions("", 80, 0)
	if err != nil || sessions == nil || len(sessions.Items) == 0 {
		return ""
	}
	for _, sess := range sessions.Items {
		talker := strings.TrimSpace(sess.UserName)
		if talker == "" {
			continue
		}
		msgs, err := s.db.GetMessages(time.Time{}, time.Time{}, talker, "", "", 200, 0)
		if err != nil || len(msgs) == 0 {
			continue
		}
		s.populateMD5PathCache(msgs)

		for _, msg := range msgs {
			if msg == nil || msg.Type != model.MessageTypeImage || msg.Contents == nil {
				continue
			}
			md5Val := strings.ToLower(strings.TrimSpace(fmt.Sprint(msg.Contents["md5"])))
			if md5Val != md5 {
				continue
			}
			pathVal := strings.TrimSpace(fmt.Sprint(msg.Contents["path"]))
			if pathVal != "" {
				s.rememberMD5Path(md5, pathVal)
				return pathVal
			}
		}
	}
	return ""
}

// tryFindFileWithSuffixes tries to find media files from cached basePath.
func (s *Service) tryFindFileWithSuffixes(mediaType, basePath string) string {
	dataDir := s.conf.GetDataDir()

	switch mediaType {
	case "image":
		var best *imageDATFileCandidate
		for _, testPath := range collectImageDATCandidates(dataDir, basePath) {
			info, err := os.Stat(testPath)
			if err == nil {
				if candidate := buildImageDATCandidate(testPath, info); candidate != nil {
					best = pickBetterImageDATCandidate(best, candidate)
				}
			}
		}
		if best != nil {
			log.Debug().Str("path", best.path).Msg("Found best image file with suffix")
			return best.path
		}
	case "video":
		for _, suffix := range []string{".mp4", ".mov", ".m4v", "_thumb.jpg"} {
			testPath := filepath.Join(dataDir, basePath+suffix)
			if _, err := os.Stat(testPath); err == nil {
				log.Debug().Str("path", testPath).Str("media_type", mediaType).Msg("Found file with suffix")
				return testPath
			}
		}
	case "file":
		// file fallback relies on exact cached path or DB-provided path.
	}

	// Try without any suffix (might already have extension)
	testPath := filepath.Join(dataDir, basePath)
	if _, err := os.Stat(testPath); err == nil {
		log.Debug().Str("path", testPath).Msg("Found file without suffix")
		return testPath
	}

	log.Debug().Str("basePath", basePath).Msg("File not found with any suffix")
	return ""
}

// findRelocatedMediaFile repairs stale hardlink directory metadata by looking
// for the exact database file name in the known WeChat media layout. Glob
// expansion is bounded by the fixed directory depth and avoids a full data
// directory walk on every 404.
func (s *Service) findRelocatedMediaFile(media *model.Media) string {
	if media == nil {
		return ""
	}
	name := filepath.Base(strings.TrimSpace(media.Name))
	if name == "" || name == "." {
		return ""
	}
	dataDir := s.conf.GetDataDir()
	patterns := make([]string, 0, 8)
	switch media.Type {
	case "image":
		patterns = append(patterns,
			filepath.Join(dataDir, "msg", "attach", "*", "*", "Img", name),
			filepath.Join(dataDir, "msg", "attach", "*", "*", "Rec", "*", "Img", name),
		)
	case "video":
		patterns = append(patterns,
			filepath.Join(dataDir, "msg", "video", "*", name),
			filepath.Join(dataDir, "msg", "attach", "*", "*", "Rec", "*", "V", name),
		)
	case "file":
		patterns = append(patterns,
			filepath.Join(dataDir, "msg", "file", "*", name),
			filepath.Join(dataDir, "msg", "attach", "*", "*", "Rec", "*", "F", "*", name),
			filepath.Join(dataDir, "msg", "attach", "*", "*", "Rec", "*", "Dat", "*", name),
		)
	}
	var best *imageDATFileCandidate
	for _, pattern := range patterns {
		matches, _ := filepath.Glob(pattern)
		for _, match := range matches {
			info, err := os.Stat(match)
			if err != nil || info.IsDir() {
				continue
			}
			if media.Type != "image" {
				return match
			}
			best = pickBetterImageDATCandidate(best, buildImageDATCandidate(match, info))
		}
	}
	if best != nil {
		return best.path
	}
	return ""
}

// populateMD5PathCache populates the md5->path cache from messages
func (s *Service) populateMD5PathCache(messages []*model.Message) {
	for _, msg := range messages {
		if msg.Contents == nil {
			continue
		}

		// Only cache for image, video, and file types
		if msg.Type != model.MessageTypeImage &&
			msg.Type != model.MessageTypeVideo &&
			msg.Type != model.MessageTypeVoice {
			continue
		}

		// Get md5 from contents
		md5Value, md5Ok := msg.Contents["md5"].(string)
		if !md5Ok || md5Value == "" {
			continue
		}

		// Get path from contents
		pathValue, pathOk := msg.Contents["path"].(string)
		if pathOk && pathValue != "" {
			s.rememberMD5Path(md5Value, pathValue)
			log.Debug().Str("md5", md5Value).Str("path", pathValue).Msg("Cached md5->path mapping")
		}
	}
}

func (s *Service) rememberMD5Path(md5, path string) {
	md5 = strings.TrimSpace(md5)
	path = strings.TrimSpace(path)
	if md5 == "" || path == "" {
		return
	}
	s.md5PathCache.Set(md5, path, md5PathCacheLimit)
}

// handleImageFile processes an image file, handling decryption if it's a .dat file or file without extension
