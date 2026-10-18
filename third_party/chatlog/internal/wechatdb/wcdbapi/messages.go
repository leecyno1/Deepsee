package wcdbapi

import (
	"crypto/md5"
	"encoding/hex"
	stderrors "errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	_ "github.com/mattn/go-sqlite3"

	"github.com/sjzar/chatlog/internal/model"
)

func (c *Client) GetSessions() ([]map[string]interface{}, error) {
	sql := `
SELECT username, unread_count, summary, last_timestamp, last_msg_sender, last_sender_display_name, last_msg_type, last_msg_sub_type
FROM SessionTable
WHERE last_timestamp > 0
ORDER BY last_timestamp DESC
`
	return c.Query("session", "", sql)
}

func (c *Client) GetMessages(username string, limit, offset int) ([]map[string]interface{}, error) {
	return c.GetMessagesInRange(username, 0, 0, limit, offset)
}

func (c *Client) GetMessage(username string, seq int64) (map[string]interface{}, error) {
	if strings.TrimSpace(username) == "" {
		return nil, fmt.Errorf("username is empty")
	}
	talkerMD5Bytes := md5.Sum([]byte(username))
	tableName := "Msg_" + hex.EncodeToString(talkerMD5Bytes[:])
	msgDBs, err := c.ListMessageDBs()
	if err != nil {
		return nil, err
	}
	var firstQueryErr error
	for _, dbPath := range msgDBs {
		exists, err := c.liveTableExists(dbPath, tableName)
		if err != nil || !exists {
			if err != nil && firstQueryErr == nil {
				firstQueryErr = err
			}
			continue
		}
		query := fmt.Sprintf(`
SELECT m.local_id, m.sort_seq, m.server_id, m.local_type, n.user_name, m.create_time, m.message_content, m.compress_content, m.source, m.packed_info_data, m.status
FROM [%s] m
LEFT JOIN Name2Id n ON m.real_sender_id = n.rowid
WHERE (m.create_time * 1000000 + m.local_id) = %d
LIMIT 1
`, tableName, seq)
		rows, err := c.queryLiveRows(dbPath, query)
		if err != nil {
			if firstQueryErr == nil {
				firstQueryErr = err
			}
			continue
		}
		if len(rows) > 0 {
			return rows[0], nil
		}
	}
	return nil, firstQueryErr
}

func (c *Client) GetMessagesInRange(username string, since, until int64, limit, offset int) ([]map[string]interface{}, error) {
	if username == "" {
		return nil, fmt.Errorf("username is empty")
	}
	_talkerMd5Bytes := md5.Sum([]byte(username))
	talkerMd5 := hex.EncodeToString(_talkerMd5Bytes[:])
	tableName := "Msg_" + talkerMd5

	msgDBs, err := c.ListMessageDBs()
	if err != nil {
		return nil, err
	}
	if len(msgDBs) == 0 {
		return nil, fmt.Errorf("message db not found")
	}

	type dbCandidate struct {
		srcPath string
		maxTS   int64
	}
	candidates := make([]dbCandidate, 0, len(msgDBs))
	shardErrors := make([]error, 0)
	for _, dbPath := range msgDBs {
		exists, err := c.liveTableExists(dbPath, tableName)
		if err != nil {
			shardErrors = append(shardErrors, fmt.Errorf("%s: inspect table: %w", filepath.Base(dbPath), err))
			continue
		}
		if !exists {
			continue
		}
		maxTS, err := c.liveTableMaxCreateTime(dbPath, tableName)
		if err != nil {
			shardErrors = append(shardErrors, fmt.Errorf("%s: inspect message range: %w", filepath.Base(dbPath), err))
			continue
		}
		candidates = append(candidates, dbCandidate{srcPath: dbPath, maxTS: maxTS})
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].maxTS > candidates[j].maxTS
	})

	perDBLimit := 0
	if limit > 0 {
		perDBLimit = limit + offset
		if perDBLimit <= 0 {
			perDBLimit = 5000
		}
		if perDBLimit > 50000 {
			perDBLimit = 50000
		}
	}

	var out []map[string]interface{}
	for _, item := range candidates {
		var where []string
		if since > 0 {
			where = append(where, fmt.Sprintf("m.create_time >= %d", since))
		}
		if until > 0 {
			where = append(where, fmt.Sprintf("m.create_time <= %d", until))
		}
		whereSQL := ""
		if len(where) > 0 {
			whereSQL = "WHERE " + strings.Join(where, " AND ")
		}

		sql := fmt.Sprintf(`
SELECT m.local_id, m.sort_seq, m.server_id, m.local_type, n.user_name, m.create_time, m.message_content, m.compress_content, m.source, m.packed_info_data, m.status
FROM [%s] m
LEFT JOIN Name2Id n ON m.real_sender_id = n.rowid
%s
ORDER BY m.create_time DESC
`, tableName, whereSQL)
		if perDBLimit > 0 {
			sql += fmt.Sprintf("LIMIT %d OFFSET 0\n", perDBLimit)
		}
		rows, err := c.queryLiveRows(item.srcPath, sql)
		if err != nil {
			shardErrors = append(shardErrors, fmt.Errorf("%s: query messages: %w", filepath.Base(item.srcPath), err))
			continue
		}
		out = append(out, rows...)
	}
	if len(shardErrors) != 0 {
		return nil, fmt.Errorf("one or more message shards failed: %w", stderrors.Join(shardErrors...))
	}

	sort.Slice(out, func(i, j int) bool {
		ti := toInt64(out[i]["create_time"])
		tj := toInt64(out[j]["create_time"])
		if ti == tj {
			return toInt64(out[i]["sort_seq"]) > toInt64(out[j]["sort_seq"])
		}
		return ti > tj
	})

	if offset > 0 {
		if offset >= len(out) {
			return []map[string]interface{}{}, nil
		}
		out = out[offset:]
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	sort.Slice(out, func(i, j int) bool {
		ti := toInt64(out[i]["create_time"])
		tj := toInt64(out[j]["create_time"])
		if ti == tj {
			return toInt64(out[i]["sort_seq"]) < toInt64(out[j]["sort_seq"])
		}
		return ti < tj
	})
	return out, nil
}

// GetMessagesAfter reads the oldest rows strictly after cursor. Each shard is
// queried in ascending order and the merged page is sorted again before the
// global limit is applied. This avoids the newest-window truncation that can
// permanently skip messages during a burst.
func (c *Client) GetMessagesAfter(username string, cursor model.MessageCursor, limit int) ([]map[string]interface{}, error) {
	msgDBs, err := c.ListMessageDBs()
	if err != nil {
		return nil, err
	}
	if len(msgDBs) == 0 {
		return nil, fmt.Errorf("message db not found")
	}
	return c.getMessagesAfterDBs(username, cursor, limit, msgDBs)
}

// GetMessagesAfterFiles limits a live incremental read to the message shards
// named by fsnotify. Startup catch-up continues to use GetMessagesAfter so it
// can span historical shard rotations.
func (c *Client) GetMessagesAfterFiles(
	username string,
	cursor model.MessageCursor,
	limit int,
	changedFiles []string,
) ([]map[string]interface{}, error) {
	msgDBs := c.changedMessageDBPaths(changedFiles)
	if len(msgDBs) == 0 {
		return []map[string]interface{}{}, nil
	}
	return c.getMessagesAfterDBs(username, cursor, limit, msgDBs)
}

func (c *Client) getMessagesAfterDBs(
	username string,
	cursor model.MessageCursor,
	limit int,
	msgDBs []string,
) ([]map[string]interface{}, error) {
	if strings.TrimSpace(username) == "" {
		return nil, fmt.Errorf("username is empty")
	}
	if limit <= 0 {
		limit = 500
	}
	if limit > 5000 {
		limit = 5000
	}

	talkerMD5Bytes := md5.Sum([]byte(username))
	tableName := "Msg_" + hex.EncodeToString(talkerMD5Bytes[:])

	out := make([]map[string]interface{}, 0, limit)
	shardErrors := make([]error, 0)
	for _, dbPath := range msgDBs {
		exists, err := c.liveTableExists(dbPath, tableName)
		if err != nil {
			shardErrors = append(shardErrors, fmt.Errorf("%s: inspect table: %w", filepath.Base(dbPath), err))
			continue
		}
		if !exists {
			continue
		}
		query := fmt.Sprintf(`
SELECT m.local_id, m.sort_seq, m.server_id, m.local_type, n.user_name, m.create_time, m.message_content, m.compress_content, m.source, m.packed_info_data, m.status
FROM [%s] m
LEFT JOIN Name2Id n ON m.real_sender_id = n.rowid
WHERE m.create_time > %d OR (m.create_time = %d AND m.local_id > %d)
ORDER BY m.create_time ASC, m.local_id ASC
LIMIT %d
`, tableName, cursor.Timestamp, cursor.Timestamp, cursor.LocalID, limit)
		rows, err := c.queryLiveRows(dbPath, query)
		if err != nil {
			shardErrors = append(shardErrors, fmt.Errorf("%s: query messages after cursor: %w", filepath.Base(dbPath), err))
			continue
		}
		out = append(out, rows...)
	}
	if len(shardErrors) != 0 {
		return nil, fmt.Errorf("one or more message shards failed: %w", stderrors.Join(shardErrors...))
	}

	sort.Slice(out, func(i, j int) bool {
		ti, tj := toInt64(out[i]["create_time"]), toInt64(out[j]["create_time"])
		if ti == tj {
			li, lj := toInt64(out[i]["local_id"]), toInt64(out[j]["local_id"])
			if li == lj {
				return toInt64(out[i]["sort_seq"]) < toInt64(out[j]["sort_seq"])
			}
			return li < lj
		}
		return ti < tj
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// ResolveChangedMessageTalkers inspects sqlite_master once per changed shard,
// then uses one batched EXISTS query to retain only tables with rows after each
// talker's cursor. This remains correct while SessionTable.last_timestamp is
// temporarily behind the message shard watermark.
func (c *Client) ResolveChangedMessageTalkers(
	changedFiles []string,
	candidates map[string]model.MessageCursor,
) ([]string, error) {
	msgDBs := c.changedMessageDBPaths(changedFiles)
	if len(msgDBs) == 0 || len(candidates) == 0 {
		return []string{}, nil
	}
	type candidateCursor struct {
		talker string
		hash   string
		cursor model.MessageCursor
	}
	candidateList := make([]candidateCursor, 0, len(candidates))
	for talker, cursor := range candidates {
		talker = strings.TrimSpace(talker)
		if talker == "" {
			continue
		}
		hash := md5.Sum([]byte(talker))
		candidateList = append(candidateList, candidateCursor{
			talker: talker,
			hash:   hex.EncodeToString(hash[:]),
			cursor: cursor,
		})
	}
	sort.Slice(candidateList, func(i, j int) bool { return candidateList[i].talker < candidateList[j].talker })
	resolvedSet := make(map[string]struct{})
	var shardErrors []error
	for _, dbPath := range msgDBs {
		rows, err := c.queryLiveRows(dbPath, `SELECT name FROM sqlite_master WHERE type='table' AND name LIKE 'Msg_%'`)
		if err != nil {
			shardErrors = append(shardErrors, fmt.Errorf("%s: inspect message tables: %w", filepath.Base(dbPath), err))
			continue
		}
		tables := make(map[string]struct{}, len(rows))
		for _, row := range rows {
			var name string
			switch value := row["name"].(type) {
			case string:
				name = value
			case []byte:
				name = string(value)
			}
			name = strings.ToLower(strings.TrimSpace(name))
			if strings.HasPrefix(name, "msg_") {
				tables[strings.TrimPrefix(name, "msg_")] = struct{}{}
			}
		}
		matching := make([]candidateCursor, 0, len(candidateList))
		for _, candidate := range candidateList {
			if _, exists := tables[candidate.hash]; exists {
				matching = append(matching, candidate)
			}
		}
		const candidatesPerQuery = 200
		for start := 0; start < len(matching); start += candidatesPerQuery {
			end := start + candidatesPerQuery
			if end > len(matching) {
				end = len(matching)
			}
			queries := make([]string, 0, end-start)
			for _, candidate := range matching[start:end] {
				queries = append(queries, fmt.Sprintf(`SELECT '%s' AS talker_hash WHERE EXISTS (
SELECT 1 FROM [Msg_%s]
WHERE create_time > %d OR (create_time = %d AND local_id > %d)
LIMIT 1
)`, candidate.hash, candidate.hash, candidate.cursor.Timestamp, candidate.cursor.Timestamp, candidate.cursor.LocalID))
			}
			changedRows, queryErr := c.queryLiveRows(dbPath, strings.Join(queries, "\nUNION ALL\n"))
			if queryErr != nil {
				shardErrors = append(shardErrors, fmt.Errorf("%s: locate advanced message tables: %w", filepath.Base(dbPath), queryErr))
				break
			}
			for _, row := range changedRows {
				var hash string
				switch value := row["talker_hash"].(type) {
				case string:
					hash = value
				case []byte:
					hash = string(value)
				}
				resolvedSet[strings.ToLower(strings.TrimSpace(hash))] = struct{}{}
			}
		}
	}
	if len(shardErrors) > 0 {
		return nil, fmt.Errorf("inspect changed message shards: %w", stderrors.Join(shardErrors...))
	}
	resolved := make([]string, 0, len(resolvedSet))
	for _, candidate := range candidateList {
		if _, changed := resolvedSet[candidate.hash]; changed {
			resolved = append(resolved, candidate.talker)
		}
	}
	return resolved, nil
}

func (c *Client) changedMessageDBPaths(changedFiles []string) []string {
	messageDir := filepath.Clean(filepath.Join(c.dataDir, "message"))
	paths := make([]string, 0, len(changedFiles))
	seen := make(map[string]struct{}, len(changedFiles))
	for _, changed := range changedFiles {
		path := filepath.Clean(strings.TrimSpace(changed))
		if path == "." || path == "" {
			continue
		}
		for _, suffix := range []string{"-wal", "-shm", "-journal"} {
			path = strings.TrimSuffix(path, suffix)
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(messageDir, filepath.Base(path))
		}
		path = filepath.Clean(path)
		if filepath.Dir(path) != messageDir ||
			!messageShardFilePattern.MatchString(strings.ToLower(filepath.Base(path))) {
			continue
		}
		if info, err := os.Stat(path); err != nil || info.IsDir() {
			continue
		}
		if _, duplicate := seen[path]; duplicate {
			continue
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}
