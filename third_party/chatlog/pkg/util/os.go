package util

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/rs/zerolog/log"
)

const (
	chatlogDirEnv  = "CHATLOG_DIR"
	chatlogDirName = ".chatlog"
)

// FindFilesWithPatterns 在指定目录下查找匹配多个正则表达式的文件
// directory: 要搜索的目录路径
// patterns: 正则表达式模式列表
// recursive: 是否递归搜索子目录
// 返回匹配的文件路径列表和可能的错误
func FindFilesWithPatterns(directory string, pattern string, recursive bool) ([]string, error) {
	// 编译所有正则表达式
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("无效的正则表达式 '%s': %v", pattern, err)
	}

	// 检查目录是否存在
	dirInfo, err := os.Stat(directory)
	if err != nil {
		return nil, fmt.Errorf("无法访问目录 '%s': %v", directory, err)
	}
	if !dirInfo.IsDir() {
		return nil, fmt.Errorf("'%s' 不是一个目录", directory)
	}

	// 存储匹配的文件路径
	var matchedFiles []string

	// 创建文件系统
	fsys := os.DirFS(directory)

	// 遍历文件系统
	err = fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// 如果是目录且不递归，则跳过子目录
		if d.IsDir() {
			if !recursive && path != "." {
				return fs.SkipDir
			}
			return nil
		}

		// 检查文件名是否匹配任何一个正则表达式
		if re.MatchString(d.Name()) {
			// 添加完整路径到结果列表
			fullPath := filepath.Join(directory, path)
			matchedFiles = append(matchedFiles, fullPath)
		}

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("遍历目录时出错: %v", err)
	}

	return matchedFiles, nil
}

// AppRootDir is the only default root that chatlog creates for persistent
// configuration, logs, runtime state, caches and decrypted workspaces.
// CHATLOG_DIR relocates the whole tree instead of relocating configuration
// alone and leaving other generated directories behind.
func AppRootDir() string {
	if configured := strings.TrimSpace(os.Getenv(chatlogDirEnv)); configured != "" {
		if absolute, err := filepath.Abs(configured); err == nil {
			return filepath.Clean(absolute)
		}
		return filepath.Clean(configured)
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return filepath.Join(os.TempDir(), "chatlog")
	}
	return filepath.Join(home, chatlogDirName)
}

// WorkDirAt returns the default decrypted workspace beneath root. Account names
// are reduced to one safe path component so detector-provided values cannot
// escape the application root.
func WorkDirAt(root, account string) string {
	root = strings.TrimSpace(root)
	if root == "" {
		root = AppRootDir()
	}
	if strings.TrimSpace(account) == "" {
		return filepath.Clean(root)
	}
	return filepath.Join(filepath.Clean(root), "workspaces", safePathComponent(account))
}

func DefaultWorkDir(account string) string {
	return WorkDirAt(AppRootDir(), account)
}

// CacheDirAt keeps generated caches under the same application root.
func CacheDirAt(root string, names ...string) string {
	root = strings.TrimSpace(root)
	if root == "" {
		root = AppRootDir()
	}
	parts := []string{filepath.Clean(root), "cache"}
	for _, name := range names {
		if value := safePathComponent(name); value != "" {
			parts = append(parts, value)
		}
	}
	return filepath.Join(parts...)
}

func DefaultCacheDir(names ...string) string {
	return CacheDirAt(AppRootDir(), names...)
}

// LegacyDefaultWorkDir returns the pre-unification workspace path. It is only
// used to identify and migrate defaults created by older chatlog versions;
// user-selected custom work directories are never rewritten.
func LegacyDefaultWorkDir(account string) string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	if strings.TrimSpace(account) == "" {
		return filepath.Join(home, "Documents", "chatlog")
	}
	return filepath.Join(home, "Documents", "chatlog", account)
}

// MigrateLegacyDefaultWorkDir atomically moves an old automatically generated
// account workspace into the unified root when it is safe to do so. Existing
// non-empty destinations and custom paths are left untouched.
func MigrateLegacyDefaultWorkDir(root, account, current string) (string, bool, error) {
	account = strings.TrimSpace(account)
	if account == "" || safePathComponent(account) != account {
		return current, false, nil
	}
	legacy := LegacyDefaultWorkDir(account)
	target := WorkDirAt(root, account)
	if legacy == "" || !sameCleanPath(current, legacy) {
		return current, false, nil
	}

	info, err := os.Stat(legacy)
	switch {
	case os.IsNotExist(err):
		return target, true, nil
	case err != nil:
		return current, false, err
	case !info.IsDir():
		return current, false, fmt.Errorf("%s is not a directory", legacy)
	}

	if targetInfo, targetErr := os.Stat(target); targetErr == nil {
		if !targetInfo.IsDir() {
			return current, false, fmt.Errorf("%s is not a directory", target)
		}
		entries, readErr := os.ReadDir(target)
		if readErr != nil {
			return current, false, readErr
		}
		if len(entries) != 0 {
			return current, false, fmt.Errorf("migration target is not empty: %s", target)
		}
		if removeErr := os.Remove(target); removeErr != nil {
			return current, false, removeErr
		}
	} else if !os.IsNotExist(targetErr) {
		return current, false, targetErr
	}

	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return current, false, err
	}
	if err := os.Rename(legacy, target); err != nil {
		return current, false, fmt.Errorf("move %s to %s: %w", legacy, target, err)
	}
	_ = os.Chmod(filepath.Dir(target), 0700)
	_ = os.Chmod(target, 0700)
	return target, true, nil
}

func sameCleanPath(a, b string) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == "" || b == "" {
		return false
	}
	aAbs, aErr := filepath.Abs(a)
	bAbs, bErr := filepath.Abs(b)
	if aErr == nil {
		a = aAbs
	}
	if bErr == nil {
		b = bAbs
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

func safePathComponent(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(value))
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '-', r == '_', r == '.', r == '@':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	result := strings.Trim(b.String(), ". ")
	if result == "" || result == ".." {
		return "default"
	}
	return result
}

func GetDirSize(dir string) string {
	var size int64
	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err == nil {
			size += info.Size()
		}
		return nil
	})
	return ByteCountSI(size)
}

func ByteCountSI(b int64) string {
	const unit = 1000
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB",
		float64(b)/float64(div), "kMGTPE"[exp])
}

// PrepareDir ensures that the specified directory path exists.
// If the directory does not exist, it attempts to create it.
func PrepareDir(path string) error {
	stat, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			if err := os.MkdirAll(path, 0700); err != nil {
				return err
			}
		} else {
			return err
		}
	} else if !stat.IsDir() {
		log.Debug().Msgf("%s is not a directory", path)
		return fmt.Errorf("%s is not a directory", path)
	}
	return os.Chmod(path, 0700)
}
