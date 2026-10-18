package wcdb

import (
	"context"
	"crypto/md5"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	_ "github.com/mattn/go-sqlite3"

	"github.com/sjzar/chatlog/internal/model"
	"github.com/sjzar/chatlog/internal/wechat/decrypt"
	decryptdarwin "github.com/sjzar/chatlog/internal/wechat/decrypt/darwin"
	"github.com/sjzar/chatlog/internal/wechatdb/wcdbapi"
)

func TestToStringMapsSQLNullToEmptyString(t *testing.T) {
	if got := toString(nil); got != "" {
		t.Fatalf("toString(nil) = %q, want empty string", got)
	}
}

func TestSetCallbackWakesOnDatabaseFileChange(t *testing.T) {
	dataDir := t.TempDir()
	for _, group := range []string{"message", "session", "contact"} {
		if err := os.MkdirAll(filepath.Join(dataDir, group), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	ds := &DataSource{
		dataDir:        dataDir,
		watchedDirs:    make(map[string]struct{}),
		watchCallbacks: make(map[string]map[uint64]func(event fsnotify.Event) error),
	}
	defer ds.Close()

	events := make(chan fsnotify.Event, 1)
	if err := ds.SetCallback("message", func(event fsnotify.Event) error {
		select {
		case events <- event:
		default:
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dataDir, "message", "message_0.db-wal")
	if err := os.WriteFile(path, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-events:
		if event.Name != path {
			t.Fatalf("event path = %q, want %q", event.Name, path)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("database file change did not wake callback")
	}
}

func TestSubscribeCallbackStopsAfterCancel(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataDir, "message"), 0o700); err != nil {
		t.Fatal(err)
	}
	ds := &DataSource{
		dataDir:        dataDir,
		watchedDirs:    make(map[string]struct{}),
		watchCallbacks: make(map[string]map[uint64]func(event fsnotify.Event) error),
	}
	defer ds.Close()

	events := make(chan fsnotify.Event, 2)
	cancel, err := ds.SubscribeCallback("message", func(event fsnotify.Event) error {
		events <- event
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dataDir, "message", "message_0.db-wal")
	if err := os.WriteFile(path, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-events:
	case <-time.After(3 * time.Second):
		t.Fatal("subscribed callback did not receive database change")
	}
	cancel()
	cancel()
	if err := os.WriteFile(path, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-events:
		t.Fatalf("callback received event after cancellation: %#v", event)
	case <-time.After(250 * time.Millisecond):
	}
}

func TestIsReadOnlySQL(t *testing.T) {
	allowed := []string{
		"SELECT * FROM contact",
		"SELECT ';' AS delimiter;",
		"  -- inspect\nWITH x AS (SELECT 1) SELECT * FROM x",
		"/* query plan */ EXPLAIN QUERY PLAN SELECT 1",
		`PRAGMA table_info("contact")`,
	}
	for _, query := range allowed {
		if !isReadOnlySQL(query) {
			t.Errorf("read-only query rejected: %q", query)
		}
	}
	rejected := []string{
		"",
		"-- comment only",
		"/* unterminated",
		"INSERT INTO contact VALUES (1)",
		"UPDATE contact SET username = 'x'",
		"DELETE FROM contact",
		"DROP TABLE contact",
		"ATTACH DATABASE '/tmp/x' AS x",
		"SELECT 1; DELETE FROM contact",
		"WITH old AS (SELECT 1) DELETE FROM contact RETURNING *",
		"EXPLAIN UPDATE contact SET username = 'x'",
		"PRAGMA query_only = OFF",
	}
	for _, query := range rejected {
		if isReadOnlySQL(query) {
			t.Errorf("mutating query accepted: %q", query)
		}
	}
}

func TestGetVoiceSearchesEveryMediaShard(t *testing.T) {
	accountDir := t.TempDir()
	messageDir := filepath.Join(accountDir, "db_storage", "message")
	if err := os.MkdirAll(messageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	createVoiceDB(t, filepath.Join(messageDir, "media_0.db"), "old", []byte("not-it"))
	createVoiceDB(t, filepath.Join(messageDir, "media_1.db"), "target", []byte{1, 2, 3, 4})

	client, err := wcdbapi.NewClient(accountDir, strings.Repeat("22", 32), func() (decrypt.Decryptor, error) {
		return decryptdarwin.NewV4Decryptor(), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ds := &DataSource{
		dataDir:      client.DataDir(),
		client:       client,
		searchSchema: make(map[string][]searchTableMeta),
	}
	media, err := ds.GetMedia(context.Background(), "voice", "target")
	if err != nil {
		t.Fatal(err)
	}
	if got := string(media.Data); got != string([]byte{1, 2, 3, 4}) {
		t.Fatalf("voice data = %v", media.Data)
	}
	if ambiguous, ambiguousErr := ds.GetMedia(context.Background(), "voice", "1"); ambiguousErr == nil {
		t.Fatalf("ambiguous local voice ID returned data: %#v", ambiguous)
	}
}

func createVoiceDB(t *testing.T, path, serverID string, voice []byte) {
	t.Helper()
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE VoiceInfo (svr_id TEXT, local_id INTEGER, voice_data BLOB)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO VoiceInfo(svr_id, local_id, voice_data) VALUES (?, ?, ?)`, serverID, 1, voice); err != nil {
		t.Fatal(err)
	}
}

func TestMessageQueryFetchLimitAvoidsLargeFloorWithoutPostFilter(t *testing.T) {
	tests := []struct {
		name          string
		limit         int
		offset        int
		hasPostFilter bool
		want          int
	}{
		{name: "small plain query", limit: 2, want: 20},
		{name: "offset included", limit: 10, offset: 10, want: 100},
		{name: "keyword or sender filter", limit: 2, hasPostFilter: true, want: 1000},
		{name: "unbounded query", limit: 0, want: 0},
		{name: "hard cap", limit: 20000, want: 50000},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := messageQueryFetchLimit(tc.limit, tc.offset, tc.hasPostFilter); got != tc.want {
				t.Fatalf("messageQueryFetchLimit() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestSearchMessagesUsesFTSContentShadow(t *testing.T) {
	accountDir := t.TempDir()
	messageDir := filepath.Join(accountDir, "db_storage", "message")
	if err := os.MkdirAll(messageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	ftsPath := filepath.Join(messageDir, "message_fts.db")
	db, err := sql.Open("sqlite3", ftsPath)
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`CREATE TABLE name2id(username TEXT PRIMARY KEY)`,
		`INSERT INTO name2id(rowid, username) VALUES (1, 'talker-a'), (2, 'sender-a'), (3, 'talker-b'), (4, 'gh_official')`,
		`CREATE TABLE message_fts_v4_0_content(id INTEGER PRIMARY KEY, c0, c1, c2, c3, c4, c5, c6)`,
		`INSERT INTO message_fts_v4_0_content(id, c0, c1, c2, c3, c4, c5, c6)
		 VALUES (1, 'needle first', 7, 1001, 1, 1, 2, 100),
		        (2, 'needle second', 8, 1002, 1, 3, 2, 101),
		        (3, 'other', 9, 1003, 1, 1, 2, 102)`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	officialTalker := "gh_official"
	officialHash := md5.Sum([]byte(officialTalker))
	bizDB, err := sql.Open("sqlite3", filepath.Join(messageDir, "biz_message_0.db"))
	if err != nil {
		t.Fatal(err)
	}
	officialTable := "Msg_" + hex.EncodeToString(officialHash[:])
	for _, statement := range []string{
		`CREATE TABLE Name2Id(user_name TEXT)`,
		`INSERT INTO Name2Id(rowid, user_name) VALUES (1, 'sender-a'), (2, 'gh_official')`,
		`CREATE TABLE "` + officialTable + `"(
			local_id INTEGER, sort_seq INTEGER, server_id INTEGER, local_type INTEGER,
			real_sender_id INTEGER, create_time INTEGER, message_content BLOB,
			compress_content BLOB, source BLOB, packed_info_data BLOB, status INTEGER)`,
		`INSERT INTO "` + officialTable + `"(
			local_id, sort_seq, server_id, local_type, real_sender_id, create_time,
			message_content, compress_content, source, packed_info_data, status)
		 VALUES (10, 1004, 44, 1, 1, 103, 'needle official', X'', X'', X'', 0)`,
	} {
		if _, err := bizDB.Exec(statement); err != nil {
			_ = bizDB.Close()
			t.Fatal(err)
		}
	}
	if err := bizDB.Close(); err != nil {
		t.Fatal(err)
	}
	talkerHash := md5.Sum([]byte("talker-a"))
	messageDB, err := sql.Open("sqlite3", filepath.Join(messageDir, "message_0.db"))
	if err != nil {
		t.Fatal(err)
	}
	talkerTable := "Msg_" + hex.EncodeToString(talkerHash[:])
	for _, statement := range []string{
		`CREATE TABLE Name2Id(user_name TEXT)`,
		`INSERT INTO Name2Id(rowid, user_name) VALUES (1, 'sender-a'), (2, 'talker-a')`,
		`CREATE TABLE "` + talkerTable + `"(
			local_id INTEGER, sort_seq INTEGER, server_id INTEGER, local_type INTEGER,
			real_sender_id INTEGER, create_time INTEGER, message_content BLOB,
			compress_content BLOB, source BLOB, packed_info_data BLOB, status INTEGER)`,
		`INSERT INTO "` + talkerTable + `"(
			local_id, sort_seq, server_id, local_type, real_sender_id, create_time,
			message_content, compress_content, source, packed_info_data, status)
		 VALUES (11, 1011, 45, 1, 1, 150, 'needle recent', X'', X'', X'', 0)`,
	} {
		if _, err := messageDB.Exec(statement); err != nil {
			_ = messageDB.Close()
			t.Fatal(err)
		}
	}
	if err := messageDB.Close(); err != nil {
		t.Fatal(err)
	}
	sessionDir := filepath.Join(accountDir, "db_storage", "session")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sessionDB, err := sql.Open("sqlite3", filepath.Join(sessionDir, "session.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sessionDB.Exec(`
CREATE TABLE SessionTable(
	username TEXT, unread_count INTEGER, summary TEXT, last_timestamp INTEGER,
	last_msg_sender TEXT, last_sender_display_name TEXT, last_msg_type INTEGER,
	last_msg_sub_type INTEGER);
INSERT INTO SessionTable VALUES ('talker-a', 0, '', 150, '', '', 1, 0)`); err != nil {
		_ = sessionDB.Close()
		t.Fatal(err)
	}
	if err := sessionDB.Close(); err != nil {
		t.Fatal(err)
	}

	client, err := wcdbapi.NewClient(accountDir, strings.Repeat("22", 32), func() (decrypt.Decryptor, error) {
		return decryptdarwin.NewV4Decryptor(), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ds := &DataSource{
		dataDir:      client.DataDir(),
		client:       client,
		searchSchema: make(map[string][]searchTableMeta),
	}
	messages, total, path, err := ds.SearchMessages(
		context.Background(),
		time.Time{},
		time.Time{},
		[]string{"talker-a"},
		"needle",
		model.MessageTypeText,
		20,
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(path, "recent_message_tables") || total != 2 || len(messages) != 2 {
		t.Fatalf("search result path=%q total=%d messages=%+v", path, total, messages)
	}
	if messages[0].Talker != "talker-a" || messages[0].Sender != "sender-a" || messages[0].Content != "needle recent" {
		t.Fatalf("message = %+v", messages[0])
	}
	analytics, err := ds.AnalyzeMessages(context.Background(), model.MessageAnalyticsRequest{
		Talkers:     []string{"talker-a"},
		Keyword:     "needle",
		MessageType: model.MessageTypeText,
	})
	if err != nil {
		t.Fatal(err)
	}
	if analytics.Total != 2 ||
		len(analytics.ByChat) != 1 || analytics.ByChat[0].UserName != "talker-a" || analytics.ByChat[0].Count != 2 ||
		len(analytics.ByType) != 1 || analytics.ByType[0].Key != "1" || analytics.ByType[0].Count != 2 ||
		len(analytics.ByDay) != 1 || analytics.ByDay[0].Count != 2 ||
		!strings.Contains(analytics.Path, "recent_message_tables") {
		t.Fatalf("message analytics = %+v", analytics)
	}

	official, officialTotal, _, err := ds.SearchMessages(
		context.Background(), time.Time{}, time.Time{},
		[]string{"BrandSessionHolder"}, "needle", 0, 20, 0,
	)
	if err != nil {
		t.Fatal(err)
	}
	if officialTotal != 1 || len(official) != 1 || official[0].Talker != "gh_official" {
		t.Fatalf("folded official search = total %d, messages %+v", officialTotal, official)
	}

	emptyPage, totalAfterPage, _, err := ds.SearchMessages(
		context.Background(), time.Time{}, time.Time{},
		[]string{"talker-a"}, "needle", 0, 20, 100,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(emptyPage) != 0 || totalAfterPage != 2 {
		t.Fatalf("out-of-range page = total %d, messages %+v", totalAfterPage, emptyPage)
	}

	advanced, err := ds.SearchMessagesAdvanced(context.Background(), model.MessageSearchRequest{
		Talkers:     []string{"talker-a"},
		Keyword:     "needle recent",
		MessageType: model.MessageTypeText,
		Limit:       1,
		Match:       "any",
		Sort:        "relevance",
	})
	if err != nil {
		t.Fatal(err)
	}
	if advanced.Total != 2 || len(advanced.Messages) != 1 ||
		advanced.Messages[0].Content != "needle recent" ||
		advanced.Scores[model.MessageSearchResultKey(advanced.Messages[0])] != 2 {
		t.Fatalf("advanced relevance search = %+v", advanced)
	}
	first := advanced.Messages[0]
	next, err := ds.SearchMessagesAdvanced(context.Background(), model.MessageSearchRequest{
		Talkers:     []string{"talker-a"},
		Keyword:     "needle recent",
		MessageType: model.MessageTypeText,
		Limit:       1,
		Match:       "any",
		Sort:        "relevance",
		Cursor: &model.MessageSearchCursor{
			Score:     advanced.Scores[model.MessageSearchResultKey(first)],
			Timestamp: first.Time.Unix(),
			Seq:       first.Seq,
			LocalID:   first.DBLocalID,
			Talker:    first.Talker,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Messages) != 1 || next.Messages[0].Content != "needle first" {
		t.Fatalf("advanced cursor page = %+v", next)
	}
	allTerms, err := ds.SearchMessagesAdvanced(context.Background(), model.MessageSearchRequest{
		Talkers: []string{"talker-a"}, Keyword: "needle recent",
		Match: "all", Sort: "time", Limit: 20,
	})
	if err != nil || len(allTerms.Messages) != 1 || allTerms.Messages[0].Content != "needle recent" {
		t.Fatalf("advanced all-term search = %+v, err=%v", allTerms, err)
	}
}

func TestDetailedDatabaseSearchPrioritizesFTSShadowAndReportsStats(t *testing.T) {
	accountDir := t.TempDir()
	messageDir := filepath.Join(accountDir, "db_storage", "message")
	if err := os.MkdirAll(messageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(accountDir, "all_keys.json"),
		[]byte(`{"message/message_fts.db":{"enc_key":"`+strings.Repeat("22", 32)+`"}}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	ftsPath := filepath.Join(messageDir, "message_fts.db")
	db, err := sql.Open("sqlite3", ftsPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
CREATE TABLE message_fts_v4_0_content(id INTEGER PRIMARY KEY, c0);
INSERT INTO message_fts_v4_0_content(c0) VALUES
	('needle in shadow'),
	('alpha beta together'),
	('alpha alone')`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	client, err := wcdbapi.NewClient(accountDir, strings.Repeat("22", 32), func() (decrypt.Decryptor, error) {
		return decryptdarwin.NewV4Decryptor(), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ds := &DataSource{
		dataDir:      client.DataDir(),
		client:       client,
		searchSchema: make(map[string][]searchTableMeta),
	}
	result, err := ds.SearchAllDetailed(context.Background(), model.DatabaseSearchRequest{
		Keyword: "needle",
		Mode:    "quick",
		Limit:   10,
		Groups:  []string{"message"},
		Files:   []string{"message_fts.db"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("items = %+v", result.Items)
	}
	if result.Stats.FTSTables != 1 || result.Stats.Path != "fts_shadow_content_then_schema_scan" {
		t.Fatalf("stats = %+v", result.Stats)
	}

	multi, err := ds.SearchAllDetailed(context.Background(), model.DatabaseSearchRequest{
		Keyword: "alpha beta",
		Mode:    "quick",
		Match:   "all",
		Sort:    "relevance",
		Limit:   10,
		Groups:  []string{"message"},
		Files:   []string{"message_fts.db"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if multi.Total != 1 || len(multi.Items) != 1 || toInt64(multi.Items[0]["relevance"]) != 2 {
		t.Fatalf("multi-term database search = %+v", multi)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	cancelledResult, err := ds.SearchAllDetailed(cancelled, model.DatabaseSearchRequest{
		Keyword: "needle",
		Mode:    "deep",
		Limit:   10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !cancelledResult.Stats.Partial || len(cancelledResult.Items) != 0 {
		t.Fatalf("cancelled search = %+v", cancelledResult)
	}
}

func TestIsVirtualTableDefinition(t *testing.T) {
	tests := []struct {
		sql  string
		want bool
	}{
		{sql: `CREATE VIRTUAL TABLE message_fts USING fts5(content)`, want: true},
		{sql: " create  virtual\n table contact_fts using fts5(name)", want: true},
		{sql: `CREATE TABLE message_fts_data(id INTEGER PRIMARY KEY, block BLOB)`, want: false},
		{sql: "", want: false},
	}
	for _, tc := range tests {
		if got := isVirtualTableDefinition(tc.sql); got != tc.want {
			t.Errorf("isVirtualTableDefinition(%q) = %v, want %v", tc.sql, got, tc.want)
		}
	}
}

func TestStreamTableDataReturnsRowsBeyondJSONCeiling(t *testing.T) {
	accountDir := t.TempDir()
	contactDir := filepath.Join(accountDir, "db_storage", "contact")
	if err := os.MkdirAll(contactDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(contactDir, "contact.db")
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		CREATE TABLE rows_fixture (id INTEGER);
		WITH RECURSIVE seq(id) AS (
			SELECT 1
			UNION ALL
			SELECT id + 1 FROM seq WHERE id < 10005
		)
		INSERT INTO rows_fixture SELECT id FROM seq;
	`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	client, err := wcdbapi.NewClient(accountDir, strings.Repeat("33", 32), func() (decrypt.Decryptor, error) {
		return decryptdarwin.NewV4Decryptor(), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ds := &DataSource{dataDir: client.DataDir(), client: client}
	var lastID int64
	count, err := ds.StreamTableData(context.Background(), "contact", dbPath, "rows_fixture", "", func(_ []string, values []interface{}) error {
		lastID = toInt64(values[0])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != 10005 || lastID != 10005 {
		t.Fatalf("stream count=%d last_id=%d", count, lastID)
	}
}
