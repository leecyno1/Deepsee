package wcdbapi

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func (c *Client) resolveDataKey(src string) (string, error) {
	if len(c.allKeys) == 0 {
		return "", fmt.Errorf("all_keys.json not found for encrypted db: %s", src)
	}
	src = filepath.Clean(src)
	c.mu.Lock()
	if key := c.resolvedKeys[src]; key != "" {
		c.mu.Unlock()
		return key, nil
	}
	c.mu.Unlock()

	ordered := make([]string, 0, len(c.allKeys)+1)
	seen := make(map[string]struct{}, len(c.allKeys)+1)
	appendKey := func(key string) {
		key = strings.ToLower(strings.TrimSpace(key))
		if len(key) != 64 {
			return
		}
		if _, err := hex.DecodeString(key); err != nil {
			return
		}
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		ordered = append(ordered, key)
	}
	if rel, ok := relPathFromDataDir(c.dataDir, src); ok {
		paths := []string{
			normalizeKeyPath(rel),
			normalizeKeyPath(strings.TrimPrefix(rel, "db_storage/")),
		}
		for _, path := range paths {
			if key, ok := c.allKeys[path]; ok {
				appendKey(key)
			}
		}
	}
	appendKey(c.dataKey)
	remaining := make([]string, 0, len(c.allKeys))
	for _, key := range c.allKeys {
		key = strings.ToLower(strings.TrimSpace(key))
		if _, ok := seen[key]; !ok {
			remaining = append(remaining, key)
		}
	}
	sort.Strings(remaining)
	for _, key := range remaining {
		appendKey(key)
	}

	for _, key := range ordered {
		if !validateEncryptedDBKey(src, key, c.newDecryptor) {
			continue
		}
		c.mu.Lock()
		c.resolvedKeys[src] = key
		c.mu.Unlock()
		return key, nil
	}
	if len(ordered) == 0 {
		return "", fmt.Errorf("all_keys.json missing key for db: %s", src)
	}
	return "", fmt.Errorf("all_keys.json has no verified key for db: %s", src)
}

func validateEncryptedDBKey(src, keyHex string, newDecryptor DecryptorFactory) bool {
	key, err := hex.DecodeString(strings.TrimSpace(keyHex))
	if err != nil || len(key) != 32 {
		return false
	}
	decryptor, err := newDecryptor()
	if err != nil {
		return false
	}
	file, err := os.Open(src)
	if err != nil {
		return false
	}
	defer file.Close()
	page := make([]byte, decryptor.GetPageSize())
	if _, err := io.ReadFull(file, page); err != nil {
		return false
	}
	return decryptor.Validate(page, key)
}

func loadAllKeysMap(dataDir string) map[string]string {
	paths := []string{filepath.Join(dataDir, "all_keys.json")}
	if strings.EqualFold(filepath.Base(filepath.Clean(dataDir)), "db_storage") {
		paths = append([]string{filepath.Join(filepath.Dir(filepath.Clean(dataDir)), "all_keys.json")}, paths...)
	}
	var raw []byte
	for _, path := range paths {
		bytes, err := os.ReadFile(path)
		if err == nil {
			raw = bytes
			break
		}
	}
	if len(raw) == 0 {
		return nil
	}
	object := map[string]any{}
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil
	}
	out := make(map[string]string, len(object))
	for path, value := range object {
		key := ""
		switch typed := value.(type) {
		case string:
			key = typed
		case map[string]any:
			key, _ = typed["enc_key"].(string)
		}
		key = strings.ToLower(strings.TrimSpace(key))
		if len(key) == 64 {
			out[normalizeKeyPath(path)] = key
		}
	}
	return out
}

func relPathFromDataDir(dataDir, dbFile string) (string, bool) {
	rel, err := filepath.Rel(dataDir, dbFile)
	if err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(rel), true
	}
	dbDir := dataDir
	if !strings.EqualFold(filepath.Base(filepath.Clean(dataDir)), "db_storage") {
		dbDir = filepath.Join(dataDir, "db_storage")
	}
	rel, err = filepath.Rel(dbDir, dbFile)
	if err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(rel), true
	}
	return "", false
}

func normalizeKeyPath(path string) string {
	return strings.TrimPrefix(strings.ToLower(strings.ReplaceAll(filepath.ToSlash(filepath.Clean(path)), "\\", "/")), "./")
}

func isPlainSQLite(path string) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()
	header := make([]byte, 16)
	n, err := io.ReadFull(file, header)
	if err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return false, nil
		}
		return false, err
	}
	return n == 16 && string(header) == "SQLite format 3\x00", nil
}
