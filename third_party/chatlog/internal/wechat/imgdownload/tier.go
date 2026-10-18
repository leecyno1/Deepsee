package imgdownload

import (
	"os"
	"path/filepath"
	"strings"
)

// Image quality tiers for WeChat V4 .dat naming.
// Higher is better for OCR.
const (
	TierNone  = 0
	TierThumb = 1
	TierMid   = 2
	TierBig   = 3
	TierHD    = 4
)

// TierOfName classifies a file name (or path basename) into an image tier.
func TierOfName(name string) int {
	lower := strings.ToLower(strings.TrimSpace(name))
	base := strings.TrimSuffix(filepath.Base(lower), ".dat")
	switch {
	case strings.HasSuffix(base, "_h"), strings.HasSuffix(base, ".h"),
		strings.HasSuffix(base, "_hd"), strings.HasSuffix(base, ".hd"):
		return TierHD
	case strings.HasSuffix(base, "_b"), strings.HasSuffix(base, ".b"):
		return TierBig
	case IsThumbnailName(base):
		return TierThumb
	case strings.HasSuffix(lower, ".dat"):
		return TierMid
	default:
		return TierNone
	}
}

// IsThumbnailName detects WeChat thumbnail suffixes independent of the file
// extension. Converted `_t.jpg` files are still thumbnails and must not become
// eligible for OCR merely because they no longer end in `.dat`.
func IsThumbnailName(name string) bool {
	base := strings.ToLower(strings.TrimSpace(filepath.Base(name)))
	for {
		if strings.HasSuffix(base, "_t") || strings.HasSuffix(base, ".t") ||
			strings.HasSuffix(base, "_thumb") || strings.HasSuffix(base, ".thumb") {
			return true
		}
		ext := filepath.Ext(base)
		if ext == "" {
			break
		}
		switch ext {
		case ".dat", ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp":
			base = strings.TrimSuffix(base, ext)
		default:
			return false
		}
	}
	return false
}

// TierName returns a short label for logs/UI.
func TierName(tier int) string {
	switch tier {
	case TierHD:
		return "hd"
	case TierBig:
		return "big"
	case TierMid:
		return "mid"
	case TierThumb:
		return "thumb"
	default:
		return "none"
	}
}

// Candidate is a local image .dat candidate.
type Candidate struct {
	Path string
	Tier int
	Size int64
}

// PreferableForOCR reports whether tier is good enough for text recognition.
// Thumbs are typically too small / heavily compressed.
func PreferableForOCR(tier int) bool {
	return tier >= TierMid
}

// CollectCandidates lists existing .dat variants for a media key or base path
// under dataDir. basePath may be a relative path ending with the md5 stem
// (with or without suffix), or an empty string when only mediaKey is known.
func CollectCandidates(dataDir, mediaKey, basePath string) []Candidate {
	dataDir = strings.TrimSpace(dataDir)
	mediaKey = strings.TrimSpace(mediaKey)
	basePath = strings.TrimSpace(basePath)
	if dataDir == "" {
		return nil
	}

	seen := map[string]struct{}{}
	out := make([]Candidate, 0, 8)
	add := func(path string) {
		if path == "" {
			return
		}
		if _, ok := seen[path]; ok {
			return
		}
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			return
		}
		seen[path] = struct{}{}
		out = append(out, Candidate{
			Path: path,
			Tier: TierOfName(path),
			Size: info.Size(),
		})
	}

	suffixes := []string{"_h.dat", ".h.dat", "_hd.dat", ".hd.dat", "_b.dat", ".b.dat", ".dat", "_t.dat", ".t.dat", "_thumb.dat", ".thumb.dat"}

	// Direct basePath variants.
	if basePath != "" {
		// Strip known suffix so we can re-expand.
		stem := stripImageDATSuffix(basePath)
		fullStem := filepath.Join(dataDir, stem)
		add(filepath.Join(dataDir, basePath))
		for _, suffix := range suffixes {
			add(fullStem + suffix)
		}
		// If basePath already pointed at a concrete file name.
		add(filepath.Join(dataDir, basePath))
	}
	// A path-backed message already identifies its session/month directory and
	// direct suffix expansion above covers all local quality variants. Avoid the
	// account-wide wildcard lookup for the common case, especially when hundreds
	// of images arrive together.
	if len(out) > 0 {
		return out
	}

	// Bounded layout search by md5 key.
	if mediaKey != "" && len(mediaKey) >= 16 {
		patterns := []string{
			filepath.Join(dataDir, "msg", "attach", "*", "*", "Img", mediaKey+"*"),
			filepath.Join(dataDir, "msg", "attach", "*", "*", "Rec", "*", "Img", mediaKey+"*"),
		}
		for _, pattern := range patterns {
			matches, err := filepath.Glob(pattern)
			if err != nil {
				continue
			}
			for _, match := range matches {
				if !strings.HasSuffix(strings.ToLower(match), ".dat") {
					continue
				}
				base := strings.ToLower(filepath.Base(match))
				if !strings.HasPrefix(base, strings.ToLower(mediaKey)) {
					continue
				}
				add(match)
			}
		}
	}

	return out
}

// BestCandidate picks the highest tier, then largest size.
func BestCandidate(candidates []Candidate) (Candidate, bool) {
	var best Candidate
	found := false
	for _, c := range candidates {
		if !found || c.Tier > best.Tier || (c.Tier == best.Tier && c.Size > best.Size) {
			best = c
			found = true
		}
	}
	return best, found
}

func stripImageDATSuffix(path string) string {
	lower := strings.ToLower(path)
	for _, suffix := range []string{
		"_h.dat", ".h.dat", "_hd.dat", ".hd.dat",
		"_b.dat", ".b.dat",
		"_t.dat", ".t.dat", "_thumb.dat", ".thumb.dat",
		".dat",
	} {
		if strings.HasSuffix(lower, suffix) {
			return path[:len(path)-len(suffix)]
		}
	}
	return path
}
