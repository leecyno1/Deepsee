package wcdbapi

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "github.com/mattn/go-sqlite3"
)

func (c *Client) resolveDBPath(kind, path string) (string, error) {
	if strings.TrimSpace(path) != "" {
		return resolvePathWithinDataDir(c.dataDir, path)
	}
	switch strings.ToLower(kind) {
	case "session":
		return filepath.Join(c.dataDir, "session", "session.db"), nil
	case "contact", "chatroom":
		return filepath.Join(c.dataDir, "contact", "contact.db"), nil
	case "sns":
		return filepath.Join(c.dataDir, "sns", "sns.db"), nil
	case "media", "voice":
		if strings.ToLower(kind) == "voice" {
			dbs, err := c.ListVoiceDBs()
			if err != nil {
				return "", err
			}
			if len(dbs) > 0 {
				return dbs[0], nil
			}
			return "", fmt.Errorf("voice db not found")
		}
		// media(hardlink) database
		for _, p := range []string{
			filepath.Join(c.dataDir, "hardlink", "hardlink.db"),
			filepath.Join(c.dataDir, "media", "hardlink.db"), // backward-compatible fallback
		} {
			if _, err := os.Stat(p); err == nil {
				return p, nil
			}
		}
		return "", fmt.Errorf("hardlink db not found")
	default:
		return "", fmt.Errorf("unsupported db kind: %s", kind)
	}
}

func resolvePathWithinDataDir(dataDir, requested string) (string, error) {
	base, err := filepath.Abs(filepath.Clean(dataDir))
	if err != nil {
		return "", fmt.Errorf("resolve data directory: %w", err)
	}

	raw := strings.TrimSpace(requested)
	slashPath := strings.ReplaceAll(raw, "\\", "/")
	var candidate string
	if filepath.IsAbs(filepath.FromSlash(slashPath)) {
		candidate = filepath.Clean(filepath.FromSlash(slashPath))
	} else {
		slashPath = strings.TrimPrefix(slashPath, "db_storage/")
		candidate = filepath.Join(base, filepath.FromSlash(slashPath))
	}
	candidate, err = filepath.Abs(filepath.Clean(candidate))
	if err != nil {
		return "", fmt.Errorf("resolve db path %q: %w", requested, err)
	}
	if !pathIsWithin(base, candidate) {
		return "", fmt.Errorf("invalid db path %q: outside data directory", requested)
	}

	// Existing symlinks must also resolve inside the account directory. The
	// database browser only opens existing files, so this closes the symlink
	// form of the same traversal without affecting normal relative/absolute
	// paths returned by GetDBs.
	realBase := base
	if resolved, resolveErr := filepath.EvalSymlinks(base); resolveErr == nil {
		realBase = resolved
	}
	if resolved, resolveErr := filepath.EvalSymlinks(candidate); resolveErr == nil && !pathIsWithin(realBase, resolved) {
		return "", fmt.Errorf("invalid db path %q: symlink escapes data directory", requested)
	}
	if resolvedParent, resolveErr := filepath.EvalSymlinks(filepath.Dir(candidate)); resolveErr == nil && !pathIsWithin(realBase, resolvedParent) {
		return "", fmt.Errorf("invalid db path %q: parent symlink escapes data directory", requested)
	}
	return candidate, nil
}

func pathIsWithin(base, candidate string) bool {
	rel, err := filepath.Rel(base, candidate)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
