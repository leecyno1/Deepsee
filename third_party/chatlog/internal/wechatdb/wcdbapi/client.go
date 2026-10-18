package wcdbapi

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	_ "github.com/mattn/go-sqlite3"

	"github.com/sjzar/chatlog/internal/wechat/decrypt"
)

var messageShardFilePattern = regexp.MustCompile(`^(?:biz_)?message_[0-9]+\.db$`)

// Client is a built-in wcdb_api compatible query client.
// It provides the core open/query methods in-process (no external dylib).
type Client struct {
	dataDir string
	dataKey string
	allKeys map[string]string // normalized rel db path -> enc key

	mu sync.Mutex
	// resolvedKeys caches a page-validated key per encrypted source DB. This
	// avoids repeating PBKDF validation on every query while still allowing a
	// stale path entry in all_keys.json to recover from another saved key.
	resolvedKeys map[string]string
	newDecryptor DecryptorFactory

	directMu     sync.Mutex
	direct       map[string]*directClientSnapshot // encrypted source path -> immutable direct view
	directClosed bool
}

func (c *Client) DataDir() string { return c.dataDir }

type DecryptorFactory func() (decrypt.Decryptor, error)

func NewClient(dataDir, dataKey string, newDecryptor DecryptorFactory) (*Client, error) {
	if newDecryptor == nil {
		return nil, fmt.Errorf("decryptor factory is not configured")
	}
	dataDir = strings.TrimSpace(dataDir)
	dataKey = strings.TrimSpace(dataKey)
	if dataDir == "" {
		return nil, fmt.Errorf("data dir is empty")
	}
	if dataKey != "" {
		if len(dataKey) != 64 {
			return nil, fmt.Errorf("invalid data key length: %d", len(dataKey))
		}
		if _, err := hex.DecodeString(dataKey); err != nil {
			return nil, fmt.Errorf("invalid data key encoding: %w", err)
		}
	}
	normalizedDir, err := normalizeDataDir(dataDir)
	if err != nil {
		return nil, err
	}
	allKeys := loadAllKeysMap(normalizedDir)
	allKeys = recoverMissingDBKeyMappings(normalizedDir, allKeys, dataKey, newDecryptor)
	return &Client{
		dataDir:      normalizedDir,
		dataKey:      dataKey,
		allKeys:      allKeys,
		resolvedKeys: make(map[string]string),
		newDecryptor: newDecryptor,
		direct:       make(map[string]*directClientSnapshot),
	}, nil
}

// recoverMissingDBKeyMappings makes databases omitted by an older or partially
// refreshed all_keys.json discoverable. It only examines missing paths and only
// adds a mapping after page-level key validation, so the common fully-populated
// startup path has no extra PBKDF cost.
func recoverMissingDBKeyMappings(dataDir string, allKeys map[string]string, dataKey string, newDecryptor DecryptorFactory) map[string]string {
	if allKeys == nil {
		allKeys = make(map[string]string)
	}
	uniqueKeys := make([]string, 0, len(allKeys)+1)
	seenKeys := make(map[string]struct{}, len(allKeys)+1)
	appendKey := func(key string) {
		key = strings.ToLower(strings.TrimSpace(key))
		if len(key) != 64 {
			return
		}
		if _, err := hex.DecodeString(key); err != nil {
			return
		}
		if _, ok := seenKeys[key]; ok {
			return
		}
		seenKeys[key] = struct{}{}
		uniqueKeys = append(uniqueKeys, key)
	}
	appendKey(dataKey)
	for _, key := range allKeys {
		appendKey(key)
	}
	if len(uniqueKeys) == 0 {
		return allKeys
	}
	sort.Strings(uniqueKeys)

	_ = filepath.WalkDir(dataDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry == nil || entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".db") {
			return nil
		}
		rel, err := filepath.Rel(dataDir, path)
		if err != nil {
			return nil
		}
		rel = normalizeKeyPath(filepath.ToSlash(rel))
		if _, ok := allKeys[rel]; ok {
			return nil
		}
		if _, ok := allKeys[normalizeKeyPath("db_storage/"+rel)]; ok {
			return nil
		}
		for _, key := range uniqueKeys {
			if validateEncryptedDBKey(path, key, newDecryptor) {
				allKeys[rel] = key
				break
			}
		}
		return nil
	})
	return allKeys
}

func normalizeDataDir(dataDir string) (string, error) {
	clean := filepath.Clean(dataDir)
	abs, err := filepath.Abs(clean)
	if err == nil {
		clean = abs
	}
	if base := strings.ToLower(filepath.Base(clean)); base == "db_storage" {
		if isDir(clean) {
			return clean, nil
		}
		return "", fmt.Errorf("db_storage dir not found: %s", clean)
	}

	// account root form: .../xwechat_files/wxid_xxx
	dbStorage := filepath.Join(clean, "db_storage")
	if isDir(dbStorage) {
		return dbStorage, nil
	}
	// already db root form (contains session/contact/message dirs)
	if hasAnyDir(clean, []string{"session", "contact", "message", "media", "sns"}) {
		return clean, nil
	}
	return "", fmt.Errorf("invalid data dir: %s (need db_storage or account root containing db_storage)", clean)
}

func isDir(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

func hasAnyDir(base string, names []string) bool {
	for _, n := range names {
		st, err := os.Stat(filepath.Join(base, n))
		if err == nil && st.IsDir() {
			return true
		}
	}
	return false
}

func (c *Client) OpenAccount(path, key string) error {
	if strings.TrimSpace(path) == "" || strings.TrimSpace(key) == "" {
		return fmt.Errorf("invalid open_account arguments")
	}
	return nil
}

func (c *Client) ListMessageDBs() ([]string, error) {
	msgDir := filepath.Join(c.dataDir, "message")
	entries, err := os.ReadDir(msgDir)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(entries))
	// Public-account history is stored in biz_message_N.db but uses the same
	// Msg_<talker-md5>/Name2Id schema as ordinary message_N.db shards.
	// Exclude message_fts.db, message_resource.db and weclaw.db explicitly by
	// accepting a numeric shard suffix only.
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if messageShardFilePattern.MatchString(strings.ToLower(e.Name())) {
			p := filepath.Join(msgDir, e.Name())
			if c.CanQueryDB(p) {
				out = append(out, p)
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

func (c *Client) ListMediaDBs() ([]string, error) {
	out := make([]string, 0, 8)
	// Prefer current WeChat v4 layout: db_storage/hardlink/hardlink.db.
	for _, p := range []string{
		filepath.Join(c.dataDir, "hardlink", "hardlink.db"),
		filepath.Join(c.dataDir, "media", "hardlink.db"), // backward-compatible fallback
	} {
		if _, err := os.Stat(p); err == nil {
			out = append(out, p)
		}
	}

	voiceDBs, _ := c.ListVoiceDBs()
	out = append(out, voiceDBs...)

	uniq := make([]string, 0, len(out))
	seen := map[string]struct{}{}
	for _, p := range out {
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		uniq = append(uniq, p)
	}
	sort.Strings(uniq)
	return uniq, nil
}

// ListVoiceDBs returns every queryable media shard which may contain VoiceInfo.
// Current v4 stores these under message/, while early layouts used media/.
func (c *Client) ListVoiceDBs() ([]string, error) {
	re := regexp.MustCompile(`^media_[0-9]+\.db$`)
	out := make([]string, 0, 8)
	for _, dir := range []string{
		filepath.Join(c.dataDir, "message"),
		filepath.Join(c.dataDir, "media"),
	} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		for _, entry := range entries {
			if entry.IsDir() || !re.MatchString(strings.ToLower(entry.Name())) {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			if c.CanQueryDB(path) {
				out = append(out, path)
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

func (c *Client) Query(kind, path, query string) ([]map[string]interface{}, error) {
	return c.QueryWithLimit(kind, path, query, 0)
}

// QueryWithLimit applies a result-row ceiling after SQLite evaluates the
// statement. It protects generic inspection endpoints even when the supplied
// SQL omits LIMIT.
func (c *Client) QueryWithLimit(kind, path, query string, maxRows int) ([]map[string]interface{}, error) {
	return c.QueryWithLimitContext(context.Background(), kind, path, query, maxRows)
}

func (c *Client) QueryWithLimitContext(
	ctx context.Context,
	kind, path, query string,
	maxRows int,
) ([]map[string]interface{}, error) {
	dbPath, err := c.resolveDBPath(kind, path)
	if err != nil {
		return nil, err
	}
	return c.queryLiveRowsLimitedContext(ctx, dbPath, query, maxRows)
}

func (c *Client) StreamQuery(
	ctx context.Context,
	kind string,
	path string,
	query string,
	visit func(columns []string, values []interface{}) error,
) (int64, error) {
	dbPath, err := c.resolveDBPath(kind, path)
	if err != nil {
		return 0, err
	}
	readPath, err := c.ensureDirectRead(dbPath)
	if err != nil {
		return 0, err
	}
	return streamQueryRows(ctx, readPath, query, visit)
}

func (c *Client) CanQueryDB(dbPath string) bool {
	p := strings.TrimSpace(dbPath)
	if p == "" {
		return false
	}
	if _, err := os.Stat(p); err != nil {
		return false
	}
	if ok, err := isReadableSQLite(p); err == nil && ok {
		return true
	}
	_, err := c.resolveDataKey(p)
	return err == nil
}

func (c *Client) ListAllKeyDBs() []string {
	if len(c.allKeys) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(c.allKeys))
	out := make([]string, 0, len(c.allKeys))
	for rel := range c.allKeys {
		p := strings.TrimSpace(rel)
		if p == "" {
			continue
		}
		abs, err := resolvePathWithinDataDir(c.dataDir, p)
		if err != nil {
			continue
		}
		info, err := os.Stat(abs)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if _, ok := seen[abs]; ok {
			continue
		}
		seen[abs] = struct{}{}
		out = append(out, abs)
	}
	sort.Strings(out)
	return out
}
